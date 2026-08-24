//========= Copyright Team Frontress, All rights reserved. ====================//
//
// Purpose: The local game coordinator. See tf_mm_backend.h.
//
//=============================================================================//

#include "cbase.h"

#include "tf_mm_backend.h"

#include "clientsteamcontext.h"
#include "fmtstr.h"
#include "gc_clientsystem.h"
#include "gcsdk/webapi_response.h"
#include "rtime.h"
#include "tf_gc_client.h"
#include "tf_item_schema.h"
#include "tf_matchcriteria.h"
#include "tf_matchmaking_shared.h"
#include "tf_shareddefs.h"
#include "tf_lobby_server.h"
#include "tf_party.h"
#include "tf_partyclient.h"

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
	, m_nPollIntervalMS( 2000 )
	, m_nInQueue( 0 )
	, m_nNeedPlayers( 0 )
	, m_flNextStatusPoll( 0.f )
{
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
	}

	// The party object tracks the Steam lobby, which changes underneath us.
	// Republishing is cheap and idempotent; only do it while a lobby exists.
	if ( m_party.BValid() )
		PublishParty();

	if ( m_eState == k_eTFMMState_Searching &&
	     !m_strTicketID.IsEmpty() &&
	     Plat_FloatTime() >= m_flNextPollTime &&
	     !m_coordinator.BBusy() )
	{
		PollQueue();
	}

	// Population for the menu. Nobody is looking at it during a match, so only
	// keep it fresh while we are out of one.
	if ( !engine->IsInGame() &&
	     Plat_FloatTime() >= m_flNextStatusPoll &&
	     !m_statusFeed.BBusy() )
	{
		PollStatus();
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

	m_status.bChecked       = true;
	m_status.bValid         = true;
	m_status.nOnlinePlayers = pValues->GetChildInt32Value( "online_players", 0 );
	m_status.nLiveMatches   = pValues->GetChildInt32Value( "live_matches", 0 );
	m_status.nFreeServers   = pValues->GetChildInt32Value( "free_servers", 0 );
	pValues->GetChildStringValue( m_status.strName, "name", "" );
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
			Warning( "[mm] could not publish the party object; matchmaking UI will not work\n" );
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

		// Standby -- joining a match your party is already in -- needs the
		// coordinator to be able to add a player to a running match. It cannot
		// yet, so answer the message (the client would retry forever
		// otherwise) and change nothing.
		case k_EMsgGCParty_QueueForStandby:
		case k_EMsgGCParty_RemoveFromStandbyQueue:
			return true;

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
void CTFMMBackend::CollectSelectedMaps( CUtlVector< CUtlString > &vecOut ) const
{
	if ( !GTFPartyClient() )
		return;

	CCasualCriteriaHelper helper = GTFPartyClient()->GetEffectiveGroupCriteria().GetCasualCriteriaHelper();
	if ( !helper.AnySelected() )
		return;

	const CUtlVector< MapDef_t * > &vecMaps = GetItemSchema()->GetMasterMapsList();
	FOR_EACH_VEC( vecMaps, i )
	{
		const MapDef_t *pMap = vecMaps[i];
		if ( pMap && pMap->pszMapName && helper.IsMapSelected( pMap ) )
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
void CTFMMBackend::SendQueueRequest()
{
	CUtlBuffer body( 0, 2048, CUtlBuffer::TEXT_BUFFER );

	body.Printf( "{\"match_group\":%d,", (int)m_eQueuedMatchGroup );
	body.Printf( "\"late_join_ok\":%s,",
	             ( GTFPartyClient() && GTFPartyClient()->GetEffectiveGroupCriteria().GetLateJoin() ) ? "true" : "false" );
	body.PutString( "\"leader\":" );
	AppendJSONString( body, CFmtStr( "%llu", m_party.GetLeader().ConvertToUint64() ) );

	body.PutString( ",\"players\":[" );
	const int nMembers = MAX( 1, m_party.GetNumMembers() );
	int nWritten = 0;
	for ( int i = 0; i < nMembers; i++ )
	{
		CSteamID member = m_party.BValid() ? m_party.GetMember( i ) : LocalSteamID();
		if ( !member.IsValid() )
			continue;

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
	m_nPollIntervalMS = MAX( 500, pValues->GetChildInt32Value( "poll_after_ms", 2000 ) );
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

	m_nPollIntervalMS = MAX( 500, pValues->GetChildInt32Value( "poll_after_ms", m_nPollIntervalMS ) );
	m_nInQueue        = pValues->GetChildInt32Value( "in_queue", m_nInQueue );
	m_nNeedPlayers    = pValues->GetChildInt32Value( "need_players", m_nNeedPlayers );

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
			const int nTeam = pPlayer->GetChildInt32Value( "team", 0 );
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

	PublishLobby();

	m_msgParty.clear_matchmaking_queues();
	m_msgParty.set_associated_lobby_id( m_msgLobby.lobby_id() );
	m_msgParty.set_associated_lobby_match_group( eMatchGroup );
	PublishParty();

	Msg( "Your party is playing on %s\n", m_strConnect.Get() );

	EnterState( k_eTFMMState_MatchReady );

	if ( tf_mm_autojoin.GetBool() )
		JoinAssignedMatch();
}

//-----------------------------------------------------------------------------
void CTFMMBackend::CancelQueue()
{
	if ( m_eState != k_eTFMMState_Searching )
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

	if ( eState == k_eTFMMState_Idle )
	{
		m_eQueuedMatchGroup = k_eTFMatchGroup_Invalid;
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
	m_strLastError = pszReason ? pszReason : "";
	m_strTicketID.Clear();
	m_msgParty.clear_matchmaking_queues();
	PublishParty();
	EnterState( k_eTFMMState_Idle );

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

CON_COMMAND( tf_mm_watch, "Connect to the SourceTV relay for your current match." )
{
	const char *pszSTV = TFMMBackend()->GetSTVAddress();
	if ( !pszSTV || !pszSTV[0] )
	{
		Msg( "This match has no SourceTV relay.\n" );
		return;
	}

	// Spectating is not playing: the match password does not apply, and we
	// must not leave it set from a previous connect.
	engine->ClientCmd_Unrestricted( "password \"\"" );
	engine->ClientCmd_Unrestricted( CFmtStr( "connect %s", pszSTV ) );
}
