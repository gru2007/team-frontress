//========= Copyright Team Frontress, All rights reserved. ====================//
//
// Purpose: See tf_mm_backend.h.
//
//=============================================================================//

#include "cbase.h"

#include "tf_mm_backend.h"

#include "tf_gc_client.h"
#include "tf_partyclient.h"
#include "tf_lobby_server.h"
#include "gcsdk/webapi_response.h"

#if !defined( _X360 ) && !defined( NO_STEAM )
#include "steam/steam_api.h"
#endif

// The coordinator address the client's real GC transport already talks to
// (see frontress_gc.cpp). The status poll reuses it rather than adding a
// second "where is our backend" convar.
extern ConVar tf_gc_address;

// memdbgon must be the last include file in a .cpp file!!!
#include "tier0/memdbgon.h"

static const double k_flStatusPollInterval = 15.0;		// Re-poll cadence on success.
static const double k_flStatusPollBackoff  = 30.0;			// Re-poll cadence after a failed attempt.

//-----------------------------------------------------------------------------
// Purpose: One in-flight GET {tf_gc_address}/v1/status. Mirrors
//          CFrontressGCRequest (frontress_gc.cpp) and CComtressRequest
//          (gc_clientsystem.cpp) -- same self-owning, orphan-on-teardown
//          shape, just for a simple unauthenticated status GET instead of
//          the GC message batch/session exchange.
//-----------------------------------------------------------------------------
class CTFMMStatusRequest
{
public:
	CTFMMStatusRequest( CTFMMBackend *pBackend, ISteamHTTP *pHTTP, HTTPRequestHandle hRequest )
		: m_pBackend( pBackend )
		, m_pHTTP( pHTTP )
		, m_hRequest( hRequest )
	{
	}

	void OnCompleted( HTTPRequestCompleted_t *pInfo, bool bIOFailure )
	{
		CTFMMBackend *pBackend = m_pBackend;
		m_pBackend = NULL;

		bool bOK = false;
		if ( pInfo && !bIOFailure && pInfo->m_bRequestSuccessful && pInfo->m_eStatusCode == k_EHTTPStatusCode200OK )
		{
			uint32 unBytes = 0;
			if ( m_pHTTP->GetHTTPResponseBodySize( m_hRequest, &unBytes ) )
			{
				CUtlBuffer bufResponse( 0, 0, CUtlBuffer::TEXT_BUFFER );
				bufResponse.EnsureCapacity( unBytes + 1 );
				if ( unBytes == 0 || m_pHTTP->GetHTTPResponseBodyData( m_hRequest, ( uint8 * )bufResponse.Base(), unBytes ) )
				{
					bufResponse.SeekPut( CUtlBuffer::SEEK_HEAD, unBytes );
					( ( char * )bufResponse.Base() )[ unBytes ] = '\0';

					if ( pBackend )
					{
						pBackend->OnStatusReceived( bufResponse );
					}
					bOK = true;
				}
			}
		}

		m_pHTTP->ReleaseHTTPRequest( m_hRequest );

		if ( pBackend )
		{
			pBackend->OnStatusRequestFinished( this, bOK );
		}
		delete this;
	}

	// The backend is going away first (shouldn't normally happen -- it's a
	// function-local static that outlives the module -- but costs nothing
	// to handle safely). Let the callback land on nothing.
	void Orphan() { m_pBackend = NULL; }

	CCallResult< CTFMMStatusRequest, HTTPRequestCompleted_t > m_CallResult;

private:
	CTFMMBackend      *m_pBackend;
	ISteamHTTP        *m_pHTTP;
	HTTPRequestHandle  m_hRequest;
};

//-----------------------------------------------------------------------------
CTFMMBackend::CTFMMBackend()
	: m_flLastRefresh( -1.0 )
	, m_eCachedState( k_eTFMMState_Idle )
	, m_eCachedQueuedGroup( k_eTFMatchGroup_Invalid )
	, m_eQueueTrackedGroup( k_eTFMatchGroup_Invalid )
	, m_flQueueStartTime( -1.0 )
	, m_flNextStatusPoll( -1.0 )
	, m_pPendingStatusRequest( NULL )
{
}

//-----------------------------------------------------------------------------
CTFMMBackend::~CTFMMBackend()
{
	if ( m_pPendingStatusRequest )
	{
		m_pPendingStatusRequest->Orphan();
		m_pPendingStatusRequest = NULL;
	}
}

//-----------------------------------------------------------------------------
// Purpose: Re-derive everything from the real GC objects and kick a status
//          poll if one is due. Called first by every public accessor so it
//          doesn't matter which order the two call sites ask for things in.
//-----------------------------------------------------------------------------
void CTFMMBackend::EnsureFresh() const
{
	double flNow = Plat_FloatTime();
	if ( flNow == m_flLastRefresh )
		return;
	m_flLastRefresh = flNow;

	CTFGCClientSystem *pGC = GTFGCClientSystem();
	CTFPartyClient *pParty = GTFPartyClient();

	m_eCachedQueuedGroup = k_eTFMatchGroup_Invalid;
	if ( pParty && pParty->GetNumQueuedMatchGroups() > 0 )
	{
		m_eCachedQueuedGroup = pParty->GetQueuedMatchGroupByIdx( 0 );
	}

	m_eCachedState = k_eTFMMState_Idle;
	if ( pGC && pParty )
	{
		if ( pGC->BConnectedToMatchServer( true ) )
		{
			m_eCachedState = k_eTFMMState_InMatch;
		}
		else
		{
			CTFGSLobby *pLobby = pGC->GetLobby();
			if ( pLobby && pLobby->GetState() == CSOTFGameServerLobby_State_SERVERSETUP )
			{
				m_eCachedState = k_eTFMMState_MatchReady;
			}
			else if ( pLobby && pLobby->GetState() == CSOTFGameServerLobby_State_RUN )
			{
				// RUN with a connect string means the lobby has a server and is
				// (auto-)joining it; RUN without one yet just means the server
				// assignment hasn't landed as a connect string in the SO cache.
				m_eCachedState = ( pLobby->HasConnect() && pLobby->GetConnect() && pLobby->GetConnect()[ 0 ] )
					? k_eTFMMState_Connecting
					: k_eTFMMState_MatchReady;
			}
			else if ( m_eCachedQueuedGroup != k_eTFMatchGroup_Invalid || pParty->BInStandbyQueue() )
			{
				m_eCachedState = k_eTFMMState_Searching;
			}
		}
	}

	RefreshQueueTiming();
	PollStatusIfDue();
}

//-----------------------------------------------------------------------------
// Purpose: Track how long we've continuously been queued for whatever group
// we're currently queued for. There's no GC-side "queued since" timestamp
// exposed to the client, so this is the client's own observation of when it
// first saw itself queued.
//-----------------------------------------------------------------------------
void CTFMMBackend::RefreshQueueTiming() const
{
	if ( m_eCachedQueuedGroup == k_eTFMatchGroup_Invalid )
	{
		m_eQueueTrackedGroup = k_eTFMatchGroup_Invalid;
		m_flQueueStartTime = -1.0;
		return;
	}

	if ( m_eQueueTrackedGroup != m_eCachedQueuedGroup || m_flQueueStartTime < 0.0 )
	{
		m_eQueueTrackedGroup = m_eCachedQueuedGroup;
		m_flQueueStartTime = Plat_FloatTime();
	}
}

//-----------------------------------------------------------------------------
ETFMMState CTFMMBackend::GetState() const
{
	EnsureFresh();
	return m_eCachedState;
}

//-----------------------------------------------------------------------------
ETFMatchGroup CTFMMBackend::GetQueuedMatchGroup() const
{
	EnsureFresh();
	return m_eCachedQueuedGroup;
}

//-----------------------------------------------------------------------------
float CTFMMBackend::GetQueueSeconds() const
{
	EnsureFresh();
	if ( m_flQueueStartTime < 0.0 )
		return 0.f;
	return ( float )MAX( 0.0, Plat_FloatTime() - m_flQueueStartTime );
}

//-----------------------------------------------------------------------------
int CTFMMBackend::GetQueuePlayerCount() const
{
	EnsureFresh();
	if ( m_eCachedQueuedGroup == k_eTFMatchGroup_Invalid )
		return 0;

	FOR_EACH_VEC( m_vecQueueGroups, i )
	{
		if ( m_vecQueueGroups[ i ].eMatchGroup == m_eCachedQueuedGroup )
			return m_vecQueueGroups[ i ].nQueued;
	}
	return 0;
}

//-----------------------------------------------------------------------------
int CTFMMBackend::GetQueueNeededCount() const
{
	EnsureFresh();
	if ( m_eCachedQueuedGroup == k_eTFMatchGroup_Invalid )
		return 0;

	FOR_EACH_VEC( m_vecQueueGroups, i )
	{
		if ( m_vecQueueGroups[ i ].eMatchGroup == m_eCachedQueuedGroup )
			return MAX( 0, m_vecQueueGroups[ i ].nMinPlayers - m_vecQueueGroups[ i ].nQueued );
	}
	return 0;
}

//-----------------------------------------------------------------------------
// Purpose: A real Valve matchmaker health-bracket token (the same data
// tf_ping_panel.cpp shows), so the "extra note" under the search line is
// genuinely GC-derived rather than invented copy.
//-----------------------------------------------------------------------------
const char *CTFMMBackend::GetQueueDetail() const
{
	EnsureFresh();
	if ( m_eCachedState != k_eTFMMState_Searching )
		return NULL;

	CTFGCClientSystem *pGC = GTFGCClientSystem();
	if ( !pGC )
		return NULL;

	CTFGCClientSystem::MatchMakerHealthData_t healthData = pGC->GetOverallHealthDataForLocalCriteria();
	if ( healthData.m_strLocToken.IsEmpty() )
		return NULL;

	m_strQueueDetail = "#";
	m_strQueueDetail += healthData.m_strLocToken;
	return m_strQueueDetail.Get();
}

//-----------------------------------------------------------------------------
const CTFMMBackend::Status_t &CTFMMBackend::GetStatus() const
{
	EnsureFresh();
	return m_Status;
}

//-----------------------------------------------------------------------------
// Purpose: Kick a fresh GET {tf_gc_address}/v1/status if one isn't already
// in flight and it's time for another. Population/health data is nobody's
// single GC session -- it's an aggregate the coordinator itself has to hand
// out, and handleStatus already exists there unauthenticated for exactly
// this (internal/api/api.go, GET /v1/status).
//-----------------------------------------------------------------------------
void CTFMMBackend::PollStatusIfDue() const
{
	if ( m_pPendingStatusRequest )
		return;

	double flNow = Plat_FloatTime();
	if ( flNow < m_flNextStatusPoll )
		return;

	const char *pszAddress = tf_gc_address.GetString();
	if ( !pszAddress || pszAddress[ 0 ] == '\0' )
	{
		m_flNextStatusPoll = flNow + k_flStatusPollBackoff;
		return;
	}

	ISteamHTTP *pHTTP = steamapicontext ? steamapicontext->SteamHTTP() : NULL;
	if ( !pHTTP )
	{
		m_flNextStatusPoll = flNow + k_flStatusPollBackoff;
		return;
	}

	char szURL[ 512 ];
	V_snprintf( szURL, sizeof( szURL ), "%s/v1/status", pszAddress );

	HTTPRequestHandle hRequest = pHTTP->CreateHTTPRequest( k_EHTTPMethodGET, szURL );
	if ( hRequest == INVALID_HTTPREQUEST_HANDLE )
	{
		m_flNextStatusPoll = flNow + k_flStatusPollBackoff;
		return;
	}

	pHTTP->SetHTTPRequestNetworkActivityTimeout( hRequest, 10 );

	SteamAPICall_t hCall;
	if ( !pHTTP->SendHTTPRequest( hRequest, &hCall ) )
	{
		pHTTP->ReleaseHTTPRequest( hRequest );
		m_flNextStatusPoll = flNow + k_flStatusPollBackoff;
		return;
	}

	m_pPendingStatusRequest = new CTFMMStatusRequest( const_cast< CTFMMBackend * >( this ), pHTTP, hRequest );
	m_pPendingStatusRequest->m_CallResult.Set( hCall, m_pPendingStatusRequest, &CTFMMStatusRequest::OnCompleted );
}

//-----------------------------------------------------------------------------
// Purpose: Parse a successful /v1/status response body. Shape (see
// internal/wire/wire.go Status / MatchGroupInfo in the coordinator):
//   { "name": str, "online_players": int, "queued_players": {"<group>": int, ...},
//     "live_matches": int, "free_servers": int, "server_capacity_known": bool,
//     "match_groups": [ { "match_group": int, ..., "min_players": int, ... }, ... ],
//     "war": {...} (ignored) }
//-----------------------------------------------------------------------------
void CTFMMBackend::OnStatusReceived( CUtlBuffer &bufResponse )
{
	GCSDK::CWebAPIValues *pRoot = GCSDK::CWebAPIValues::ParseJSON( bufResponse );
	if ( !pRoot )
	{
		m_Status.bValid = false;
		return;
	}

	m_Status.bValid = true;
	pRoot->GetChildStringValue( m_Status.strName, "name", "" );
	m_Status.nOnlinePlayers = pRoot->GetChildInt32Value( "online_players", 0 );
	m_Status.nLiveMatches   = pRoot->GetChildInt32Value( "live_matches", 0 );
	m_Status.nFreeServers   = pRoot->GetChildInt32Value( "free_servers", 0 );
	m_Status.bServerCapacityKnown = pRoot->GetChildBoolValue( "server_capacity_known", false );

	m_vecQueueGroups.Purge();
	const GCSDK::CWebAPIValues *pGroups = pRoot->FindChild( "match_groups" );
	if ( pGroups )
	{
		for ( const GCSDK::CWebAPIValues *pEntry = pGroups->GetFirstChild(); pEntry; pEntry = pEntry->GetNextChild() )
		{
			QueueGroupInfo_t info;
			info.eMatchGroup = ( ETFMatchGroup )pEntry->GetChildInt32Value( "match_group", ( int32 )k_eTFMatchGroup_Invalid );
			info.nMinPlayers = pEntry->GetChildInt32Value( "min_players", 0 );
			info.nQueued = 0;
			m_vecQueueGroups.AddToTail( info );
		}
	}

	const GCSDK::CWebAPIValues *pQueued = pRoot->FindChild( "queued_players" );
	if ( pQueued )
	{
		FOR_EACH_VEC( m_vecQueueGroups, i )
		{
			char szKey[ 16 ];
			V_snprintf( szKey, sizeof( szKey ), "%d", ( int )m_vecQueueGroups[ i ].eMatchGroup );
			m_vecQueueGroups[ i ].nQueued = pQueued->GetChildInt32Value( szKey, 0 );
		}
	}

	delete pRoot;
}

//-----------------------------------------------------------------------------
void CTFMMBackend::OnStatusRequestFinished( CTFMMStatusRequest *pRequest, bool bSuccess )
{
	if ( m_pPendingStatusRequest == pRequest )
	{
		m_pPendingStatusRequest = NULL;
	}

	m_Status.bChecked = true;

	double flNow = Plat_FloatTime();
	if ( bSuccess )
	{
		m_flNextStatusPoll = flNow + k_flStatusPollInterval;
	}
	else
	{
		m_Status.bValid = false;
		m_flNextStatusPoll = flNow + k_flStatusPollBackoff;
	}
}

//-----------------------------------------------------------------------------
const CTFMMBackend *TFMMBackend()
{
	static CTFMMBackend s_Backend;
	return &s_Backend;
}
