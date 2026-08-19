//========= GREYLINE FRONTRESS ============================================//
//
// Purpose: a small, non-blocking HTTP client built on ISteamHTTP — the same
// transport the game's own menu bridge exposes to the web panel as "fetch"
// (see greyline_bridge.cpp and game/tc2/loose/resource/html/greyline.html),
// so a page talking to a coordinator on another host never has to fight CORS.
//
// Everything here is driven by Steam's own callback pump, which the engine
// already runs every frame: sending a request never blocks, and a result
// arrives later as an ordinary call to the callback the caller supplied. This
// mirrors game/client/youtubeapi.cpp's use of CCallResult<T,
// HTTPRequestCompleted_t>, minus the background thread pool that file uses to
// turn it back into something a synchronous caller can wait on — nothing here
// needs to be synchronous.
//
//=========================================================================//

#ifndef GREYLINE_HTTP_H
#define GREYLINE_HTTP_H
#ifdef _WIN32
#pragma once
#endif

#include "igamesystem.h"
#include "steam/steam_api.h"
#include "steam/isteamhttp.h"
#include "utlvector.h"
#include "utlstring.h"

namespace greyline
{

enum HttpMethod_t
{
	HTTP_GET = 0,
	HTTP_POST,
};

struct HttpHeader_t
{
	const char *pszName;
	const char *pszValue;
};

// A finished request's result. pszBody points into storage owned by the
// caller of the callback and is only valid for the duration of that one call
// — copy it if it needs to outlive the callback.
struct HttpResult_t
{
	// False only for a client-side failure: no Steam HTTP interface, a bad
	// URL, a connection that never got a response at all. A response with a
	// non-2xx status is still bTransportOK — that is a server opinion, not a
	// transport failure, and the caller decides what to do with nStatusCode.
	bool		bTransportOK;
	int			nStatusCode;
	const char *pszBody;
	uint32		nBodyLen;
};

typedef void ( *HttpCallback_t )( void *pContext, const HttpResult_t &result );

//-----------------------------------------------------------------------------
// Purpose: fire-and-forget-with-a-callback HTTP requests.
//-----------------------------------------------------------------------------
class CGreylineHTTP : public CAutoGameSystemPerFrame
{
public:
	CGreylineHTTP();

	virtual void Shutdown() OVERRIDE;

	// pContext is passed back to pfnCallback verbatim and otherwise untouched.
	// It is the caller's job to make sure whatever it points to outlives the
	// request, or to arrange some other way of noticing it no longer does —
	// this class has no way to know.
	//
	// headers and pszBody are copied before this call returns; nothing about
	// them needs to outlive it. pszBody is ignored for HTTP_GET.
	void Send( HttpMethod_t eMethod, const char *pszURL,
		const CUtlVector<HttpHeader_t> &headers, const char *pszBody,
		HttpCallback_t pfnCallback, void *pContext, float flTimeoutSeconds = 30.0f );

private:
	struct InFlight_t
	{
		HTTPRequestHandle	hRequest;
		HttpCallback_t		pfnCallback;
		void *				pContext;
		CCallResult<CGreylineHTTP, HTTPRequestCompleted_t> *pCallResult;
	};

	void OnHTTPRequestCompleted( HTTPRequestCompleted_t *pParam, bool bIOFailure );
	void FailImmediately( HttpCallback_t pfnCallback, void *pContext );

	CUtlVector<InFlight_t> m_InFlight;
};

CGreylineHTTP *GreylineHTTP();

} // namespace greyline

#endif // GREYLINE_HTTP_H
