//========= GREYLINE FRONTRESS ============================================//
//
// Purpose: see greyline_bridge.h. Wire protocol match: see the "Bridge"
// object in game/tc2/loose/resource/html/greyline.html.
//
//=========================================================================//

#include "cbase.h"
#include "greyline_bridge.h"
#include "cdll_client_int.h"
#include "tier1/checksum_sha1.h"
#include "tier1/keyvaluesjson.h"
#include "tier1/fmtstr.h"
#include "KeyValues.h"

#ifdef _WIN32
#include <winsock2.h>
#include <ws2tcpip.h>
typedef int socklen_t;
#define GL_LAST_ERROR		WSAGetLastError()
#define GL_WOULDBLOCK		WSAEWOULDBLOCK
#define GL_CLOSE( s )		closesocket( s )
#else
#include <sys/types.h>
#include <sys/socket.h>
#include <netinet/in.h>
#include <netinet/tcp.h>
#include <arpa/inet.h>
#include <unistd.h>
#include <fcntl.h>
#include <errno.h>
#define GL_LAST_ERROR		errno
#define GL_WOULDBLOCK		EWOULDBLOCK
#define GL_CLOSE( s )		close( s )
#endif

// memdbgon must be the last include file in a .cpp file!!!
#include "tier0/memdbgon.h"

ConVar greyline_bridge_enable( "greyline_bridge_enable", "1", FCVAR_CLIENTDLL | FCVAR_ARCHIVE,
	"Run the loopback websocket bridge the main menu's war map page uses to reach the engine and the coordinator." );

ConVar greyline_bridge_port( "greyline_bridge_port", "58270", FCVAR_CLIENTDLL,
	"Loopback port for the Greyline menu bridge. Must match the port game/tc2/loose/resource/html/greyline.html connects to." );

ConVar greyline_bridge_debug( "greyline_bridge_debug", "0", FCVAR_CLIENTDLL,
	"Spew Greyline bridge connections and dispatched methods." );

static CGreylineBridge g_GreylineBridge;
CGreylineBridge *GreylineBridge() { return &g_GreylineBridge; }

//-----------------------------------------------------------------------------
// Small helpers
//-----------------------------------------------------------------------------
static void SetNonBlocking( int s )
{
#ifdef _WIN32
	u_long nMode = 1;
	ioctlsocket( s, FIONBIO, &nMode );
#else
	int flags = fcntl( s, F_GETFL, 0 );
	fcntl( s, F_SETFL, flags | O_NONBLOCK );
#endif
}

// RFC 4648 standard base64, the form the websocket handshake needs. Nothing
// here ever needs to decode it back.
static void Base64Encode( const byte *pData, int nLen, char *pszOut, int nOutSize )
{
	static const char s_Table[] = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
	int nOut = 0;
	int i = 0;
	for ( ; i + 2 < nLen; i += 3 )
	{
		const uint32 n = ( (uint32)pData[i] << 16 ) | ( (uint32)pData[i + 1] << 8 ) | pData[i + 2];
		if ( nOut + 4 >= nOutSize ) break;
		pszOut[nOut++] = s_Table[ ( n >> 18 ) & 0x3F ];
		pszOut[nOut++] = s_Table[ ( n >> 12 ) & 0x3F ];
		pszOut[nOut++] = s_Table[ ( n >> 6 ) & 0x3F ];
		pszOut[nOut++] = s_Table[ n & 0x3F ];
	}
	const int nRemain = nLen - i;
	if ( nRemain > 0 && nOut + 4 < nOutSize )
	{
		uint32 n = (uint32)pData[i] << 16;
		if ( nRemain == 2 ) n |= (uint32)pData[i + 1] << 8;
		pszOut[nOut++] = s_Table[ ( n >> 18 ) & 0x3F ];
		pszOut[nOut++] = s_Table[ ( n >> 12 ) & 0x3F ];
		pszOut[nOut++] = ( nRemain == 2 ) ? s_Table[ ( n >> 6 ) & 0x3F ] : '=';
		pszOut[nOut++] = '=';
	}
	pszOut[nOut] = '\0';
}

// The fixed GUID RFC 6455 defines for turning a client's Sec-WebSocket-Key
// into the Sec-WebSocket-Accept the handshake response needs.
static void ComputeWebSocketAccept( const char *pszKey, char *pszOut, int nOutSize )
{
	CFmtStr strConcat( "%s258EAFA5-E914-47DA-95CA-C5AB0DC85B11", pszKey );
	unsigned char digest[ k_cubHash ];
	GenerateHash( digest, (byte *)strConcat.Access(), strConcat.Length() );
	Base64Encode( digest, k_cubHash, pszOut, nOutSize );
}

// Appends pszRaw to out as a double-quoted, escaped JSON string. Every value
// the bridge ever sends back is text, so this is the one formatting primitive
// Reply() needs.
static void AppendJSONString( CUtlBuffer &out, const char *pszRaw )
{
	out.PutChar( '"' );
	if ( pszRaw )
	{
		for ( const char *p = pszRaw; *p; ++p )
		{
			const unsigned char c = (unsigned char)*p;
			switch ( c )
			{
			case '"':  out.PutString( "\\\"" ); break;
			case '\\': out.PutString( "\\\\" ); break;
			case '\n': out.PutString( "\\n" ); break;
			case '\r': out.PutString( "\\r" ); break;
			case '\t': out.PutString( "\\t" ); break;
			default:
				if ( c < 0x20 )
				{
					char buf[8];
					V_snprintf( buf, sizeof( buf ), "\\u%04x", c );
					out.PutString( buf );
				}
				else
				{
					out.PutChar( (char)c );
				}
			}
		}
	}
	out.PutChar( '"' );
}

//-----------------------------------------------------------------------------
CGreylineBridge::CGreylineBridge()
	: CAutoGameSystemPerFrame( "CGreylineBridge" )
{
	m_nListenSocket = -1;
	m_pConn = NULL;
	m_nGameOverSeq = 0;
}

//-----------------------------------------------------------------------------
bool CGreylineBridge::Init()
{
#ifdef _WIN32
	WSADATA wsa;
	WSAStartup( MAKEWORD( 2, 2 ), &wsa );
#endif
	ListenForGameEvent( "teamplay_game_over" );
	ListenForGameEvent( "tf_game_over" );
	return true;
}

//-----------------------------------------------------------------------------
void CGreylineBridge::Shutdown()
{
	CloseConn( "shutting down" );
	CloseListener();
}

//-----------------------------------------------------------------------------
void CGreylineBridge::OpenListener()
{
	if ( m_nListenSocket != -1 )
	{
		return;
	}

	const int nSocket = (int)socket( AF_INET, SOCK_STREAM, IPPROTO_TCP );
	if ( nSocket == -1 )
	{
		return;
	}
	SetNonBlocking( nSocket );

	int nReuse = 1;
	setsockopt( nSocket, SOL_SOCKET, SO_REUSEADDR, (const char *)&nReuse, sizeof( nReuse ) );

	sockaddr_in addr;
	V_memset( &addr, 0, sizeof( addr ) );
	addr.sin_family = AF_INET;
	// Loopback only. This socket can run a console command on this machine on
	// nothing more than an unauthenticated request; it must never be reachable
	// from anywhere but this same computer.
	addr.sin_addr.s_addr = htonl( INADDR_LOOPBACK );
	addr.sin_port = htons( (unsigned short)greyline_bridge_port.GetInt() );

	if ( bind( nSocket, (sockaddr *)&addr, sizeof( addr ) ) != 0 || listen( nSocket, 1 ) != 0 )
	{
		Warning( "[greyline/bridge] could not bind 127.0.0.1:%d — is another instance already running?\n",
			greyline_bridge_port.GetInt() );
		GL_CLOSE( nSocket );
		return;
	}

	m_nListenSocket = nSocket;
	if ( greyline_bridge_debug.GetBool() )
	{
		Msg( "[greyline/bridge] listening on 127.0.0.1:%d\n", greyline_bridge_port.GetInt() );
	}
}

//-----------------------------------------------------------------------------
void CGreylineBridge::CloseListener()
{
	if ( m_nListenSocket != -1 )
	{
		GL_CLOSE( m_nListenSocket );
		m_nListenSocket = -1;
	}
}

//-----------------------------------------------------------------------------
void CGreylineBridge::AcceptNew()
{
	sockaddr_in from;
	socklen_t nFromLen = sizeof( from );
	const int nSocket = (int)accept( m_nListenSocket, (sockaddr *)&from, &nFromLen );
	if ( nSocket == -1 )
	{
		return;
	}

	// Belt and suspenders: refuse anything that did not come from this same
	// machine, in case some future change ever widens the bind address.
	if ( from.sin_addr.s_addr != htonl( INADDR_LOOPBACK ) )
	{
		GL_CLOSE( nSocket );
		return;
	}

	SetNonBlocking( nSocket );
	int nNoDelay = 1;
	setsockopt( nSocket, IPPROTO_TCP, TCP_NODELAY, (const char *)&nNoDelay, sizeof( nNoDelay ) );

	// One connection at a time: a new one is a reloaded or reopened menu page,
	// not a second legitimate client.
	CloseConn( "replaced by a new connection" );

	m_pConn = new Conn_t;
	m_pConn->nSocket = nSocket;
	m_pConn->eState = CONN_HANDSHAKE;

	if ( greyline_bridge_debug.GetBool() )
	{
		Msg( "[greyline/bridge] menu panel connected\n" );
	}
}

//-----------------------------------------------------------------------------
void CGreylineBridge::CloseConn( const char *pszReason )
{
	if ( !m_pConn )
	{
		return;
	}
	if ( greyline_bridge_debug.GetBool() )
	{
		Msg( "[greyline/bridge] connection closed: %s\n", pszReason ? pszReason : "closed" );
	}
	GL_CLOSE( m_pConn->nSocket );
	delete m_pConn;
	m_pConn = NULL;
}

//-----------------------------------------------------------------------------
void CGreylineBridge::Update( float flFrameTime )
{
	if ( !greyline_bridge_enable.GetBool() )
	{
		CloseConn( "greyline_bridge_enable is 0" );
		CloseListener();
		return;
	}

	OpenListener();
	if ( m_nListenSocket == -1 )
	{
		return;
	}
	AcceptNew();
	if ( !m_pConn )
	{
		return;
	}

	PumpRecv();
	if ( !m_pConn )
	{
		return;
	}
	if ( m_pConn->eState == CONN_HANDSHAKE )
	{
		TryCompleteHandshake();
	}
	else
	{
		ProcessFrames();
	}
	if ( !m_pConn )
	{
		return;
	}
	PumpSend();
}

//-----------------------------------------------------------------------------
void CGreylineBridge::PumpRecv()
{
	byte chunk[8192];
	for ( ;; )
	{
		const int nRead = recv( m_pConn->nSocket, (char *)chunk, sizeof( chunk ), 0 );
		if ( nRead > 0 )
		{
			m_pConn->RecvBuf.Put( chunk, nRead );
			continue;
		}
		if ( nRead == 0 )
		{
			CloseConn( "menu panel disconnected" );
			return;
		}
		if ( GL_LAST_ERROR == GL_WOULDBLOCK )
		{
			return; // drained for this frame
		}
		CloseConn( "read error" );
		return;
	}
}

//-----------------------------------------------------------------------------
void CGreylineBridge::PumpSend()
{
	if ( !m_pConn )
	{
		return;
	}
	CUtlBuffer &buf = m_pConn->SendBuf;
	while ( buf.TellPut() > buf.TellGet() )
	{
		const byte *pBase = (const byte *)buf.Base() + buf.TellGet();
		const int nPending = buf.TellPut() - buf.TellGet();
		const int nSent = send( m_pConn->nSocket, (const char *)pBase, nPending, 0 );
		if ( nSent > 0 )
		{
			buf.SeekGet( CUtlBuffer::SEEK_CURRENT, nSent );
			continue;
		}
		if ( nSent < 0 && GL_LAST_ERROR == GL_WOULDBLOCK )
		{
			return;
		}
		CloseConn( "write error" );
		return;
	}
	buf.Clear();
}

//-----------------------------------------------------------------------------
// Purpose: consumes the buffered HTTP upgrade request once it has fully
// arrived (ends in a blank line) and replies with the 101 handshake.
//-----------------------------------------------------------------------------
bool CGreylineBridge::TryCompleteHandshake()
{
	CUtlBuffer &buf = m_pConn->RecvBuf;
	const char *pData = (const char *)buf.Base() + buf.TellGet();
	const int nAvailable = buf.TellPut() - buf.TellGet();

	// Look for the blank line ending the request headers. A real browser
	// upgrade request is a few hundred bytes at most; refuse to buffer forever
	// on something that will never send one.
	const char *pEnd = NULL;
	for ( int i = 0; i + 3 < nAvailable; ++i )
	{
		if ( pData[i] == '\r' && pData[i + 1] == '\n' && pData[i + 2] == '\r' && pData[i + 3] == '\n' )
		{
			pEnd = pData + i;
			break;
		}
	}
	if ( !pEnd )
	{
		if ( nAvailable > 8192 )
		{
			CloseConn( "handshake request too large" );
		}
		return false;
	}

	CUtlString strHeaders;
	strHeaders.SetDirect( pData, pEnd - pData );

	// Refuse anything an ordinary web page could have sent. This socket can run
	// a console command on nothing more than an unauthenticated local request,
	// and being loopback-only is not enough on its own: a malicious page open
	// in the player's regular browser can still open a websocket to
	// 127.0.0.1 and would otherwise be indistinguishable from the game's own
	// CEF panel, which sends no Origin header (or "null") for locally loaded
	// content. A real website always sends a http(s):// one.
	const char *pOriginHeader = V_stristr( strHeaders.Get(), "Origin:" );
	if ( pOriginHeader )
	{
		const char *pOriginVal = pOriginHeader + V_strlen( "Origin:" );
		while ( *pOriginVal == ' ' ) ++pOriginVal;
		if ( !V_strnicmp( pOriginVal, "http://", 7 ) || !V_strnicmp( pOriginVal, "https://", 8 ) )
		{
			CloseConn( "refusing a websocket handshake with a browser Origin header" );
			return false;
		}
	}

	const char *pKeyHeader = V_stristr( strHeaders.Get(), "Sec-WebSocket-Key:" );
	if ( !pKeyHeader )
	{
		CloseConn( "no Sec-WebSocket-Key in the upgrade request" );
		return false;
	}
	pKeyHeader += V_strlen( "Sec-WebSocket-Key:" );
	while ( *pKeyHeader == ' ' ) ++pKeyHeader;
	const char *pKeyEnd = strstr( pKeyHeader, "\r\n" );
	if ( !pKeyEnd )
	{
		pKeyEnd = pKeyHeader + V_strlen( pKeyHeader );
	}
	CUtlString strKey;
	strKey.SetDirect( pKeyHeader, pKeyEnd - pKeyHeader );

	char szAccept[64];
	ComputeWebSocketAccept( strKey.Get(), szAccept, sizeof( szAccept ) );

	CFmtStrN<512> strResponse(
		"HTTP/1.1 101 Switching Protocols\r\n"
		"Upgrade: websocket\r\n"
		"Connection: Upgrade\r\n"
		"Sec-WebSocket-Accept: %s\r\n\r\n", szAccept );

	// Consume exactly the request line through the blank line that ends it —
	// anything after belongs to the first websocket frame, if one is already
	// buffered.
	buf.SeekGet( CUtlBuffer::SEEK_HEAD, (int)( pEnd - (const char *)buf.Base() ) + 4 );

	m_pConn->SendBuf.Put( strResponse.Access(), strResponse.Length() );
	m_pConn->eState = CONN_OPEN;

	if ( greyline_bridge_debug.GetBool() )
	{
		Msg( "[greyline/bridge] handshake complete\n" );
	}
	return true;
}

//-----------------------------------------------------------------------------
// Purpose: pulls one complete websocket frame out of buf if one is fully
// buffered. Supports text/binary/close/ping with a 7-bit or 16-bit extended
// payload length — everything this bridge ever actually receives, which is
// small JSON control messages from a browser websocket client that does not
// fragment small text sends. A 64-bit extended length or a fragmented message
// is refused rather than mishandled.
//-----------------------------------------------------------------------------
bool CGreylineBridge::ExtractOneFrame( CUtlBuffer &buf, int &opcode, CUtlBuffer &payloadOut )
{
	const int nAvailable = buf.TellPut() - buf.TellGet();
	if ( nAvailable < 2 )
	{
		return false;
	}
	const byte *pBase = (const byte *)buf.Base() + buf.TellGet();

	const bool bFin = ( pBase[0] & 0x80 ) != 0;
	opcode = pBase[0] & 0x0F;
	const bool bMasked = ( pBase[1] & 0x80 ) != 0;
	uint64 nLen = pBase[1] & 0x7F;

	int nHeaderLen = 2;
	if ( nLen == 126 )
	{
		if ( nAvailable < 4 ) return false;
		nLen = ( (uint16)pBase[2] << 8 ) | pBase[3];
		nHeaderLen = 4;
	}
	else if ( nLen == 127 )
	{
		CloseConn( "refusing an oversized websocket frame" );
		return false;
	}

	if ( !bFin )
	{
		CloseConn( "fragmented websocket messages are not supported" );
		return false;
	}

	const int nMaskLen = bMasked ? 4 : 0;
	if ( (uint64)nAvailable < (uint64)nHeaderLen + nMaskLen + nLen )
	{
		return false; // wait for the rest
	}

	const byte *pMask = pBase + nHeaderLen;
	const byte *pPayload = pMask + nMaskLen;

	payloadOut.Clear();
	if ( nLen > 0 )
	{
		payloadOut.EnsureCapacity( (int)nLen );
		for ( uint64 i = 0; i < nLen; ++i )
		{
			const byte b = bMasked ? (byte)( pPayload[i] ^ pMask[i % 4] ) : pPayload[i];
			payloadOut.PutChar( (char)b );
		}
	}

	buf.SeekGet( CUtlBuffer::SEEK_CURRENT, nHeaderLen + nMaskLen + (int)nLen );
	return true;
}

//-----------------------------------------------------------------------------
void CGreylineBridge::ProcessFrames()
{
	for ( ;; )
	{
		int opcode = 0;
		CUtlBuffer payload( 0, 256 );
		if ( !ExtractOneFrame( m_pConn->RecvBuf, opcode, payload ) )
		{
			break;
		}
		if ( !m_pConn )
		{
			return; // ExtractOneFrame closed the connection
		}

		switch ( opcode )
		{
		case 0x8: // close
			CloseConn( "menu panel closed the socket" );
			return;

		case 0x9: // ping — answer with the same payload as a pong
			SendFrame( 0xA, payload.Base(), payload.TellPut() );
			break;

		case 0x1: // text
		{
			payload.PutChar( '\0' ); // KeyValuesJSONParser wants a C string
			const char *pszText = (const char *)payload.Base();

			KeyValuesJSONParser parser( pszText );
			KeyValues *pRoot = parser.ParseFile();
			if ( !pRoot )
			{
				if ( greyline_bridge_debug.GetBool() )
				{
					Msg( "[greyline/bridge] malformed message: %s\n", parser.m_szErrMsg );
				}
				break;
			}
			const int nID = pRoot->GetInt( "i", -1 );
			const char *pszMethod = pRoot->GetString( "m", "" );
			const char *pszParams = pRoot->GetString( "p", "" );
			if ( nID >= 0 && pszMethod[0] )
			{
				DispatchMessage( nID, pszMethod, pszParams );
			}
			pRoot->deleteThis();
			break;
		}

		default:
			break; // binary and continuation frames never occur in this protocol
		}
	}
}

//-----------------------------------------------------------------------------
void CGreylineBridge::SendFrame( int opcode, const void *pData, int nLen )
{
	if ( !m_pConn )
	{
		return;
	}
	CUtlBuffer &out = m_pConn->SendBuf;

	out.PutUnsignedChar( (unsigned char)( 0x80 | ( opcode & 0x0F ) ) ); // FIN + opcode

	if ( nLen < 126 )
	{
		out.PutUnsignedChar( (unsigned char)nLen ); // no mask bit: server frames are unmasked
	}
	else
	{
		out.PutUnsignedChar( 126 );
		out.PutUnsignedChar( (unsigned char)( ( nLen >> 8 ) & 0xFF ) );
		out.PutUnsignedChar( (unsigned char)( nLen & 0xFF ) );
	}
	if ( nLen > 0 )
	{
		out.Put( pData, nLen );
	}
}

//-----------------------------------------------------------------------------
void CGreylineBridge::Reply( int nID, const char *pszResult )
{
	if ( !m_pConn )
	{
		return;
	}
	CUtlBuffer json( 0, 256 );
	CFmtStr strHead( "{\"i\":%d,\"r\":", nID );
	json.Put( strHead.Access(), strHead.Length() );
	AppendJSONString( json, pszResult );
	json.PutChar( '}' );

	SendFrame( 0x1, json.Base(), json.TellPut() );
}

//-----------------------------------------------------------------------------
// Purpose: the three methods the menu page already speaks (cmd, getcvar,
// fetch — see greyline.html's Bridge object), plus two small ones only the
// engine can answer: state (is the client in a battle yet, for the loading /
// connecting UI) and host_address (has this listen server's own FakeIP
// arrived yet, for standing up a P2P battle — see greyline_hoststate.cpp,
// which is the only thing that ever sets these two convars).
//-----------------------------------------------------------------------------
void CGreylineBridge::DispatchMessage( int nID, const char *pszMethod, const char *pszParams )
{
	if ( greyline_bridge_debug.GetBool() )
	{
		Msg( "[greyline/bridge] <- %s\n", pszMethod );
	}

	if ( !V_strcmp( pszMethod, "cmd" ) )
	{
		engine->ClientCmd_Unrestricted( CFmtStr( "%s\n", pszParams ) );
		Reply( nID, "" );
		return;
	}

	if ( !V_strcmp( pszMethod, "getcvar" ) )
	{
		ConVarRef ref( pszParams );
		Reply( nID, ref.IsValid() ? ref.GetString() : "" );
		return;
	}

	if ( !V_strcmp( pszMethod, "state" ) )
	{
		const bool bInGame = engine->IsInGame();
		const char *pszLevel = bInGame ? engine->GetLevelName() : "";
		CFmtStr strJSON( "{\"in_game\":%s,\"level\":%s,\"game_over_seq\":%d}",
			bInGame ? "true" : "false",
			pszLevel && pszLevel[0] ? CFmtStr( "\"%s\"", pszLevel ).Access() : "\"\"",
			m_nGameOverSeq );
		Reply( nID, strJSON.Access() );
		return;
	}

	if ( !V_strcmp( pszMethod, "host_address" ) )
	{
		ConVarRef addr( "greyline_server_addr" );
		ConVarRef id( "greyline_server_id" );
		const bool bIdentityReady = id.IsValid() && id.GetString()[0] && V_strcmp( id.GetString(), "0" ) != 0;
		CFmtStr strJSON( "{\"ready\":%s,\"address\":\"%s\",\"steam_id\":\"%s\"}",
			bIdentityReady ? "true" : "false",
			addr.IsValid() ? addr.GetString() : "",
			id.IsValid() ? id.GetString() : "0" );
		Reply( nID, strJSON.Access() );
		return;
	}

	if ( !V_strcmp( pszMethod, "fetch" ) )
	{
		HandleFetch( nID, pszParams );
		return;
	}

	Reply( nID, "" );
}

//-----------------------------------------------------------------------------
namespace
{
	// Carries a pending fetch's reply id across the async CGreylineHTTP
	// callback, which only ever hands raw context back.
	struct FetchContext_t
	{
		int nID;
	};

	void OnFetchComplete( void *pContext, const greyline::HttpResult_t &result )
	{
		FetchContext_t *pCtx = (FetchContext_t *)pContext;
		// "<status> <body>" — see greyline.html's GC.request, which parses
		// exactly this shape back out.
		CUtlBuffer reply( 0, 256 );
		CFmtStr strStatus( "%d ", result.bTransportOK ? result.nStatusCode : 0 );
		reply.Put( strStatus.Access(), strStatus.Length() );
		if ( result.pszBody && result.nBodyLen > 0 )
		{
			reply.Put( result.pszBody, (int)result.nBodyLen );
		}
		reply.PutChar( '\0' );
		GreylineBridge()->Reply( pCtx->nID, (const char *)reply.Base() );
		delete pCtx;
	}
}

//-----------------------------------------------------------------------------
void CGreylineBridge::HandleFetch( int nID, const char *pszParamsJSON )
{
	KeyValuesJSONParser parser( pszParamsJSON );
	KeyValues *pReq = parser.ParseFile();
	if ( !pReq )
	{
		Reply( nID, "0 " );
		return;
	}

	const char *pszURL = pReq->GetString( "url", "" );
	const char *pszMethodName = pReq->GetString( "method", "GET" );
	const char *pszBody = pReq->GetString( "body", "" );
	KeyValues *pHeaders = pReq->FindKey( "headers" );

	CUtlVector<greyline::HttpHeader_t> headers;
	// Copy header strings out of the KeyValues tree into storage that outlives
	// this function: greyline::CGreylineHTTP::Send only promises to copy them
	// before it returns, and pReq is about to be freed.
	CUtlVector<CUtlString> headerStorage;
	if ( pHeaders )
	{
		for ( KeyValues *pKV = pHeaders->GetFirstValue(); pKV; pKV = pKV->GetNextValue() )
		{
			headerStorage.AddToTail( pKV->GetName() );
			headerStorage.AddToTail( pKV->GetString() );
		}
		for ( int i = 0; i + 1 < headerStorage.Count(); i += 2 )
		{
			greyline::HttpHeader_t h = { headerStorage[i].Get(), headerStorage[i + 1].Get() };
			headers.AddToTail( h );
		}
	}

	const greyline::HttpMethod_t eMethod = V_stricmp( pszMethodName, "POST" ) == 0
		? greyline::HTTP_POST : greyline::HTTP_GET;

	FetchContext_t *pCtx = new FetchContext_t;
	pCtx->nID = nID;

	// pszURL/pszBody/headers are all copied by Send before it returns, so it is
	// safe to free pReq (which owns their storage) right after this call.
	greyline::GreylineHTTP()->Send( eMethod, pszURL, headers, pszBody, &OnFetchComplete, pCtx );

	pReq->deleteThis();
}

//-----------------------------------------------------------------------------
void CGreylineBridge::FireGameEvent( IGameEvent *pEvent )
{
	if ( !pEvent )
	{
		return;
	}
	const char *pszName = pEvent->GetName();
	if ( !V_strcmp( pszName, "teamplay_game_over" ) || !V_strcmp( pszName, "tf_game_over" ) )
	{
		++m_nGameOverSeq;
	}
}
