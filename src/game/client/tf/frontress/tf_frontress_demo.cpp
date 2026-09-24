//========= Copyright Team Frontress, All rights reserved. ====================//
//
// Purpose: The game's side of the Team Frontress demo. See tf_frontress_demo.h.
//
//=============================================================================//

#include "cbase.h"

#include "tf_frontress_demo.h"

#include "gamestate/gamestate.h"
#include "c_baseplayer.h"
#include "igamesystem.h"
#include "GameEventListener.h"
#include "tf_shareddefs.h"
#include "filesystem.h"
#include "fmtstr.h"
#include "tier0/icommandline.h"
#include "game/client/iviewport.h"
#include "viewport_panel_names.h"

// memdbgon must be the last include file in a .cpp file!!!
#include "tier0/memdbgon.h"

ConVar tf_frontress_demo( "tf_frontress_demo", "1", FCVAR_NONE,
                          "Open the Team Frontress demo (resource/html/frontress) instead of the main menu. "
                          "Read when the menu is built." );

ConVar tf_frontress_demo_return_delay( "tf_frontress_demo_return_delay", "8", FCVAR_NONE,
                                       "Seconds between the round that decides a demo battle and the return to the war map.",
                                       true, 0.f, true, 60.f );

//-----------------------------------------------------------------------------
bool TFFrontressDemoMenu()
{
	if ( CommandLine()->CheckParm( "-nofrontressdemo" ) )
		return false;
	return CommandLine()->CheckParm( "-frontressdemo" ) || tf_frontress_demo.GetBool();
}

const char *TFFrontressMenuPage()
{
	return TFFrontressDemoMenu() ? "ui/frontress/index.html" : "ui/index.html";
}

//-----------------------------------------------------------------------------
// Everything the page sends is echoed into a map name, a console line or a
// JSON document, so it is held to a character set that is safe in all three.
//-----------------------------------------------------------------------------
static bool IsSafeToken( const char *psz, int nMaxLen )
{
	if ( !psz || !psz[0] || V_strlen( psz ) > nMaxLen )
		return false;
	for ( const char *p = psz; *p; ++p )
	{
		if ( !V_isalnum( *p ) && *p != '_' && *p != '-' )
			return false;
	}
	return true;
}

static const char *s_pszClasses[] =
{
	"scout", "soldier", "pyro", "demoman", "heavyweapons", "engineer", "medic", "sniper", "spy",
};

//=============================================================================
// One battle at a time: the ticket the page issued, and what became of it.
//=============================================================================
class CTFFrontressDemo : public CAutoGameSystemPerFrame, public CGameEventListener
{
public:
	CTFFrontressDemo() : CAutoGameSystemPerFrame( "CTFFrontressDemo" )
	{
		m_bActive = false;
		m_bDecided = false;
		m_nPlayers = 0;
		m_bSwapUniforms = false;
		m_flSetupAt = -1.f;
		m_flSetupDeadline = -1.f;
		m_flReturnAt = -1.f;
	}

	virtual bool Init() OVERRIDE
	{
		ListenForGameEvent( "teamplay_round_win" );
		return true;
	}

	virtual void LevelInitPostEntity() OVERRIDE
	{
		// The page's battle, not whatever else the player may have loaded.
		if ( m_bActive && !m_bDecided && BOnBattleMap() )
		{
			// Update waits for the local player to exist; this is the earliest
			// it may try, and the deadline after which it stops waiting.
			m_flSetupAt = (float)Plat_FloatTime() + 1.f;
			m_flSetupDeadline = m_flSetupAt + 30.f;
		}
	}

	virtual void LevelShutdownPreEntity() OVERRIDE
	{
		m_flSetupAt = -1.f;
		m_flReturnAt = -1.f;
	}

	virtual void Update( float frametime ) OVERRIDE;
	virtual void FireGameEvent( IGameEvent *pEvent ) OVERRIDE;

	void Deploy( const CCommand &args );
	void Clear();
	void Decide( int nWinningTeam );
	void Status();

	bool BActive() const { return m_bActive && !m_bDecided; }

private:
	bool BOnBattleMap() const
	{
		char szLevel[ MAX_PATH ];
		V_FileBase( engine->GetLevelName(), szLevel, sizeof( szLevel ) );
		return !V_stricmp( szLevel, m_strMap.Get() );
	}

	void Publish( const char *pszWinner = NULL, const char *pszError = NULL );
	void SetUp();

	CUtlString m_strTicket;
	CUtlString m_strMap;
	CUtlString m_strTeam;		// "red" or "blue", as jointeam wants it
	CUtlString m_strClass;		// a joinclass name, or empty to let the player pick
	int        m_nPlayers;		// both teams, the player included
	bool       m_bSwapUniforms;	// the page's side plays the other colour's team
	bool       m_bActive;
	bool       m_bDecided;
	float      m_flSetupAt;
	float      m_flSetupDeadline;
	float      m_flReturnAt;
};

static CTFFrontressDemo s_FrontressDemo;

//-----------------------------------------------------------------------------
void CTFFrontressDemo::Publish( const char *pszWinner, const char *pszError )
{
	if ( !GetGameStateManager()->IsReady() )
		return;

	if ( !m_bActive )
	{
		GetGameStateManager()->SetDemoBattleJSON( "{}" );
		return;
	}

	// Every value was checked by IsSafeToken, so none needs escaping.
	CFmtStr1024 doc( "{\"ticket\":\"%s\",\"map\":\"%s\",\"team\":\"%s\",\"players\":%d,\"swap\":%s",
	                 m_strTicket.Get(), m_strMap.Get(), m_strTeam.Get(), m_nPlayers, m_bSwapUniforms ? "true" : "false" );
	if ( pszWinner )
		doc.AppendFormat( ",\"winner\":\"%s\"", pszWinner );
	if ( pszError )
		doc.AppendFormat( ",\"error\":\"%s\"", pszError );
	doc.Append( "}" );

	GetGameStateManager()->SetDemoBattleJSON( doc.Get() );
}

//-----------------------------------------------------------------------------
void CTFFrontressDemo::Deploy( const CCommand &args )
{
	if ( args.ArgC() < 5 )
	{
		Msg( "Usage: frontress_demo_deploy <ticket> <map> <red|blue> <players> [class|any] [swap 0|1]\n" );
		return;
	}

	const char *pszTicket = args[ 1 ];
	const char *pszMap = args[ 2 ];
	const char *pszTeam = args[ 3 ];
	const int nPlayers = clamp( V_atoi( args[ 4 ] ), 2, 24 );
	const char *pszClass = args.ArgC() > 5 ? args[ 5 ] : "any";
	const bool bSwap = args.ArgC() > 6 && V_atoi( args[ 6 ] ) != 0;

	if ( !IsSafeToken( pszTicket, 40 ) || !IsSafeToken( pszMap, 64 ) )
	{
		Warning( "frontress_demo_deploy: bad ticket or map\n" );
		return;
	}
	if ( V_stricmp( pszTeam, "red" ) && V_stricmp( pszTeam, "blue" ) )
	{
		Warning( "frontress_demo_deploy: team must be red or blue\n" );
		return;
	}

	m_strTicket = pszTicket;
	m_strMap = pszMap;
	m_strTeam = !V_stricmp( pszTeam, "red" ) ? "red" : "blue";
	m_strClass.Clear();
	for ( int i = 0; i < ARRAYSIZE( s_pszClasses ); ++i )
	{
		if ( !V_stricmp( pszClass, s_pszClasses[ i ] ) )
			m_strClass = s_pszClasses[ i ];
	}
	m_nPlayers = nPlayers;
	m_bSwapUniforms = bSwap;
	m_bActive = true;
	m_bDecided = false;
	m_flSetupAt = -1.f;
	m_flReturnAt = -1.f;

	// A map that is not installed would leave the player on a console error
	// with the menu still waiting; say so to the page instead.
	if ( !g_pFullFileSystem->FileExists( CFmtStr( "maps/%s.bsp", pszMap ), "GAME" ) )
	{
		Warning( "frontress_demo_deploy: maps/%s.bsp is not installed\n", pszMap );
		Publish( NULL, "missing_map" );
		return;
	}

	Publish();

	// A fresh listen server, sized for the battle. Bots come after the player
	// has a team (see Update), so the quota starts at zero.
	CFmtStr1024 launch( "disconnect\nwait\nsv_lan 1\ntf_bot_quota 0\nmaxplayers %d\nmap %s\n",
	                    nPlayers + 1, pszMap );
	engine->ClientCmd_Unrestricted( launch.Get() );
}

//-----------------------------------------------------------------------------
void CTFFrontressDemo::Clear()
{
	// The page may read the result and clear it while the win screen is still
	// up; the trip back to the war map it is waiting for must still happen,
	// so m_flReturnAt is left alone.
	m_bActive = false;
	m_bDecided = false;
	m_flSetupAt = -1.f;
	m_strTicket.Clear();
	Publish();

	// Nothing outside a demo battle should be drawn in swapped colours.
	if ( !engine->IsInGame() )
		engine->ClientCmd_Unrestricted( "greyline_uniform_swap 0\n" );
}

//-----------------------------------------------------------------------------
void CTFFrontressDemo::Decide( int nWinningTeam )
{
	if ( !BActive() )
		return;

	m_bDecided = true;
	const char *pszWinner = nWinningTeam == TF_TEAM_RED ? "red" : nWinningTeam == TF_TEAM_BLUE ? "blue" : "none";
	Publish( pszWinner );
	m_flReturnAt = (float)Plat_FloatTime() + tf_frontress_demo_return_delay.GetFloat();
	DevMsg( "Frontress demo: battle %s on %s decided, winner %s\n", m_strTicket.Get(), m_strMap.Get(), pszWinner );
}

//-----------------------------------------------------------------------------
// The player has spawned into the battle map: make it the page's battle.
//-----------------------------------------------------------------------------
void CTFFrontressDemo::SetUp()
{
	// Human on the page's team first; Source's fill quota counts humans
	// already on a team, so the quota then brings both teams to size.
	//
	// greyline_uniform_swap is the war-colours switch from
	// src/game/shared/greyline/greyline_uniform.h. On main the battle roster
	// sets it; the demo has no roster, so the page says whether this battle
	// has the player's side on the other colour's team (a RED offensive on a
	// BLU-attacks map) and the host sets it here.
	CFmtStr1024 setup( "exec frontress_demo.cfg\ngreyline_uniform_swap %d\njointeam %s\n",
	                   m_bSwapUniforms ? 1 : 0, m_strTeam.Get() );
	if ( !m_strClass.IsEmpty() )
		setup.AppendFormat( "joinclass %s\n", m_strClass.Get() );
	setup.AppendFormat( "tf_bot_quota %d\n", m_nPlayers );
	engine->ClientCmd_Unrestricted( setup.Get() );

	// The server's welcome (MOTD) and the team menu would sit over a battle
	// the page has already chosen for. The class menu stays when the page
	// left the class to the player.
	if ( gViewPortInterface )
	{
		gViewPortInterface->ShowPanel( PANEL_INFO, false );
		gViewPortInterface->ShowPanel( PANEL_TEAM, false );
		if ( !m_strClass.IsEmpty() )
		{
			gViewPortInterface->ShowPanel( PANEL_CLASS_RED, false );
			gViewPortInterface->ShowPanel( PANEL_CLASS_BLUE, false );
		}
	}
}

//-----------------------------------------------------------------------------
void CTFFrontressDemo::Update( float frametime )
{
	const float flNow = (float)Plat_FloatTime();

	if ( m_flSetupAt > 0.f && flNow >= m_flSetupAt )
	{
		// jointeam before the player entity exists is dropped by the server,
		// so wait for it -- but not forever, a stuck load is not our problem.
		if ( engine->IsInGame() && C_BasePlayer::GetLocalPlayer() )
		{
			m_flSetupAt = -1.f;
			SetUp();
		}
		else if ( flNow >= m_flSetupDeadline )
		{
			m_flSetupAt = -1.f;
			Warning( "Frontress demo: never got a local player on %s; not setting the battle up\n", m_strMap.Get() );
		}
	}

	if ( m_flReturnAt > 0.f && flNow >= m_flReturnAt )
	{
		m_flReturnAt = -1.f;
		engine->ClientCmd_Unrestricted( "disconnect\ngreyline_uniform_swap 0\n" );
	}
}

//-----------------------------------------------------------------------------
void CTFFrontressDemo::FireGameEvent( IGameEvent *pEvent )
{
	if ( !BActive() || !engine->IsInGame() || !BOnBattleMap() )
		return;

	// Mini-rounds (a Payload stage, one half of a Dustbowl round) do not
	// decide anything; the round that resets the map does.
	if ( !V_stricmp( pEvent->GetName(), "teamplay_round_win" ) && pEvent->GetBool( "full_round", true ) )
	{
		Decide( pEvent->GetInt( "team" ) );
	}
}

//-----------------------------------------------------------------------------
void CTFFrontressDemo::Status()
{
	Msg( "Frontress demo menu: %s (%s)\n", TFFrontressDemoMenu() ? "on" : "off", TFFrontressMenuPage() );
	if ( !m_bActive )
	{
		Msg( "No battle.\n" );
		return;
	}
	Msg( "Battle %s: %s as %s%s, %d players, class %s, %s\n", m_strTicket.Get(), m_strMap.Get(), m_strTeam.Get(),
	     m_bSwapUniforms ? " (uniforms swapped)" : "", m_nPlayers, m_strClass.IsEmpty() ? "any" : m_strClass.Get(),
	     m_bDecided ? "decided" : "in progress" );
}

//=============================================================================
// Commands. The page uses deploy and clear; the rest are for QA.
//=============================================================================
CON_COMMAND( frontress_demo_deploy, "Team Frontress demo: <ticket> <map> <red|blue> <players> [class|any] [swap 0|1] -- start a local battle with bots." )
{
	s_FrontressDemo.Deploy( args );
}

CON_COMMAND( frontress_demo_clear, "Team Frontress demo: forget the current battle and its result." )
{
	s_FrontressDemo.Clear();
}

CON_COMMAND( frontress_demo_status, "Team Frontress demo: print the current battle." )
{
	s_FrontressDemo.Status();
}

CON_COMMAND( frontress_demo_force_win, "Team Frontress demo QA: end the current battle as a win for <red|blue|none>." )
{
	const char *pszTeam = args.ArgC() > 1 ? args[ 1 ] : "";
	s_FrontressDemo.Decide( !V_stricmp( pszTeam, "red" ) ? TF_TEAM_RED : !V_stricmp( pszTeam, "blue" ) ? TF_TEAM_BLUE : TEAM_UNASSIGNED );
}
