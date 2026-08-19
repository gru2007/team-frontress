//========= GREYLINE FRONTRESS ============================================//
//
// Purpose: see greyline_http.h.
//
//=========================================================================//

#include "cbase.h"
#include "greyline_http.h"
#include "tier1/utlstring.h"

// memdbgon must be the last include file in a .cpp file!!!
#include "tier0/memdbgon.h"

using namespace greyline;

static CGreylineHTTP g_GreylineHTTP;
CGreylineHTTP *greyline::GreylineHTTP() { return &g_GreylineHTTP; }

//-----------------------------------------------------------------------------
CGreylineHTTP::CGreylineHTTP()
	: CAutoGameSystemPerFrame( "CGreylineHTTP" )
{
}

//-----------------------------------------------------------------------------
void CGreylineHTTP::Shutdown()
{
	ISteamHTTP *pHTTP = steamapicontext ? steamapicontext->SteamHTTP() : NULL;
	FOR_EACH_VEC( m_InFlight, i )
	{
		if ( pHTTP )
		{
			pHTTP->ReleaseHTTPRequest( m_InFlight[i].hRequest );
		}
		delete m_InFlight[i].pCallResult;
	}
	m_InFlight.Purge();
}

//-----------------------------------------------------------------------------
void CGreylineHTTP::FailImmediately( HttpCallback_t pfnCallback, void *pContext )
{
	if ( !pfnCallback )
	{
		return;
	}
	HttpResult_t result = { false, 0, NULL, 0 };
	pfnCallback( pContext, result );
}

//-----------------------------------------------------------------------------
void CGreylineHTTP::Send( HttpMethod_t eMethod, const char *pszURL,
	const CUtlVector<HttpHeader_t> &headers, const char *pszBody,
	HttpCallback_t pfnCallback, void *pContext, float flTimeoutSeconds )
{
	ISteamHTTP *pHTTP = steamapicontext ? steamapicontext->SteamHTTP() : NULL;
	if ( !pHTTP || !pszURL || !pszURL[0] )
	{
		FailImmediately( pfnCallback, pContext );
		return;
	}

	const EHTTPMethod eSteamMethod = ( eMethod == HTTP_POST ) ? k_EHTTPMethodPOST : k_EHTTPMethodGET;
	HTTPRequestHandle hRequest = pHTTP->CreateHTTPRequest( eSteamMethod, pszURL );
	if ( hRequest == INVALID_HTTPREQUEST_HANDLE )
	{
		FailImmediately( pfnCallback, pContext );
		return;
	}

	pHTTP->SetHTTPRequestNetworkActivityTimeout( hRequest, (uint32)flTimeoutSeconds );

	FOR_EACH_VEC( headers, i )
	{
		pHTTP->SetHTTPRequestHeaderValue( hRequest, headers[i].pszName, headers[i].pszValue );
	}

	// The coordinator's whole API is JSON. Nothing here ever needs to send
	// anything else, so the content type is not worth a parameter.
	if ( eMethod == HTTP_POST && pszBody && pszBody[0] )
	{
		pHTTP->SetHTTPRequestRawPostBody( hRequest, "application/json",
			(uint8 *)pszBody, (uint32)V_strlen( pszBody ) );
	}

	SteamAPICall_t hCall;
	if ( !pHTTP->SendHTTPRequest( hRequest, &hCall ) )
	{
		pHTTP->ReleaseHTTPRequest( hRequest );
		FailImmediately( pfnCallback, pContext );
		return;
	}

	InFlight_t &slot = m_InFlight[ m_InFlight.AddToTail() ];
	slot.hRequest = hRequest;
	slot.pfnCallback = pfnCallback;
	slot.pContext = pContext;
	slot.pCallResult = new CCallResult<CGreylineHTTP, HTTPRequestCompleted_t>();
	slot.pCallResult->Set( hCall, this, &CGreylineHTTP::OnHTTPRequestCompleted );
}

//-----------------------------------------------------------------------------
void CGreylineHTTP::OnHTTPRequestCompleted( HTTPRequestCompleted_t *pParam, bool bIOFailure )
{
	ISteamHTTP *pHTTP = steamapicontext ? steamapicontext->SteamHTTP() : NULL;

	int nSlot = -1;
	FOR_EACH_VEC( m_InFlight, i )
	{
		if ( m_InFlight[i].hRequest == pParam->m_hRequest )
		{
			nSlot = i;
			break;
		}
	}
	if ( nSlot < 0 )
	{
		return; // stale or unknown; nothing to call back
	}

	InFlight_t slot = m_InFlight[nSlot];
	m_InFlight.Remove( nSlot );

	HttpResult_t result = { false, 0, NULL, 0 };
	CUtlString strBody;
	if ( !bIOFailure && pParam->m_bRequestSuccessful && pHTTP )
	{
		uint32 unBodySize = 0;
		pHTTP->GetHTTPResponseBodySize( pParam->m_hRequest, &unBodySize );
		if ( unBodySize > 0 )
		{
			strBody.SetLength( unBodySize );
			pHTTP->GetHTTPResponseBodyData( pParam->m_hRequest, (uint8 *)strBody.Get(), unBodySize );
		}
		result.bTransportOK = true;
		result.nStatusCode = (int)pParam->m_eStatusCode;
		result.pszBody = strBody.Get();
		result.nBodyLen = unBodySize;
	}

	if ( pHTTP )
	{
		pHTTP->ReleaseHTTPRequest( pParam->m_hRequest );
	}

	if ( slot.pfnCallback )
	{
		slot.pfnCallback( slot.pContext, result );
	}

	delete slot.pCallResult;
}
