//========= Copyright Team Frontress, All rights reserved. ====================//
//
// Purpose: The client's link to the Go coordinator.
//
// Plain JSON over Steam's HTTP interface, for the same reason the rest of the
// game uses it: it already knows about the user's proxy and certificate store,
// and it does not need a socket the game has to pump.
//
//=============================================================================//

#include "cbase.h"

#include "tf_mm_backend.h"

#include "clientsteamcontext.h"
#include "fmtstr.h"
#include "gcsdk/webapi_response.h"

// memdbgon must be the last include file in a .cpp file!!!
#include "tier0/memdbgon.h"

ConVar tf_mm_coordinator( "tf_mm_coordinator", "http://coordinator.r-artemev.ru:44974", FCVAR_ARCHIVE,
                          "Base URL of the Team Frontress matchmaking coordinator." );

extern ConVar tf_mm_debug;

// Responses are small. A coordinator that wants to send us a megabyte is a
// coordinator we should not be listening to.
static const uint32 k_unMaxResponseBytes = 256 * 1024;

//-----------------------------------------------------------------------------
CTFMMCoordinator::CTFMMCoordinator()
	: m_hRequest( INVALID_HTTPREQUEST_HANDLE )
	, m_pfnCallback( NULL )
	, m_pContext( NULL )
	, m_hAuthTicket( k_HAuthTicketInvalid )
{
}

//-----------------------------------------------------------------------------
CTFMMCoordinator::~CTFMMCoordinator()
{
	Cancel();

	if ( m_hAuthTicket != k_HAuthTicketInvalid && steamapicontext && steamapicontext->SteamUser() )
	{
		steamapicontext->SteamUser()->CancelAuthTicket( m_hAuthTicket );
		m_hAuthTicket = k_HAuthTicketInvalid;
	}
}

//-----------------------------------------------------------------------------
// The identity the ticket is issued for. Steam records it alongside the ticket
// and the coordinator does not check it, but a ticket minted for "this service"
// is not reusable against a different one, which is the whole point of naming
// it.
#define TFMM_WEBAPI_IDENTITY "frontress-coordinator"

//-----------------------------------------------------------------------------
// Purpose: The ticket the coordinator can actually verify.
//
//			This used to be GetAuthSessionTicket, whose own header says of it:
//			"not to be used for ISteamUserAuth\AuthenticateUserTicket - it will
//			fail". It does fail, with Invalid ticket (101), which is what the
//			coordinator logged for every queue request from a client whose
//			Steam was new enough to enforce it. GetAuthTicketForWebApi is the
//			one that endpoint accepts.
//
//			It is asynchronous, so this returns empty until the ticket lands.
//			Callers must cope with that -- see SendQueueRequest, which waits.
//-----------------------------------------------------------------------------
const char *CTFMMCoordinator::GetAuthTicket()
{
	if ( !m_strTicket.IsEmpty() )
		return m_strTicket.Get();

	RequestAuthTicket();
	return "";
}

//-----------------------------------------------------------------------------
void CTFMMCoordinator::RequestAuthTicket()
{
	if ( !m_strTicket.IsEmpty() || m_hAuthTicket != k_HAuthTicketInvalid )
		return; // have one, or one is on the way

	if ( !steamapicontext || !steamapicontext->SteamUser() || !steamapicontext->SteamUser()->BLoggedOn() )
		return;

	m_hAuthTicket = steamapicontext->SteamUser()->GetAuthTicketForWebApi( TFMM_WEBAPI_IDENTITY );
	if ( tf_mm_debug.GetBool() )
		Msg( "[mm] asked Steam for a web API auth ticket\n" );
}

//-----------------------------------------------------------------------------
void CTFMMCoordinator::OnWebApiTicket( GetTicketForWebApiResponse_t *pResponse )
{
	if ( !pResponse || pResponse->m_hAuthTicket != m_hAuthTicket )
		return;

	if ( pResponse->m_eResult != k_EResultOK || pResponse->m_cubTicket <= 0 )
	{
		Warning( "[mm] Steam would not issue an auth ticket (result %d); "
		         "a coordinator that verifies tickets will refuse to queue you\n",
		         (int)pResponse->m_eResult );
		// Let the next call try again rather than latching the failure.
		m_hAuthTicket = k_HAuthTicketInvalid;
		return;
	}

	// Hex, because that is what the coordinator hands to Steam's web API and
	// what the rest of this codebase already sends (see BSendMessageComtress).
	const int cub = MIN( pResponse->m_cubTicket, (int)GetTicketForWebApiResponse_t::k_nCubTicketMaxLength );
	CUtlVector< char > vecHex;
	vecHex.SetCount( cub * 2 + 1 );
	for ( int i = 0; i < cub; i++ )
		V_snprintf( &vecHex[ i * 2 ], 3, "%02x", pResponse->m_rgubTicket[i] );
	vecHex[ cub * 2 ] = '\0';

	m_strTicket = vecHex.Base();

	if ( tf_mm_debug.GetBool() )
		Msg( "[mm] web API auth ticket ready (%d bytes)\n", cub );
}

//-----------------------------------------------------------------------------
bool CTFMMCoordinator::BSend( EHTTPMethod eMethod, const char *pszPath, const char *pszBody,
                              FnResponse pfnCallback, void *pContext )
{
	ISteamHTTP *pHTTP = SteamHTTP();
	if ( !pHTTP )
		return false;

	if ( BBusy() )
	{
		// One request at a time. The backend's state machine never needs two,
		// and letting them overlap would let a stale poll answer after a
		// cancel.
		if ( tf_mm_debug.GetBool() )
			Warning( "[mm] dropping %s: a request is already in flight\n", pszPath );
		return false;
	}

	CFmtStr1024 url( "%s%s", tf_mm_coordinator.GetString(), pszPath );
	HTTPRequestHandle hRequest = pHTTP->CreateHTTPRequest( eMethod, url.Get() );
	if ( hRequest == INVALID_HTTPREQUEST_HANDLE )
		return false;

	pHTTP->SetHTTPRequestNetworkActivityTimeout( hRequest, 15 );

	const char *pszTicket = GetAuthTicket();
	if ( pszTicket && pszTicket[0] )
	{
		// A hex-encoded Steam session ticket is a couple of thousand
		// characters. CFmtStr is 256 and truncates silently -- which produced
		// a header the coordinator could not validate, and a queue request it
		// refused without saying why.
		CFmtStrMax authorization( "Steam %s", pszTicket );
		pHTTP->SetHTTPRequestHeaderValue( hRequest, "Authorization", authorization.Get() );
	}

	if ( pszBody && pszBody[0] )
	{
		pHTTP->SetHTTPRequestRawPostBody( hRequest, "application/json",
		                                  (uint8 *)pszBody, V_strlen( pszBody ) );
	}

	SteamAPICall_t hCall = k_uAPICallInvalid;
	if ( !pHTTP->SendHTTPRequest( hRequest, &hCall ) )
	{
		pHTTP->ReleaseHTTPRequest( hRequest );
		return false;
	}

	m_hRequest = hRequest;
	m_pfnCallback = pfnCallback;
	m_pContext = pContext;
	m_callCompleted.Set( hCall, this, &CTFMMCoordinator::OnRequestCompleted );

	if ( tf_mm_debug.GetBool() )
		Msg( "[mm] -> %s\n", url.Get() );

	return true;
}

//-----------------------------------------------------------------------------
void CTFMMCoordinator::Cancel()
{
	if ( m_hRequest == INVALID_HTTPREQUEST_HANDLE )
		return;

	if ( SteamHTTP() )
	{
		SteamHTTP()->ReleaseHTTPRequest( m_hRequest );
	}
	m_callCompleted.Cancel();
	m_hRequest = INVALID_HTTPREQUEST_HANDLE;
	m_pfnCallback = NULL;
	m_pContext = NULL;
}

//-----------------------------------------------------------------------------
void CTFMMCoordinator::OnRequestCompleted( HTTPRequestCompleted_t *pInfo, bool bIOFailure )
{
	FnResponse pfnCallback = m_pfnCallback;
	void *pContext = m_pContext;
	HTTPRequestHandle hRequest = m_hRequest;

	// Clear our state before the callback runs, so the callback is free to
	// start the next request.
	m_hRequest = INVALID_HTTPREQUEST_HANDLE;
	m_pfnCallback = NULL;
	m_pContext = NULL;

	ISteamHTTP *pHTTP = SteamHTTP();
	if ( !pHTTP || hRequest == INVALID_HTTPREQUEST_HANDLE )
	{
		if ( pfnCallback )
			pfnCallback( NULL, 0, pContext );
		return;
	}

	int eStatusCode = 0;
	GCSDK::CWebAPIValues *pValues = NULL;

	if ( !bIOFailure && pInfo )
	{
		eStatusCode = pInfo->m_eStatusCode;

		uint32 unBytes = 0;
		if ( pHTTP->GetHTTPResponseBodySize( pInfo->m_hRequest, &unBytes ) &&
		     unBytes > 0 && unBytes < k_unMaxResponseBytes )
		{
			CUtlBuffer bufResponse( 0, unBytes + 1, CUtlBuffer::TEXT_BUFFER );
			if ( pHTTP->GetHTTPResponseBodyData( pInfo->m_hRequest, (uint8 *)bufResponse.Base(), unBytes ) )
			{
				bufResponse.SeekPut( CUtlBuffer::SEEK_HEAD, unBytes );
				( (char *)bufResponse.Base() )[ unBytes ] = '\0';
				pValues = GCSDK::CWebAPIValues::ParseJSON( bufResponse );
			}
		}

		if ( tf_mm_debug.GetBool() )
			Msg( "[mm] <- HTTP %d (%u bytes)\n", eStatusCode, unBytes );
	}
	else if ( tf_mm_debug.GetBool() )
	{
		Warning( "[mm] <- request failed to reach the coordinator\n" );
	}

	pHTTP->ReleaseHTTPRequest( hRequest );

	if ( pfnCallback )
		pfnCallback( pValues, eStatusCode, pContext );

	delete pValues;
}
