//========= Copyright Team Frontress, All rights reserved. ====================//
//
// Purpose: The local game coordinator. See tf_mm_backend.h.
//
//=============================================================================//

#include "cbase.h"

#include "tf_mm_backend.h"

#include "clientsteamcontext.h"
#include "fmtstr.h"
#include "inetchannelinfo.h"
#include "gc_clientsystem.h"
#include "gcsdk/webapi_response.h"
#include "rtime.h"
#include "tf_gc_client.h"
#include "tf_item_schema.h"
#include "tf_matchcriteria.h"
#include "tf_matchmaking_shared.h"
#include "tf_shareddefs.h"
#include "tf_lobby_server.h"
#include "tf_match_description.h"
#include "tf_party.h"
#include "tf_partyclient.h"
#include "tf_rating_data.h"

// memdbgon must be the last include file in a .cpp file!!!
#include "tier0/memdbgon.h"

ConVar tf_mm_enable( "tf_mm_enable", "1", FCVAR_ARCHIVE,
                     "Use the Team Frontress matchmaking backend instead of waiting for a game coordinator." );
ConVar tf_mm_autojoin( "tf_mm_autojoin", "1", FCVAR_ARCHIVE,
                       "Connect automatically when matchmaking finds a match." );

extern ConVar tf_mm_debug;
extern ConVar tf_mm_coordinator;
extern ConVar tf_mm_party_autocreate;

static CTFMMBackend s_TFMMBackend;
CTFMMBackend *TFMMBackend() { return &s_TFMMBackend; }

#define MMDbg( ... ) do { if ( tf_mm_debug.GetBool() ) Msg( "[mm] " __VA_ARGS__ ); } while ( false )

// How often the menu's population line is refreshed. Slow on purpose: it is a
// courtesy readout, not something anybody waits on.
static const float k_flStatusPollInterval = 30.f;

// How long a connect to the match server may take before we call it off. A map
// download on a slow line is the long case this has to survive.
static const float k_flConnectGiveUpSecs = 180.f;

// How long a queue request waits for Steam to mint an auth ticket before it
// goes out without one.
static const float k_flAuthTicketWaitSecs = 10.f;

// How often the player's own record is refreshed at the menu. It only changes
// when a match ends, and that path asks for it directly.
static const float k_flProgressPollInterval = 120.f;

// Defined below, next to the rest of the JSON reading.
static int ReadJSONInt( GCSDK::CWebAPIValues *pValues, const char *pszName, int nDefault );

static CSteamID LocalSteamID()
{
	if ( steamapicontext && steamapicontext->SteamUser() )
		return steamapicontext->SteamUser()->GetSteamID();
	return CSteamID();
}

//-----------------------------------------------------------------------------
CTFMMBackend::CTFMMBackend()
	: CAutoGameSystemPerFrame( "CTFMMBackend" )
	, m_eState( k_eTFMMState_Idle )
	, m_bSubscribedToCache( false )
	, m_bWarnedPublishFailed( false )
	, m_bPartyPublished( false )
	, m_bLobbyPublished( false )
	, m_eQueuedMatchGroup( k_eTFMatchGroup_Invalid )
	, m_flNextPollTime( 0.f )
	, m_flQueueStartTime( 0.f )
	, m_flConnectStartTime( 0.f )
	, m_bConnectLeftOldServer( false )
	, m_bFollowLeaderServer( false )
	, m_nPollIntervalMS( 2000 )
	, m_nInQueue( 0 )
	, m_nNeedPlayers( 0 )
	, m_flNextStatusPoll( 0.f )
	, m_flNextProgressPoll( 0.f )
	, m_bGroupsKnown( false )
	, m_nMapPoolGeneration( 0 )
{
	for ( int i = 0; i < k_nMatchGroupPools; i++ )
	{
		m_arGroupOffered[i] = false;
	}
}

//-----------------------------------------------------------------------------
float CTFMMBackend::GetQueueSeconds() const
{
	if ( m_eState != k_eTFMMState_Searching || m_flQueueStartTime <= 0.f )
		return 0.f;

	return MAX( 0.f, Plat_FloatTime() - m_flQueueStartTime );
}

//-----------------------------------------------------------------------------
bool CTFMMBackend::Init()
{
	return true;
}

//-----------------------------------------------------------------------------
void CTFMMBackend::Shutdown()
{
	m_coordinator.Cancel();

	if ( m_bSubscribedToCache )
	{
		GCSDK::CGCClientSharedObjectCache *pCache = GetLocalCache( false );
		if ( pCache )
			pCache->RemoveListener( this );
		m_bSubscribedToCache = false;
	}
}

//-----------------------------------------------------------------------------
bool CTFMMBackend::BActive() const
{
	return tf_mm_enable.GetBool() && LocalSteamID().IsValid() && ClientSteamContext().BLoggedOn();
}

//-----------------------------------------------------------------------------
GCSDK::CGCClientSharedObjectCache *CTFMMBackend::GetLocalCache( bool bCreate )
{
	CSteamID steamID = LocalSteamID();
	if ( !steamID.IsValid() || !GCClientSystem() )
		return NULL;

	return bCreate ? GCClientSystem()->FindOrAddSOCache( steamID )
	               : GCClientSystem()->GetSOCache( steamID );
}

//-----------------------------------------------------------------------------
void CTFMMBackend::SOCacheSubscribed( const CSteamID &steamIDOwner, GCSDK::ESOCacheEvent eEvent )
{
	if ( steamIDOwner != LocalSteamID() )
		return;

	// Something else -- the web-API inventory fetch, most likely -- just
	// replaced the contents of our cache. Anything we had published is gone,
	// so publish it again rather than leaving the UI looking at nothing.
	MMDbg( "local SO cache (re)subscribed, republishing party and lobby\n" );
	m_bPartyPublished = false;
	m_bLobbyPublished = false;
	PublishParty();
	if ( m_eState == k_eTFMMState_MatchReady || m_eState == k_eTFMMState_Connecting ||
	     m_eState == k_eTFMMState_InMatch )
	{
		PublishLobby();
	}
}

//-----------------------------------------------------------------------------
void CTFMMBackend::SOCacheUnsubscribed( const CSteamID &steamIDOwner, GCSDK::ESOCacheEvent eEvent )
{
	if ( steamIDOwner != LocalSteamID() )
		return;

	m_bPartyPublished = false;
	m_bLobbyPublished = false;
}

//-----------------------------------------------------------------------------
void CTFMMBackend::Update( float frametime )
{
	if ( !BActive() )
		return;

	if ( !m_bSubscribedToCache )
	{
		GCSDK::CGCClientSharedObjectCache *pCache = GetLocalCache( true );
		if ( !pCache )
			return;

		pCache->AddListener( this );
		m_bSubscribedToCache = true;

		// Everything downstream reads the party object, so it has to exist
		// before the first frame of UI does.
		PublishParty();

		// A player with no lobby cannot be joined or invited, because there is
		// nothing for Steam to point a friend at. Retail hides this by having
		// the GC put everyone in a party of one; we host one instead.
		if ( tf_mm_party_autocreate.GetBool() && !m_party.BValid() )
			m_party.Create();

		// Steam mints the web API ticket asynchronously. Asking now means it
		// is in hand by the time somebody presses play, rather than the first
		// queue request of the session being the one that waits for it.
		m_coordinator.RequestAuthTicket();
	}

	// The party object tracks the Steam lobby, which changes underneath us.
	// Republishing is cheap and idempotent; only do it while a lobby exists.
	if ( m_party.BValid() )
	{
		PublishParty();
		// The settings panel writes a convar; Steam has to be told.
		m_party.ApplyJoinPolicy();
	}

	if ( m_eState == k_eTFMMState_Searching && !m_coordinator.BBusy() )
	{
		if ( m_strTicketID.IsEmpty() )
		{
			// A queue request that could not go out yet -- no auth ticket in
			// hand -- has to be retried, or pressing play before Steam answers
			// leaves the player standing in a search that never started.
			if ( Plat_FloatTime() >= m_flNextPollTime )
				SendQueueRequest();
		}
		else
		{
			// The queue API ticket contains a roster snapshot. Its GET
			// heartbeat is for the ticket as a whole, so if one lobby member
			// Alt-F4s while the leader keeps polling, that old member
			// otherwise stays queued forever.
			//
			// Re-POSTing is deliberately used instead of DELETE + POST:
			// Enqueue() replaces the previous ticket for the same
			// leader/group atomically, while two HTTP requests would race on
			// this single coordinator channel.
			//
			// Compare like with like: BuildRoster is also what wrote the
			// snapshot, so a member the request would have skipped anyway
			// cannot read as a change. Comparing against the raw lobby count
			// instead made a party holding one invalid member look different
			// from its own ticket every frame, and re-queue on every one.
			CUtlVector< CSteamID > vecNow;
			BuildRoster( vecNow );

			if ( !BRosterMatches( vecNow ) )
			{
				MMDbg( "party roster changed while queued; replacing ticket\n" );
				m_strTicketID.Clear();
				SendQueueRequest();
			}
			else if ( Plat_FloatTime() >= m_flNextPollTime )
			{
				PollQueue();
			}
		}
	}

	// The main menu's background map counts as being in a level, which is the
	// whole reason IsInGame is never asked on its own.
	const bool bReallyInGame = ( engine->IsInGame() && !engine->IsLevelMainMenuBackground() );

	// Getting out of a match again.
	//
	// Nothing used to close this loop: JoinAssignedMatch left us in Connecting
	// and no path ever left it, so the lobby object stayed in the SO cache for
	// the rest of the session. The stock UI reads that object to answer "do I
	// have a live match", so after one game the menu believed forever that a
	// match was still running.
	if ( m_eState == k_eTFMMState_Connecting )
	{
		// Being in a level is only evidence that we arrived once we have left
		// wherever we were. Connecting from another server -- a party that was
		// playing somewhere when the assignment landed -- keeps IsInGame()
		// answering about the *old* server until the disconnect, and taking
		// that as "we are in the match" flips straight to InMatch and then
		// back to Idle on the disconnect a moment later, destroying the lobby
		// object and the match with it.
		if ( !bReallyInGame )
		{
			m_bConnectLeftOldServer = true;
		}
		else if ( m_bConnectLeftOldServer )
		{
			EnterState( k_eTFMMState_InMatch );
		}

		if ( m_eState == k_eTFMMState_Connecting &&
		     Plat_FloatTime() - m_flConnectStartTime > k_flConnectGiveUpSecs )
		{
			// A connect that never lands -- a full server, a password the
			// server no longer has, a machine that went away between the
			// assignment and us. Waiting forever leaves the player with a
			// match they cannot join and a queue they cannot restart.
			Fail( "could not connect to the match server" );
		}
	}
	else if ( m_eState == k_eTFMMState_InMatch && !bReallyInGame )
	{
		// The match is over for us the moment we are off its server, however
		// we left it.
		MMDbg( "left the match server\n" );
		EnterState( k_eTFMMState_Idle );
		// A finished match is the only thing that moves the record, so this is
		// the one moment worth asking about it out of turn.
		m_flNextProgressPoll = 0.f;
	}

	// Tell the party where we are, so a member who joins can follow us in.
	PublishPartyServer( bReallyInGame );

	// Population for the menu. Nobody is looking at it during a match, so only
	// keep it fresh while we are out of one.
	if ( !bReallyInGame && !m_statusFeed.BBusy() )
	{
		if ( Plat_FloatTime() >= m_flNextStatusPoll )
			PollStatus();
		else if ( Plat_FloatTime() >= m_flNextProgressPoll )
			RefreshProgress();
	}
}

//-----------------------------------------------------------------------------
void CTFMMBackend::PollStatus()
{
	m_flNextStatusPoll = Plat_FloatTime() + k_flStatusPollInterval;

	if ( !m_statusFeed.BSend( k_EHTTPMethodGET, "/v1/status", NULL,
	                          &CTFMMBackend::StatusThunk, this ) )
	{
		MMDbg( "status poll could not be sent\n" );
	}
}

//-----------------------------------------------------------------------------
void CTFMMBackend::StatusThunk( GCSDK::CWebAPIValues *pValues, int eStatusCode, void *pContext )
{
	( (CTFMMBackend *)pContext )->OnStatus( pValues, eStatusCode );
}

//-----------------------------------------------------------------------------
// Purpose: Ask the coordinator what our match record adds up to.
//
//			The stock rank panel reads XP out of CTFRatingData objects in the
//			SO cache, which only the GC ever wrote -- so without this the badge
//			on the main menu is level 1 with an empty bar forever, however many
//			matches somebody has played. The coordinator has the record; this
//			fetches it and PublishRatings puts it where the panel looks.
//-----------------------------------------------------------------------------
void CTFMMBackend::RefreshProgress()
{
	m_flNextProgressPoll = Plat_FloatTime() + k_flProgressPollInterval;

	const CSteamID steamID = LocalSteamID();
	if ( !steamID.IsValid() )
		return;

	CFmtStr path( "/v1/player/%llu", (unsigned long long)steamID.ConvertToUint64() );
	if ( !m_statusFeed.BSend( k_EHTTPMethodGET, path.Get(), NULL,
	                          &CTFMMBackend::ProgressThunk, this ) )
	{
		MMDbg( "progress poll could not be sent\n" );
	}
}

//-----------------------------------------------------------------------------
void CTFMMBackend::ProgressThunk( GCSDK::CWebAPIValues *pValues, int eStatusCode, void *pContext )
{
	( (CTFMMBackend *)pContext )->OnProgress( pValues, eStatusCode );
}

//-----------------------------------------------------------------------------
void CTFMMBackend::OnProgress( GCSDK::CWebAPIValues *pValues, int eStatusCode )
{
	if ( eStatusCode != 200 || !pValues )
	{
		// A coordinator that keeps no records answers 503, which is a real
		// answer: there is no XP here and the badge should stay where it is.
		MMDbg( "progress poll returned HTTP %d\n", eStatusCode );
		return;
	}

	m_progress.bValid        = true;
	m_progress.nMatches      = ReadJSONInt( pValues, "matches", 0 );
	m_progress.nWins         = ReadJSONInt( pValues, "wins", 0 );
	m_progress.nLosses       = ReadJSONInt( pValues, "losses", 0 );
	m_progress.nAbandons     = ReadJSONInt( pValues, "abandons", 0 );
	m_progress.nXP           = ReadJSONInt( pValues, "xp", 0 );
	m_progress.nLevel        = MAX( 1, ReadJSONInt( pValues, "level", 1 ) );
	m_progress.nLevelXP      = ReadJSONInt( pValues, "level_xp", 0 );
	m_progress.nLevelXPTotal = ReadJSONInt( pValues, "level_xp_total", 0 );

	MMDbg( "progress: %d xp, level %d, %d matches\n",
	       m_progress.nXP, m_progress.nLevel, m_progress.nMatches );

	PublishRatings();
}

//-----------------------------------------------------------------------------
// Purpose: Put the XP in the SO cache, under every rating type the match
//			descriptions say they display.
//
//			Two objects per match group, not one: the panel reads the "current"
//			rating and the "last acknowledged" one and animates the difference.
//			Publishing the same number as both means the bar is simply correct
//			rather than sliding on every menu open, which is the honest answer
//			until the coordinator reports XP per match rather than as a total.
//-----------------------------------------------------------------------------
void CTFMMBackend::PublishRatings()
{
	if ( !m_progress.bValid )
		return;

	const CSteamID steamID = LocalSteamID();
	if ( !steamID.IsValid() || !BEnsureCacheSubscribed() )
		return;

	GCSDK::CGCClientSharedObjectCache *pCache = GetLocalCache( true );
	if ( !pCache )
		return;

	static const ETFMatchGroup s_eGroups[] = { k_eTFMatchGroup_Casual_12v12, k_eTFMatchGroup_Ladder_6v6 };

	bool bChanged = false;
	for ( int i = 0; i < ARRAYSIZE( s_eGroups ); i++ )
	{
		const IMatchGroupDescription *pDesc = GetMatchGroupDescription( s_eGroups[i] );
		if ( !pDesc || !pDesc->m_pProgressionDesc )
			continue;

		const EMMRating eRatings[] = { pDesc->GetCurrentDisplayRating(), pDesc->GetLastAckdDisplayRating() };
		for ( int j = 0; j < ARRAYSIZE( eRatings ); j++ )
		{
			CSOTFRatingData msg;
			msg.set_account_id( steamID.GetAccountID() );
			msg.set_rating_type( (int32)eRatings[j] );
			msg.set_rating_primary( (uint32)m_progress.nXP );

			std::string strData;
			if ( !msg.SerializeToString( &strData ) )
				continue;

			const bool bHave = ( m_vecPublishedRatingTypes.Find( (int)eRatings[j] ) !=
			                     m_vecPublishedRatingTypes.InvalidIndex() );

			bool bOK = bHave
				? pCache->BUpdateFromMsg( CTFRatingData::k_nTypeID, strData.data(), (uint32)strData.size() )
				: pCache->BCreateFromMsg( CTFRatingData::k_nTypeID, strData.data(), (uint32)strData.size() );

			if ( bOK )
			{
				if ( !bHave )
					m_vecPublishedRatingTypes.AddToTail( (int)eRatings[j] );
				bChanged = true;
			}
		}
	}

	if ( !bChanged )
		return;

	// The rank panel listens for this and re-reads the cache. Without it the
	// badge only updates the next time something else happens to redraw it.
	if ( gameeventmanager )
	{
		IGameEvent *pEvent = gameeventmanager->CreateEvent( "experience_changed" );
		if ( pEvent )
			gameeventmanager->FireEventClientSide( pEvent );
	}
}

//-----------------------------------------------------------------------------
// Purpose: Read a JSON field for what it means, not for how it was spelled.
//
//			CWebAPIValues accessors are typed, and a mismatched one answers zero
//			rather than the default it was given: on `"enabled": true`, which the
//			parser stores as a bool, GetChildInt32Value( "enabled", 1 ) returns
//			0. That read every match group the coordinator serves as switched
//			off -- which is why every mode said it was closed while the console
//			queue command for the same mode worked, and why every mode then
//			vanished once the menu started hiding what is not offered.
//
//			Whether a JSON number arrives as Int32, Int64 or Double is the
//			encoder's business and not something the reading side should have
//			to agree with in advance, so every numeric type is accepted here,
//			and the field's default survives a type nobody expected.
//-----------------------------------------------------------------------------
static int ReadJSONInt( GCSDK::CWebAPIValues *pValues, const char *pszName, int nDefault )
{
	GCSDK::CWebAPIValues *pChild = pValues ? pValues->FindChild( pszName ) : NULL;
	if ( !pChild )
		return nDefault;

	switch ( pChild->GetType() )
	{
		case GCSDK::k_EWebAPIValueType_Int32:
			return pChild->GetInt32Value();
		case GCSDK::k_EWebAPIValueType_UInt32:
			return (int)pChild->GetUInt32Value();
		case GCSDK::k_EWebAPIValueType_Int64:
			return (int)pChild->GetInt64Value();
		case GCSDK::k_EWebAPIValueType_UInt64:
			return (int)pChild->GetUInt64Value();
		case GCSDK::k_EWebAPIValueType_Double:
			return (int)pChild->GetDoubleValue();
		case GCSDK::k_EWebAPIValueType_Bool:
			return pChild->GetBoolValue() ? 1 : 0;
		case GCSDK::k_EWebAPIValueType_String:
		{
			CUtlString strValue;
			pChild->GetStringValue( strValue );
			return V_atoi( strValue.Get() );
		}
		default:
			return nDefault;
	}
}

//-----------------------------------------------------------------------------
static bool BReadJSONFlag( GCSDK::CWebAPIValues *pValues, const char *pszName, bool bDefault )
{
	GCSDK::CWebAPIValues *pChild = pValues ? pValues->FindChild( pszName ) : NULL;
	if ( !pChild )
		return bDefault;

	switch ( pChild->GetType() )
	{
		case GCSDK::k_EWebAPIValueType_Bool:
			return pChild->GetBoolValue();
		case GCSDK::k_EWebAPIValueType_Int32:
			return pChild->GetInt32Value() != 0;
		case GCSDK::k_EWebAPIValueType_UInt32:
			return pChild->GetUInt32Value() != 0;
		case GCSDK::k_EWebAPIValueType_Int64:
			return pChild->GetInt64Value() != 0;
		case GCSDK::k_EWebAPIValueType_UInt64:
			return pChild->GetUInt64Value() != 0;
		case GCSDK::k_EWebAPIValueType_Double:
			return pChild->GetDoubleValue() != 0.0;
		case GCSDK::k_EWebAPIValueType_String:
		{
			CUtlString strValue;
			pChild->GetStringValue( strValue );
			return !V_stricmp( strValue.Get(), "true" ) || !V_stricmp( strValue.Get(), "1" );
		}
		default:
			return bDefault;
	}
}

//-----------------------------------------------------------------------------
void CTFMMBackend::OnStatus( GCSDK::CWebAPIValues *pValues, int eStatusCode )
{
	if ( eStatusCode != 200 || !pValues )
	{
		// Say nothing rather than something stale: the menu draws "offline"
		// off the back of this.
		m_status = Status_t();
		m_status.bChecked = true;
		return;
	}

	m_status.bChecked             = true;
	m_status.bValid               = true;
	m_status.bServerCapacityKnown = BReadJSONFlag( pValues, "server_capacity_known", true );
	m_status.nOnlinePlayers       = ReadJSONInt( pValues, "online_players", 0 );
	m_status.nLiveMatches         = ReadJSONInt( pValues, "live_matches", 0 );
	m_status.nFreeServers         = ReadJSONInt( pValues, "free_servers", 0 );
	pValues->GetChildStringValue( m_status.strName, "name", "" );

	// The map pools. A group that reports none leaves its pool empty, and the
	// UI then falls back to showing everything the schema knows.
	for ( int i = 0; i < k_nMatchGroupPools; i++ )
	{
		m_arGroupMaps[i].RemoveAll();
		m_arGroupOffered[i] = false;
	}

	GCSDK::CWebAPIValues *pGroups = pValues->FindChild( "match_groups" );
	m_bGroupsKnown = ( pGroups != NULL );

	CUtlString strSignature;
	for ( GCSDK::CWebAPIValues *pGroup = pGroups ? pGroups->GetFirstChild() : NULL;
	      pGroup != NULL;
	      pGroup = pGroup->GetNextChild() )
	{
		const int nGroup = ReadJSONInt( pGroup, "match_group", -1 );
		if ( nGroup < 0 || nGroup >= k_nMatchGroupPools )
			continue;

		m_arGroupOffered[ nGroup ] = BReadJSONFlag( pGroup, "enabled", true );
		strSignature += CFmtStr( "%d%s:", nGroup, m_arGroupOffered[ nGroup ] ? "+" : "-" );

		GCSDK::CWebAPIValues *pMaps = pGroup->FindChild( "maps" );
		for ( GCSDK::CWebAPIValues *pMap = pMaps ? pMaps->GetFirstChild() : NULL;
		      pMap != NULL;
		      pMap = pMap->GetNextChild() )
		{
			CUtlString strMap;
			pMap->GetStringValue( strMap );
			if ( !strMap.IsEmpty() )
			{
				m_arGroupMaps[ nGroup ].AddToTail( strMap );
				strSignature += strMap;
				strSignature += ",";
			}
		}
	}

	// Only say something changed when it did: the map list rebuilds off this,
	// and rebuilding it every poll would blink in the player's face.
	if ( strSignature != m_strGroupSignature )
	{
		m_strGroupSignature = strSignature;
		m_nMapPoolGeneration++;
	}
}

//-----------------------------------------------------------------------------
const CUtlVector< CUtlString > *CTFMMBackend::GetGroupMaps( ETFMatchGroup eMatchGroup ) const
{
	const int nGroup = (int)eMatchGroup;
	if ( nGroup < 0 || nGroup >= k_nMatchGroupPools )
		return NULL;

	return m_arGroupMaps[ nGroup ].Count() > 0 ? &m_arGroupMaps[ nGroup ] : NULL;
}

//-----------------------------------------------------------------------------
bool CTFMMBackend::BAnyGroupOffered() const
{
	for ( int i = 0; i < k_nMatchGroupPools; i++ )
	{
		if ( m_arGroupOffered[i] )
			return true;
	}
	return false;
}

//-----------------------------------------------------------------------------
bool CTFMMBackend::BGroupOffered( ETFMatchGroup eMatchGroup ) const
{
	if ( !m_bGroupsKnown )
		return true;

	const int nGroup = (int)eMatchGroup;
	if ( nGroup < 0 || nGroup >= k_nMatchGroupPools )
		return false;

	return m_arGroupOffered[ nGroup ];
}

//
// Shared objects
//

//-----------------------------------------------------------------------------
void CTFMMBackend::RebuildPartyProto()
{
	// The party id must be stable and non-zero while a party exists, and zero
	// when it does not: the stock client treats "no party id" as "party of
	// one" and skips a lot of work in that case.
	m_msgParty.set_party_id( m_party.GetPartyID() );
	m_msgParty.set_leader_id( m_party.GetLeader().ConvertToUint64() );

	m_msgParty.clear_member_ids();
	m_msgParty.clear_members();

	const int nMembers = MAX( 1, m_party.GetNumMembers() );
	for ( int i = 0; i < nMembers; i++ )
	{
		CSteamID member = m_party.BValid() ? m_party.GetMember( i ) : LocalSteamID();
		if ( !member.IsValid() )
			continue;

		m_msgParty.add_member_ids( member.ConvertToUint64() );

		// member_ids and members are parallel arrays -- CTFParty reads the
		// activity for member i out of members(i), so they must stay the same
		// length even when we have nothing to say about a member.
		CSOTFPartyMember *pMember = m_msgParty.add_members();
		CSOTFPartyMember_Activity *pActivity = pMember->mutable_activity();
		pActivity->set_online( true );

		// Competitive access is a Valve entitlement we have no way to check and
		// no reason to enforce: whether a ladder group is playable here is the
		// coordinator's decision, not Steam's.
		pMember->set_competitive_access( true );
	}
}

//-----------------------------------------------------------------------------
// Protobuf wire encoding, by hand.
//
// The shared object cache only starts notifying listeners once it has been
// *subscribed*, and the one entry point that subscribes a local cache --
// CGCClient::AddLocalSOCache -- takes a serialized CMsgSOCacheSubscribed. That
// message lives in the prebuilt gcsdk library and its generated header is not
// part of this build, so we cannot construct one.
//
// We can write its bytes, though: it is four fields of plain protobuf.
// Generating the type instead would mean adding gcsdk_gcmessages.proto to the
// build, which would register a second copy of descriptors the library already
// owns and abort at startup.
//
//   message CMsgSOCacheSubscribed {
//     message SubscribedType { optional int32 type_id = 1; repeated bytes object_data = 2; }
//     optional fixed64 owner = 1;
//     repeated SubscribedType objects = 2;
//   }
//-----------------------------------------------------------------------------
static void PBWriteVarint( CUtlBuffer &buf, uint64 value )
{
	do
	{
		uint8 byte = (uint8)( value & 0x7F );
		value >>= 7;
		if ( value != 0 )
			byte |= 0x80;
		buf.PutUnsignedChar( byte );
	} while ( value != 0 );
}

static void PBWriteTag( CUtlBuffer &buf, int nField, int nWireType )
{
	PBWriteVarint( buf, ( (uint64)nField << 3 ) | (uint64)nWireType );
}

static void PBWriteFixed64( CUtlBuffer &buf, int nField, uint64 value )
{
	PBWriteTag( buf, nField, 1 ); // 1 = 64-bit
	for ( int i = 0; i < 8; i++ )
		buf.PutUnsignedChar( (uint8)( ( value >> ( i * 8 ) ) & 0xFF ) );
}

static void PBWriteBytes( CUtlBuffer &buf, int nField, const void *pData, int nBytes )
{
	PBWriteTag( buf, nField, 2 ); // 2 = length-delimited
	PBWriteVarint( buf, (uint64)nBytes );
	if ( nBytes > 0 )
		buf.Put( pData, nBytes );
}

//-----------------------------------------------------------------------------
// Subscribe the local player's cache, carrying whatever objects we have.
//
// Returns true if the cache is subscribed afterwards. Until it is, listeners --
// which is the entire matchmaking UI -- hear nothing at all, no matter what we
// put in the cache.
//-----------------------------------------------------------------------------
bool CTFMMBackend::BEnsureCacheSubscribed()
{
	GCSDK::CGCClientSharedObjectCache *pCache = GetLocalCache( false );
	if ( pCache && pCache->BIsSubscribed() )
		return true;

	const CSteamID steamID = LocalSteamID();
	if ( !steamID.IsValid() || !GCClientSystem() || !GCClientSystem()->GetGCClient() )
		return false;

	std::string strParty;
	if ( !m_msgParty.SerializeToString( &strParty ) )
		return false;

	CUtlBuffer bufType( 0, 512, 0 );
	PBWriteTag( bufType, 1, 0 ); // type_id, varint
	PBWriteVarint( bufType, (uint64)CTFParty::k_nTypeID );
	PBWriteBytes( bufType, 2, strParty.data(), (int)strParty.size() );

	CUtlBuffer bufMsg( 0, 640, 0 );
	PBWriteFixed64( bufMsg, 1, steamID.ConvertToUint64() );
	PBWriteBytes( bufMsg, 2, bufType.Base(), bufType.TellPut() );

	pCache = GCClientSystem()->GetGCClient()->AddLocalSOCache( steamID, bufMsg.Base(), (uint32)bufMsg.TellPut() );
	if ( !pCache || !pCache->BIsSubscribed() )
	{
		Warning( "[mm] could not subscribe the local shared object cache; matchmaking UI will not respond\n" );
		return false;
	}

	// The objects went in with the subscription, so nothing is pending.
	m_bPartyPublished = true;
	m_strLastPublishedParty = strParty;
	m_bLobbyPublished = false;

	MMDbg( "subscribed the local SO cache ourselves\n" );
	return true;
}

//-----------------------------------------------------------------------------
void CTFMMBackend::PublishParty()
{
	RebuildPartyProto();

	// Nothing downstream reacts to an unsubscribed cache. Usually the web-API
	// inventory has already subscribed it for us; when that fails -- an
	// unreachable endpoint, a refused AppID -- matchmaking must not fail with
	// it, so subscribe it ourselves, carrying the party we just built.
	if ( !BEnsureCacheSubscribed() )
		return;

	GCSDK::CGCClientSharedObjectCache *pCache = GetLocalCache( true );
	if ( !pCache )
		return;

	std::string strData;
	if ( !m_msgParty.SerializeToString( &strData ) )
		return;

	// Republishing an unchanged object would walk every listener -- and the
	// party panel and the dashboard both redraw off that -- once a frame.
	if ( m_bPartyPublished && strData == m_strLastPublishedParty )
		return;

	GCSDK::CSharedObjectTypeCache *pPartyType =
    pCache->FindBaseTypeCache( CTFParty::k_nTypeID );

	const bool bPartyAlreadyExists =
		pPartyType && pPartyType->GetCount() > 0;

	bool bOK;

	if ( bPartyAlreadyExists )
	{
		bOK = pCache->BUpdateFromMsg(
			CTFParty::k_nTypeID,
			strData.data(),
			(uint32)strData.size()
		);
	}
	else
	{
		bOK = pCache->BCreateFromMsg(
			CTFParty::k_nTypeID,
			strData.data(),
			(uint32)strData.size()
		);
	}
	if ( !bOK && bPartyAlreadyExists && !m_strLastPublishedParty.empty() )
	{
		// An update is aimed at the object we published last. The party's id
		// is 0 until Steam gives us a lobby and a real one afterwards, so the
		// object we are trying to update is not always the object that is
		// there. Replace it rather than talk past it.
		pCache->BDestroyFromMsg( CTFParty::k_nTypeID,
		                         m_strLastPublishedParty.data(),
		                         (uint32)m_strLastPublishedParty.size() );

		bOK = pCache->BCreateFromMsg( CTFParty::k_nTypeID, strData.data(), (uint32)strData.size() );
		if ( bOK )
		{
			MMDbg( "party object replaced rather than updated\n" );
		}
	}

	if ( bOK )
	{
		m_bPartyPublished = true;
		m_strLastPublishedParty = strData;
		m_bWarnedPublishFailed = false;
	}
	else
	{
		// Publishing failing is not recoverable by retrying the same way, but
		// it must be visible: with no party object the whole UI is inert. Say
		// it once -- this runs every frame, and a wall of the same line hides
		// whatever else went wrong.
		if ( !m_bWarnedPublishFailed )
		{
			m_bWarnedPublishFailed = true;
			Warning( "[mm] could not publish the party object (%s, %d bytes, party %llu); "
			         "matchmaking UI will not work\n",
			         bPartyAlreadyExists ? "update" : "create",
			         (int)strData.size(),
			         (unsigned long long)m_msgParty.party_id() );
		}
		m_bPartyPublished = false;
	}
}

//-----------------------------------------------------------------------------
void CTFMMBackend::PublishLobby()
{
	if ( !BEnsureCacheSubscribed() )
		return;

	GCSDK::CGCClientSharedObjectCache *pCache = GetLocalCache( true );
	if ( !pCache )
		return;

	std::string strData;
	if ( !m_msgLobby.SerializeToString( &strData ) )
		return;

	bool bOK;
	if ( m_bLobbyPublished )
		bOK = pCache->BUpdateFromMsg( CTFGSLobby::k_nTypeID, strData.data(), (uint32)strData.size() );
	else
		bOK = pCache->BCreateFromMsg( CTFGSLobby::k_nTypeID, strData.data(), (uint32)strData.size() );

	if ( bOK )
		m_bLobbyPublished = true;
}

//-----------------------------------------------------------------------------
void CTFMMBackend::DestroyLobbySO()
{
	if ( !m_bLobbyPublished )
		return;

	GCSDK::CGCClientSharedObjectCache *pCache = GetLocalCache( false );
	if ( pCache )
	{
		std::string strData;
		if ( m_msgLobby.SerializeToString( &strData ) )
			pCache->BDestroyFromMsg( CTFGSLobby::k_nTypeID, strData.data(), (uint32)strData.size() );
	}

	m_bLobbyPublished = false;
	m_msgLobby.Clear();
}

//
// Messages the stock client would have sent to the GC
//

// Downcast a protobuf message we already identified by its EMsg.
//
// The descriptor check is what makes this safe: the message type and the EMsg
// are tied together by the template that sent it, but a mismatch would be
// undefined behaviour rather than a bad answer, and this is cheaper than RTTI.
template < typename T >
static const T *ProtoAs( const ::google::protobuf::Message &msg )
{
	if ( msg.GetDescriptor() != T::descriptor() )
	{
		Warning( "[mm] message arrived as %s, expected %s\n",
		         msg.GetDescriptor() ? msg.GetDescriptor()->full_name().c_str() : "?",
		         T::descriptor()->full_name().c_str() );
		return NULL;
	}
	return static_cast< const T * >( &msg );
}

//-----------------------------------------------------------------------------
bool CTFMMBackend::BHandleClientMsg( uint32 unMsgType, const ::google::protobuf::Message &msgRequest,
                                     ::google::protobuf::Message *pMsgReply )
{
	if ( !BActive() )
		return false;

	switch ( unMsgType )
	{
		case k_EMsgGCParty_SetOptions:
		{
			const CMsgPartySetOptions *pMsg = ProtoAs< CMsgPartySetOptions >( msgRequest );
			if ( !pMsg )
				return false;

			const CTFPartyOptions &options = pMsg->options();
			if ( options.has_group_criteria() )
			{
				// The client decides whether it is sending a delta or a whole
				// new set. Getting this backwards leaves its criteria and ours
				// permanently out of step, and it will re-send forever.
				if ( options.overwrite_existing() )
					m_msgParty.mutable_group_criteria()->CopyFrom( options.group_criteria() );
				else
					m_msgParty.mutable_group_criteria()->MergeFrom( options.group_criteria() );
			}
			if ( options.has_player_uistate() )
				m_msgParty.mutable_leader_ui_state()->CopyFrom( options.player_uistate() );

			if ( options.has_player_criteria() && m_msgParty.members_size() > 0 )
			{
				// Per-player criteria belongs to us, and we are always our own
				// party's first member in the object we publish.
				int idx = 0;
				for ( int i = 0; i < m_msgParty.member_ids_size(); i++ )
				{
					if ( m_msgParty.member_ids( i ) == LocalSteamID().ConvertToUint64() )
					{
						idx = i;
						break;
					}
				}
				if ( idx < m_msgParty.members_size() )
				{
					m_msgParty.mutable_members( idx )->mutable_player_criteria()
						->CopyFrom( options.player_criteria() );
				}
			}

			PublishParty();
			return true;
		}

		case k_EMsgGCParty_QueueForMatch:
		{
			const CMsgPartyQueueForMatch *pMsg = ProtoAs< CMsgPartyQueueForMatch >( msgRequest );
			if ( !pMsg )
				return false;

			if ( pMsg->has_final_options() && pMsg->final_options().has_group_criteria() )
			{
				m_msgParty.mutable_group_criteria()->CopyFrom( pMsg->final_options().group_criteria() );
			}

			QueueForMatch( pMsg->match_group() );
			return true;
		}

		case k_EMsgGCParty_RemoveFromQueue:
		{
			CancelQueue();
			return true;
		}

		case k_EMsgGCParty_QueueForStandby:
		{
			// "Let me into the game my party is in." The leader published the
			// match id into the lobby when they got the assignment, so we know
			// which match to ask for -- and the coordinator seats us in it and
			// tells the server to expect us, which with the roster gate up is
			// the only way in.
			QueueForStandby();
			return true;
		}

		case k_EMsgGCParty_RemoveFromStandbyQueue:
		{
			CancelQueue();
			return true;
		}

		// Invites are Steam's. The stock client's own invite bookkeeping has
		// nothing to clear on our side.
		case k_EMsgGCParty_ClearPendingPlayer:
		case k_EMsgGCParty_ClearOtherPartyRequest:
			return true;

		//
		// Fire-and-forget messages. These arrive through the BSendMessage hook
		// rather than the reliable queue, and msgReply is not looked at.
		//

		case k_EMsgGCParty_SendChat:
		{
			const CMsgPartySendChat *pMsg = ProtoAs< CMsgPartySendChat >( msgRequest );
			if ( !pMsg )
				return false;
			m_party.SendChat( pMsg->msg().c_str() );
			return true;
		}

		case k_EMsgGCParty_InvitePlayer:
		{
			// Steam owns invites. If we have no lobby yet, make one: the
			// player just asked for a party.
			if ( !m_party.BValid() )
				m_party.Create();
			m_party.OpenInviteDialog();
			return true;
		}

		case k_EMsgGCParty_RequestJoinPlayer:
		{
			const CMsgPartyRequestJoinPlayer *pMsg = ProtoAs< CMsgPartyRequestJoinPlayer >( msgRequest );
			if ( !pMsg )
				return false;

			// "Join Game" in the friends list ends up here. The target's own
			// rich presence says which lobby they are in, so no third party
			// has to broker it.
			const CSteamID target( pMsg->join_player_id() );
			const CSteamID lobbyID = CTFMMParty::GetFriendPartyLobby( target );
			if ( !lobbyID.IsValid() )
			{
				Warning( "Could not find %s's party. They may not be in one, or may not be playing right now.\n",
				         steamapicontext && steamapicontext->SteamFriends()
				             ? steamapicontext->SteamFriends()->GetFriendPersonaName( target )
				             : "that player" );
				return true;
			}

			MMDbg( "joining %llu's lobby %llu\n",
			       (unsigned long long)target.ConvertToUint64(),
			       (unsigned long long)lobbyID.ConvertToUint64() );
			m_party.BTryJoin( lobbyID );
			return true;
		}

		case k_EMsgGCParty_PromoteToLeader:
		{
			const CMsgPartyPromoteToLeader *pMsg = ProtoAs< CMsgPartyPromoteToLeader >( msgRequest );
			if ( !pMsg || !m_party.BValid() || !m_party.BIsLeader() )
				return true;

			if ( steamapicontext && steamapicontext->SteamMatchmaking() )
			{
				CSteamID newLeader( pMsg->new_leader_id() );
				steamapicontext->SteamMatchmaking()->SetLobbyOwner( m_party.GetLobbyID(), newLeader );
				m_party.SetLobbyData( "leader", CFmtStr( "%llu", newLeader.ConvertToUint64() ) );
			}
			return true;
		}

		case k_EMsgGCParty_KickMember:
		{
			// Steam has no "kick from lobby". The honest answer is that we
			// cannot do this, and saying so beats a button that silently does
			// nothing.
			Warning( "Kicking a party member is not supported: a Steam lobby has no kick. Ask them to leave.\n" );
			return true;
		}

		default:
			break;
	}

	return false;
}

//
// Queueing
//

//-----------------------------------------------------------------------------
bool BMapInPool( const CUtlVector< CUtlString > &vecPool, const char *pszMap )
{
	FOR_EACH_VEC( vecPool, i )
	{
		if ( !V_stricmp( vecPool[i].Get(), pszMap ) )
			return true;
	}

	return false;
}

//-----------------------------------------------------------------------------
void CTFMMBackend::CollectSelectedMaps( CUtlVector< CUtlString > &vecOut ) const
{
	if ( !GTFPartyClient() )
		return;

	CCasualCriteriaHelper helper = GTFPartyClient()->GetEffectiveGroupCriteria().GetCasualCriteriaHelper();
	if ( !helper.AnySelected() )
		return;

	// Never ask for a map the group does not run: the coordinator would drop
	// the preference and play something else, which is exactly the surprise
	// the menu is supposed to prevent.
	const CUtlVector< CUtlString > *pPool = GetGroupMaps( m_eQueuedMatchGroup );

	const CUtlVector< MapDef_t * > &vecMaps = GetItemSchema()->GetMasterMapsList();
	FOR_EACH_VEC( vecMaps, i )
	{
		const MapDef_t *pMap = vecMaps[i];
		if ( !pMap || !pMap->pszMapName || !helper.IsMapSelected( pMap ) )
			continue;

		if ( pPool && !BMapInPool( *pPool, pMap->pszMapName ) )
			continue;

		vecOut.AddToTail( pMap->pszMapName );
	}
}

//-----------------------------------------------------------------------------
void CTFMMBackend::QueueForMatch( ETFMatchGroup eMatchGroup )
{
	if ( !BActive() )
		return;

	if ( !m_party.BIsLeader() )
	{
		// Only the leader talks to the coordinator. Members follow the
		// assignment the leader publishes into the lobby.
		MMDbg( "not the party leader; waiting for them to queue\n" );
		return;
	}

	m_eQueuedMatchGroup = eMatchGroup;
	m_strLastError.Clear();
	m_flQueueStartTime = Plat_FloatTime();
	// A normal queue is not a standby request, whatever the last one was.
	m_strStandbyMatchID.Clear();

	// Show the queue in the party object immediately. The UI predicts being in
	// queue the moment it asks, and a party object that disagrees makes it
	// flicker back out.
	m_msgParty.clear_matchmaking_queues();
	CSOTFParty_QueueEntry *pEntry = m_msgParty.add_matchmaking_queues();
	pEntry->set_match_group( eMatchGroup );
	pEntry->set_queued_time( CRTime::RTime32TimeCur() );
	PublishParty();

	EnterState( k_eTFMMState_Searching );
	SendQueueRequest();
}

//-----------------------------------------------------------------------------
// Purpose: Ask to be seated in the match our party is already playing.
//
//			The difference from QueueForMatch is one field in the request and
//			everything else is the same -- same ticket, same poll, same
//			assignment. It has to be: with the roster gate up, "join my party's
//			match" and "be backfilled into a match" are the same act on the
//			server, and the only thing that separates them is which match was
//			asked for.
//-----------------------------------------------------------------------------
void CTFMMBackend::QueueForStandby()
{
	if ( !BActive() )
		return;

	if ( m_eState != k_eTFMMState_Idle && m_eState != k_eTFMMState_Searching )
	{
		MMDbg( "already in a match; not asking for a standby seat\n" );
		return;
	}

	const char *pszMatchID = m_party.GetLobbyData( "match_id" );
	if ( !pszMatchID || !pszMatchID[0] )
	{
		Warning( "Your party is not in a match to join.\n" );
		return;
	}

	int nGroup = atoi( m_party.GetLobbyData( "queue_group" ) );
	if ( nGroup == 0 )
		nGroup = (int)k_eTFMatchGroup_Casual_12v12;

	m_strStandbyMatchID = pszMatchID;
	m_eQueuedMatchGroup = (ETFMatchGroup)nGroup;
	m_strLastError.Clear();
	m_flQueueStartTime = Plat_FloatTime();
	m_strTicketID.Clear();

	// The party object drives the UI's "in queue" state, and standby is a
	// queue as far as the player is concerned.
	m_msgParty.clear_matchmaking_queues();
	CSOTFParty_QueueEntry *pEntry = m_msgParty.add_matchmaking_queues();
	pEntry->set_match_group( m_eQueuedMatchGroup );
	pEntry->set_queued_time( CRTime::RTime32TimeCur() );
	PublishParty();

	EnterState( k_eTFMMState_Searching );
	SendQueueRequest();
}

//-----------------------------------------------------------------------------
// Append a JSON string literal, escaped. Persona names are user-controlled and
// arrive from Steam unfiltered, so this is not optional.
static void AppendJSONString( CUtlBuffer &buf, const char *pszValue )
{
	buf.PutChar( '"' );
	for ( const unsigned char *p = (const unsigned char *)( pszValue ? pszValue : "" ); *p; p++ )
	{
		switch ( *p )
		{
			case '"':  buf.PutString( "\\\"" ); break;
			case '\\': buf.PutString( "\\\\" ); break;
			case '\n': buf.PutString( "\\n" ); break;
			case '\r': buf.PutString( "\\r" ); break;
			case '\t': buf.PutString( "\\t" ); break;
			default:
				if ( *p < 0x20 )
				{
					char szEscape[8];
					V_snprintf( szEscape, sizeof( szEscape ), "\\u%04x", *p );
					buf.PutString( szEscape );
				}
				else
				{
					buf.PutChar( (char)*p );
				}
				break;
		}
	}
	buf.PutChar( '"' );
}

static void AppendJSONField( CUtlBuffer &buf, const char *pszKey, const char *pszValue )
{
	AppendJSONString( buf, pszKey );
	buf.PutChar( ':' );
	AppendJSONString( buf, pszValue );
}

//-----------------------------------------------------------------------------
// Purpose: The players a queue request stands for: every valid member of the
//			Steam lobby, or just us when there is no lobby. One function so the
//			request and the "did the party change" check cannot disagree.
//-----------------------------------------------------------------------------
void CTFMMBackend::BuildRoster( CUtlVector< CSteamID > &vecOut ) const
{
	vecOut.RemoveAll();

	if ( !m_party.BValid() )
	{
		const CSteamID local = LocalSteamID();
		if ( local.IsValid() )
			vecOut.AddToTail( local );
		return;
	}

	const int nMembers = m_party.GetNumMembers();
	for ( int i = 0; i < nMembers; i++ )
	{
		const CSteamID member = m_party.GetMember( i );
		if ( member.IsValid() )
			vecOut.AddToTail( member );
	}

	// A lobby that has not synced yet still contains us.
	if ( vecOut.Count() == 0 )
	{
		const CSteamID local = LocalSteamID();
		if ( local.IsValid() )
			vecOut.AddToTail( local );
	}
}

//-----------------------------------------------------------------------------
bool CTFMMBackend::BRosterMatches( const CUtlVector< CSteamID > &vecNow ) const
{
	if ( vecNow.Count() != m_vecQueuedRoster.Count() )
		return false;

	FOR_EACH_VEC( vecNow, i )
	{
		bool bFound = false;
		FOR_EACH_VEC( m_vecQueuedRoster, j )
		{
			if ( m_vecQueuedRoster[j] == vecNow[i] )
			{
				bFound = true;
				break;
			}
		}
		if ( !bFound )
			return false;
	}
	return true;
}

//-----------------------------------------------------------------------------
void CTFMMBackend::SendQueueRequest()
{
	// Steam issues the web API ticket asynchronously, and a coordinator in
	// webapi mode refuses a queue request that arrives without one. Wait for
	// it -- but only for a while: a coordinator in dev mode does not want a
	// ticket at all, and Steam sometimes will not issue one (offline mode,
	// a family-shared copy), and neither of those should be a queue that
	// never starts.
	if ( !m_coordinator.BHaveAuthTicket() &&
	     Plat_FloatTime() - m_flQueueStartTime < k_flAuthTicketWaitSecs )
	{
		m_coordinator.RequestAuthTicket();
		m_strQueueDetail = "Waiting for Steam.";
		m_flNextPollTime = Plat_FloatTime() + 0.5f;
		return;
	}

	// Whatever the last ticket was told, this one has not been told anything.
	m_strQueueDetail.Clear();

	CUtlBuffer body( 0, 2048, CUtlBuffer::TEXT_BUFFER );

	body.Printf( "{\"match_group\":%d,", (int)m_eQueuedMatchGroup );
	if ( !m_strStandbyMatchID.IsEmpty() )
	{
		body.PutString( "\"standby_match_id\":" );
		AppendJSONString( body, m_strStandbyMatchID.Get() );
		body.PutChar( ',' );
	}
	body.Printf( "\"late_join_ok\":%s,",
	             ( GTFPartyClient() && GTFPartyClient()->GetEffectiveGroupCriteria().GetLateJoin() ) ? "true" : "false" );
	body.PutString( "\"leader\":" );
	AppendJSONString( body, CFmtStr( "%llu", m_party.GetLeader().ConvertToUint64() ) );

	body.PutString( ",\"players\":[" );

	// Remember exactly which Steam lobby members this ticket represents.
	// Update() rebuilds the same list and compares it while searching.
	BuildRoster( m_vecQueuedRoster );

	int nWritten = 0;
	FOR_EACH_VEC( m_vecQueuedRoster, i )
	{
		const CSteamID member = m_vecQueuedRoster[i];

		if ( nWritten++ > 0 )
			body.PutChar( ',' );

		body.PutChar( '{' );
		AppendJSONField( body, "steam_id", CFmtStr( "%llu", member.ConvertToUint64() ) );

		if ( steamapicontext && steamapicontext->SteamFriends() )
		{
			body.PutChar( ',' );
			AppendJSONField( body, "name", steamapicontext->SteamFriends()->GetFriendPersonaName( member ) );
		}

		// Only our own ticket, and only for ourselves: an auth ticket proves
		// who we are and nothing about anybody else.
		if ( member == LocalSteamID() )
		{
			const char *pszTicket = m_coordinator.GetAuthTicket();
			if ( pszTicket && pszTicket[0] )
			{
				body.PutChar( ',' );
				AppendJSONField( body, "ticket", pszTicket );
			}
		}
		body.PutChar( '}' );
	}
	body.PutChar( ']' );

	CUtlVector< CUtlString > vecMaps;
	CollectSelectedMaps( vecMaps );
	if ( vecMaps.Count() > 0 )
	{
		body.PutString( ",\"maps\":[" );
		FOR_EACH_VEC( vecMaps, i )
		{
			if ( i > 0 )
				body.PutChar( ',' );
			AppendJSONString( body, vecMaps[i].Get() );
		}
		body.PutChar( ']' );
	}

	body.PutChar( '}' );
	body.PutChar( '\0' );

	if ( !m_coordinator.BSend( k_EHTTPMethodPOST, "/v1/queue", (const char *)body.Base(),
	                           &CTFMMBackend::QueueReplyThunk, this ) )
	{
		Fail( "could not reach the coordinator" );
	}
}

//-----------------------------------------------------------------------------
void CTFMMBackend::QueueReplyThunk( GCSDK::CWebAPIValues *pValues, int eStatusCode, void *pContext )
{
	( (CTFMMBackend *)pContext )->OnQueueReply( pValues, eStatusCode );
}

//-----------------------------------------------------------------------------
void CTFMMBackend::OnQueueReply( GCSDK::CWebAPIValues *pValues, int eStatusCode )
{
	if ( m_eState != k_eTFMMState_Searching )
		return; // cancelled while the request was in flight

	if ( eStatusCode != 200 || !pValues )
	{
		CUtlString strError;
		if ( pValues )
			pValues->GetChildStringValue( strError, "error", "" );

		Warning( "[mm] the coordinator refused the queue request%s%s\n",
		         strError.IsEmpty() ? "" : ": ", strError.Get() );
		Fail( strError.IsEmpty() ? "the coordinator refused the queue request" : strError.Get() );
		return;
	}

	CUtlString strTicket;
	pValues->GetChildStringValue( strTicket, "ticket_id", "" );
	if ( strTicket.IsEmpty() )
	{
		Fail( "the coordinator accepted the queue request but returned no ticket" );
		return;
	}

	m_strTicketID = strTicket;
	m_nPollIntervalMS = MAX( 500, ReadJSONInt( pValues, "poll_after_ms", 2000 ) );
	m_flNextPollTime = Plat_FloatTime() + ( m_nPollIntervalMS / 1000.f );

	MMDbg( "queued, ticket %s\n", m_strTicketID.Get() );

	// Members of the party follow the leader in, so tell them where.
	if ( m_party.BValid() && m_party.BIsLeader() )
		m_party.SetLobbyData( "queue_group", CFmtStr( "%d", (int)m_eQueuedMatchGroup ) );
}

//-----------------------------------------------------------------------------
void CTFMMBackend::PollQueue()
{
	m_flNextPollTime = Plat_FloatTime() + ( m_nPollIntervalMS / 1000.f );

	CFmtStr path( "/v1/queue/%s", m_strTicketID.Get() );
	if ( !m_coordinator.BSend( k_EHTTPMethodGET, path.Get(), NULL,
	                           &CTFMMBackend::QueueStatusThunk, this ) )
	{
		// A send that could not even start is a transient problem; the next
		// poll will try again. Only a refusal from the coordinator cancels.
		MMDbg( "poll could not be sent, will retry\n" );
	}
}

//-----------------------------------------------------------------------------
void CTFMMBackend::QueueStatusThunk( GCSDK::CWebAPIValues *pValues, int eStatusCode, void *pContext )
{
	( (CTFMMBackend *)pContext )->OnQueueStatus( pValues, eStatusCode );
}

//-----------------------------------------------------------------------------
void CTFMMBackend::OnQueueStatus( GCSDK::CWebAPIValues *pValues, int eStatusCode )
{
	if ( m_eState != k_eTFMMState_Searching )
		return;

	if ( eStatusCode == 0 || !pValues )
	{
		// Could not reach the coordinator. Keep waiting: a queue that gives up
		// on one dropped poll is worse than one that waits out a hiccup.
		MMDbg( "poll failed, still queued\n" );
		return;
	}

	if ( eStatusCode == 404 )
	{
		Fail( "the queue ticket is no longer valid" );
		return;
	}

	if ( eStatusCode != 200 )
	{
		MMDbg( "poll returned HTTP %d\n", eStatusCode );
		return;
	}

	m_nPollIntervalMS = MAX( 500, ReadJSONInt( pValues, "poll_after_ms", m_nPollIntervalMS ) );
	m_nInQueue        = ReadJSONInt( pValues, "in_queue", m_nInQueue );
	m_nNeedPlayers    = ReadJSONInt( pValues, "need_players", m_nNeedPlayers );

	// Why the queue is not moving, when the coordinator knows. A match that
	// formed and is waiting for a free server looks exactly like an empty
	// queue from here, and they are not the same thing to wait through.
	pValues->GetChildStringValue( m_strQueueDetail, "detail", "" );

	CUtlString strState;
	pValues->GetChildStringValue( strState, "state", "" );

	if ( !V_stricmp( strState.Get(), "cancelled" ) || !V_stricmp( strState.Get(), "expired" ) )
	{
		Fail( "the queue ticket is no longer valid" );
		return;
	}
	if ( !V_stricmp( strState.Get(), "failed" ) )
	{
		CUtlString strError;
		pValues->GetChildStringValue( strError, "error", "" );
		Fail( strError.IsEmpty() ? "the coordinator refused the queue request" : strError.Get() );
		return;
	}
	if ( V_stricmp( strState.Get(), "assigned" ) != 0 )
		return; // still searching

	GCSDK::CWebAPIValues *pAssignment = pValues->FindChild( "assignment" );
	if ( !pAssignment )
	{
		MMDbg( "assigned with no assignment; waiting\n" );
		return;
	}

	CUtlString strConnect, strPassword, strMap, strMatchID, strSTV;
	pAssignment->GetChildStringValue( strConnect, "connect", "" );
	pAssignment->GetChildStringValue( strPassword, "password", "" );
	pAssignment->GetChildStringValue( strMap, "map", "" );
	pAssignment->GetChildStringValue( strMatchID, "match_id", "" );
	pAssignment->GetChildStringValue( strSTV, "stv", "" );

	if ( strConnect.IsEmpty() )
	{
		Fail( "the coordinator sent a match with no server to connect to" );
		return;
	}

	m_strConnect = strConnect;
	m_strPassword = strPassword;
	m_strSTV = strSTV;
	m_strMatchID = strMatchID;

	//
	// Build the lobby object the UI reads. State RUN plus a connect string is
	// what makes the stock client say it has a match and offer to join it.
	//
	m_msgLobby.Clear();
	m_msgLobby.set_lobby_id( m_party.GetPartyID() != 0 ? m_party.GetPartyID() : 1 );
	m_msgLobby.set_state( CSOTFGameServerLobby_State_RUN );
	m_msgLobby.set_connect( strConnect.Get() );
	m_msgLobby.set_match_group( (uint32)m_eQueuedMatchGroup );

	GCSDK::CWebAPIValues *pRoster = pAssignment->FindChild( "roster" );
	if ( pRoster )
	{
		for ( GCSDK::CWebAPIValues *pPlayer = pRoster->GetFirstChild();
		      pPlayer != NULL;
		      pPlayer = pPlayer->GetNextChild() )
		{
			CUtlString strID;
			pPlayer->GetChildStringValue( strID, "steam_id", "" );
			uint64 ulID = Q_atoui64( strID.Get() );
			if ( ulID == 0 )
				continue;

			CUtlString strName;
			pPlayer->GetChildStringValue( strName, "name", "" );

			CTFLobbyPlayerProto *pMember = m_msgLobby.add_members();
			pMember->set_id( ulID );
			pMember->set_name( strName.Get() );
			pMember->set_type( CTFLobbyPlayerProto_Type_MATCH_PLAYER );

			// The coordinator speaks in the game's team indices; the lobby
			// object speaks in the GC's, where defenders are red.
			const int nTeam = ReadJSONInt( pPlayer, "team", 0 );
			if ( nTeam == TF_TEAM_RED )
				pMember->set_team( TF_GC_TEAM_DEFENDERS );
			else if ( nTeam == TF_TEAM_BLUE )
				pMember->set_team( TF_GC_TEAM_INVADERS );
			else
				pMember->set_team( TF_GC_TEAM_PLAYER_POOL );
		}
	}

	PublishLobby();

	// The party is no longer queued -- it is in a match.
	m_msgParty.clear_matchmaking_queues();
	m_msgParty.set_associated_lobby_id( m_msgLobby.lobby_id() );
	m_msgParty.set_associated_lobby_match_group( m_eQueuedMatchGroup );
	PublishParty();

	Msg( "Match found: %s%s%s\n", strConnect.Get(),
	     strMap.IsEmpty() ? "" : " on ", strMap.Get() );

	if ( m_party.BValid() && m_party.BIsLeader() )
	{
		// Party members never talk to the coordinator; the leader queued for
		// all of them. This is the whole handover.
		CUtlString strTeams;
		for ( int i = 0; i < m_msgLobby.members_size(); i++ )
		{
			const CTFLobbyPlayerProto &member = m_msgLobby.members( i );
			strTeams += CFmtStr( "%s%llu:%d", strTeams.IsEmpty() ? "" : ",",
			                     (unsigned long long)member.id(), (int)member.team() );
		}

		m_party.SetLobbyData( "connect", strConnect.Get() );
		m_party.SetLobbyData( "password", strPassword.Get() );
		m_party.SetLobbyData( "match_id", strMatchID.Get() );
		m_party.SetLobbyData( "stv", strSTV.Get() );
		m_party.SetLobbyData( "teams", strTeams.Get() );
	}

	EnterState( k_eTFMMState_MatchReady );

	if ( tf_mm_autojoin.GetBool() )
		JoinAssignedMatch();
}

//-----------------------------------------------------------------------------
// Purpose: Publish the server we are on into the party's lobby data.
//
//			A matchmade match travels as "connect"/"match_id", which every
//			member acts on. This is the other case, and it was missing: the
//			leader on a community server, whose party had no way to know where
//			that was. Steam's "Join Game" hands a joiner our party lobby -- it
//			cannot hand them a server we never told anyone about -- so without
//			this, joining a friend who was playing put you in an empty party.
//-----------------------------------------------------------------------------
void CTFMMBackend::PublishPartyServer( bool bInGame )
{
	if ( !m_party.BValid() || !m_party.BIsLeader() )
		return;

	CUtlString strServer;

	// A matchmade match is already published, and publishing it twice would
	// have members racing between two ways of joining the same game.
	const bool bMatchmade = ( m_eState == k_eTFMMState_MatchReady ||
	                          m_eState == k_eTFMMState_Connecting ||
	                          m_eState == k_eTFMMState_InMatch );
	if ( bInGame && !bMatchmade )
	{
		INetChannelInfo *pChan = engine->GetNetChannelInfo();
		const char *pszAddr = pChan ? pChan->GetAddress() : NULL;
		if ( pszAddr && pszAddr[0] && !V_stristr( pszAddr, "loopback" ) )
			strServer = pszAddr;
	}

	if ( strServer == m_strPublishedServer )
		return;

	m_strPublishedServer = strServer;
	m_party.SetLobbyData( "server", strServer.Get() );
}

//-----------------------------------------------------------------------------
void CTFMMBackend::OnPartyLobbyEntered()
{
	m_bFollowLeaderServer = !m_party.BIsLeader();
	OnPartyLobbyDataChanged();
}

//-----------------------------------------------------------------------------
// A party member watching the leader.
//
// Members never hold a queue ticket. The leader queued on behalf of everybody,
// and publishes the result into the lobby -- so for a member, "a match was
// found" is a lobby data update and nothing more.
//-----------------------------------------------------------------------------
void CTFMMBackend::OnPartyLobbyDataChanged()
{
	if ( !BActive() || !m_party.BValid() || m_party.BIsLeader() )
		return;

	const char *pszMatchID = m_party.GetLobbyData( "match_id" );
	if ( !pszMatchID || !pszMatchID[0] )
	{
		// The leader has no match. If we were following one, we are not any
		// more -- but do not tear down a match we have already joined.
		if ( m_eState == k_eTFMMState_MatchReady )
			EnterState( k_eTFMMState_Idle );

		// They may still be playing somewhere -- a community server, a server
		// they found in the browser. That is what we just asked to join.
		FollowLeaderToServer();
		return;
	}

	if ( m_strMatchID == pszMatchID )
		return; // already acted on this one

	const char *pszConnect = m_party.GetLobbyData( "connect" );
	if ( !pszConnect || !pszConnect[0] )
		return; // the leader is mid-publish; the next update will have it

	int nGroup = atoi( m_party.GetLobbyData( "queue_group" ) );
	AdoptAssignment( pszMatchID, pszConnect,
	                 m_party.GetLobbyData( "password" ),
	                 m_party.GetLobbyData( "stv" ),
	                 m_party.GetLobbyData( "teams" ),
	                 (ETFMatchGroup)nGroup );
}

//-----------------------------------------------------------------------------
// Purpose: Spend the one-shot follow on whatever server the leader is on.
//-----------------------------------------------------------------------------
void CTFMMBackend::FollowLeaderToServer()
{
	if ( !m_bFollowLeaderServer )
		return;

	const char *pszServer = m_party.GetLobbyData( "server" );
	if ( !pszServer || !pszServer[0] )
		return; // they are at the menu, or have not said yet

	// Spend it whether or not the connect works out. Retrying forever would
	// mean a member who deliberately left the leader's server gets dragged
	// back the next time anything in the lobby changes.
	m_bFollowLeaderServer = false;

	INetChannelInfo *pChan = engine->GetNetChannelInfo();
	if ( pChan && pChan->GetAddress() && !V_stricmp( pChan->GetAddress(), pszServer ) )
		return; // already there

	Msg( "Joining your party on %s\n", pszServer );
	engine->ClientCmd_Unrestricted( CFmtStr( "connect %s", pszServer ) );
}

//-----------------------------------------------------------------------------
void CTFMMBackend::AdoptAssignment( const char *pszMatchID, const char *pszConnect,
                                    const char *pszPassword, const char *pszSTV,
                                    const char *pszTeams, ETFMatchGroup eMatchGroup )
{
	m_strMatchID = pszMatchID;
	m_strConnect = pszConnect;
	m_strPassword = pszPassword ? pszPassword : "";
	m_strSTV = pszSTV ? pszSTV : "";
	m_eQueuedMatchGroup = eMatchGroup;

	m_msgLobby.Clear();
	m_msgLobby.set_lobby_id( m_party.GetPartyID() != 0 ? m_party.GetPartyID() : 1 );
	m_msgLobby.set_state( CSOTFGameServerLobby_State_RUN );
	m_msgLobby.set_connect( m_strConnect.Get() );
	m_msgLobby.set_match_group( (uint32)eMatchGroup );

	// "76561198000000001:0,76561198000000002:1" -- id and GC team, the same
	// pairs the leader read off its own assignment.
	CUtlVector< CUtlString > vecEntries;
	V_SplitString( pszTeams ? pszTeams : "", ",", vecEntries );
	FOR_EACH_VEC( vecEntries, i )
	{
		CUtlVector< CUtlString > vecPair;
		V_SplitString( vecEntries[i].Get(), ":", vecPair );
		if ( vecPair.Count() != 2 )
			continue;

		const uint64 ulID = Q_atoui64( vecPair[0].Get() );
		if ( ulID == 0 )
			continue;

		CTFLobbyPlayerProto *pMember = m_msgLobby.add_members();
		pMember->set_id( ulID );
		pMember->set_team( (TF_GC_TEAM)atoi( vecPair[1].Get() ) );
		pMember->set_type( CTFLobbyPlayerProto_Type_MATCH_PLAYER );
	}

	// The party is in a match either way -- that is what makes the stock UI
	// offer "join your party's match", and it is how QueueForStandby knows
	// which match to ask for.
	m_msgParty.clear_matchmaking_queues();
	m_msgParty.set_associated_lobby_id( m_msgLobby.lobby_id() );
	m_msgParty.set_associated_lobby_match_group( eMatchGroup );
	PublishParty();

	// A member who joined the party after the match formed has no seat in it,
	// and with the roster gate up the server will turn them away. So do not
	// publish the lobby object for them: that object means "I am in this
	// match", it is what BHaveLiveMatch reads, and claiming it would both send
	// them at a closed door and switch off the standby button that is the way
	// through it.
	if ( !BSeatedInAssignment() )
	{
		DestroyLobbySO();
		Msg( "Your party is playing on %s and you have no seat in it yet. "
		     "Ask to join the match%s.\n",
		     m_strConnect.Get(),
		     m_strSTV.IsEmpty() ? "" : ", or use tf_mm_watch to watch it" );
		m_party.UpdateRichPresence();

		// Not our match, so the state stays Idle: the queue bar must not come
		// up for a match we are only standing next to.
		if ( gameeventmanager )
		{
			IGameEvent *pEvent = gameeventmanager->CreateEvent( "lobby_updated" );
			if ( pEvent )
				gameeventmanager->FireEventClientSide( pEvent );
		}
		if ( GTFPartyClient() )
			GTFPartyClient()->ForcePartyUpdate();
		return;
	}

	PublishLobby();

	Msg( "Your party is playing on %s\n", m_strConnect.Get() );

	EnterState( k_eTFMMState_MatchReady );

	if ( tf_mm_autojoin.GetBool() )
		JoinAssignedMatch();
}

//-----------------------------------------------------------------------------
// Purpose: Does the assignment we just adopted actually have a seat for us?
//
//			The roster travels with the assignment as the lobby's member list.
//			An empty list is not an answer -- an older client publishes none --
//			so it counts as yes, which keeps the old behaviour rather than
//			locking somebody out on missing information.
//-----------------------------------------------------------------------------
bool CTFMMBackend::BSeatedInAssignment() const
{
	if ( m_msgLobby.members_size() == 0 )
		return true;

	const uint64 ulMe = LocalSteamID().ConvertToUint64();
	for ( int i = 0; i < m_msgLobby.members_size(); i++ )
	{
		if ( m_msgLobby.members( i ).id() == ulMe )
			return true;
	}
	return false;
}

//-----------------------------------------------------------------------------
void CTFMMBackend::CancelQueue()
{
	// Searching is the obvious case. MatchReady is the one that mattered: the
	// coordinator had handed us a server and the player had not connected to
	// it yet, and refusing to act here left the queue bar up with an X that
	// did nothing. Giving up an assignment we never took is the player's to
	// decide; once we are actually connecting or on the server it is not a
	// queue any more and "cancel" means disconnect, which is not ours.
	if ( m_eState != k_eTFMMState_Searching && m_eState != k_eTFMMState_MatchReady )
		return;

	SendQueueCancel();

	m_strTicketID.Clear();
	m_msgParty.clear_matchmaking_queues();
	PublishParty();
	EnterState( k_eTFMMState_Idle );
}

//-----------------------------------------------------------------------------
void CTFMMBackend::SendQueueCancel()
{
	if ( m_strTicketID.IsEmpty() )
		return;

	// A cancel that cannot be sent right now still takes us out of queue
	// locally; the coordinator expires the ticket when we stop polling it.
	m_coordinator.Cancel();

	CFmtStr path( "/v1/queue/%s", m_strTicketID.Get() );
	m_coordinator.BSend( k_EHTTPMethodDELETE, path.Get(), NULL, NULL, NULL );
}

//-----------------------------------------------------------------------------
void CTFMMBackend::JoinAssignedMatch()
{
	if ( m_strConnect.IsEmpty() )
	{
		Warning( "No match to join.\n" );
		return;
	}

	if ( !m_strPassword.IsEmpty() )
	{
		// The server is passworded so the browser cannot walk into a matchmade
		// game. Set it before connecting, not after.
		engine->ClientCmd_Unrestricted( CFmtStr( "password \"%s\"", m_strPassword.Get() ) );
	}

	m_flConnectStartTime = Plat_FloatTime();
	// If we are on a server right now, arriving means leaving that one first.
	// See the Connecting case in Update().
	m_bConnectLeftOldServer = !( engine->IsInGame() && !engine->IsLevelMainMenuBackground() );
	EnterState( k_eTFMMState_Connecting );
	GTFGCClientSystem()->ConnectToServer( m_strConnect.Get() );
}

//-----------------------------------------------------------------------------
void CTFMMBackend::EnterState( ETFMMState eState )
{
	if ( m_eState == eState )
		return;

	MMDbg( "state %d -> %d\n", (int)m_eState, (int)eState );
	m_eState = eState;

	// The detail line belongs to one search. Leaving it up after the state
	// changes would have the menu still explaining a queue nobody is in.
	if ( eState != k_eTFMMState_Searching )
		m_strQueueDetail.Clear();

	if ( eState == k_eTFMMState_Idle )
	{
		m_eQueuedMatchGroup = k_eTFMatchGroup_Invalid;
		m_strStandbyMatchID.Clear();
		DestroyLobbySO();
		m_msgParty.clear_associated_lobby_id();
		m_msgParty.clear_associated_lobby_match_group();
		m_strConnect.Clear();
		m_strPassword.Clear();
		m_strSTV.Clear();
		m_strMatchID.Clear();

		// A member that joins later must not be sent to a match that is over.
		if ( m_party.BValid() && m_party.BIsLeader() )
		{
			m_party.SetLobbyData( "match_id", "" );
			m_party.SetLobbyData( "connect", "" );
			m_party.SetLobbyData( "password", "" );
			m_party.SetLobbyData( "stv", "" );
			m_party.SetLobbyData( "teams", "" );
			m_party.SetLobbyData( "queue_group", "" );
		}
	}

	m_party.UpdateRichPresence();

	// The party client latches "am I in queue" and only re-evaluates it when
	// the party object changes. Our own state is one of its inputs
	// (UpdateActiveParty consults GetState()), and every path that leaves the
	// queue publishes the party *before* it gets here -- so without this the
	// last thing it ever saw was a backend that said "searching", and the
	// queue bar stayed up for the rest of the session with nothing able to
	// take it down.
	if ( GTFPartyClient() )
		GTFPartyClient()->ForcePartyUpdate();

	// The dashboard, the playlist and the party panel all redraw off this.
	if ( gameeventmanager )
	{
		IGameEvent *pEvent = gameeventmanager->CreateEvent( "lobby_updated" );
		if ( pEvent )
			gameeventmanager->FireEventClientSide( pEvent );
	}
}

//-----------------------------------------------------------------------------
void CTFMMBackend::Fail( const char *pszReason )
{
	// A refused standby seat is not the same thing as the party leaving its
	// match. Preserve the local associated lobby that makes the stock standby
	// button available, so a temporary full/error response can be retried.
	const bool bWasStandby = !m_strStandbyMatchID.IsEmpty();
	const bool bHadAssociation = bWasStandby && m_msgParty.has_associated_lobby_id();
	const uint64 ulAssociatedLobby = bHadAssociation ? m_msgParty.associated_lobby_id() : 0;
	const ETFMatchGroup eAssociatedGroup = bHadAssociation
		? (ETFMatchGroup)m_msgParty.associated_lobby_match_group()
		: k_eTFMatchGroup_Invalid;

	m_strLastError = pszReason ? pszReason : "";
	m_strTicketID.Clear();
	m_msgParty.clear_matchmaking_queues();
	EnterState( k_eTFMMState_Idle );

	if ( bHadAssociation )
	{
		m_msgParty.set_associated_lobby_id( ulAssociatedLobby );
		m_msgParty.set_associated_lobby_match_group( eAssociatedGroup );
	}
	PublishParty();
	if ( GTFPartyClient() )
		GTFPartyClient()->ForcePartyUpdate();

	Warning( "Matchmaking stopped: %s\n", m_strLastError.Get() );
}

//-----------------------------------------------------------------------------
void CTFMMBackend::Spew() const
{
	Msg( "Team Frontress matchmaking\n" );
	Msg( "  coordinator : %s\n", tf_mm_coordinator.GetString() );
	Msg( "  active      : %s\n", BActive() ? "yes" : "no" );
	Msg( "  state       : %d\n", (int)m_eState );
	Msg( "  party lobby : %llu (%d members, %s)\n",
	     (unsigned long long)m_party.GetPartyID(), m_party.GetNumMembers(),
	     m_party.BIsLeader() ? "leader" : "member" );
	Msg( "  queue ticket: %s\n", m_strTicketID.IsEmpty() ? "(none)" : m_strTicketID.Get() );
	Msg( "  connect     : %s\n", m_strConnect.IsEmpty() ? "(none)" : m_strConnect.Get() );
	Msg( "  sourcetv    : %s\n", m_strSTV.IsEmpty() ? "(none)" : m_strSTV.Get() );
	Msg( "  match id    : %s\n", m_strMatchID.IsEmpty() ? "(none)" : m_strMatchID.Get() );
	if ( !m_strLastError.IsEmpty() )
		Msg( "  last error  : %s\n", m_strLastError.Get() );

	// Which modes the menu will show, and why. This is the one thing that
	// cannot be worked out from the outside: a mode missing from the play list
	// and a mode the coordinator never mentioned look identical on screen.
	if ( !m_bGroupsKnown )
	{
		Msg( "  groups      : the coordinator has not answered yet\n" );
	}
	else
	{
		Msg( "  groups      :\n" );
		for ( int i = 0; i < k_nMatchGroupPools; i++ )
		{
			Msg( "    %-30s %-12s %d maps\n",
			     GetMatchGroupName( (ETFMatchGroup)i ),
			     m_arGroupOffered[i] ? "offered" : "not offered",
			     m_arGroupMaps[i].Count() );
		}
	}
}

//
// Console commands
//

CON_COMMAND( tf_mm_status, "Print what the matchmaking backend is doing." )
{
	TFMMBackend()->Spew();
}

CON_COMMAND( tf_mm_queue, "Queue for a match group (see ETFMatchGroup; 7 is casual 12v12)." )
{
	if ( args.ArgC() < 2 )
	{
		Msg( "Usage: tf_mm_queue <match_group>\n" );
		return;
	}
	TFMMBackend()->QueueForMatch( (ETFMatchGroup)atoi( args.Arg( 1 ) ) );
}

CON_COMMAND( tf_mm_cancel, "Leave the matchmaking queue." )
{
	TFMMBackend()->CancelQueue();
}

CON_COMMAND( tf_mm_join, "Connect to the match matchmaking assigned you." )
{
	TFMMBackend()->JoinAssignedMatch();
}

CON_COMMAND( tf_mm_watch, "Watch a match on SourceTV. With no argument, the one you are in; "
                          "with a SteamID64, the one that friend is in." )
{
	CUtlString strSTV;

	if ( args.ArgC() > 1 )
	{
		const uint64 ulID = Q_atoui64( args[1] );
		if ( ulID == 0 )
		{
			Msg( "Usage: tf_mm_watch [steamid64]\n" );
			return;
		}

		const char *pszFound = CTFMMParty::GetFriendSTV( CSteamID( ulID ) );
		if ( !pszFound || !pszFound[0] )
		{
			Msg( "That player is not in a match with a SourceTV relay, or is not on your friends list.\n" );
			return;
		}
		strSTV = pszFound;
	}
	else
	{
		strSTV = TFMMBackend()->GetSTVAddress();
		if ( strSTV.IsEmpty() )
		{
			Msg( "This match has no SourceTV relay.\n" );
			return;
		}
	}

	// Spectating is not playing: the match password does not apply, and we
	// must not leave it set from a previous connect.
	engine->ClientCmd_Unrestricted( "password \"\"" );
	engine->ClientCmd_Unrestricted( CFmtStr( "connect %s", strSTV.Get() ) );
}
