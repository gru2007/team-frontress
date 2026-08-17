//========= GREYLINE FRONTRESS ============================================//
//
// Purpose: client half of a Greyline battle.
//
// The whole flow, end to end:
//
//   coordinator: "you are hosting battle 381 on cp_process"
//        |
//        +-- map cp_process
//        +-- wait for the level and for the server to say where it is
//        +-- tell the coordinator: ready, here is the address
//
//   coordinator: "battle 381 is at 169.254.4.12:27015"
//        |
//        +-- connect 169.254.4.12:27015
//
// That is the entire protocol. No lobby, no search, no room to be a member of.
// The address is whatever the host's own engine advertises — under Steam
// Networking a FakeIP, which is a handle for a Steam P2P/SDR route rather than
// a real internet address, so it works from behind NAT and cannot be used to
// find anybody's real IP.
//
//=========================================================================//

#include "cbase.h"
#include "greyline_battle.h"
#include "greyline_gc.h"
#include "greyline/greyline_shared.h"
#include "cdll_client_int.h"
#include "inetchannelinfo.h"
#include "tier1/fmtstr.h"

// memdbgon must be the last include file in a .cpp file!!!
#include "tier0/memdbgon.h"

ConVar greyline_battle_debug( "greyline_battle_debug", "0", FCVAR_CLIENTDLL,
	"Spew Greyline battle state transitions." );

ConVar greyline_host_boot_timeout( "greyline_host_boot_timeout", "90", FCVAR_CLIENTDLL,
	"Seconds a host has to get its server up before giving up, when the coordinator did not say." );

ConVar greyline_join_timeout( "greyline_join_timeout", "120", FCVAR_CLIENTDLL,
	"Seconds to spend reaching a battle before giving up, when the coordinator did not say." );

static CGreylineBattle g_GreylineBattle;
CGreylineBattle *GreylineBattle() { return &g_GreylineBattle; }

//-----------------------------------------------------------------------------
CGreylineBattle::CGreylineBattle()
	: CAutoGameSystemPerFrame( "CGreylineBattle" )
{
	m_eState = GREYLINE_IDLE;
	m_flStateDeadline = 0.0f;
	m_bRestoreIssued = false;
}

//-----------------------------------------------------------------------------
const char *CGreylineBattle::GetStateName() const
{
	switch ( m_eState )
	{
	case GREYLINE_IDLE:			return "idle";
	case GREYLINE_HOST_LOADING:	return "hosting, loading map";
	case GREYLINE_HOST_LIVE:	return "hosting, battle live";
	case GREYLINE_CONNECTING:	return "connecting";
	case GREYLINE_IN_BATTLE:	return "in battle";
	case GREYLINE_HOLDING:		return "holding for a new host";
	}
	return "unknown";
}

//-----------------------------------------------------------------------------
// Purpose: enter a state, with the deadline it may sit there for. A zero
//			timeout means the state cannot get stuck and needs no watchdog.
//-----------------------------------------------------------------------------
void CGreylineBattle::SetState( GreylineBattleState_t eState, float flTimeout )
{
	if ( greyline_battle_debug.GetBool() && eState != m_eState )
	{
		const char *pszFrom = GetStateName();
		m_eState = eState;
		Msg( "[greyline] battle state: %s -> %s\n", pszFrom, GetStateName() );
	}
	m_eState = eState;
	m_flStateDeadline = flTimeout > 0.0f ? gpGlobals->realtime + flTimeout : 0.0f;
}

//-----------------------------------------------------------------------------
// Purpose: how long this assignment gets. The coordinator's own deadline wins,
//			so both sides give up at the same moment; the convar is only there
//			for the console commands at the bottom of this file.
//-----------------------------------------------------------------------------
static float BattleDeadline( const GreylineBattleAssignment_t &assignment, ConVar &fallback )
{
	return assignment.flDeadlineSeconds > 0.0f ? assignment.flDeadlineSeconds : fallback.GetFloat();
}

//-----------------------------------------------------------------------------
void CGreylineBattle::Deploy( const GreylineBattleAssignment_t &assignment )
{
	m_Assignment = assignment;
	m_bRestoreIssued = false;

	if ( assignment.bBeHost )
	{
		StartHosting();
	}
	else
	{
		ConnectToBattle();
	}
}

//-----------------------------------------------------------------------------
void CGreylineBattle::Hold( float flSeconds, const char *pszMessage )
{
	if ( m_eState == GREYLINE_IDLE )
	{
		return;
	}

	Msg( "[greyline] %s\n", pszMessage && pszMessage[0] ? pszMessage : "holding for a new host" );
	SetState( GREYLINE_HOLDING, flSeconds > 0.0f ? flSeconds : greyline_join_timeout.GetFloat() );
}

//-----------------------------------------------------------------------------
void CGreylineBattle::Abort( const char *pszReason )
{
	if ( m_eState == GREYLINE_IDLE )
	{
		return;
	}

	Msg( "[greyline] leaving battle: %s\n", pszReason ? pszReason : "cancelled" );
	m_bRestoreIssued = false;
	SetState( GREYLINE_IDLE, 0.0f );
}

//-----------------------------------------------------------------------------
void CGreylineBattle::StartHosting()
{
	if ( !m_Assignment.szMap[0] )
	{
		Warning( "[greyline] asked to host without a map\n" );
		GreylineGC()->OnHostFailed( "assigned to host with no map" );
		return;
	}

	SetState( GREYLINE_HOST_LOADING, BattleDeadline( m_Assignment, greyline_host_boot_timeout ) );
	Msg( "[greyline] hosting battle '%s' on %s\n", m_Assignment.szBattle, m_Assignment.szMap );

	// The server half applies the hosting contract on level init and publishes
	// the address and identity once the engine has them.
	engine->ClientCmd_Unrestricted( CFmtStr( "map %s\n", m_Assignment.szMap ) );
}

//-----------------------------------------------------------------------------
// Purpose: are we actually running the battle we were told to run?
//
// The replicated convars this module reads — the server's address and identity,
// and the score — still hold the *previous* level's values until our own map
// finishes loading. On a migration those describe the server that just died, so
// trusting them early would report a corpse and its score as ours.
//-----------------------------------------------------------------------------
bool CGreylineBattle::LevelReady() const
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
// this process. Connected clients cannot run it: it carries no
// FCVAR_CLIENTCMD_CAN_EXECUTE.
//-----------------------------------------------------------------------------
void CGreylineBattle::IssueScoreRestore()
{
	engine->ClientCmd_Unrestricted( CFmtStr( "greyline_restore_score %d %d %d\n",
		m_Assignment.nRestoreRed, m_Assignment.nRestoreBlu, m_Assignment.nRestoreRounds ) );

	Msg( "[greyline] restoring battle score RED %d - BLU %d after %d round(s)\n",
		m_Assignment.nRestoreRed, m_Assignment.nRestoreBlu, m_Assignment.nRestoreRounds );
}

//-----------------------------------------------------------------------------
// Purpose: tell the coordinator where this battle now lives.
//
// Both halves come from the server through replicated convars, because the
// engine only exposes them server-side while the coordinator link lives on the
// client. The address is what guests actually use; the SteamID goes along for
// diagnostics and as the fallback for a build that advertises no address.
//-----------------------------------------------------------------------------
void CGreylineBattle::PublishHost()
{
	const char *pszAddress = greyline_server_addr.GetString();
	const uint64 ulServerID = V_atoui64( greyline_server_id.GetString() );

	if ( ( !pszAddress || !pszAddress[0] ) && ulServerID == 0 )
	{
		return; // the engine has not told the server where it is yet
	}

	SetState( GREYLINE_HOST_LIVE, 0.0f );

	Msg( "[greyline] battle '%s' is up at %s (game server %llu)\n",
		m_Assignment.szBattle,
		pszAddress && pszAddress[0] ? pszAddress : "no advertised address",
		ulServerID );

	GreylineGC()->OnBattleHosted( pszAddress, ulServerID, m_Assignment.szMap );
}

//-----------------------------------------------------------------------------
// Purpose: go to the battle. One command, and the engine does the rest.
//-----------------------------------------------------------------------------
void CGreylineBattle::ConnectToBattle()
{
	if ( m_Assignment.szConnectAddress[0] )
	{
		Msg( "[greyline] connecting to battle '%s' at %s\n",
			m_Assignment.szBattle, m_Assignment.szConnectAddress );
		engine->ClientCmd_Unrestricted( CFmtStr( "connect %s\n", m_Assignment.szConnectAddress ) );
	}
	else if ( m_Assignment.ulGameServerSteamID != 0 )
	{
		// No advertised address. Connecting by the server's Steam identity is the
		// only thing left to try, and whether this engine build accepts it has
		// not been proven — if it does not, the coordinator sees the join fail
		// and the host is the one to fix, not this path.
		Msg( "[greyline] no address for battle '%s'; trying game server %llu\n",
			m_Assignment.szBattle, m_Assignment.ulGameServerSteamID );
		engine->ClientCmd_Unrestricted( CFmtStr( "connect %llu\n", m_Assignment.ulGameServerSteamID ) );
	}
	else
	{
		GreylineGC()->OnJoinedBattle( false, 0, "battle assignment carried no address" );
		Abort( "battle assignment carried no address" );
		return;
	}

	SetState( GREYLINE_CONNECTING, BattleDeadline( m_Assignment, greyline_join_timeout ) );
}

//-----------------------------------------------------------------------------
void CGreylineBattle::ReadScore( int &nRed, int &nBlu, int &nRounds ) const
{
	nRed = greyline_score_red.GetInt();
	nBlu = greyline_score_blu.GetInt();
	nRounds = greyline_rounds_played.GetInt();
}

//-----------------------------------------------------------------------------
void CGreylineBattle::Update( float frametime )
{
	if ( m_eState == GREYLINE_IDLE )
	{
		return;
	}

	// The server's address and identity arrive asynchronously after the map
	// loads, so the host polls the replicated convars the server half writes.
	if ( m_eState == GREYLINE_HOST_LOADING && LevelReady() )
	{
		// Score first, server second. Reporting ready before the restore lands
		// would let players connect to a battle showing 0-0 and watch it jump.
		if ( m_Assignment.bRestore && !m_bRestoreIssued )
		{
			IssueScoreRestore();
			m_bRestoreIssued = true;
			return;
		}
		PublishHost();
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
		SetState( GREYLINE_IN_BATTLE, 0.0f );
	}

	if ( m_eState == GREYLINE_HOST_LIVE && engine->IsInGame() )
	{
		SetState( GREYLINE_IN_BATTLE, 0.0f );
	}

	if ( m_flStateDeadline > 0.0f && gpGlobals->realtime > m_flStateDeadline )
	{
		const GreylineBattleState_t eWas = m_eState;
		CFmtStr reason( "timed out while %s", GetStateName() );

		// Tell the coordinator which half failed: a host that never came up is
		// re-elected, a client that never arrived is dropped.
		if ( eWas == GREYLINE_HOST_LOADING )
		{
			GreylineGC()->OnHostFailed( reason );
		}
		else if ( eWas == GREYLINE_CONNECTING || eWas == GREYLINE_HOLDING )
		{
			GreylineGC()->OnJoinedBattle( false, 0, reason );
		}

		Abort( reason );
	}
}

//-----------------------------------------------------------------------------
// Test and diagnostic commands. These stand in for the coordinator link, and
// are what the first end-to-end run on a new build uses.
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

	GreylineBattle()->Deploy( assignment );
}

CON_COMMAND_F( greyline_join_battle, "Join a Greyline battle by address: greyline_join_battle <address> [battle_id]", FCVAR_CLIENTDLL )
{
	if ( args.ArgC() < 2 )
	{
		Msg( "usage: greyline_join_battle <address> [battle_id]\n" );
		return;
	}

	GreylineBattleAssignment_t assignment;
	assignment.bBeHost = false;
	V_strncpy( assignment.szConnectAddress, args[1], sizeof( assignment.szConnectAddress ) );
	V_strncpy( assignment.szBattle, args.ArgC() > 2 ? args[2] : "test", sizeof( assignment.szBattle ) );

	GreylineBattle()->Deploy( assignment );
}

CON_COMMAND_F( greyline_takeover_battle, "Take over a battle whose host died: greyline_takeover_battle <map> [red] [blu] [rounds]", FCVAR_CLIENTDLL )
{
	if ( args.ArgC() < 2 )
	{
		Msg( "usage: greyline_takeover_battle <map> [red] [blu] [rounds]\n" );
		return;
	}

	GreylineBattleAssignment_t assignment;
	assignment.bBeHost = true;
	V_strncpy( assignment.szMap, args[1], sizeof( assignment.szMap ) );
	V_strncpy( assignment.szBattle, "test", sizeof( assignment.szBattle ) );

	if ( args.ArgC() >= 4 )
	{
		assignment.bRestore = true;
		assignment.nRestoreRed = V_atoi( args[2] );
		assignment.nRestoreBlu = V_atoi( args[3] );
		assignment.nRestoreRounds = args.ArgC() > 4 ? V_atoi( args[4] ) : 0;
	}

	GreylineBattle()->Deploy( assignment );
}

CON_COMMAND_F( greyline_leave_battle, "Leave the current Greyline battle.", FCVAR_CLIENTDLL )
{
	GreylineBattle()->Abort( "left by request" );
}

CON_COMMAND_F( greyline_status, "Show Greyline battle state.", FCVAR_CLIENTDLL )
{
	CGreylineBattle *pBattle = GreylineBattle();
	const GreylineBattleAssignment_t &a = pBattle->GetAssignment();

	Msg( "greyline: %s\n", pBattle->GetStateName() );
	Msg( "  front/battle : %s / %s\n", a.szFront, a.szBattle );
	Msg( "  map          : %s\n", a.szMap );
	Msg( "  address      : %s\n", a.szConnectAddress[0] ? a.szConnectAddress : greyline_server_addr.GetString() );
	Msg( "  server id    : %s\n", greyline_server_id.GetString() );

	int nRed = 0, nBlu = 0, nRounds = 0;
	pBattle->ReadScore( nRed, nBlu, nRounds );
	Msg( "  score        : RED %d - BLU %d, %d round(s)\n", nRed, nBlu, nRounds );
}
