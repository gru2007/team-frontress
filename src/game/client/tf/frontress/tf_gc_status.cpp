//========= Copyright Team Frontress, All rights reserved. ====================//
//
// Purpose: What the main menu shows about the game coordinator. See header.
//
//=============================================================================//

#include "cbase.h"
#include "tf_gc_status.h"
#include "frontress_gc.h"
#include "tf_gc_client.h"
#include "tf_party.h"
#include "tf_partyclient.h"
#include "tf_lobby_shared.h"
#include "gcsdk/webapi_response.h"
#include "fmtstr.h"

// memdbgon must be the last include file in a .cpp file!!!
#include "tier0/memdbgon.h"


// How often the menu asks. The numbers are for people to read, not to act on.
static const float k_flStatusPollSeconds = 15.f;

static CTFGCStatus s_TFGCStatus;
CTFGCStatus *TFGCStatus() { return &s_TFGCStatus; }

//-----------------------------------------------------------------------------
CTFGCStatus::CTFGCStatus()
	: CAutoGameSystemPerFrame( "CTFGCStatus" )
	, m_bGroupsKnown( false )
	, m_nMapPoolGeneration( 0 )
	, m_flNextPoll( 0.f )
	, m_hRequest( INVALID_HTTPREQUEST_HANDLE )
{
}

bool CTFGCStatus::BActive() const
{
	return GCClientSystem() && GCClientSystem()->BConnectedtoGC();
}

void CTFGCStatus::Shutdown()
{
	if ( m_hRequest != INVALID_HTTPREQUEST_HANDLE && SteamHTTP() )
		SteamHTTP()->ReleaseHTTPRequest( m_hRequest );
	m_hRequest = INVALID_HTTPREQUEST_HANDLE;
	m_callStatus.Cancel();
}

void CTFGCStatus::Update( float )
{
	if ( m_hRequest != INVALID_HTTPREQUEST_HANDLE || Plat_FloatTime() < m_flNextPoll )
		return;
	// Nobody reads the menu numbers mid-match.
	if ( engine->IsInGame() && !engine->IsLevelMainMenuBackground() )
		return;
	Poll();
}

void CTFGCStatus::Poll()
{
	m_flNextPoll = (float)Plat_FloatTime() + ( GetState() == k_eTFMMState_Searching ? 5.f : k_flStatusPollSeconds );
	if ( FrontressGC()->BConnected() )
		GTFGCClientSystem()->RequestMatchMakerStats();
	ISteamHTTP *pHTTP = SteamHTTP();
	const char *pszBase = FrontressGC()->GetBaseURL();
	if ( !pHTTP || !pszBase || !pszBase[0] )
		return;

	CUtlString strBase( pszBase );
	strBase.StripTrailingSlash();
	HTTPRequestHandle h = pHTTP->CreateHTTPRequest( k_EHTTPMethodGET, CFmtStr1024( "%s/v1/status", strBase.Get() ) );
	if ( h == INVALID_HTTPREQUEST_HANDLE )
		return;
	pHTTP->SetHTTPRequestNetworkActivityTimeout( h, 10 );
	SteamAPICall_t hCall = k_uAPICallInvalid;
	if ( !pHTTP->SendHTTPRequest( h, &hCall ) )
	{
		pHTTP->ReleaseHTTPRequest( h );
		return;
	}
	m_hRequest = h;
	m_callStatus.Set( hCall, this, &CTFGCStatus::OnStatus );
}

static int JSONInt( GCSDK::CWebAPIValues *pValues, const char *pszName )
{
	GCSDK::CWebAPIValues *pChild = pValues ? pValues->FindChild( pszName ) : NULL;
	if ( !pChild )
		return 0;
	switch ( pChild->GetType() )
	{
		case GCSDK::k_EWebAPIValueType_Int32:  return pChild->GetInt32Value();
		case GCSDK::k_EWebAPIValueType_UInt32: return (int)pChild->GetUInt32Value();
		case GCSDK::k_EWebAPIValueType_Int64:  return (int)pChild->GetInt64Value();
		case GCSDK::k_EWebAPIValueType_UInt64: return (int)pChild->GetUInt64Value();
		case GCSDK::k_EWebAPIValueType_Double: return (int)pChild->GetDoubleValue();
		case GCSDK::k_EWebAPIValueType_Bool:   return pChild->GetBoolValue() ? 1 : 0;
		default: return 0;
	}
}

void CTFGCStatus::OnStatus( HTTPRequestCompleted_t *pInfo, bool bIOFailure )
{
	ISteamHTTP *pHTTP = SteamHTTP();
	m_hRequest = INVALID_HTTPREQUEST_HANDLE;
	m_status.bChecked = true;

	GCSDK::CWebAPIValues *pValues = NULL;
	if ( pHTTP && pInfo && !bIOFailure && pInfo->m_eStatusCode == k_EHTTPStatusCode200OK )
	{
		uint32 cub = 0;
		if ( pHTTP->GetHTTPResponseBodySize( pInfo->m_hRequest, &cub ) && cub > 0 && cub < ( 1 << 20 ) )
		{
			CUtlBuffer buf( 0, cub + 1, CUtlBuffer::TEXT_BUFFER );
			if ( pHTTP->GetHTTPResponseBodyData( pInfo->m_hRequest, (uint8 *)buf.Base(), cub ) )
			{
				buf.SeekPut( CUtlBuffer::SEEK_HEAD, cub );
				( (char *)buf.Base() )[ cub ] = '\0';
				pValues = GCSDK::CWebAPIValues::ParseJSON( buf );
			}
		}
	}
	if ( pHTTP && pInfo )
		pHTTP->ReleaseHTTPRequest( pInfo->m_hRequest );

	if ( !pValues )
	{
		// Nothing rather than something stale: the menu says "offline".
		const bool bChecked = m_status.bChecked;
		m_status = Status_t();
		m_status.bChecked = bChecked;
		return;
	}

	m_status.bValid = true;
	m_status.nOnlinePlayers = JSONInt( pValues, "online" );
	m_status.nLiveMatches = JSONInt( pValues, "live_matches" );
	m_status.nFreeServers = JSONInt( pValues, "free_servers" );
	// With an on-demand provider the free count is not the capacity.
	m_status.bServerCapacityKnown = ( JSONInt( pValues, "on_demand_servers" ) == 0 );
	pValues->GetChildStringValue( m_status.strName, "name", "" );

	for ( int i = 0; i < k_nGroups; i++ )
	{
		m_groups[i].bOffered = false;
		m_groups[i].nQueued = 0;
		m_groups[i].nNeed = 0;
		m_groups[i].vecMaps.RemoveAll();
		m_groups[i].vecMapQueued.RemoveAll();
		m_groups[i].vecMapPlaying.RemoveAll();
	}

	CUtlString strSignature;
	GCSDK::CWebAPIValues *pGroups = pValues->FindChild( "groups" );
	m_bGroupsKnown = ( pGroups != NULL );
	for ( GCSDK::CWebAPIValues *pGroup = pGroups ? pGroups->GetFirstChild() : NULL; pGroup; pGroup = pGroup->GetNextChild() )
	{
		const int nGroup = JSONInt( pGroup, "match_group" );
		if ( nGroup < 0 || nGroup >= k_nGroups )
			continue;
		Group_t &group = m_groups[ nGroup ];
		group.bOffered = true;
		group.nQueued = JSONInt( pGroup, "queued" );
		group.nNeed = JSONInt( pGroup, "need" );
		strSignature += CFmtStr( "%d:", nGroup );

		GCSDK::CWebAPIValues *pMaps = pGroup->FindChild( "maps" );
		GCSDK::CWebAPIValues *pQueued = pGroup->FindChild( "map_queued" );
		GCSDK::CWebAPIValues *pPlaying = pGroup->FindChild( "map_playing" );
		for ( GCSDK::CWebAPIValues *pMap = pMaps ? pMaps->GetFirstChild() : NULL; pMap; pMap = pMap->GetNextChild() )
		{
			CUtlString strMap;
			pMap->GetStringValue( strMap );
			if ( !strMap.IsEmpty() )
			{
				group.vecMaps.AddToTail( strMap );
				// An older coordinator says nothing per map: -1, not "nobody".
				group.vecMapQueued.AddToTail( pQueued && pQueued->FindChild( strMap.Get() ) ? JSONInt( pQueued, strMap.Get() ) : -1 );
				group.vecMapPlaying.AddToTail( pPlaying && pPlaying->FindChild( strMap.Get() ) ? JSONInt( pPlaying, strMap.Get() ) : -1 );
				strSignature += strMap;
				strSignature += ",";
			}
		}
	}
	delete pValues;

	// Only when it really changed: the map list rebuilds off this.
	if ( strSignature != m_strSignature )
	{
		m_strSignature = strSignature;
		m_nMapPoolGeneration++;
	}
}

//-----------------------------------------------------------------------------
// The player's own state, read from the objects the GC put in our cache.
//-----------------------------------------------------------------------------
static const CTFParty *ActiveParty()
{
	return GTFPartyClient() ? GTFPartyClient()->GetActiveParty() : NULL;
}

ETFMMState CTFGCStatus::GetState() const
{
	CTFGCClientSystem *pGC = GTFGCClientSystem();
	if ( pGC->BConnectingToMatchServer() && pGC->BHaveLiveMatch() )
		return k_eTFMMState_Connecting;
	if ( pGC->BConnectedToMatchServer( true ) )
		return k_eTFMMState_InMatch;
	if ( pGC->BCanJoinMatch() )
		return k_eTFMMState_MatchReady;
	if ( pGC->BHaveRunningMatch() )
		return k_eTFMMState_Preparing;
	if ( GetQueuedMatchGroup() != k_eTFMatchGroup_Invalid )
		return k_eTFMMState_Searching;
	return k_eTFMMState_Idle;
}

ETFMatchGroup CTFGCStatus::GetQueuedMatchGroup() const
{
	const CTFParty *pParty = ActiveParty();
	if ( pParty && pParty->GetNumQueueEntries() > 0 )
		return pParty->GetQueueEntryMatchGroup( 0 );
	if ( GTFPartyClient() )
	{
		for ( const auto group : { k_eTFMatchGroup_Casual_12v12, k_eTFMatchGroup_Ladder_6v6 } )
		{
			if ( GTFPartyClient()->BInQueueForMatchGroup( group ) )
				return group;
		}
	}
	return GTFGCClientSystem()->GetLiveMatchGroup();
}

int CTFGCStatus::GetQueuePlayerCount() const
{
	const int nGroup = (int)GetQueuedMatchGroup();
	return ( nGroup >= 0 && nGroup < k_nGroups ) ? m_groups[ nGroup ].nQueued : 0;
}

int CTFGCStatus::GetQueueNeededCount() const
{
	const int nGroup = (int)GetQueuedMatchGroup();
	return ( nGroup >= 0 && nGroup < k_nGroups ) ? m_groups[ nGroup ].nNeed : 0;
}

float CTFGCStatus::GetQueueSeconds() const
{
	const CTFParty *pParty = ActiveParty();
	if ( !pParty || pParty->GetNumQueueEntries() == 0 || !steamapicontext || !steamapicontext->SteamUtils() )
		return 0.f;
	const RTime32 rtNow = steamapicontext->SteamUtils()->GetServerRealTime();
	const RTime32 rtQueued = pParty->GetQueueEntryStartTime( 0 );
	return rtNow > rtQueued ? (float)( rtNow - rtQueued ) : 0.f;
}

const char *CTFGCStatus::GetQueueDetail() const
{
	CTFGCClientSystem *pGC = GTFGCClientSystem();
	if ( pGC->BHaveRunningMatch() && !pGC->BHaveLiveMatch() )
		return "Match found. Getting a server ready...";
	return "";
}

const CUtlVector< CUtlString > *CTFGCStatus::GetGroupMaps( ETFMatchGroup eMatchGroup ) const
{
	const int nGroup = (int)eMatchGroup;
	if ( nGroup < 0 || nGroup >= k_nGroups )
		return NULL;
	return m_groups[ nGroup ].vecMaps.Count() ? &m_groups[ nGroup ].vecMaps : NULL;
}

int CTFGCStatus::MapCount( ETFMatchGroup eMatchGroup, const char *pszMap, bool bPlaying ) const
{
	const int nGroup = (int)eMatchGroup;
	if ( nGroup < 0 || nGroup >= k_nGroups || !pszMap )
		return -1;
	const Group_t &group = m_groups[ nGroup ];
	FOR_EACH_VEC( group.vecMaps, i )
	{
		if ( !V_stricmp( group.vecMaps[ i ].Get(), pszMap ) )
			return bPlaying ? group.vecMapPlaying[ i ] : group.vecMapQueued[ i ];
	}
	return -1;
}

int CTFGCStatus::GetMapQueued( ETFMatchGroup eMatchGroup, const char *pszMap ) const
{
	return MapCount( eMatchGroup, pszMap, false );
}

int CTFGCStatus::GetMapPlaying( ETFMatchGroup eMatchGroup, const char *pszMap ) const
{
	return MapCount( eMatchGroup, pszMap, true );
}

int CTFGCStatus::GetGroupQueued( ETFMatchGroup eMatchGroup ) const
{
	const int nGroup = (int)eMatchGroup;
	return ( nGroup >= 0 && nGroup < k_nGroups ) ? m_groups[ nGroup ].nQueued : 0;
}

bool CTFGCStatus::BGroupOffered( ETFMatchGroup eMatchGroup ) const
{
	if ( !m_bGroupsKnown )
		return true;
	const int nGroup = (int)eMatchGroup;
	return nGroup >= 0 && nGroup < k_nGroups && m_groups[ nGroup ].bOffered;
}

bool CTFGCStatus::BAnyGroupOffered() const
{
	for ( int i = 0; i < k_nGroups; i++ )
	{
		if ( m_groups[i].bOffered )
			return true;
	}
	return false;
}

bool BMapInPool( const CUtlVector< CUtlString > &vecPool, const char *pszMap )
{
	FOR_EACH_VEC( vecPool, i )
	{
		if ( !V_stricmp( vecPool[i].Get(), pszMap ) )
			return true;
	}
	return false;
}
