//========= GREYLINE FRONTRESS ============================================//
//
// Purpose: client half of Greyline battle hosting.
//
// The flow, end to end:
//
//   coordinator says "fight at Foundry, battle 381"
//        |
//        +-- not host --> RequestLobbyList(battle=381, build=..., slots>0)
//        |                     |
//        |                     +-- found --> JoinLobby
//        |                     |                 |
//        |                     |                 +-- server already set --> connect
//        |                     |                 +-- not yet --> wait for LobbyGameCreated_t
//        |                     +-- none --> tell the coordinator; it decides who hosts
//        |
//        +-- host ------> CreateLobby --> map <x> --> wait for the server's
//                              SteamID --> SetLobbyGameServer --> everyone connects
//
// There is no transport code here on purpose. The engine's Steam Networking
// support makes the listen server reachable; this file only decides who hosts
// and tells everyone where to go.
//
//=========================================================================//

#include "cbase.h"
#include "greyline_lobby.h"
#include "greyline_gc.h"
#include "greyline/greyline_shared.h"
#include "cdll_client_int.h"
#include "ienginevgui.h"

// memdbgon must be the last include file in a .cpp file!!!
#include "tier0/memdbgon.h"

ConVar greyline_lobby_debug( "greyline_lobby_debug", "0", FCVAR_CLIENTDLL,
	"Spew Greyline lobby state transitions." );

ConVar greyline_host_boot_timeout( "greyline_host_boot_timeout", "90", FCVAR_CLIENTDLL,
	"Seconds a host has to get its game server published before giving up." );

ConVar greyline_join_timeout( "greyline_join_timeout", "120", FCVAR_CLIENTDLL,
	"Seconds to wait in a lobby for its game server before giving up." );

static CGreylineLobby g_GreylineLobby;
CGreylineLobby *GreylineLobby() { return &g_GreylineLobby; }

//-----------------------------------------------------------------------------
static ISteamMatchmaking *GetMatchmaking()
{
	if ( !steamapicontext )
	{
		return NULL;
	}
	return steamapicontext->SteamMatchmaking();
}

//-----------------------------------------------------------------------------
CGreylineLobby::CGreylineLobby()
	: CAutoGameSystemPerFrame( "CGreylineLobby" )
	, m_callbackLobbyGameCreated( this, &CGreylineLobby::OnLobbyGameCreated )
	, m_callbackJoinRequested( this, &CGreylineLobby::OnGameLobbyJoinRequested )
{
	m_eState = GREYLINE_IDLE;
	m_flStateDeadline = 0.0f;
	m_bServerPublished = false;
	m_bRestoreIssued = false;
}

//-----------------------------------------------------------------------------
bool CGreylineLobby::Init()
{
	// Steam launches the game with "+connect_lobby <id>" when a player accepts
	// an invite from outside the game. The engine runs that as a console
	// command, so it lands in the concommand at the bottom of this file rather
	// than here.
	return true;
}

//-----------------------------------------------------------------------------
const char *CGreylineLobby::GetStateName() const
{
	switch ( m_eState )
	{
	case GREYLINE_IDLE:			return "idle";
	case GREYLINE_SEARCHING:	return "searching";
	case GREYLINE_JOINING:		return "joining";
	case GREYLINE_IN_LOBBY:		return "in lobby, waiting for the battle";
	case GREYLINE_CREATING:		return "creating lobby";
	case GREYLINE_HOST_LOADING:	return "hosting, loading map";
	case GREYLINE_HOST_LIVE:	return "hosting, battle live";
	case GREYLINE_CONNECTING:	return "connecting";
	case GREYLINE_IN_BATTLE:	return "in battle";
	}
	return "unknown";
}

//-----------------------------------------------------------------------------
void CGreylineLobby::SetState( GreylineLobbyState_t eState )
{
	if ( eState == m_eState )
	{
		return;
	}

	if ( greyline_lobby_debug.GetBool() )
	{
		const char *pszFrom = GetStateName();
		m_eState = eState;
		Msg( "[greyline] lobby state: %s -> %s\n", pszFrom, GetStateName() );
	}
	m_eState = eState;

	// Only the states we can be stuck in get a deadline.
	switch ( eState )
	{
	case GREYLINE_HOST_LOADING:
		m_flStateDeadline = gpGlobals->realtime + greyline_host_boot_timeout.GetFloat();
		break;
	case GREYLINE_IN_LOBBY:
		m_flStateDeadline = gpGlobals->realtime + greyline_join_timeout.GetFloat();
		break;
	case GREYLINE_SEARCHING:
	case GREYLINE_JOINING:
	case GREYLINE_CREATING:
		m_flStateDeadline = gpGlobals->realtime + 30.0f;
		break;
	default:
		m_flStateDeadline = 0.0f;
		break;
	}
}

//-----------------------------------------------------------------------------
void CGreylineLobby::Deploy( const GreylineBattleAssignment_t &assignment )
{
	if ( !GetMatchmaking() )
	{
		Warning( "[greyline] Steam matchmaking unavailable; cannot deploy\n" );
		return;
	}

	if ( m_eState != GREYLINE_IDLE )
	{
		// Keep the room when this deployment is a takeover of the lobby we are
		// already sitting in — leaving it would forfeit the membership, and
		// with it any chance of owning and publishing to it.
		const bool bSameLobby = assignment.ulLobbyID != 0 &&
			m_Lobby.ConvertToUint64() == assignment.ulLobbyID;
		Reset( "superseded by a new deployment", !bSameLobby );
	}

	m_Assignment = assignment;
	m_bServerPublished = false;
	m_bRestoreIssued = false;

	if ( assignment.bBeHost )
	{
		if ( assignment.ulLobbyID != 0 )
		{
			TakeOverBattle();
		}
		else
		{
			CreateBattleLobby();
		}
	}
	else
	{
		SearchForBattle();
	}
}

//-----------------------------------------------------------------------------
void CGreylineLobby::Abort( const char *pszReason )
{
	Reset( pszReason, true );
}

//-----------------------------------------------------------------------------
void CGreylineLobby::Reset( const char *pszReason, bool bLeaveLobby )
{
	if ( m_eState == GREYLINE_IDLE )
	{
		return;
	}

	Msg( "[greyline] leaving battle: %s\n", pszReason ? pszReason : "cancelled" );

	if ( bLeaveLobby && m_Lobby.IsValid() )
	{
		if ( ISteamMatchmaking *pMM = GetMatchmaking() )
		{
			pMM->LeaveLobby( m_Lobby );
		}
		m_Lobby.Clear();
	}

	m_callLobbyMatchList.Cancel();
	m_callLobbyCreated.Cancel();
	m_callLobbyEntered.Cancel();

	m_bServerPublished = false;
	SetState( GREYLINE_IDLE );
}

//-----------------------------------------------------------------------------
// Purpose: look for a lobby already running this battle.
//-----------------------------------------------------------------------------
void CGreylineLobby::SearchForBattle()
{
	ISteamMatchmaking *pMM = GetMatchmaking();
	if ( !pMM )
	{
		return;
	}

	pMM->AddRequestLobbyListStringFilter( GREYLINE_KEY_GAME, "1", k_ELobbyComparisonEqual );
	pMM->AddRequestLobbyListStringFilter( GREYLINE_KEY_PROTOCOL, GREYLINE_PROTOCOL_VERSION, k_ELobbyComparisonEqual );
	pMM->AddRequestLobbyListStringFilter( GREYLINE_KEY_BUILD, engine->GetProductVersionString(), k_ELobbyComparisonEqual );

	if ( m_Assignment.szBattle[0] )
	{
		// The coordinator named a specific battle, so join that one or nothing.
		pMM->AddRequestLobbyListStringFilter( GREYLINE_KEY_BATTLE, m_Assignment.szBattle, k_ELobbyComparisonEqual );
	}
	else if ( m_Assignment.szFront[0] )
	{
		// Front-wide search: any battle on this front with room.
		pMM->AddRequestLobbyListStringFilter( GREYLINE_KEY_FRONT, m_Assignment.szFront, k_ELobbyComparisonEqual );
	}

	pMM->AddRequestLobbyListFilterSlotsAvailable( 1 );

	// Closest relay first, so a front with several battles hands the player the
	// one they will actually have a good time on.
	pMM->AddRequestLobbyListDistanceFilter( k_ELobbyDistanceFilterDefault );

	SteamAPICall_t hCall = pMM->RequestLobbyList();
	m_callLobbyMatchList.Set( hCall, this, &CGreylineLobby::OnLobbyMatchList );
	SetState( GREYLINE_SEARCHING );
}

//-----------------------------------------------------------------------------
void CGreylineLobby::OnLobbyMatchList( LobbyMatchList_t *pResult, bool bIOFailure )
{
	if ( bIOFailure || !pResult )
	{
		Abort( "lobby search failed" );
		return;
	}

	ISteamMatchmaking *pMM = GetMatchmaking();
	if ( !pMM )
	{
		Abort( "Steam matchmaking went away mid-search" );
		return;
	}

	if ( greyline_lobby_debug.GetBool() )
	{
		Msg( "[greyline] lobby search returned %u result(s)\n", pResult->m_nLobbiesMatching );
	}

	if ( pResult->m_nLobbiesMatching == 0 )
	{
		// Nobody is fighting this battle yet. We do not self-promote to host:
		// the coordinator decides that, so it can weigh connection quality and
		// avoid two clients racing to create the same battle.
		Msg( "[greyline] no lobby for battle '%s'; the coordinator must elect a host\n",
			m_Assignment.szBattle[0] ? m_Assignment.szBattle : m_Assignment.szFront );
		SetState( GREYLINE_IDLE );
		return;
	}

	// The list already came back distance-sorted and slot-filtered.
	CSteamID lobby = pMM->GetLobbyByIndex( 0 );
	if ( !lobby.IsValid() )
	{
		Abort( "lobby search returned an invalid lobby" );
		return;
	}

	SteamAPICall_t hCall = pMM->JoinLobby( lobby );
	m_callLobbyEntered.Set( hCall, this, &CGreylineLobby::OnLobbyEntered );
	SetState( GREYLINE_JOINING );
}

//-----------------------------------------------------------------------------
void CGreylineLobby::OnLobbyEntered( LobbyEnter_t *pResult, bool bIOFailure )
{
	if ( bIOFailure || !pResult )
	{
		Abort( "failed to enter lobby" );
		return;
	}

	if ( pResult->m_EChatRoomEnterResponse != k_EChatRoomEnterResponseSuccess )
	{
		Warning( "[greyline] lobby refused entry (response %u)\n", pResult->m_EChatRoomEnterResponse );
		Abort( "lobby refused entry" );
		return;
	}

	m_Lobby = CSteamID( pResult->m_ulSteamIDLobby );
	SetState( GREYLINE_IN_LOBBY );

	ISteamMatchmaking *pMM = GetMatchmaking();
	if ( !pMM )
	{
		return;
	}

	// Fill in the assignment from the lobby for anything we did not already
	// know — this is the invite path, where there was no coordinator assignment.
	if ( !m_Assignment.szBattle[0] )
	{
		V_strncpy( m_Assignment.szBattle, pMM->GetLobbyData( m_Lobby, GREYLINE_KEY_BATTLE ), sizeof( m_Assignment.szBattle ) );
		V_strncpy( m_Assignment.szFront, pMM->GetLobbyData( m_Lobby, GREYLINE_KEY_FRONT ), sizeof( m_Assignment.szFront ) );
	}

	// A battle already in progress published its server before we arrived, so
	// LobbyGameCreated_t has already fired and will not fire again for us.
	// Asking directly is the only way a late joiner gets in.
	uint32 unIP = 0;
	uint16 usPort = 0;
	CSteamID steamIDServer;
	if ( pMM->GetLobbyGameServer( m_Lobby, &unIP, &usPort, &steamIDServer ) )
	{
		ConnectTo( unIP, usPort, steamIDServer );
		return;
	}

	Msg( "[greyline] joined battle lobby, waiting for the host to bring the server up\n" );
}

//-----------------------------------------------------------------------------
// Purpose: we were elected host. Put the lobby up first, so other players can
//			find the battle while the map is still loading.
//-----------------------------------------------------------------------------
void CGreylineLobby::CreateBattleLobby()
{
	ISteamMatchmaking *pMM = GetMatchmaking();
	if ( !pMM )
	{
		return;
	}

	if ( !m_Assignment.szMap[0] )
	{
		Warning( "[greyline] asked to host without a map\n" );
		return;
	}

	int nMaxPlayers = clamp( m_Assignment.nMaxPlayers, 2, 100 );

	// Public rather than friends-only: the coordinator matched these players,
	// and they are not each other's Steam friends.
	SteamAPICall_t hCall = pMM->CreateLobby( k_ELobbyTypePublic, nMaxPlayers );
	m_callLobbyCreated.Set( hCall, this, &CGreylineLobby::OnLobbyCreated );
	SetState( GREYLINE_CREATING );
}

//-----------------------------------------------------------------------------
void CGreylineLobby::OnLobbyCreated( LobbyCreated_t *pResult, bool bIOFailure )
{
	if ( bIOFailure || !pResult || pResult->m_eResult != k_EResultOK )
	{
		Warning( "[greyline] CreateLobby failed (result %d)\n", pResult ? pResult->m_eResult : -1 );
		Abort( "could not create the battle lobby" );
		return;
	}

	m_Lobby = CSteamID( pResult->m_ulSteamIDLobby );
	WriteLobbyMetadata();
	StartHosting();
}

//-----------------------------------------------------------------------------
// Purpose: take over a battle whose host died.
//
// The lobby outlives its host: Steam keeps it and promotes a surviving member
// to owner. So there is nothing to create and nobody to move — the other
// players stay put, and they get the replacement server through the same
// LobbyGameCreated_t path they used to join in the first place.
//-----------------------------------------------------------------------------
void CGreylineLobby::TakeOverBattle()
{
	ISteamMatchmaking *pMM = GetMatchmaking();
	if ( !pMM || !m_Assignment.szMap[0] )
	{
		Warning( "[greyline] asked to take over a battle without a map\n" );
		return;
	}

	m_Lobby = CSteamID( m_Assignment.ulLobbyID );

	if ( !OwnsLobby() )
	{
		// Steam handed the lobby to somebody else. Only they can transfer it,
		// so they will call YieldLobbyOwnership when the coordinator tells them
		// who the new host is. Carry on loading the map meanwhile; publishing
		// waits for ownership.
		Msg( "[greyline] taking over battle '%s'; waiting for lobby ownership\n", m_Assignment.szBattle );
	}

	pMM->SetLobbyData( m_Lobby, GREYLINE_KEY_STATE, GREYLINE_STATE_FORMING );
	StartHosting();
}

//-----------------------------------------------------------------------------
void CGreylineLobby::StartHosting()
{
	m_bServerPublished = false;
	m_bRestoreIssued = false;
	SetState( GREYLINE_HOST_LOADING );
	Msg( "[greyline] hosting battle '%s' on %s\n", m_Assignment.szBattle, m_Assignment.szMap );

	// The server half applies the hosting contract on level init and publishes
	// the game server's SteamID when Steam hands it over.
	engine->ClientCmd_Unrestricted( CFmtStr( "map %s\n", m_Assignment.szMap ) );
}

//-----------------------------------------------------------------------------
bool CGreylineLobby::OwnsLobby() const
{
	ISteamMatchmaking *pMM = GetMatchmaking();
	if ( !pMM || !m_Lobby.IsValid() || !steamapicontext || !steamapicontext->SteamUser() )
	{
		return false;
	}
	return pMM->GetLobbyOwner( m_Lobby ) == steamapicontext->SteamUser()->GetSteamID();
}

//-----------------------------------------------------------------------------
void CGreylineLobby::YieldLobbyOwnership( CSteamID steamIDNewHost )
{
	ISteamMatchmaking *pMM = GetMatchmaking();
	if ( !pMM || !m_Lobby.IsValid() || !steamIDNewHost.IsValid() )
	{
		return;
	}

	if ( !OwnsLobby() || steamIDNewHost == steamapicontext->SteamUser()->GetSteamID() )
	{
		return;
	}

	Msg( "[greyline] handing lobby to the new host %llu\n", steamIDNewHost.ConvertToUint64() );
	pMM->SetLobbyOwner( m_Lobby, steamIDNewHost );
}

//-----------------------------------------------------------------------------
void CGreylineLobby::WriteLobbyMetadata()
{
	ISteamMatchmaking *pMM = GetMatchmaking();
	if ( !pMM || !m_Lobby.IsValid() )
	{
		return;
	}

	pMM->SetLobbyData( m_Lobby, GREYLINE_KEY_GAME, "1" );
	pMM->SetLobbyData( m_Lobby, GREYLINE_KEY_PROTOCOL, GREYLINE_PROTOCOL_VERSION );
	pMM->SetLobbyData( m_Lobby, GREYLINE_KEY_BUILD, engine->GetProductVersionString() );
	pMM->SetLobbyData( m_Lobby, GREYLINE_KEY_FRONT, m_Assignment.szFront );
	pMM->SetLobbyData( m_Lobby, GREYLINE_KEY_BATTLE, m_Assignment.szBattle );
	pMM->SetLobbyData( m_Lobby, GREYLINE_KEY_MAP, m_Assignment.szMap );
	pMM->SetLobbyData( m_Lobby, GREYLINE_KEY_STATE, GREYLINE_STATE_FORMING );
}

//-----------------------------------------------------------------------------
// Purpose: hand the running listen server to the lobby.
//
// SetLobbyGameServer is the API Steam provides for exactly this, so the battle
// does not need a private convention in lobby key/values: members are notified
// with LobbyGameCreated_t, and late joiners can ask with GetLobbyGameServer.
//-----------------------------------------------------------------------------
void CGreylineLobby::PublishGameServer()
{
	ISteamMatchmaking *pMM = GetMatchmaking();
	if ( !pMM || !m_Lobby.IsValid() )
	{
		return;
	}

	// Only the lobby owner may publish. On a migration ownership may still be
	// in transit from whoever Steam promoted.
	if ( !OwnsLobby() )
	{
		return;
	}

	uint64 ulServerID = V_atoui64( greyline_server_id.GetString() );
	if ( ulServerID == 0 )
	{
		return; // not up yet
	}

	CSteamID steamIDServer( ulServerID );

	// Identify the server by SteamID and let Steam Networking route to it. The
	// address arguments are for servers reachable by plain IP; a player-hosted
	// listen server behind NAT is not, which is the entire point.
	pMM->SetLobbyGameServer( m_Lobby, 0, 0, steamIDServer );
	pMM->SetLobbyData( m_Lobby, GREYLINE_KEY_STATE, GREYLINE_STATE_PLAYING );

	m_bServerPublished = true;
	SetState( GREYLINE_HOST_LIVE );

	Msg( "[greyline] battle '%s' published, game server %llu\n",
		m_Assignment.szBattle, ulServerID );

	// The coordinator can now point the rest of the roster at this lobby.
	GreylineGC()->OnBattleHosted( m_Lobby.ConvertToUint64(), ulServerID, m_Assignment.szMap );
}

//-----------------------------------------------------------------------------
// Purpose: are we actually running the battle we were told to run?
//
// The replicated convars this module reads — the server identity and the score —
// still hold the *previous* host's values until our own map finishes loading.
// On a migration those describe the server that just died, so trusting them
// early would publish a corpse and report its score as ours.
//-----------------------------------------------------------------------------
bool CGreylineLobby::LevelReady() const
{
	if ( !engine->IsInGame() )
	{
		return false;
	}
	const char *pszLevel = engine->GetLevelName();
	return pszLevel && pszLevel[0] && V_stristr( pszLevel, m_Assignment.szMap ) != NULL;
}

//-----------------------------------------------------------------------------
// Purpose: put a migrated battle's score back before anyone can join it.
//
// This is a server command, so it only works because a listen server shares
// this process. Clients cannot run it: it carries no FCVAR_CLIENTCMD_CAN_EXECUTE.
//-----------------------------------------------------------------------------
void CGreylineLobby::IssueScoreRestore()
{
	engine->ClientCmd_Unrestricted( CFmtStr( "greyline_restore_score %d %d %d\n",
		m_Assignment.nRestoreRed, m_Assignment.nRestoreBlu, m_Assignment.nRestoreRounds ) );

	Msg( "[greyline] restoring battle score RED %d - BLU %d after %d round(s)\n",
		m_Assignment.nRestoreRed, m_Assignment.nRestoreBlu, m_Assignment.nRestoreRounds );
}

//-----------------------------------------------------------------------------
void CGreylineLobby::ReadScore( int &nRed, int &nBlu, int &nRounds ) const
{
	nRed = greyline_score_red.GetInt();
	nBlu = greyline_score_blu.GetInt();
	nRounds = greyline_rounds_played.GetInt();
}

//-----------------------------------------------------------------------------
void CGreylineLobby::ConnectTo( uint32 unIP, uint16 usPort, CSteamID steamIDServer )
{
	// Prefer a routable address when Steam gives us one; otherwise go by
	// SteamID, which is how a NAT'd listen server is reached.
	if ( unIP != 0 && usPort != 0 )
	{
		Msg( "[greyline] connecting to %u.%u.%u.%u:%u\n",
			( unIP >> 24 ) & 0xff, ( unIP >> 16 ) & 0xff, ( unIP >> 8 ) & 0xff, unIP & 0xff, usPort );
		engine->ClientCmd_Unrestricted( CFmtStr( "connect %u.%u.%u.%u:%u\n",
			( unIP >> 24 ) & 0xff, ( unIP >> 16 ) & 0xff, ( unIP >> 8 ) & 0xff, unIP & 0xff, usPort ) );
	}
	else if ( steamIDServer.IsValid() )
	{
		Msg( "[greyline] connecting to game server %llu\n", steamIDServer.ConvertToUint64() );
		engine->ClientCmd_Unrestricted( CFmtStr( "connect %llu\n", steamIDServer.ConvertToUint64() ) );
	}
	else
	{
		Abort( "lobby published a game server with no address and no SteamID" );
		return;
	}

	SetState( GREYLINE_CONNECTING );
}

//-----------------------------------------------------------------------------
// Purpose: the host published its server while we were sitting in the lobby.
//-----------------------------------------------------------------------------
void CGreylineLobby::OnLobbyGameCreated( LobbyGameCreated_t *pParam )
{
	if ( !pParam || !m_Lobby.IsValid() || pParam->m_ulSteamIDLobby != m_Lobby.ConvertToUint64() )
	{
		return;
	}

	// The host receives this for its own lobby and is already on the server.
	if ( m_eState == GREYLINE_HOST_LIVE || m_eState == GREYLINE_HOST_LOADING )
	{
		return;
	}

	ConnectTo( pParam->m_unIP, pParam->m_usPort, CSteamID( pParam->m_ulSteamIDGameServer ) );
}

//-----------------------------------------------------------------------------
// Purpose: a Steam invite accepted while the game is already running.
//-----------------------------------------------------------------------------
void CGreylineLobby::OnGameLobbyJoinRequested( GameLobbyJoinRequested_t *pParam )
{
	if ( !pParam )
	{
		return;
	}

	JoinLobbyByID( pParam->m_steamIDLobby );
}

//-----------------------------------------------------------------------------
void CGreylineLobby::JoinLobbyByID( CSteamID lobby )
{
	ISteamMatchmaking *pMM = GetMatchmaking();
	if ( !pMM || !lobby.IsValid() )
	{
		return;
	}

	Abort( "joining an invited battle" );

	// The assignment is unknown on this path; OnLobbyEntered fills it in from
	// the lobby's own metadata.
	m_Assignment = GreylineBattleAssignment_t();

	SteamAPICall_t hCall = pMM->JoinLobby( lobby );
	m_callLobbyEntered.Set( hCall, this, &CGreylineLobby::OnLobbyEntered );
	SetState( GREYLINE_JOINING );
}

//-----------------------------------------------------------------------------
void CGreylineLobby::Update( float frametime )
{
	if ( m_eState == GREYLINE_IDLE )
	{
		return;
	}

	// The server's Steam identity arrives asynchronously after the map loads,
	// so the host polls the replicated convar the server half writes.
	if ( m_eState == GREYLINE_HOST_LOADING && !m_bServerPublished && LevelReady() )
	{
		// Score first, server second. Publishing before the restore lands would
		// let players connect to a battle showing 0-0 and watch it jump.
		if ( m_Assignment.bRestore && !m_bRestoreIssued )
		{
			IssueScoreRestore();
			m_bRestoreIssued = true;
			return;
		}
		PublishGameServer();
	}

	// Connecting is finished by the engine, not by us: notice when we are
	// actually in the game and tell the coordinator, which is what moves the
	// battle from "loading" to "live".
	if ( m_eState == GREYLINE_CONNECTING && engine->IsInGame() )
	{
		uint32 unRTT = 0;
		if ( INetChannelInfo *pChannel = engine->GetNetChannelInfo() )
		{
			unRTT = (uint32)( pChannel->GetAvgLatency( FLOW_OUTGOING ) * 1000.0f );
		}
		GreylineGC()->OnJoinedBattle( true, unRTT, NULL );
		SetState( GREYLINE_IN_BATTLE );
	}

	if ( m_eState == GREYLINE_HOST_LIVE && engine->IsInGame() )
	{
		SetState( GREYLINE_IN_BATTLE );
	}

	if ( m_flStateDeadline > 0.0f && gpGlobals->realtime > m_flStateDeadline )
	{
		const GreylineLobbyState_t eWas = m_eState;
		CFmtStr reason( "timed out while %s", GetStateName() );

		// Tell the coordinator which half failed: a host that never came up is
		// re-elected, a client that never arrived is dropped.
		if ( eWas == GREYLINE_HOST_LOADING || eWas == GREYLINE_CREATING )
		{
			GreylineGC()->OnHostFailed( reason );
		}
		else if ( eWas == GREYLINE_CONNECTING || eWas == GREYLINE_IN_LOBBY )
		{
			GreylineGC()->OnJoinedBattle( false, 0, reason );
		}

		Abort( reason );
	}
}

//-----------------------------------------------------------------------------
// Test and diagnostic commands. These stand in for the coordinator link until
// it exists, and are what the first end-to-end run uses.
//-----------------------------------------------------------------------------
CON_COMMAND_F( greyline_host_battle, "Host a Greyline battle: greyline_host_battle <map> [battle_id] [front_id] [max_players]", FCVAR_CLIENTDLL )
{
	if ( args.ArgC() < 2 )
	{
		Msg( "usage: greyline_host_battle <map> [battle_id] [front_id] [max_players]\n" );
		return;
	}

	GreylineBattleAssignment_t assignment;
	assignment.bBeHost = true;
	V_strncpy( assignment.szMap, args[1], sizeof( assignment.szMap ) );
	V_strncpy( assignment.szBattle, args.ArgC() > 2 ? args[2] : "test", sizeof( assignment.szBattle ) );
	V_strncpy( assignment.szFront, args.ArgC() > 3 ? args[3] : "test_front", sizeof( assignment.szFront ) );
	assignment.nMaxPlayers = args.ArgC() > 4 ? V_atoi( args[4] ) : 12;

	GreylineLobby()->Deploy( assignment );
}

CON_COMMAND_F( greyline_join_battle, "Join a Greyline battle: greyline_join_battle [battle_id] [front_id]", FCVAR_CLIENTDLL )
{
	GreylineBattleAssignment_t assignment;
	assignment.bBeHost = false;
	V_strncpy( assignment.szBattle, args.ArgC() > 1 ? args[1] : "test", sizeof( assignment.szBattle ) );
	V_strncpy( assignment.szFront, args.ArgC() > 2 ? args[2] : "test_front", sizeof( assignment.szFront ) );

	GreylineLobby()->Deploy( assignment );
}

CON_COMMAND_F( greyline_takeover_battle, "Take over a battle whose host died: greyline_takeover_battle <lobby_id> <map> [red] [blu] [rounds]", FCVAR_CLIENTDLL )
{
	if ( args.ArgC() < 3 )
	{
		Msg( "usage: greyline_takeover_battle <lobby_id> <map> [red] [blu] [rounds]\n" );
		return;
	}

	GreylineBattleAssignment_t assignment;
	assignment.bBeHost = true;
	assignment.ulLobbyID = V_atoui64( args[1] );
	V_strncpy( assignment.szMap, args[2], sizeof( assignment.szMap ) );
	V_strncpy( assignment.szBattle, "test", sizeof( assignment.szBattle ) );

	if ( args.ArgC() >= 5 )
	{
		assignment.bRestore = true;
		assignment.nRestoreRed = V_atoi( args[3] );
		assignment.nRestoreBlu = V_atoi( args[4] );
		assignment.nRestoreRounds = args.ArgC() > 5 ? V_atoi( args[5] ) : 0;
	}

	GreylineLobby()->Deploy( assignment );
}

CON_COMMAND_F( greyline_yield_lobby, "Hand the current battle lobby to another player: greyline_yield_lobby <steamid>", FCVAR_CLIENTDLL )
{
	if ( args.ArgC() < 2 )
	{
		Msg( "usage: greyline_yield_lobby <steamid>\n" );
		return;
	}
	GreylineLobby()->YieldLobbyOwnership( CSteamID( V_atoui64( args[1] ) ) );
}

CON_COMMAND_F( greyline_leave_battle, "Leave the current Greyline battle lobby.", FCVAR_CLIENTDLL )
{
	GreylineLobby()->Abort( "left by request" );
}

CON_COMMAND_F( greyline_status, "Show Greyline lobby state.", FCVAR_CLIENTDLL )
{
	CGreylineLobby *pLobby = GreylineLobby();
	const GreylineBattleAssignment_t &a = pLobby->GetAssignment();

	Msg( "greyline: %s\n", pLobby->GetStateName() );
	Msg( "  lobby        : %llu\n", pLobby->GetLobby().ConvertToUint64() );
	Msg( "  front/battle : %s / %s\n", a.szFront, a.szBattle );
	Msg( "  map          : %s\n", a.szMap );
	Msg( "  server id    : %s\n", greyline_server_id.GetString() );

	int nRed = 0, nBlu = 0, nRounds = 0;
	pLobby->ReadScore( nRed, nBlu, nRounds );
	Msg( "  score        : RED %d - BLU %d, %d round(s)\n", nRed, nBlu, nRounds );
}

// Steam runs this when a player accepts an invite with the game closed.
CON_COMMAND_F( connect_lobby, "Join a Greyline battle lobby by id.", FCVAR_CLIENTDLL | FCVAR_HIDDEN )
{
	if ( args.ArgC() < 2 )
	{
		return;
	}

	uint64 ulLobby = V_atoui64( args[1] );
	if ( ulLobby == 0 )
	{
		return;
	}

	GreylineLobby()->JoinLobbyByID( CSteamID( ulLobby ) );
}
