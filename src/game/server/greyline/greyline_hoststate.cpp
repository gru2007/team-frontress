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
	}

	virtual void LevelInitPostEntity() OVERRIDE
	{
		m_bIdentityPublished = false;
		m_flNextIdentityCheck = 0.0f;
		m_flNextScorePublish = 0.0f;
		m_flNextPolicyCheck = 0.0f;
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
		if ( !battleID.IsValid() || !battleID.GetString()[0] )
		{
			return;
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
		};
		for ( int i = 0; i < ARRAYSIZE( settings ); ++i )
		{
			ConVarRef var( settings[i].name, true );
			if ( var.IsValid() && var.GetInt() != settings[i].value )
			{
				var.SetValue( settings[i].value );
			}
		}

		if ( !engine->IsDedicatedServer() )
		{
			ConVarRef advertise( "sv_allow_server_adverisement_to_master_server", true );
			if ( advertise.IsValid() && advertise.GetInt() != 0 )
			{
				advertise.SetValue( 0 );
			}
		}
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
