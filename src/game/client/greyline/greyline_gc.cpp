//========= GREYLINE FRONTRESS ============================================//
//
// Purpose: the client's link to the Greyline Game Coordinator.
//
//=========================================================================//

#include "cbase.h"
#include "greyline_gc.h"
#include "greyline_lobby.h"
#include "greyline/greyline_shared.h"
#include "cdll_client_int.h"
#include "inetchannelinfo.h"
#include "c_playerresource.h"
#include "greyline.pb.h"

#ifdef _WIN32
#include <winsock2.h>
#include <ws2tcpip.h>
typedef int socklen_t;
#define GL_LAST_ERROR		WSAGetLastError()
#define GL_WOULDBLOCK		WSAEWOULDBLOCK
#define GL_INPROGRESS		WSAEWOULDBLOCK
#define GL_CLOSE( s )		closesocket( s )
#else
#include <sys/types.h>
#include <sys/socket.h>
#include <netinet/in.h>
#include <netinet/tcp.h>
#include <arpa/inet.h>
#include <netdb.h>
#include <unistd.h>
#include <fcntl.h>
#include <errno.h>
#define GL_LAST_ERROR		errno
#define GL_WOULDBLOCK		EWOULDBLOCK
#define GL_INPROGRESS		EINPROGRESS
#define GL_CLOSE( s )		close( s )
#endif

// memdbgon must be the last include file in a .cpp file!!!
#include "tier0/memdbgon.h"

// Must match config.ProtocolVersion on the coordinator.
#define GREYLINE_GC_PROTOCOL_VERSION	1

// A single envelope. The coordinator enforces the same ceiling; anything larger
// means the stream has desynchronised and cannot be recovered.
#define GREYLINE_MAX_FRAME				( 1 * 1024 * 1024 )

ConVar greyline_gc_address( "greyline_gc_address", "127.0.0.1:27100", FCVAR_CLIENTDLL | FCVAR_ARCHIVE,
	"Address of the Greyline Game Coordinator." );

ConVar greyline_gc_enable( "greyline_gc_enable", "1", FCVAR_CLIENTDLL | FCVAR_ARCHIVE,
	"Connect to the Greyline Game Coordinator." );

ConVar greyline_gc_debug( "greyline_gc_debug", "0", FCVAR_CLIENTDLL,
	"Spew Greyline coordinator traffic." );

ConVar greyline_can_host( "greyline_can_host", "1", FCVAR_CLIENTDLL | FCVAR_ARCHIVE,
	"Offer this machine as a battle host. Turning it off never makes you the host." );

ConVar greyline_upload_kbps( "greyline_upload_kbps", "0", FCVAR_CLIENTDLL | FCVAR_ARCHIVE,
	"Upload bandwidth to report to the coordinator, in kbps. 0 uses a conservative estimate." );

static CGreylineGC g_GreylineGC;
CGreylineGC *GreylineGC() { return &g_GreylineGC; }

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

// Splits "host:port". Returns false if the port is missing or unusable, since
// silently defaulting it would connect to the wrong place.
static bool ParseAddress( const char *pszAddress, char *pszHost, int nHostLen, int &nPort )
{
	const char *pszColon = V_strrchr( pszAddress, ':' );
	if ( !pszColon || pszColon == pszAddress )
	{
		return false;
	}

	int nLen = pszColon - pszAddress;
	if ( nLen >= nHostLen )
	{
		return false;
	}
	V_memcpy( pszHost, pszAddress, nLen );
	pszHost[nLen] = '\0';

	nPort = V_atoi( pszColon + 1 );
	return nPort > 0 && nPort < 65536;
}

//-----------------------------------------------------------------------------
CGreylineGC::CGreylineGC()
	: CAutoGameSystemPerFrame( "CGreylineGC" )
	, m_RecvBuffer( 0, 64 * 1024 )
	, m_SendBuffer( 0, 16 * 1024 )
{
	m_eState = GC_OFFLINE;
	m_nSocket = -1;
	m_flRetryAt = 0.0f;
	m_flRetryDelay = 1.0f;
	m_flNextHeartbeat = 0.0f;
	m_flHeartbeatInterval = 10.0f;
	m_ulJobID = 1;
	m_szMatchID[0] = '\0';
	m_bIsHost = false;
	m_bResultReported = false;
	m_szQueueMessage[0] = '\0';
	m_cbTicket = 0;
	m_hAuthTicket = k_HAuthTicketInvalid;
}

//-----------------------------------------------------------------------------
bool CGreylineGC::Init()
{
#ifdef _WIN32
	// The engine has already done this, but doing it twice is harmless and
	// makes this module independent of that assumption.
	WSADATA wsa;
	WSAStartup( MAKEWORD( 2, 2 ), &wsa );
#endif

	ListenForGameEvent( "teamplay_game_over" );
	ListenForGameEvent( "tf_game_over" );

	// Ships in game/tc2/cfg. Keeps the coordinator address out of the console
	// for anyone who is not developing this.
	engine->ClientCmd_Unrestricted( "exec greyline.cfg\n" );
	return true;
}

//-----------------------------------------------------------------------------
void CGreylineGC::Shutdown()
{
	CloseSocket();

	if ( m_hAuthTicket != k_HAuthTicketInvalid && steamapicontext && steamapicontext->SteamUser() )
	{
		steamapicontext->SteamUser()->CancelAuthTicket( m_hAuthTicket );
		m_hAuthTicket = k_HAuthTicketInvalid;
	}
}

//-----------------------------------------------------------------------------
const char *CGreylineGC::GetStateName() const
{
	switch ( m_eState )
	{
	case GC_OFFLINE:	return "offline";
	case GC_CONNECTING:	return "connecting";
	case GC_HANDSHAKE:	return "authenticating";
	case GC_READY:		return "ready";
	}
	return "unknown";
}

//-----------------------------------------------------------------------------
void CGreylineGC::SetState( GreylineGCState_t eState )
{
	if ( eState == m_eState )
	{
		return;
	}
	m_eState = eState;

	if ( greyline_gc_debug.GetBool() )
	{
		Msg( "[greyline/gc] %s\n", GetStateName() );
	}
}

//-----------------------------------------------------------------------------
void CGreylineGC::CloseSocket()
{
	if ( m_nSocket != -1 )
	{
		GL_CLOSE( m_nSocket );
		m_nSocket = -1;
	}
	m_RecvBuffer.Purge();
	m_SendBuffer.Purge();
	SetState( GC_OFFLINE );
}

//-----------------------------------------------------------------------------
void CGreylineGC::Disconnect( const char *pszReason )
{
	if ( m_nSocket != -1 )
	{
		Msg( "[greyline/gc] disconnected: %s\n", pszReason ? pszReason : "closed" );
	}
	CloseSocket();

	// Back off so a coordinator that is down does not get hammered, but keep
	// the ceiling low enough that a restart is picked up quickly.
	m_flRetryDelay = MIN( m_flRetryDelay * 2.0f, 30.0f );
	m_flRetryAt = gpGlobals->realtime + m_flRetryDelay;
}

//-----------------------------------------------------------------------------
void CGreylineGC::Connect()
{
	if ( m_nSocket != -1 )
	{
		return;
	}

	char szHost[128];
	int nPort = 0;
	if ( !ParseAddress( greyline_gc_address.GetString(), szHost, sizeof( szHost ), nPort ) )
	{
		Warning( "[greyline/gc] greyline_gc_address must be host:port, got '%s'\n",
			greyline_gc_address.GetString() );
		m_flRetryAt = gpGlobals->realtime + 30.0f;
		return;
	}

	struct addrinfo hints;
	V_memset( &hints, 0, sizeof( hints ) );
	hints.ai_family = AF_INET;
	hints.ai_socktype = SOCK_STREAM;

	char szPort[16];
	V_snprintf( szPort, sizeof( szPort ), "%d", nPort );

	struct addrinfo *pResult = NULL;
	if ( getaddrinfo( szHost, szPort, &hints, &pResult ) != 0 || !pResult )
	{
		Warning( "[greyline/gc] cannot resolve %s\n", szHost );
		m_flRetryDelay = MIN( m_flRetryDelay * 2.0f, 30.0f );
		m_flRetryAt = gpGlobals->realtime + m_flRetryDelay;
		return;
	}

	m_nSocket = (int)socket( pResult->ai_family, pResult->ai_socktype, pResult->ai_protocol );
	if ( m_nSocket == -1 )
	{
		freeaddrinfo( pResult );
		m_flRetryAt = gpGlobals->realtime + 5.0f;
		return;
	}

	SetNonBlocking( m_nSocket );

	int nNoDelay = 1;
	setsockopt( m_nSocket, IPPROTO_TCP, TCP_NODELAY, (const char *)&nNoDelay, sizeof( nNoDelay ) );

	int nResult = connect( m_nSocket, pResult->ai_addr, (socklen_t)pResult->ai_addrlen );
	freeaddrinfo( pResult );

	if ( nResult == 0 )
	{
		SetState( GC_CONNECTING ); // completion is confirmed by PumpConnect
	}
	else if ( GL_LAST_ERROR == GL_INPROGRESS || GL_LAST_ERROR == GL_WOULDBLOCK )
	{
		SetState( GC_CONNECTING );
	}
	else
	{
		Warning( "[greyline/gc] connect to %s failed (%d)\n", greyline_gc_address.GetString(), GL_LAST_ERROR );
		Disconnect( "connect failed" );
	}
}

//-----------------------------------------------------------------------------
// Purpose: a non-blocking connect reports completion through writability.
//			Returns true once the socket is usable.
//-----------------------------------------------------------------------------
bool CGreylineGC::PumpConnect()
{
	fd_set writeSet, errSet;
	FD_ZERO( &writeSet );
	FD_ZERO( &errSet );
	FD_SET( (unsigned)m_nSocket, &writeSet );
	FD_SET( (unsigned)m_nSocket, &errSet );

	struct timeval tv;
	tv.tv_sec = 0;
	tv.tv_usec = 0;

	int nReady = select( m_nSocket + 1, NULL, &writeSet, &errSet, &tv );
	if ( nReady <= 0 )
	{
		return false; // still pending
	}

	if ( FD_ISSET( (unsigned)m_nSocket, &errSet ) )
	{
		Disconnect( "connect refused" );
		return false;
	}

	// Writability alone is not proof on every stack; ask for the real result.
	int nError = 0;
	socklen_t nLen = sizeof( nError );
	if ( getsockopt( m_nSocket, SOL_SOCKET, SO_ERROR, (char *)&nError, &nLen ) != 0 || nError != 0 )
	{
		Disconnect( "connect failed" );
		return false;
	}

	m_flRetryDelay = 1.0f;
	SetState( GC_HANDSHAKE );
	SendHello();
	return true;
}

//-----------------------------------------------------------------------------
void CGreylineGC::PumpRecv()
{
	byte chunk[8192];

	for ( ;; )
	{
		int nRead = recv( m_nSocket, (char *)chunk, sizeof( chunk ), 0 );
		if ( nRead > 0 )
		{
			m_RecvBuffer.Put( chunk, nRead );
			continue;
		}

		if ( nRead == 0 )
		{
			Disconnect( "coordinator closed the link" );
			return;
		}

		if ( GL_LAST_ERROR == GL_WOULDBLOCK )
		{
			break; // drained
		}

		Disconnect( "read error" );
		return;
	}

	// Frames are "uint32 length, then that many bytes". Consume whole ones.
	for ( ;; )
	{
		int nAvailable = m_RecvBuffer.TellPut() - m_RecvBuffer.TellGet();
		if ( nAvailable < 4 )
		{
			break;
		}

		const byte *pBase = (const byte *)m_RecvBuffer.Base() + m_RecvBuffer.TellGet();
		uint32 nFrame = LittleDWord( *(const uint32 *)pBase );

		if ( nFrame > GREYLINE_MAX_FRAME )
		{
			Disconnect( "oversized frame" );
			return;
		}
		if ( (uint32)nAvailable < 4 + nFrame )
		{
			break; // wait for the rest
		}

		DispatchFrame( pBase + 4, (int)nFrame );
		m_RecvBuffer.SeekGet( CUtlBuffer::SEEK_CURRENT, 4 + nFrame );

		if ( m_nSocket == -1 )
		{
			return; // a handler hung up
		}
	}

	// Reclaim the consumed prefix once it is worth the memmove.
	if ( m_RecvBuffer.TellGet() > 32 * 1024 )
	{
		int nRemaining = m_RecvBuffer.TellPut() - m_RecvBuffer.TellGet();
		if ( nRemaining > 0 )
		{
			V_memmove( m_RecvBuffer.Base(),
				(const byte *)m_RecvBuffer.Base() + m_RecvBuffer.TellGet(), nRemaining );
		}
		m_RecvBuffer.SeekGet( CUtlBuffer::SEEK_HEAD, 0 );
		m_RecvBuffer.SeekPut( CUtlBuffer::SEEK_HEAD, nRemaining );
	}
}

//-----------------------------------------------------------------------------
void CGreylineGC::PumpSend()
{
	while ( m_SendBuffer.TellPut() > m_SendBuffer.TellGet() )
	{
		const byte *pBase = (const byte *)m_SendBuffer.Base() + m_SendBuffer.TellGet();
		int nPending = m_SendBuffer.TellPut() - m_SendBuffer.TellGet();

		int nSent = send( m_nSocket, (const char *)pBase, nPending, 0 );
		if ( nSent > 0 )
		{
			m_SendBuffer.SeekGet( CUtlBuffer::SEEK_CURRENT, nSent );
			continue;
		}

		if ( nSent < 0 && GL_LAST_ERROR == GL_WOULDBLOCK )
		{
			return; // try again next frame
		}

		Disconnect( "write error" );
		return;
	}

	m_SendBuffer.Clear();
}

//-----------------------------------------------------------------------------
void CGreylineGC::Send( const greyline::CMsgEnvelope &env )
{
	if ( m_nSocket == -1 )
	{
		return;
	}

	const int nSize = env.ByteSize();
	if ( nSize <= 0 || nSize > GREYLINE_MAX_FRAME )
	{
		Warning( "[greyline/gc] refusing to send a %d byte envelope\n", nSize );
		return;
	}

	CUtlMemory< byte > payload;
	payload.EnsureCapacity( nSize );
	if ( !env.SerializeToArray( payload.Base(), nSize ) )
	{
		Warning( "[greyline/gc] failed to serialize an envelope\n" );
		return;
	}

	const uint32 nHeader = LittleDWord( (uint32)nSize );
	m_SendBuffer.Put( &nHeader, sizeof( nHeader ) );
	m_SendBuffer.Put( payload.Base(), nSize );

	PumpSend();
}

//-----------------------------------------------------------------------------
void CGreylineGC::FillCapabilities( greyline::CMsgEnvelope &env )
{
	greyline::CMsgHostCapabilities *pCaps = env.mutable_hello()->mutable_capabilities();

	pCaps->set_can_host( greyline_can_host.GetBool() );
	pCaps->set_is_dedicated( false );
	pCaps->set_max_players_supported( 24 );

	// Upload is the one figure the machine cannot measure on its own without
	// actually pushing traffic, so it is either configured or estimated low.
	// Guessing high would win elections this machine cannot honour.
	const int nUpload = greyline_upload_kbps.GetInt();
	pCaps->set_upload_kbps( nUpload > 0 ? nUpload : 3000 );

	const CPUInformation &cpu = *GetCPUInformation();
	int nCores = cpu.m_nPhysicalProcessors > 0 ? cpu.m_nPhysicalProcessors : 1;
	// A rough 0-100 figure: cores against a modern eight-core baseline, scaled
	// by clock against 3 GHz. The coordinator only ever compares these.
	int nScore = (int)( 50.0 * ( nCores / 8.0 ) + 50.0 * ( cpu.m_Speed / 3.0e9 ) );
	pCaps->set_cpu_score( clamp( nScore, 1, 100 ) );

	pCaps->set_memory_mb( 4096 );
}

//-----------------------------------------------------------------------------
void CGreylineGC::SendHello()
{
	if ( !steamapicontext || !steamapicontext->SteamUser() )
	{
		Disconnect( "Steam is not available" );
		return;
	}

	CSteamID me = steamapicontext->SteamUser()->GetSteamID();

	// One ticket per process, reused across reconnects. The coordinator caches
	// validation results, so re-presenting the same ticket is cheap for it too.
	if ( m_cbTicket == 0 )
	{
		m_hAuthTicket = steamapicontext->SteamUser()->GetAuthSessionTicket(
			m_TicketBuffer, sizeof( m_TicketBuffer ), &m_cbTicket, NULL );

		if ( m_hAuthTicket == k_HAuthTicketInvalid )
		{
			// Not fatal: a coordinator in dev auth mode accepts the asserted
			// SteamID, and one in webapi mode will refuse us with a clear
			// reason rather than us guessing here.
			Warning( "[greyline/gc] could not get a Steam auth ticket; connecting without one\n" );
			m_cbTicket = 0;
		}
	}

	greyline::CMsgEnvelope env;
	env.set_job_id( m_ulJobID++ );

	greyline::CMsgHello *pHello = env.mutable_hello();
	pHello->set_protocol_version( GREYLINE_GC_PROTOCOL_VERSION );
	pHello->set_steam_id( me.ConvertToUint64() );
	pHello->set_client_version( engine->GetProductVersionString() );
	pHello->set_is_dedicated( false );
#ifdef _WIN32
	pHello->set_platform( "windows" );
#else
	pHello->set_platform( "linux" );
#endif
	if ( m_cbTicket > 0 )
	{
		pHello->set_auth_ticket( m_TicketBuffer, m_cbTicket );
	}

	FillCapabilities( env );
	Send( env );
}

//-----------------------------------------------------------------------------
void CGreylineGC::DispatchFrame( const byte *pData, int nLength )
{
	greyline::CMsgEnvelope env;
	if ( !env.ParseFromArray( pData, nLength ) )
	{
		Disconnect( "malformed envelope" );
		return;
	}

	if ( greyline_gc_debug.GetBool() )
	{
		Msg( "[greyline/gc] <- %d bytes\n", nLength );
	}

	if ( env.has_welcome() )			{ HandleWelcome( env ); return; }
	if ( env.has_queue_status() )	{ HandleQueueStatus( env ); return; }
	if ( env.has_assign_host() )	{ HandleAssignHost( env ); return; }
	if ( env.has_join_battle() )	{ HandleJoinBattle( env ); return; }
	if ( env.has_migrate_host() )	{ HandleMigrateHost( env ); return; }
	if ( env.has_match_over() )		{ HandleMatchOver( env ); return; }
	if ( env.has_security_verdict() ) { HandleSecurityVerdict( env ); return; }

	if ( env.has_ping() )
	{
		greyline::CMsgEnvelope reply;
		reply.mutable_pong()->set_nonce( env.ping().nonce() );
		Send( reply );
		return;
	}

	if ( env.has_goodbye() )
	{
		Disconnect( env.goodbye().reason().c_str() );
		return;
	}
}

//-----------------------------------------------------------------------------
void CGreylineGC::HandleWelcome( const greyline::CMsgEnvelope &env )
{
	const greyline::CMsgWelcome &welcome = env.welcome();

	if ( welcome.result() != greyline::RESULT_OK )
	{
		Warning( "[greyline/gc] refused: %s\n", welcome.message().c_str() );
		Disconnect( "refused by the coordinator" );

		// A refusal is a decision, not a hiccup. Retrying every second would
		// spam a coordinator that has already said no.
		m_flRetryDelay = 30.0f;
		m_flRetryAt = gpGlobals->realtime + m_flRetryDelay;
		return;
	}

	SetState( GC_READY );
	m_flHeartbeatInterval = welcome.has_heartbeat_interval_s() ? (float)welcome.heartbeat_interval_s() : 10.0f;
	m_flNextHeartbeat = gpGlobals->realtime + m_flHeartbeatInterval;

	Msg( "[greyline/gc] connected to %s\n", greyline_gc_address.GetString() );

	// The coordinator held our slot in a battle we dropped out of.
	if ( welcome.has_rejoin() )
	{
		Msg( "[greyline/gc] rejoining the battle we dropped out of\n" );
		greyline::CMsgEnvelope wrapper;
		wrapper.mutable_join_battle()->CopyFrom( welcome.rejoin() );
		HandleJoinBattle( wrapper );
	}
}

//-----------------------------------------------------------------------------
void CGreylineGC::HandleQueueStatus( const greyline::CMsgEnvelope &env )
{
	const greyline::CMsgQueueStatus &status = env.queue_status();
	V_strncpy( m_szQueueMessage, status.message().c_str(), sizeof( m_szQueueMessage ) );

	if ( greyline_gc_debug.GetBool() || !status.queued() )
	{
		Msg( "[greyline] %s\n", m_szQueueMessage );
	}
}

//-----------------------------------------------------------------------------
// Purpose: the coordinator elected us to host this battle.
//-----------------------------------------------------------------------------
void CGreylineGC::HandleAssignHost( const greyline::CMsgEnvelope &env )
{
	const greyline::CMsgAssignHost &assign = env.assign_host();

	V_strncpy( m_szMatchID, assign.match_id().c_str(), sizeof( m_szMatchID ) );
	m_bIsHost = true;
	m_bResultReported = false;

	GreylineBattleAssignment_t battle;
	battle.bBeHost = true;
	battle.ulLobbyID = assign.lobby_id();
	battle.nMaxPlayers = assign.has_max_players() ? assign.max_players() : 12;
	V_strncpy( battle.szMap, assign.map_name().c_str(), sizeof( battle.szMap ) );
	V_strncpy( battle.szBattle, assign.match_id().c_str(), sizeof( battle.szBattle ) );
	V_strncpy( battle.szFront, assign.front_id().c_str(), sizeof( battle.szFront ) );

	if ( assign.is_migration() && assign.has_restore() )
	{
		battle.bRestore = true;
		battle.nRestoreRed = assign.restore().red_score();
		battle.nRestoreBlu = assign.restore().blu_score();
		battle.nRestoreRounds = assign.restore().round();
	}

	Msg( "[greyline] you are hosting battle %s on %s%s\n",
		battle.szBattle, battle.szMap, assign.is_migration() ? " (taking over)" : "" );

	GreylineLobby()->Deploy( battle );
}

//-----------------------------------------------------------------------------
// Purpose: go join a battle somebody else is hosting.
//-----------------------------------------------------------------------------
void CGreylineGC::HandleJoinBattle( const greyline::CMsgEnvelope &env )
{
	const greyline::CMsgJoinBattle &join = env.join_battle();

	V_strncpy( m_szMatchID, join.match_id().c_str(), sizeof( m_szMatchID ) );
	m_bIsHost = false;
	m_bResultReported = false;

	GreylineBattleAssignment_t battle;
	battle.bBeHost = false;
	battle.ulLobbyID = join.lobby_id();
	V_strncpy( battle.szMap, join.map_name().c_str(), sizeof( battle.szMap ) );
	V_strncpy( battle.szBattle, join.match_id().c_str(), sizeof( battle.szBattle ) );
	V_strncpy( battle.szFront, join.front_id().c_str(), sizeof( battle.szFront ) );

	Msg( "[greyline] deploying to battle %s\n", battle.szBattle );

	// Joining by lobby id is exact; a search is the fallback for a coordinator
	// that knew the battle but not yet its room.
	if ( battle.ulLobbyID != 0 )
	{
		GreylineLobby()->JoinLobbyByID( CSteamID( battle.ulLobbyID ) );
	}
	else
	{
		GreylineLobby()->Deploy( battle );
	}
}

//-----------------------------------------------------------------------------
// Purpose: the host died and somebody else is taking over.
//-----------------------------------------------------------------------------
void CGreylineGC::HandleMigrateHost( const greyline::CMsgEnvelope &env )
{
	const greyline::CMsgMigrateHost &migrate = env.migrate_host();

	Msg( "[greyline] %s (holding up to %us)\n",
		migrate.message().c_str(), migrate.hold_seconds() );

	// Steam promotes an arbitrary member when a lobby's owner leaves, and only
	// the owner can publish the replacement server. If that happens to be us,
	// hand the room to the player the coordinator actually chose.
	GreylineLobby()->YieldLobbyOwnership( CSteamID( migrate.new_host_steam_id() ) );
}

//-----------------------------------------------------------------------------
void CGreylineGC::HandleMatchOver( const greyline::CMsgEnvelope &env )
{
	const greyline::CMsgMatchOver &over = env.match_over();

	Msg( "[greyline] %s\n", over.message().c_str() );
	if ( over.has_war_update() && !over.war_update().headline().empty() )
	{
		Msg( "[greyline] %s\n", over.war_update().headline().c_str() );
	}

	m_szMatchID[0] = '\0';
	m_bIsHost = false;
	m_bResultReported = false;
	GreylineLobby()->Abort( "battle over" );
}

//-----------------------------------------------------------------------------
void CGreylineGC::HandleSecurityVerdict( const greyline::CMsgEnvelope &env )
{
	const greyline::CMsgSecurityVerdict &verdict = env.security_verdict();

	Warning( "[greyline] %s: %s\n", verdict.action().c_str(), verdict.reason().c_str() );

	if ( verdict.action() == "kick" || verdict.action() == "abort" )
	{
		GreylineLobby()->Abort( verdict.reason().c_str() );
		engine->ClientCmd_Unrestricted( "disconnect\n" );
	}
}

//-----------------------------------------------------------------------------
// Called by the lobby module once its listen server is published.
//-----------------------------------------------------------------------------
void CGreylineGC::OnBattleHosted( uint64 ulLobbyID, uint64 ulGameServerID, const char *pszMap )
{
	if ( !IsReady() || !m_szMatchID[0] )
	{
		return;
	}

	greyline::CMsgEnvelope env;
	greyline::CMsgHostReady *pReady = env.mutable_host_ready();
	pReady->set_match_id( m_szMatchID );
	pReady->set_lobby_id( ulLobbyID );
	pReady->set_game_server_steam_id( ulGameServerID );
	pReady->set_map_name( pszMap ? pszMap : "" );
	Send( env );
}

//-----------------------------------------------------------------------------
void CGreylineGC::OnHostFailed( const char *pszReason )
{
	if ( !IsReady() || !m_szMatchID[0] )
	{
		return;
	}

	greyline::CMsgEnvelope env;
	greyline::CMsgHostFailed *pFailed = env.mutable_host_failed();
	pFailed->set_match_id( m_szMatchID );
	pFailed->set_reason( pszReason ? pszReason : "unspecified" );
	Send( env );
}

//-----------------------------------------------------------------------------
void CGreylineGC::OnJoinedBattle( bool bConnected, uint32 unRTTMs, const char *pszFailure )
{
	if ( !IsReady() || !m_szMatchID[0] )
	{
		return;
	}

	greyline::CMsgEnvelope env;
	greyline::CMsgClientJoinState *pState = env.mutable_client_join_state();
	pState->set_match_id( m_szMatchID );
	pState->set_connected( bConnected );
	if ( unRTTMs > 0 )
	{
		pState->set_rtt_ms( unRTTMs );
	}
	if ( pszFailure && pszFailure[0] )
	{
		pState->set_failure( pszFailure );
	}
	Send( env );
}

//-----------------------------------------------------------------------------
void CGreylineGC::RequestDeploy( int eSide, const char *pszFrontID )
{
	if ( !IsReady() )
	{
		Warning( "[greyline] not connected to the coordinator (%s)\n", GetStateName() );
		return;
	}

	greyline::CMsgEnvelope env;
	env.set_job_id( m_ulJobID++ );

	greyline::CMsgDeployRequest *pDeploy = env.mutable_deploy_request();
	pDeploy->set_preferred_side( (greyline::ESide)eSide );
	pDeploy->set_accept_any_side( eSide == greyline::SIDE_UNASSIGNED );
	if ( pszFrontID && pszFrontID[0] )
	{
		pDeploy->set_front_id( pszFrontID );
	}
	Send( env );
}

//-----------------------------------------------------------------------------
void CGreylineGC::CancelDeploy()
{
	if ( !IsReady() )
	{
		return;
	}
	greyline::CMsgEnvelope env;
	env.mutable_deploy_cancel();
	Send( env );
}

//-----------------------------------------------------------------------------
void CGreylineGC::RequestWorldState()
{
	if ( !IsReady() )
	{
		return;
	}
	greyline::CMsgEnvelope env;
	env.set_job_id( m_ulJobID++ );
	env.mutable_world_state_request();
	Send( env );
}

//-----------------------------------------------------------------------------
// Purpose: the heartbeat doubles as the snapshot channel. A host that stops
//			sending these is treated as gone, and without the score in them a
//			migration would resume the battle at 0-0.
//-----------------------------------------------------------------------------
void CGreylineGC::PumpHeartbeat()
{
	if ( gpGlobals->realtime < m_flNextHeartbeat )
	{
		return;
	}
	m_flNextHeartbeat = gpGlobals->realtime + m_flHeartbeatInterval;

	if ( !m_szMatchID[0] )
	{
		return;
	}

	greyline::CMsgEnvelope env;

	if ( m_bIsHost )
	{
		greyline::CMsgHostHeartbeat *pBeat = env.mutable_host_heartbeat();
		pBeat->set_match_id( m_szMatchID );
		pBeat->set_state( engine->IsInGame() ? greyline::MATCH_LIVE : greyline::MATCH_LOADING );

		int nRed = 0, nBlu = 0, nRounds = 0;
		GreylineLobby()->ReadScore( nRed, nBlu, nRounds );

		greyline::CMsgMatchSnapshot *pSnap = pBeat->mutable_snapshot();
		pSnap->set_red_score( nRed );
		pSnap->set_blu_score( nBlu );
		pSnap->set_round( nRounds );
		pSnap->set_map_name( engine->GetLevelName() ? engine->GetLevelName() : "" );
	}
	else
	{
		greyline::CMsgClientHeartbeat *pBeat = env.mutable_client_heartbeat();
		pBeat->set_match_id( m_szMatchID );
		pBeat->set_in_match( engine->IsInGame() );

		INetChannelInfo *pChannel = engine->GetNetChannelInfo();
		if ( pChannel )
		{
			pBeat->set_rtt_to_host_ms( (uint32)( pChannel->GetAvgLatency( FLOW_OUTGOING ) * 1000.0f ) );
		}
	}

	Send( env );
}

//-----------------------------------------------------------------------------
// Purpose: report the battle's outcome when it ends.
//
// The host's report is what the coordinator records; every other player sends
// the same thing as an attestation, and a scoreline nobody else saw does not
// advance the war.
//-----------------------------------------------------------------------------
void CGreylineGC::ReportMatchResult()
{
	if ( !IsReady() || !m_szMatchID[0] || m_bResultReported )
	{
		return;
	}
	m_bResultReported = true;

	int nRed = 0, nBlu = 0, nRounds = 0;
	GreylineLobby()->ReadScore( nRed, nBlu, nRounds );

	greyline::EMatchOutcome eOutcome = greyline::OUTCOME_STALEMATE;
	if ( nRed > nBlu )
	{
		eOutcome = greyline::OUTCOME_RED_WIN;
	}
	else if ( nBlu > nRed )
	{
		eOutcome = greyline::OUTCOME_BLU_WIN;
	}

	greyline::CMsgEnvelope env;

	if ( m_bIsHost )
	{
		greyline::CMsgMatchResult *pResult = env.mutable_match_result();
		pResult->set_match_id( m_szMatchID );
		pResult->set_outcome( eOutcome );
		pResult->set_red_score( nRed );
		pResult->set_blu_score( nBlu );
		pResult->set_duration_s( 0 );
	}
	else
	{
		greyline::CMsgResultAttestation *pAttest = env.mutable_result_attestation();
		pAttest->set_match_id( m_szMatchID );
		pAttest->set_outcome( eOutcome );
		pAttest->set_red_score( nRed );
		pAttest->set_blu_score( nBlu );
	}

	Msg( "[greyline] reporting RED %d - BLU %d\n", nRed, nBlu );
	Send( env );
}

//-----------------------------------------------------------------------------
void CGreylineGC::FireGameEvent( IGameEvent *pEvent )
{
	if ( !pEvent )
	{
		return;
	}

	const char *pszName = pEvent->GetName();
	if ( !V_strcmp( pszName, "teamplay_game_over" ) || !V_strcmp( pszName, "tf_game_over" ) )
	{
		ReportMatchResult();
	}
}

//-----------------------------------------------------------------------------
void CGreylineGC::Update( float frametime )
{
	if ( !greyline_gc_enable.GetBool() )
	{
		if ( m_nSocket != -1 )
		{
			CloseSocket();
		}
		return;
	}

	if ( m_nSocket == -1 )
	{
		if ( gpGlobals->realtime >= m_flRetryAt )
		{
			Connect();
		}
		return;
	}

	if ( m_eState == GC_CONNECTING )
	{
		if ( !PumpConnect() )
		{
			return;
		}
	}

	PumpRecv();
	if ( m_nSocket == -1 )
	{
		return;
	}

	PumpSend();
	if ( m_nSocket == -1 )
	{
		return;
	}

	if ( m_eState == GC_READY )
	{
		PumpHeartbeat();
	}
}

//-----------------------------------------------------------------------------
// Player-facing commands.
//-----------------------------------------------------------------------------
CON_COMMAND_F( greyline_deploy, "Deploy to the war: greyline_deploy [red|blu|any] [front_id]", FCVAR_CLIENTDLL )
{
	int eSide = greyline::SIDE_UNASSIGNED;
	if ( args.ArgC() > 1 )
	{
		if ( !V_stricmp( args[1], "red" ) )		 eSide = greyline::SIDE_RED;
		else if ( !V_stricmp( args[1], "blu" ) ) eSide = greyline::SIDE_BLU;
	}

	GreylineGC()->RequestDeploy( eSide, args.ArgC() > 2 ? args[2] : NULL );
}

CON_COMMAND_F( greyline_cancel, "Stop searching for a battle.", FCVAR_CLIENTDLL )
{
	GreylineGC()->CancelDeploy();
}

CON_COMMAND_F( greyline_gc_status, "Show the Greyline coordinator link state.", FCVAR_CLIENTDLL )
{
	CGreylineGC *pGC = GreylineGC();
	Msg( "greyline coordinator: %s (%s)\n", pGC->GetStateName(), greyline_gc_address.GetString() );
	Msg( "  battle : %s%s\n", pGC->GetMatchID()[0] ? pGC->GetMatchID() : "none",
		pGC->IsHost() ? " (hosting)" : "" );
	Msg( "  queue  : %s\n", pGC->GetQueueMessage() );
}

CON_COMMAND_F( greyline_gc_reconnect, "Reconnect to the Greyline coordinator.", FCVAR_CLIENTDLL )
{
	GreylineGC()->Disconnect( "reconnecting by request" );
	GreylineGC()->Connect();
}
