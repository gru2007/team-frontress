//========= GREYLINE FRONTRESS ============================================//
//
// Purpose: publishes a player-hosted (P2P) listen server's own address and
// live score into replicated convars, so the client half of this same
// process — the menu page, through the web RPC methods in
// src/game/client/greyline/greyline_menu_rpc.cpp — can read them and drive
// the coordinator's server-pool protocol over HTTP.
//
// This is a trimmed port of what the retired P2P prototype's
// src/game/greyline_legacy/greyline_host.cpp did. It keeps exactly the two
// parts that are still needed — publishing the address once the engine's
// Steam FakeIP allocation lands, and publishing the round score so a result
// can be reported — and drops migration/snapshot-restore, which the current
// design does not have: a P2P host disappearing simply aborts its battle and
// re-queues the roster (see internal/mm's abort()), rather than being resumed
// on a different machine.
//
// Nothing here runs on a dedicated server: it is configured by its own cfg
// files and already knows where it is.
//
//=========================================================================//

#include "cbase.h"
#include "igamesystem.h"
#include "eiface.h"
#include "iserver.h"
#include "team.h"
#include "player.h"
#include "tf_shareddefs.h"
#include "tf_gamerules.h"

// memdbgon must be the last include file in a .cpp file!!!
#include "tier0/memdbgon.h"

ConVar greyline_hoststate_debug( "greyline_hoststate_debug", "0", FCVAR_NONE,
	"Spew Greyline P2P host address/score publishing." );

//-----------------------------------------------------------------------------
// Replicated channel from a player-hosted listen server to its own client.
// The websocket bridge that talks to the coordinator lives client-side, but
// the address and the score only exist server-side — see the module comment.
//
// greyline_server_addr is what guests connect to. Under Steam Networking it
// is a FakeIP: an address-shaped handle for a Steam P2P/SDR route, not a real
// internet address, so publishing it exposes nobody's IP.
//-----------------------------------------------------------------------------
ConVar greyline_server_addr( "greyline_server_addr", "",
	FCVAR_REPLICATED | FCVAR_HIDDEN,
	"Address this P2P battle's listen server advertises, as the engine reports it." );
ConVar greyline_server_id( "greyline_server_id", "0",
	FCVAR_REPLICATED | FCVAR_HIDDEN,
	"SteamID of the listen server hosting this P2P battle. Fallback for a build that advertises no address." );
ConVar greyline_score_red( "greyline_score_red", "0",
	FCVAR_REPLICATED | FCVAR_HIDDEN,
	"RED's round score in the running P2P battle." );
ConVar greyline_score_blu( "greyline_score_blu", "0",
	FCVAR_REPLICATED | FCVAR_HIDDEN,
	"BLU's round score in the running P2P battle." );
ConVar greyline_rounds_played( "greyline_rounds_played", "0",
	FCVAR_REPLICATED | FCVAR_HIDDEN,
	"Rounds completed in the running P2P battle." );
ConVar greyline_policy_armed( "greyline_policy_armed", "0", FCVAR_GAMEDLL,
	"Enable Greyline's protected battle convar watchdog after assignment setup completes." );
ConVar greyline_policy_settle( "greyline_policy_settle", "20", FCVAR_GAMEDLL,
	"Seconds after a level starts during which a protected setting is put back without "
	"tainting the battle.\n"
	"The game resets these itself: a map's own cfg and the mode config the engine runs "
	"on spawn (\"Executing server arena config file\") both write mp_teams_unbalance_limit "
	"and friends after the assignment set them. Tainting for that voids a battle before "
	"a shot is fired, which is a far worse failure than the one the watchdog exists to "
	"catch — and nobody is playing yet, so there is nothing to gain in the window." );
ConVar greyline_policy_tainted( "greyline_policy_tainted", "0",
	FCVAR_REPLICATED | FCVAR_HIDDEN,
	"Set permanently for this battle when a protected server setting changes." );
ConVar greyline_policy_violation( "greyline_policy_violation", "",
	FCVAR_REPLICATED | FCVAR_HIDDEN,
	"First protected server setting changed during this battle." );

//-----------------------------------------------------------------------------
// Purpose: is this mode's state actually the team scoreline?
//
// CTeamplayRoundBasedRules::SetWinningTeam adds to the team score once per
// round when ShouldScorePerRound() is set, so for arena, KOTH, CP, payload and
// attack/defend the team score really is the match. Mann vs. Machine and
// tournament/stopwatch keep their state elsewhere; the coordinator's own
// theater does not offer those modes to a P2P front (see
// docs/GREYLINE-WAR.md), but this guard is cheap insurance against publishing
// a scoreline that would mean the wrong thing.
//-----------------------------------------------------------------------------
static bool BattleStateIsTeamScore()
{
	if ( !TFGameRules() )
	{
		return false;
	}
	if ( TFGameRules()->IsMannVsMachineMode() )
	{
		return false;
	}
	if ( TFGameRules()->IsInTournamentMode() )
	{
		return false;
	}
	return true;
}

//-----------------------------------------------------------------------------
class CGreylineHostState : public CAutoGameSystemPerFrame
{
public:
	CGreylineHostState() : CAutoGameSystemPerFrame( "CGreylineHostState" )
	{
		m_bIdentityPublished = false;
		m_flNextIdentityCheck = 0.0f;
		m_flNextScorePublish = 0.0f;
		m_flNextPolicyCheck = 0.0f;
		m_flNextBotKick = 0.0f;
		m_flPolicySettleUntil = 0.0f;
		m_bPolicyWasArmed = false;
		m_bPolicyTainted = false;
		m_szPolicyViolation[0] = '\0';
		m_szArmedBattleID[0] = '\0';
	}

	virtual void LevelInitPostEntity() OVERRIDE
	{
		m_bIdentityPublished = false;
		m_flNextIdentityCheck = 0.0f;
		m_flNextScorePublish = 0.0f;
		m_flNextPolicyCheck = 0.0f;
		m_flNextBotKick = 0.0f;
		m_flPolicySettleUntil = gpGlobals->curtime + MAX( 0.0f, greyline_policy_settle.GetFloat() );
		ConVarRef battleID( "greyline_battle_id", true );
		const bool bAssignedTransition = battleID.IsValid() && battleID.GetString()[0]
			&& greyline_policy_armed.GetBool();
		m_bPolicyWasArmed = bAssignedTransition;
		if ( !bAssignedTransition )
		{
			m_bPolicyTainted = false;
			m_szPolicyViolation[0] = '\0';
			m_szArmedBattleID[0] = '\0';
			greyline_policy_armed.SetValue( 0 );
			greyline_policy_tainted.SetValue( 0 );
			greyline_policy_violation.SetValue( "" );
		}
		greyline_server_id.SetValue( "0" );
		greyline_server_addr.SetValue( "" );
		greyline_score_red.SetValue( 0 );
		greyline_score_blu.SetValue( 0 );
		greyline_rounds_played.SetValue( 0 );
	}

	virtual void LevelShutdownPostEntity() OVERRIDE
	{
		m_bIdentityPublished = false;
		greyline_server_id.SetValue( "0" );
		greyline_server_addr.SetValue( "" );
	}

	virtual void FrameUpdatePostEntityThink() OVERRIDE
	{
		if ( gpGlobals->curtime >= m_flNextPolicyCheck )
		{
			m_flNextPolicyCheck = gpGlobals->curtime + 1.0f;
			EnforceBattlePolicy();
		}

		// Only a listen server shares this process with the client the
		// coordinator link runs on. A dedicated server is joined to the pool
		// by greyline-agent instead — see cmd/greyline-agent.
		if ( engine->IsDedicatedServer() )
		{
			return;
		}

		if ( gpGlobals->curtime >= m_flNextScorePublish )
		{
			m_flNextScorePublish = gpGlobals->curtime + 1.0f;
			PublishScore();
		}

		if ( m_bIdentityPublished )
		{
			return;
		}
		if ( gpGlobals->curtime < m_flNextIdentityCheck )
		{
			return;
		}
		m_flNextIdentityCheck = gpGlobals->curtime + 0.5f;
		TryPublishEndpoint();
	}

private:
	void EnforceBattlePolicy()
	{
		ConVarRef battleID( "greyline_battle_id", true );
		if ( !battleID.IsValid() )
		{
			return;
		}
		if ( !battleID.GetString()[0] )
		{
			if ( m_bPolicyWasArmed && greyline_policy_armed.GetBool() )
			{
				MarkPolicyTainted( "greyline_battle_id" );
			}
			return;
		}
		// A taint belongs to one battle. Without this it outlives its own:
		// greyline_battle_id is only overwritten once the *next* battle's map
		// is up, so at that map's LevelInit the id still reads as the old
		// battle's, the watchdog stays armed across the transition, and the
		// verdict on the last battle is applied to a battle that has not
		// started. Re-latching on the id changing is what scopes it.
		if ( V_strcmp( m_szArmedBattleID, battleID.GetString() ) != 0 )
		{
			V_strncpy( m_szArmedBattleID, battleID.GetString(), sizeof( m_szArmedBattleID ) );
			m_bPolicyTainted = false;
			m_szPolicyViolation[0] = '\0';
			m_bPolicyWasArmed = greyline_policy_armed.GetBool();
			greyline_policy_tainted.SetValue( 0 );
			greyline_policy_violation.SetValue( "" );
		}

		// Before anything else about policy: a battle's slots belong to the
		// roster. Enforced from the moment there is a battle id rather than
		// from the moment policy is armed, because the quota fills the server
		// while the map is still spawning — long before the assignment
		// commands finish arriving.
		KeepBotsOut();

		if ( greyline_policy_armed.GetBool() )
		{
			m_bPolicyWasArmed = true;
		}
		if ( !m_bPolicyWasArmed )
		{
			return;
		}
		if ( m_bPolicyTainted )
		{
			greyline_policy_tainted.SetValue( 1 );
			greyline_policy_violation.SetValue( m_szPolicyViolation );
		}
		if ( !greyline_policy_armed.GetBool() )
		{
			MarkPolicyTainted( "greyline_policy_armed" );
			greyline_policy_armed.SetValue( 1 );
		}

		struct Setting
		{
			const char *name;
			int value;
		};
		static const Setting settings[] =
		{
			{ "sv_allow_votes", 0 },
			{ "mp_autoteambalance", 0 },
			{ "mp_teams_unbalance_limit", 0 },
			{ "sv_pausable", 0 },
			{ "mp_forcecamera", 0 },
			{ "mp_tournament", 0 },
			{ "sv_cheats", 0 },
			{ "tf_weapon_criticals", 0 },
			{ "tf_weapon_criticals_melee", 0 },
			{ "tf_use_fixed_weaponspreads", 1 },
			{ "tf_damage_disablespread", 1 },
			{ "mp_friendlyfire", 0 },
			{ "mp_disable_respawn_times", 0 },
		};
		for ( int i = 0; i < ARRAYSIZE( settings ); ++i )
		{
			ConVarRef var( settings[i].name, true );
			if ( var.IsValid() && var.GetInt() != settings[i].value )
			{
				MarkPolicyTainted( settings[i].name );
				var.SetValue( settings[i].value );
			}
		}

		if ( !engine->IsDedicatedServer() )
		{
			ConVarRef advertise( "sv_allow_server_adverisement_to_master_server", true );
			if ( advertise.IsValid() && advertise.GetInt() != 0 )
			{
				MarkPolicyTainted( "sv_allow_server_adverisement_to_master_server" );
				advertise.SetValue( 0 );
			}
		}
		ConVarRef hostTimescale( "host_timescale", true );
		if ( hostTimescale.IsValid() && hostTimescale.GetFloat() != 1.0f )
		{
			MarkPolicyTainted( "host_timescale" );
			hostTimescale.SetValue( 1.0f );
		}
		if ( TFGameRules() && TFGameRules()->HaveCheatsBeenEnabledDuringLevel() )
		{
			MarkPolicyTainted( "sv_cheats" );
		}
	}

	// A listen server whose owner has ever run offline practice still carries
	// tf_bot_quota in their config, and quota mode "fill" takes every free slot
	// the moment a map is up. The roster then meets "Server is full." and the
	// player is told nothing at all — which is how both P2P test sessions
	// ended. This is not treated as tampering: it is somebody's leftover
	// setting, so it is corrected quietly rather than tainting their battle.
	void KeepBotsOut()
	{
		ConVarRef botQuota( "tf_bot_quota", true );
		if ( botQuota.IsValid() && botQuota.GetInt() != 0 )
		{
			botQuota.SetValue( 0 );
		}
		ConVarRef botVacate( "tf_bot_auto_vacate", true );
		if ( botVacate.IsValid() && botVacate.GetInt() != 1 )
		{
			botVacate.SetValue( 1 );
		}

		if ( gpGlobals->curtime < m_flNextBotKick )
		{
			return;
		}
		m_flNextBotKick = gpGlobals->curtime + 5.0f;

		for ( int i = 1; i <= gpGlobals->maxClients; ++i )
		{
			CBasePlayer *pPlayer = UTIL_PlayerByIndex( i );
			if ( pPlayer && pPlayer->IsConnected() && pPlayer->IsFakeClient() )
			{
				Msg( "[greyline] removing bots: a battle's slots are for the roster it was formed from\n" );
				engine->ServerCommand( "tf_bot_kick all\n" );
				return;
			}
		}
	}

	void MarkPolicyTainted( const char *pszSetting )
	{
		// Still settling after a level start. The caller puts the setting back
		// either way; all this skips is calling it cheating.
		if ( gpGlobals->curtime < m_flPolicySettleUntil )
		{
			if ( greyline_hoststate_debug.GetBool() )
			{
				Msg( "[greyline] put %s back during the level's settle window; not a violation\n",
					pszSetting ? pszSetting : "a protected setting" );
			}
			return;
		}

		if ( !m_bPolicyTainted )
		{
			m_bPolicyTainted = true;
			V_strncpy( m_szPolicyViolation, pszSetting ? pszSetting : "protected setting",
				sizeof( m_szPolicyViolation ) );
			Warning( "[greyline/security] battle tainted: %s changed\n", m_szPolicyViolation );
		}
		greyline_policy_tainted.SetValue( 1 );
		greyline_policy_violation.SetValue( m_szPolicyViolation );
	}

	void PublishScore()
	{
		CTeam *pRed = GetGlobalTeam( TF_TEAM_RED );
		CTeam *pBlu = GetGlobalTeam( TF_TEAM_BLUE );
		if ( !pRed || !pBlu || !BattleStateIsTeamScore() )
		{
			return;
		}
		greyline_score_red.SetValue( pRed->GetScore() );
		greyline_score_blu.SetValue( pBlu->GetScore() );
		if ( TFGameRules() )
		{
			greyline_rounds_played.SetValue( TFGameRules()->GetRoundsPlayed() );
		}
	}

	// Returns false while the address is still being allocated. A FakeIP
	// allocation is asynchronous; publishing before it lands would hand out an
	// invalid address, which is worse than saying nothing yet.
	bool TryPublishAddress()
	{
		IServer *pServer = engine->GetIServer();
		if ( !pServer )
		{
			return true; // nothing to wait for; the SteamID is all this build has
		}

		const bool bFakeIP = pServer->IsUsingFakeIP();
		const netadr_t adr = pServer->GetPublicAddress();

		if ( !adr.IsValid() )
		{
			return !bFakeIP;
		}

		static ConVarRef sv_lan( "sv_lan" );
		const bool bUsable = bFakeIP || !adr.IsReservedAdr() ||
			( sv_lan.IsValid() && sv_lan.GetBool() );

		if ( !bUsable )
		{
			if ( greyline_hoststate_debug.GetBool() )
			{
				Msg( "[greyline] not advertising %s: a private address reaches nobody outside this network\n",
					adr.ToString() );
			}
			return true;
		}

		char szAddress[64];
		adr.ToString_safe( szAddress );
		greyline_server_addr.SetValue( szAddress );

		Msg( "[greyline] P2P battle address %s%s\n", szAddress, bFakeIP ? " (Steam Networking)" : "" );
		return true;
	}

	void TryPublishEndpoint()
	{
		const CSteamID *pSteamID = engine->GetGameServerSteamID();
		if ( !pSteamID || !pSteamID->IsValid() || pSteamID->GetAccountID() == 0 )
		{
			return;
		}
		if ( !TryPublishAddress() )
		{
			return;
		}

		char szID[32];
		V_snprintf( szID, sizeof( szID ), "%llu", pSteamID->ConvertToUint64() );
		greyline_server_id.SetValue( szID );
		m_bIdentityPublished = true;

		if ( !greyline_server_addr.GetString()[0] )
		{
			Warning( "[greyline] this listen server advertises no address; remote players may not reach it.\n" );
			Warning( "[greyline] the engine only requests a Steam FakeIP when launched with -enablefakeip.\n" );
		}

		Msg( "[greyline] P2P listen server identity %s ready\n", szID );
	}

	bool	m_bIdentityPublished;
	float	m_flNextIdentityCheck;
	float	m_flNextScorePublish;
	float	m_flNextPolicyCheck;
	float	m_flNextBotKick;
	float	m_flPolicySettleUntil;
	bool	m_bPolicyWasArmed;
	char	m_szArmedBattleID[64];
	bool	m_bPolicyTainted;
	char	m_szPolicyViolation[64];
};

static CGreylineHostState g_GreylineHostState;

//-----------------------------------------------------------------------------
CON_COMMAND_F( greyline_hoststate_status, "Show the Greyline P2P host state.", FCVAR_GAMEDLL )
{
	Msg( "greyline P2P host state:\n" );
	Msg( "  dedicated     : %s\n", engine->IsDedicatedServer() ? "yes (not a P2P host)" : "no" );
	Msg( "  server address: %s\n",
		greyline_server_addr.GetString()[0] ? greyline_server_addr.GetString() : "<none advertised>" );
	Msg( "  game server id: %s\n", greyline_server_id.GetString() );
	Msg( "  score         : RED %s - BLU %s, %s round(s) played\n",
		greyline_score_red.GetString(), greyline_score_blu.GetString(),
		greyline_rounds_played.GetString() );
}
