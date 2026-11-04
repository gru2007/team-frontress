//========= Copyright Team Frontress, All rights reserved. ====================//
//
// Purpose: The connection to the Team Frontress game coordinator. See the
//			header for what this is and why it is all there is.
//
//=============================================================================//

#include "cbase.h"
#include "frontress_gc.h"
#include "gc_clientsystem.h"
#include "gcsdk/gcsdk_auto.h"
#include "gcsdk/netpacketpool.h"
#include "tf_gcmessages.h"
#include "tf_gcmessages.pb.h"
#include "fmtstr.h"
#include "tier1/convar.h"

#ifdef CLIENT_DLL
#include "clientsteamcontext.h"
#include "tf_gc_client.h"
#include "confirm_dialog.h"
#else
#include "steam/steam_gameserver.h"
#endif

#include <stdlib.h>

// memdbgon must be the last include file in a .cpp file!!!
#include "tier0/memdbgon.h"

// gcsdk's own helper for turning received bytes into a message. It is in the
// prebuilt library; the public headers just never declared it.
namespace GCSDK
{
	IMsgNetPacket *IMsgNetPacketFromCNetPacket( CNetPacket *pNetPacket );
}

// Must match services/gc/internal/httpapi.ProtocolVersion.
static const int k_nGCProtocolVersion = 1;

// SO message ids (gcsystemmsgs.proto), which the game build does not generate.
static const uint32 k_ESOMsg_CacheSubscriptionRefresh = 28;

// Ours: a line of text from the GC for the player (body: CMsgPartyChatMsg).
static const uint32 k_EMsgGCFrontress_Notice = 51000;
static const uint32 k_EMsgGCFrontress_PartyLooks = 51002;	// see services/gc/internal/gc/looks.go
static const uint32 k_EMsgGCFrontress_SocialEvent = 51011;	// see services/gc/internal/gc/social.go

static const float k_flMaxBackoff = 60.f;
static const uint32 k_cubMaxBatch = 256 * 1024;

#ifdef CLIENT_DLL
ConVar tf_gc_url( "tf_gc_url", "http://gc.team-frontress.org:27100", FCVAR_ARCHIVE,
	"Address of the Team Frontress game coordinator. Empty plays without one." );
#else
// Server names differ from the client's: on a listen server both dlls are
// loaded, and the engine will not let the client's convar share a name with
// the server's (the client's setting would silently never apply).
ConVar tf_gc_server_url( "tf_gc_server_url", "", FCVAR_NONE,
	"Address of the game coordinator this dedicated server takes matches from. Falls back to $GC_URL." );
ConVar tf_gc_server_key( "tf_gc_server_key", "", FCVAR_PROTECTED | FCVAR_DONTRECORD,
	"The key this server presents to the game coordinator. Falls back to $GC_SECRET." );
ConVar tf_gc_server_connect( "tf_gc_server_connect", "", FCVAR_NONE,
	"The address players use to reach this server, if it is not the public IP and hostport. Falls back to $SERVER_CONNECT." );
ConVar tf_gc_server_region( "tf_gc_server_region", "", FCVAR_NONE,
	"Region this server reports to the game coordinator." );
#endif
#ifdef CLIENT_DLL
ConVar tf_gc_debug( "tf_gc_debug", "0", FCVAR_NONE, "Log game coordinator transport traffic." );
#else
ConVar tf_gc_debug( "tf_gc_server_debug", "0", FCVAR_NONE, "Log this server's game coordinator transport traffic." );
#endif

#define GCDbg( ... ) do { if ( tf_gc_debug.GetBool() ) Msg( "[gc] " __VA_ARGS__ ); } while ( 0 )

//-----------------------------------------------------------------------------
static CFrontressGC s_FrontressGC;
CFrontressGC *FrontressGC() { return &s_FrontressGC; }

#ifdef GAME_DLL
// A convar, or the environment variable a serveme container sets.
static const char *ConfigValue( const ConVar &var, const char *pszEnv )
{
	const char *psz = var.GetString();
	if ( psz && psz[0] )
		return psz;
	psz = getenv( pszEnv );
	return psz ? psz : "";
}
#endif

//-----------------------------------------------------------------------------
CFrontressGC::CFrontressGC()
#ifdef CLIENT_DLL
	: m_hTicket( k_HAuthTicketInvalid )
	, m_eState( k_eState_Idle )
#else
	: m_eState( k_eState_Idle )
#endif
	, m_flNextAttempt( 0.f )
	, m_flBackoff( 2.f )
	, m_unPollMS( 25000 )
	, m_ulNextOutSeq( 1 )
	, m_ulSendUpTo( 0 )
	, m_ulLastInSeq( 0 )
	, m_flNextRecv( 0.f )
	, m_flNextRefresh( 0.f )
	, m_hSession( INVALID_HTTPREQUEST_HANDLE )
	, m_hSend( INVALID_HTTPREQUEST_HANDLE )
	, m_hRecv( INVALID_HTTPREQUEST_HANDLE )
{
}

CFrontressGC::~CFrontressGC()
{
	m_vecOutbox.PurgeAndDeleteElements();
}

//-----------------------------------------------------------------------------
const char *CFrontressGC::GetBaseURL() const
{
#ifdef CLIENT_DLL
	return tf_gc_url.GetString();
#else
	return ConfigValue( tf_gc_server_url, "GC_URL" );
#endif
}

ISteamHTTP *CFrontressGC::HTTP() const
{
	return GCClientSystem() ? GCClientSystem()->GetSteamHTTP() : NULL;
}

CSteamID CFrontressGC::OurSteamID() const
{
#ifdef CLIENT_DLL
	return ClientSteamContext().GetLocalPlayerSteamID();
#else
	const CSteamID *pID = engine->GetGameServerSteamID();
	return pID ? *pID : CSteamID();
#endif
}

bool CFrontressGC::BWantSession() const
{
	const char *pszURL = GetBaseURL();
	if ( !pszURL || !pszURL[0] || !HTTP() )
		return false;
#ifdef CLIENT_DLL
	return ClientSteamContext().BLoggedOn() && steamapicontext && steamapicontext->SteamUser();
#else
	// Only a dedicated server with a key takes matches. A listen server is a
	// player's game, not a match host.
	if ( !engine->IsDedicatedServer() )
		return false;
	const char *pszKey = ConfigValue( tf_gc_server_key, "GC_SECRET" );
	if ( !pszKey[0] )
		return false;
	return steamgameserverapicontext && steamgameserverapicontext->SteamGameServer() &&
	       steamgameserverapicontext->SteamGameServer()->BLoggedOn() && OurSteamID().IsValid();
#endif
}

const char *CFrontressGC::GetStateName() const
{
	switch ( m_eState )
	{
		case k_eState_Idle:          return "idle";
		case k_eState_WaitingTicket: return "waiting for a Steam ticket";
		case k_eState_Starting:      return "connecting";
		case k_eState_Connected:     return "connected";
		case k_eState_Backoff:       return "retrying";
	}
	return "?";
}

//-----------------------------------------------------------------------------
HTTPRequestHandle CFrontressGC::CreateRequest( EHTTPMethod eMethod, const char *pszPath )
{
	ISteamHTTP *pHTTP = HTTP();
	if ( !pHTTP )
		return INVALID_HTTPREQUEST_HANDLE;

	CUtlString strBase( GetBaseURL() );
	strBase.StripTrailingSlash();
	CFmtStr1024 url( "%s%s", strBase.Get(), pszPath );
	HTTPRequestHandle h = pHTTP->CreateHTTPRequest( eMethod, url.Get() );
	if ( h != INVALID_HTTPREQUEST_HANDLE && !m_strSession.IsEmpty() )
	{
		pHTTP->SetHTTPRequestHeaderValue( h, "X-GC-Session", m_strSession.Get() );
	}
	return h;
}

//-----------------------------------------------------------------------------
// Purpose: gcsdk's outbound path. Messages are only accepted while there is a
//			session: before that, failing is the honest answer, and gcsdk's
//			reliable queue is built to wait for BConnectedtoGC() and retry.
//-----------------------------------------------------------------------------
EGCResults CFrontressGC::SendGCMessage( uint32 unMsgType, const void *pubData, uint32 cubData )
{
	if ( m_eState != k_eState_Connected )
		return k_EGCResultNotLoggedOn;
	if ( cubData > k_cubMaxBatch / 2 )
		return k_EGCResultInvalidMessage;

	Frame_t *pFrame = new Frame_t;
	pFrame->m_ulSeq = m_ulNextOutSeq++;
	pFrame->m_unType = unMsgType;
	pFrame->m_data.SetCount( cubData );
	if ( cubData )
		V_memcpy( pFrame->m_data.Base(), pubData, cubData );
	m_vecOutbox.AddToTail( pFrame );

	GCDbg( "-> msg %u (%u bytes) seq %llu\n", unMsgType & 0x7FFFFFFF, cubData, (unsigned long long)pFrame->m_ulSeq );

	// Send now rather than next frame: replies are latency the player sees.
	FlushSends();
	return k_EGCResultOK;
}

//-----------------------------------------------------------------------------
void CFrontressGC::Think()
{
	const float flNow = (float)Plat_FloatTime();

	if ( !BWantSession() )
	{
		if ( m_eState != k_eState_Idle )
			LoseSession( "no longer configured or logged on" );
		m_eState = k_eState_Idle;
		return;
	}

	switch ( m_eState )
	{
		case k_eState_Idle:
			StartSession();
			break;

		case k_eState_Backoff:
			if ( flNow >= m_flNextAttempt )
				StartSession();
			break;

		case k_eState_WaitingTicket:
			// Steam answers a ticket request in well under a second; one that
			// never comes back must not leave us waiting forever.
			if ( flNow >= m_flNextAttempt )
			{
#ifdef CLIENT_DLL
				// A coordinator on this machine is a developer's, in dev auth
				// mode, which takes any ticket; some development engines never
				// get one from Steam. A real coordinator still turns this away.
				const char *pszURL = GetBaseURL();
				if ( V_strstr( pszURL, "://127.0.0.1" ) || V_strstr( pszURL, "://localhost" ) )
				{
					Msg( "Game coordinator: no Steam ticket; trying the local coordinator without one\n" );
					m_strTicket = "dev";
					StartSession();
					break;
				}
#endif
				Backoff( "Steam did not answer the ticket request", m_flBackoff );
			}
			break;

		case k_eState_Starting:
			// The HTTP request has its own timeout, which ends here.
			break;

		case k_eState_Connected:
			FlushSends();
			if ( m_hRecv == INVALID_HTTPREQUEST_HANDLE && flNow >= m_flNextRecv )
				StartRecv();
			break;
	}
}

void CFrontressGC::Shutdown()
{
	LoseSession( "shutting down" );
	m_eState = k_eState_Idle;
}

//-----------------------------------------------------------------------------
void CFrontressGC::StartSession()
{
	m_strSession.Clear();

#ifdef CLIENT_DLL
	// A fresh web API ticket for every session. Steam answers through
	// OnWebApiTicket, which comes back here with it.
	if ( m_strTicket.IsEmpty() )
	{
		ISteamUser *pUser = steamapicontext->SteamUser();
		if ( m_hTicket != k_HAuthTicketInvalid )
			pUser->CancelAuthTicket( m_hTicket );
		m_hTicket = pUser->GetAuthTicketForWebApi( "frontress-gc" );
		m_eState = k_eState_WaitingTicket;
		m_flNextAttempt = (float)Plat_FloatTime() + 15.f;
		GCDbg( "asked Steam for a web API ticket\n" );
		return;
	}
#endif

	HTTPRequestHandle h = CreateRequest( k_EHTTPMethodPOST, "/gc/v1/session" );
	if ( h == INVALID_HTTPREQUEST_HANDLE )
	{
		Backoff( "could not create an HTTP request", m_flBackoff );
		return;
	}
	ISteamHTTP *pHTTP = HTTP();
	pHTTP->SetHTTPRequestGetOrPostParameter( h, "protocol", CNumStr( k_nGCProtocolVersion ) );
	CNumStr strSteamID;
	strSteamID.SetUint64( OurSteamID().ConvertToUint64() );
	pHTTP->SetHTTPRequestGetOrPostParameter( h, "steam_id", strSteamID );
#ifdef CLIENT_DLL
	pHTTP->SetHTTPRequestGetOrPostParameter( h, "kind", "client" );
	pHTTP->SetHTTPRequestGetOrPostParameter( h, "ticket", m_strTicket.Get() );
	// A ticket is good for one session.
	m_strTicket.Clear();
#else
	pHTTP->SetHTTPRequestGetOrPostParameter( h, "kind", "server" );
	pHTTP->SetHTTPRequestGetOrPostParameter( h, "key", ConfigValue( tf_gc_server_key, "GC_SECRET" ) );
	pHTTP->SetHTTPRequestGetOrPostParameter( h, "region", tf_gc_server_region.GetString() );
	static ConVarRef hostname( "hostname" );
	pHTTP->SetHTTPRequestGetOrPostParameter( h, "name", hostname.IsValid() ? hostname.GetString() : "" );

	CUtlString strConnect( ConfigValue( tf_gc_server_connect, "SERVER_CONNECT" ) );
	if ( strConnect.IsEmpty() && steamgameserverapicontext->SteamGameServer() )
	{
		SteamIPAddress_t ip = steamgameserverapicontext->SteamGameServer()->GetPublicIP();
		static ConVarRef hostport( "hostport" );
		if ( ip.m_eType == k_ESteamIPTypeIPv4 && ip.m_unIPv4 != 0 && hostport.IsValid() )
		{
			strConnect.Format( "%u.%u.%u.%u:%d",
			                   ( ip.m_unIPv4 >> 24 ) & 0xFF, ( ip.m_unIPv4 >> 16 ) & 0xFF,
			                   ( ip.m_unIPv4 >> 8 ) & 0xFF, ip.m_unIPv4 & 0xFF, hostport.GetInt() );
		}
	}
	pHTTP->SetHTTPRequestGetOrPostParameter( h, "connect", strConnect.Get() );
#endif
	pHTTP->SetHTTPRequestNetworkActivityTimeout( h, 30 );

	SteamAPICall_t hCall = k_uAPICallInvalid;
	if ( !pHTTP->SendHTTPRequest( h, &hCall ) )
	{
		pHTTP->ReleaseHTTPRequest( h );
		Backoff( "could not send the session request", m_flBackoff );
		return;
	}
	m_hSession = h;
	m_callSession.Set( hCall, this, &CFrontressGC::OnSessionCompleted );
	m_eState = k_eState_Starting;
	GCDbg( "starting a session with %s\n", GetBaseURL() );
}

#ifdef CLIENT_DLL
void CFrontressGC::OnWebApiTicket( GetTicketForWebApiResponse_t *pResponse )
{
	if ( !pResponse || pResponse->m_hAuthTicket != m_hTicket || m_eState != k_eState_WaitingTicket )
		return;

	if ( pResponse->m_eResult != k_EResultOK || pResponse->m_cubTicket <= 0 )
	{
		Backoff( CFmtStr( "Steam would not issue a ticket (result %d)", (int)pResponse->m_eResult ), m_flBackoff );
		return;
	}
	const int cub = MIN( pResponse->m_cubTicket, (int)GetTicketForWebApiResponse_t::k_nCubTicketMaxLength );
	CUtlVector< char > vecHex;
	vecHex.SetCount( cub * 2 + 1 );
	for ( int i = 0; i < cub; i++ )
		V_snprintf( &vecHex[ i * 2 ], 3, "%02x", pResponse->m_rgubTicket[i] );
	vecHex[ cub * 2 ] = '\0';
	m_strTicket = vecHex.Base();
	StartSession();
}
#endif

// Reads a "key=value" line response.
static CUtlString KVLine( const char *pszBody, const char *pszKey )
{
	const int nKey = V_strlen( pszKey );
	for ( const char *p = pszBody; p && *p; )
	{
		const char *pEnd = V_strstr( p, "\n" );
		const int nLine = pEnd ? (int)( pEnd - p ) : V_strlen( p );
		if ( nLine > nKey && !V_strncmp( p, pszKey, nKey ) && p[nKey] == '=' )
		{
			CUtlString str;
			str.SetDirect( p + nKey + 1, nLine - nKey - 1 );
			return str;
		}
		p = pEnd ? pEnd + 1 : NULL;
	}
	return CUtlString();
}

// The body of a completed request, NUL-terminated.
static bool ReadBody( ISteamHTTP *pHTTP, HTTPRequestHandle h, CUtlVector< uint8 > &out )
{
	uint32 cub = 0;
	if ( !pHTTP->GetHTTPResponseBodySize( h, &cub ) )
		return false;
	out.SetCount( cub + 1 );
	if ( cub && !pHTTP->GetHTTPResponseBodyData( h, out.Base(), cub ) )
		return false;
	out[ cub ] = 0;
	out.SetCount( cub ); // the terminator stays in the allocation
	return true;
}

void CFrontressGC::OnSessionCompleted( HTTPRequestCompleted_t *pInfo, bool bIOFailure )
{
	ISteamHTTP *pHTTP = HTTP();
	HTTPRequestHandle h = m_hSession;
	m_hSession = INVALID_HTTPREQUEST_HANDLE;
	if ( m_eState != k_eState_Starting )
	{
		if ( pHTTP && h != INVALID_HTTPREQUEST_HANDLE )
			pHTTP->ReleaseHTTPRequest( h );
		return;
	}
	if ( bIOFailure || !pInfo || !pHTTP )
	{
		if ( pHTTP && h != INVALID_HTTPREQUEST_HANDLE )
			pHTTP->ReleaseHTTPRequest( h );
		Backoff( "the coordinator could not be reached", m_flBackoff );
		return;
	}

	CUtlVector< uint8 > body;
	ReadBody( pHTTP, pInfo->m_hRequest, body );
	const int nStatus = pInfo->m_eStatusCode;
	pHTTP->ReleaseHTTPRequest( pInfo->m_hRequest );
	const char *pszBody = body.Count() ? (const char *)body.Base() : "";

	if ( nStatus != k_EHTTPStatusCode200OK )
	{
		CUtlString strError = KVLine( pszBody, "error" );
		CFmtStr1024 why( "HTTP %d: %s", nStatus, strError.IsEmpty() ? "no reason given" : strError.Get() );
		// Being refused (bad key, protocol mismatch) is not fixed by hammering.
		const bool bRefused = ( nStatus == 401 || nStatus == 403 || nStatus == 426 );
		Backoff( why, bRefused ? k_flMaxBackoff : m_flBackoff );
		return;
	}

	m_strSession = KVLine( pszBody, "session" );
	const int nPoll = V_atoi( KVLine( pszBody, "poll_ms" ) );
	m_unPollMS = nPoll > 0 ? nPoll : 25000;
	if ( m_strSession.IsEmpty() )
	{
		Backoff( "the coordinator answered without a session", m_flBackoff );
		return;
	}

	// Sequence numbers are per session, in both directions.
	m_vecOutbox.PurgeAndDeleteElements();
	m_ulNextOutSeq = 1;
	m_ulSendUpTo = 0;
	m_ulLastInSeq = 0;
	m_flBackoff = 2.f;
	m_flNextRecv = 0.f;
	m_strLastError.Clear();
	m_eState = k_eState_Connected;

	// The GC's objects arrive as creates into our own cache, which the game
	// only reads once it is subscribed.
	EnsureOwnCacheSubscribed();

	Msg( "Connected to the game coordinator at %s (%s)\n", GetBaseURL(), KVLine( pszBody, "name" ).Get() );
	StartRecv();
}

//-----------------------------------------------------------------------------
void CFrontressGC::Backoff( const char *pszWhy, float flSeconds )
{
	if ( m_strLastError != pszWhy )
		Warning( "Game coordinator: %s. Retrying in %.0f seconds.\n", pszWhy, flSeconds );
	m_strLastError = pszWhy;
	LoseSession( pszWhy );
	m_eState = k_eState_Backoff;
	m_flNextAttempt = (float)Plat_FloatTime() + flSeconds;
	m_flBackoff = MIN( k_flMaxBackoff, m_flBackoff * 2.f );
}

void CFrontressGC::LoseSession( const char *pszWhy )
{
	ISteamHTTP *pHTTP = HTTP();
	if ( pHTTP )
	{
		if ( m_hSend != INVALID_HTTPREQUEST_HANDLE )
			pHTTP->ReleaseHTTPRequest( m_hSend );
		if ( m_hRecv != INVALID_HTTPREQUEST_HANDLE )
			pHTTP->ReleaseHTTPRequest( m_hRecv );
		if ( m_hSession != INVALID_HTTPREQUEST_HANDLE )
			pHTTP->ReleaseHTTPRequest( m_hSession );
	}
	m_callSend.Cancel();
	m_callRecv.Cancel();
	m_callSession.Cancel();
	m_hSend = m_hRecv = m_hSession = INVALID_HTTPREQUEST_HANDLE;

	if ( m_eState == k_eState_Connected )
		Warning( "Lost the game coordinator: %s\n", pszWhy );
	m_strSession.Clear();
	// Whatever was not confirmed is dropped: reliable messages retry on their
	// own once the GC is back, and anything else is stale by then.
	m_vecOutbox.PurgeAndDeleteElements();
	m_eState = k_eState_Idle;
}

//-----------------------------------------------------------------------------
// Purpose: One send in flight at a time, carrying everything queued. Frames
//			stay queued until the GC confirms them; a send that fails is
//			simply repeated, and the GC drops what it has already seen.
//-----------------------------------------------------------------------------
void CFrontressGC::FlushSends()
{
	if ( m_eState != k_eState_Connected || m_hSend != INVALID_HTTPREQUEST_HANDLE || m_vecOutbox.Count() == 0 )
		return;

	CUtlBuffer buf;
	uint64 ulUpTo = 0;
	FOR_EACH_VEC( m_vecOutbox, i )
	{
		const Frame_t *pFrame = m_vecOutbox[i];
		if ( i > 0 && (uint32)buf.TellPut() + 16 + pFrame->m_data.Count() > k_cubMaxBatch )
			break;
		buf.PutInt64( (int64)pFrame->m_ulSeq );
		buf.PutUnsignedInt( pFrame->m_unType );
		buf.PutUnsignedInt( (uint32)pFrame->m_data.Count() );
		buf.Put( pFrame->m_data.Base(), pFrame->m_data.Count() );
		ulUpTo = pFrame->m_ulSeq;
	}

	HTTPRequestHandle h = CreateRequest( k_EHTTPMethodPOST, "/gc/v1/send" );
	if ( h == INVALID_HTTPREQUEST_HANDLE )
		return;
	ISteamHTTP *pHTTP = HTTP();
	pHTTP->SetHTTPRequestRawPostBody( h, "application/octet-stream", (uint8 *)buf.Base(), buf.TellPut() );
	pHTTP->SetHTTPRequestNetworkActivityTimeout( h, 20 );
	SteamAPICall_t hCall = k_uAPICallInvalid;
	if ( !pHTTP->SendHTTPRequest( h, &hCall ) )
	{
		pHTTP->ReleaseHTTPRequest( h );
		return;
	}
	m_hSend = h;
	m_ulSendUpTo = ulUpTo;
	m_callSend.Set( hCall, this, &CFrontressGC::OnSendCompleted );
}

void CFrontressGC::OnSendCompleted( HTTPRequestCompleted_t *pInfo, bool bIOFailure )
{
	ISteamHTTP *pHTTP = HTTP();
	m_hSend = INVALID_HTTPREQUEST_HANDLE;
	if ( !pInfo || !pHTTP )
		return;
	const int nStatus = bIOFailure ? 0 : pInfo->m_eStatusCode;
	pHTTP->ReleaseHTTPRequest( pInfo->m_hRequest );

	if ( nStatus == k_EHTTPStatusCode401Unauthorized )
	{
		LoseSession( "the coordinator ended our session" );
		return;
	}
	if ( nStatus != k_EHTTPStatusCode204NoContent && nStatus != k_EHTTPStatusCode200OK )
	{
		// Leave the frames queued; the next Think sends them again.
		GCDbg( "send failed (HTTP %d); will retry\n", nStatus );
		return;
	}
	while ( m_vecOutbox.Count() && m_vecOutbox[0]->m_ulSeq <= m_ulSendUpTo )
	{
		delete m_vecOutbox[0];
		m_vecOutbox.Remove( 0 );
	}
	FlushSends();
}

//-----------------------------------------------------------------------------
void CFrontressGC::StartRecv()
{
	if ( m_eState != k_eState_Connected || m_hRecv != INVALID_HTTPREQUEST_HANDLE )
		return;
	CNumStr strAck;
	strAck.SetUint64( m_ulLastInSeq );
	HTTPRequestHandle h = CreateRequest( k_EHTTPMethodGET, CFmtStr( "/gc/v1/recv?ack=%s", strAck.String() ) );
	if ( h == INVALID_HTTPREQUEST_HANDLE )
		return;
	ISteamHTTP *pHTTP = HTTP();
	pHTTP->SetHTTPRequestNetworkActivityTimeout( h, m_unPollMS / 1000 + 15 );
	SteamAPICall_t hCall = k_uAPICallInvalid;
	if ( !pHTTP->SendHTTPRequest( h, &hCall ) )
	{
		pHTTP->ReleaseHTTPRequest( h );
		m_flNextRecv = (float)Plat_FloatTime() + 1.f;
		return;
	}
	m_hRecv = h;
	m_callRecv.Set( hCall, this, &CFrontressGC::OnRecvCompleted );
}

void CFrontressGC::OnRecvCompleted( HTTPRequestCompleted_t *pInfo, bool bIOFailure )
{
	ISteamHTTP *pHTTP = HTTP();
	m_hRecv = INVALID_HTTPREQUEST_HANDLE;
	if ( !pInfo || !pHTTP )
		return;
	const int nStatus = bIOFailure ? 0 : pInfo->m_eStatusCode;

	if ( nStatus == k_EHTTPStatusCode401Unauthorized )
	{
		pHTTP->ReleaseHTTPRequest( pInfo->m_hRequest );
		LoseSession( "the coordinator ended our session" );
		return;
	}
	if ( nStatus != k_EHTTPStatusCode200OK )
	{
		pHTTP->ReleaseHTTPRequest( pInfo->m_hRequest );
		// 204 is "nothing yet": poll again straight away. Anything else is
		// trouble on the way; give it a moment.
		m_flNextRecv = (float)Plat_FloatTime() + ( nStatus == k_EHTTPStatusCode204NoContent ? 0.f : 2.f );
		return;
	}

	CUtlVector< uint8 > body;
	const bool bRead = ReadBody( pHTTP, pInfo->m_hRequest, body );
	pHTTP->ReleaseHTTPRequest( pInfo->m_hRequest );
	m_flNextRecv = 0.f;
	if ( !bRead )
		return;

	// [u64 seq][u32 type][u32 len][data] ...
	const uint8 *p = body.Base();
	uint32 cubLeft = (uint32)body.Count();
	while ( cubLeft >= 16 )
	{
		uint64 ulSeq;
		uint32 unType, cub;
		V_memcpy( &ulSeq, p, 8 );
		V_memcpy( &unType, p + 8, 4 );
		V_memcpy( &cub, p + 12, 4 );
		if ( cub > cubLeft - 16 )
		{
			Warning( "Game coordinator sent a malformed batch\n" );
			break;
		}
		if ( ulSeq > m_ulLastInSeq )
		{
			m_ulLastInSeq = ulSeq;
			GCDbg( "<- msg %u (%u bytes) seq %llu\n", unType & 0x7FFFFFFF, cub, (unsigned long long)ulSeq );
			Dispatch( p + 16, cub );
			// A handler may have ended the session (a shutdown, a level
			// change); stop rather than dispatch into a dead one.
			if ( m_eState != k_eState_Connected )
				return;
		}
		p += 16 + cub;
		cubLeft -= 16 + cub;
	}
	StartRecv();
}

//-----------------------------------------------------------------------------
// Purpose: Hand one message to gcsdk, exactly as Steam's GCMessageAvailable
//			callback used to: a reply goes to the job waiting on it, anything
//			else starts the job registered for its type.
//-----------------------------------------------------------------------------
void CFrontressGC::Dispatch( const uint8 *pubData, uint32 cubData )
{
	GCSDK::CGCClient *pClient = GCClientSystem() ? GCClientSystem()->GetGCClient() : NULL;
	if ( !pClient )
		return;

	GCSDK::CNetPacket *pPacket = GCSDK::CNetPacketPool::AllocNetPacket();
	pPacket->Init( cubData, pubData );
	GCSDK::IMsgNetPacket *pMsg = GCSDK::IMsgNetPacketFromCNetPacket( pPacket );
	pPacket->Release();
	if ( !pMsg )
	{
		Warning( "Game coordinator sent a message gcsdk could not parse (%u bytes)\n", cubData );
		return;
	}
	pClient->GetJobMgr().BRouteMsgToJob( pClient, pMsg,
		GCSDK::JobMsgInfo_t( pMsg->GetEMsg(), pMsg->GetSourceJobID(), pMsg->GetTargetJobID(), GCSDK::k_EServerTypeGCClient ) );
	pMsg->Release();
}

//-----------------------------------------------------------------------------
// Purpose: The game only reads objects from a cache that is subscribed. A
//			client's cache is normally subscribed by the web API inventory; if
//			that has not arrived (or never will), subscribe it empty so the
//			GC's objects are visible anyway. When the inventory does arrive it
//			replaces the cache, and RequestCacheRefresh puts ours back.
//-----------------------------------------------------------------------------
void CFrontressGC::EnsureOwnCacheSubscribed()
{
	const CSteamID steamID = OurSteamID();
	if ( !steamID.IsValid() || !GCClientSystem() )
		return;
	GCSDK::CGCClientSharedObjectCache *pCache = GCClientSystem()->GetSOCache( steamID );
	if ( pCache && pCache->BIsSubscribed() )
		return;

	// CMsgSOCacheSubscribed { fixed64 owner = 1; }, written by hand: its
	// generated class lives in gcsdk, not in the game build.
	uint8 msg[9];
	msg[0] = ( 1 << 3 ) | 1;
	const uint64 ulOwner = steamID.ConvertToUint64();
	V_memcpy( &msg[1], &ulOwner, 8 );
	GCClientSystem()->GetGCClient()->AddLocalSOCache( steamID, msg, sizeof( msg ) );
}

void CFrontressGC::RequestCacheRefresh( const CSteamID &owner )
{
	if ( m_eState != k_eState_Connected || !owner.IsValid() )
		return;
	// The inventory and our own subscribe can land in the same second;
	// one refresh covers both.
	const float flNow = (float)Plat_FloatTime();
	if ( flNow < m_flNextRefresh )
		return;
	m_flNextRefresh = flNow + 1.f;

	// [u32 emsg|proto][u32 hdrlen = 0][CMsgSOCacheSubscriptionRefresh { fixed64 owner = 1 }]
	uint8 msg[8 + 9];
	const uint32 unType = k_ESOMsg_CacheSubscriptionRefresh | GCSDK::k_EMsgProtoBufFlag;
	const uint32 unHdr = 0;
	V_memcpy( &msg[0], &unType, 4 );
	V_memcpy( &msg[4], &unHdr, 4 );
	msg[8] = ( 1 << 3 ) | 1;
	const uint64 ulOwner = owner.ConvertToUint64();
	V_memcpy( &msg[9], &ulOwner, 8 );
	SendGCMessage( unType, msg, sizeof( msg ) );
}

//=============================================================================
// Messages from the GC that only we handle.
//=============================================================================
#ifdef CLIENT_DLL

// What the GC offers, which greys out the modes it does not run.
class CGCWorldStatusBroadcast : public GCSDK::CGCClientJob
{
public:
	CGCWorldStatusBroadcast( GCSDK::CGCClient *pClient ) : GCSDK::CGCClientJob( pClient ) {}
	virtual bool BYieldingRunGCJob( GCSDK::IMsgNetPacket *pNetPacket ) OVERRIDE
	{
		GCSDK::CProtoBufMsg< CMsgTFWorldStatus > msg( pNetPacket );
		GTFGCClientSystem()->SetWorldStatus( msg.Body() );
		return true;
	}
};
GC_REG_JOB( GCSDK::CGCClient, CGCWorldStatusBroadcast, "CGCWorldStatusBroadcast", k_EMsgGC_WorldStatusBroadcast, GCSDK::k_EServerTypeGCClient );

// Something the GC wants the player to read: why a queue was refused, why a
// party could not be joined.
class CGCFrontressNotice : public GCSDK::CGCClientJob
{
public:
	CGCFrontressNotice( GCSDK::CGCClient *pClient ) : GCSDK::CGCClientJob( pClient ) {}
	virtual bool BYieldingRunGCJob( GCSDK::IMsgNetPacket *pNetPacket ) OVERRIDE
	{
		GCSDK::CProtoBufMsg< CMsgPartyChatMsg > msg( pNetPacket );
		const char *pszText = msg.Body().msg().c_str();
		if ( pszText && pszText[0] )
		{
			Msg( "Game coordinator: %s\n", pszText );
			ShowMessageBox( "#TF_MM_GenericFailure_Title", pszText );
		}
		return true;
	}
};
GC_REG_JOB( GCSDK::CGCClient, CGCFrontressNotice, "CGCFrontressNotice", k_EMsgGCFrontress_Notice, GCSDK::k_EServerTypeGCClient );

// How everyone in the party looks (class, team, items), for the menu's lobby.
// JSON, read by the Frontress menu (fui_tf.cpp).
extern void FuiOnPartyLooks( const char *pszJSON );
class CGCFrontressPartyLooks : public GCSDK::CGCClientJob
{
public:
	CGCFrontressPartyLooks( GCSDK::CGCClient *pClient ) : GCSDK::CGCClientJob( pClient ) {}
	virtual bool BYieldingRunGCJob( GCSDK::IMsgNetPacket *pNetPacket ) OVERRIDE
	{
		GCSDK::CProtoBufMsg< CMsgPartyChatMsg > msg( pNetPacket );
		FuiOnPartyLooks( msg.Body().msg().c_str() );
		return true;
	}
};
GC_REG_JOB( GCSDK::CGCClient, CGCFrontressPartyLooks, "CGCFrontressPartyLooks", k_EMsgGCFrontress_PartyLooks, GCSDK::k_EServerTypeGCClient );

// Chat, who is online, the clan, the support tab: one JSON event each (see
// services/gc/internal/gc/social.go). The menu applies them on its frame.
extern void FuiOnSocialEvent( const char *pszJSON );
class CGCFrontressSocialEvent : public GCSDK::CGCClientJob
{
public:
	CGCFrontressSocialEvent( GCSDK::CGCClient *pClient ) : GCSDK::CGCClientJob( pClient ) {}
	virtual bool BYieldingRunGCJob( GCSDK::IMsgNetPacket *pNetPacket ) OVERRIDE
	{
		GCSDK::CProtoBufMsg< CMsgPartyChatMsg > msg( pNetPacket );
		FuiOnSocialEvent( msg.Body().msg().c_str() );
		return true;
	}
};
GC_REG_JOB( GCSDK::CGCClient, CGCFrontressSocialEvent, "CGCFrontressSocialEvent", k_EMsgGCFrontress_SocialEvent, GCSDK::k_EServerTypeGCClient );

#endif // CLIENT_DLL

//-----------------------------------------------------------------------------
CON_COMMAND( tf_gc_status, "Print the state of the game coordinator connection." )
{
	CFrontressGC *pGC = FrontressGC();
	Msg( "Game coordinator: %s\n", pGC->GetBaseURL()[0] ? pGC->GetBaseURL() : "(not configured)" );
	Msg( "  state: %s\n", pGC->GetStateName() );
	if ( pGC->GetLastError()[0] )
		Msg( "  last error: %s\n", pGC->GetLastError() );
}
