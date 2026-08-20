//========= GREYLINE FRONTRESS ============================================//
//
// Purpose: holds a battle at the gate until the people it was formed out of
// are actually on the server.
//
// A map loads in half a minute and the round starts the moment it does, so on
// a small population most of a battle is fought by whoever loaded fastest
// while everyone else is still on a loading screen. That is not a rough edge:
// the war counts the result, and a result decided before half the roster
// arrived is a result the war should not have been given.
//
// The mechanism is the engine's own WAITING FOR PLAYERS period, which already
// does exactly this and already tells players what it is doing — it is simply
// off by default (mp_waitingforplayers_time is 0). All this file does is turn
// it on for the length of the muster, and end it early the moment the roster
// is present.
//
// What "the roster" is comes from the coordinator, the same way everything
// else about a battle does:
//
//   coordinator --(HTTP)--> agent or menu page --(console)--> these convars
//
//=========================================================================//

#include "cbase.h"
#include "igamesystem.h"
#include "tf_gamerules.h"
#include "tf_player.h"
#include "greyline/greyline_roster_gate.h"

// memdbgon must be the last include file in a .cpp file!!!
#include "tier0/memdbgon.h"

ConVar greyline_min_players( "greyline_min_players", "0", FCVAR_GAMEDLL,
	"Hold the battle in WAITING FOR PLAYERS until this many humans are connected. "
	"0 or 1 disables the hold; the coordinator sets it to the size of the roster it formed." );

ConVar greyline_muster_timeout( "greyline_muster_timeout", "90", FCVAR_GAMEDLL,
	"Give up waiting for the roster after this many seconds and start the battle anyway, "
	"so one player who never arrives costs a wait rather than the battle." );

ConVar greyline_muster_debug( "greyline_muster_debug", "0", FCVAR_GAMEDLL,
	"Spew what the battle is waiting for." );

//-----------------------------------------------------------------------------
// Purpose: arms the engine's waiting-for-players period for one battle, and
// disarms it as soon as the roster is here.
//-----------------------------------------------------------------------------
class CGreylineMuster : public CAutoGameSystemPerFrame
{
public:
	CGreylineMuster() : CAutoGameSystemPerFrame( "CGreylineMuster" )
	{
		Reset();
	}

	virtual void LevelInitPostEntity() OVERRIDE
	{
		Reset();
	}

	virtual void LevelShutdownPreEntity() OVERRIDE
	{
		// A level change while holding must not leave the next map waiting for
		// a period this battle armed.
		if ( m_eState == MUSTER_HOLDING )
		{
			Release( "the level changed" );
		}
		Reset();
	}

	virtual void FrameUpdatePostEntityThink() OVERRIDE;

private:
	enum MusterState_t
	{
		MUSTER_IDLE = 0,	// nothing to wait for, or nothing has asked us to
		MUSTER_HOLDING,		// the wait is armed and the roster is not here yet
		MUSTER_DONE,		// this battle has started; do not hold it again
	};

	void Reset()
	{
		m_eState = MUSTER_IDLE;
		m_flNextThink = 0.0f;
		m_flPreviousWaitTime = 0.0f;
		m_nLastAnnouncedCount = -1;
	}

	void Arm( int nWanted );
	void Release( const char *pszWhy );
	void AnnounceProgress( int nPresent, int nWanted );

	static int CountHumans();

	MusterState_t	m_eState;
	float			m_flNextThink;
	float			m_flPreviousWaitTime;
	int				m_nLastAnnouncedCount;
};

static CGreylineMuster g_GreylineMuster;

//-----------------------------------------------------------------------------
int CGreylineMuster::CountHumans()
{
	int nHumans = 0;
	for ( int i = 1; i <= gpGlobals->maxClients; ++i )
	{
		CBasePlayer *pPlayer = UTIL_PlayerByIndex( i );
		if ( pPlayer && pPlayer->IsConnected() && !pPlayer->IsFakeClient() &&
			( !greyline::BattleHasRoster() || greyline::IsRosterMember( pPlayer->GetSteamIDAsUInt64() ) ) )
		{
			++nHumans;
		}
	}
	return nHumans;
}

//-----------------------------------------------------------------------------
// Purpose: starts the wait, sized so the engine's own timer is the muster
// timeout. Ending it early is then the only thing left to decide.
//-----------------------------------------------------------------------------
void CGreylineMuster::Arm( int nWanted )
{
	ConVarRef waitTime( "mp_waitingforplayers_time" );
	ConVarRef waitRestart( "mp_waitingforplayers_restart" );
	if ( !waitTime.IsValid() || !waitRestart.IsValid() )
	{
		// No waiting-for-players in this game's rules. Nothing to arm, and
		// nothing worth failing over.
		m_eState = MUSTER_DONE;
		return;
	}

	m_flPreviousWaitTime = waitTime.GetFloat();
	waitTime.SetValue( MAX( 5.0f, greyline_muster_timeout.GetFloat() ) );
	waitRestart.SetValue( 1 );

	m_eState = MUSTER_HOLDING;
	m_nLastAnnouncedCount = -1;

	if ( greyline_muster_debug.GetBool() )
	{
		Msg( "[greyline/muster] holding the battle for %d players, up to %.0fs\n",
			nWanted, waitTime.GetFloat() );
	}
}

//-----------------------------------------------------------------------------
void CGreylineMuster::Release( const char *pszWhy )
{
	ConVarRef waitTime( "mp_waitingforplayers_time" );
	ConVarRef waitCancel( "mp_waitingforplayers_cancel" );
	if ( waitCancel.IsValid() )
	{
		waitCancel.SetValue( 1 );
	}
	// Put the server's own setting back: the hold belongs to this battle, not
	// to every round that follows it.
	if ( waitTime.IsValid() )
	{
		waitTime.SetValue( m_flPreviousWaitTime );
	}

	m_eState = MUSTER_DONE;

	if ( greyline_muster_debug.GetBool() )
	{
		Msg( "[greyline/muster] starting the battle: %s\n", pszWhy ? pszWhy : "" );
	}
}

//-----------------------------------------------------------------------------
// Purpose: tells everyone waiting how many of them there are. Once per change
// in the count — a line a second would bury the briefing.
//-----------------------------------------------------------------------------
void CGreylineMuster::AnnounceProgress( int nPresent, int nWanted )
{
	if ( nPresent == m_nLastAnnouncedCount )
		return;
	m_nLastAnnouncedCount = nPresent;

	char szPresent[16], szWanted[16];
	V_snprintf( szPresent, sizeof( szPresent ), "%d", nPresent );
	V_snprintf( szWanted, sizeof( szWanted ), "%d", nWanted );

	for ( int i = 1; i <= gpGlobals->maxClients; ++i )
	{
		CBasePlayer *pPlayer = UTIL_PlayerByIndex( i );
		if ( !pPlayer || !pPlayer->IsConnected() || pPlayer->IsFakeClient() )
			continue;
		ClientPrint( pPlayer, HUD_PRINTTALK, "#Greyline_Muster_Waiting", szPresent, szWanted );
	}
}

//-----------------------------------------------------------------------------
void CGreylineMuster::FrameUpdatePostEntityThink()
{
	if ( m_eState == MUSTER_DONE )
		return;

	// Once a second. Nothing here is worth doing per tick, and the convars it
	// reads are set over the console by a machine on the other side of an HTTP
	// request — they arrive whenever they arrive.
	if ( gpGlobals->curtime < m_flNextThink )
		return;
	m_flNextThink = gpGlobals->curtime + 1.0f;

	const int nWanted = greyline_min_players.GetInt();
	if ( nWanted <= 1 )
	{
		// Not a coordinator battle, or a battle of one. Either way there is
		// nothing to wait for.
		return;
	}

	CTeamplayRoundBasedRules *pRules = TFGameRules();
	if ( !pRules )
		return;

	const int nPresent = CountHumans();

	if ( m_eState == MUSTER_IDLE )
	{
		if ( nPresent >= nWanted )
		{
			// Everyone beat us to it. Nothing to hold.
			m_eState = MUSTER_DONE;
			return;
		}
		// The agent sets these before the changelevel and the menu page sets
		// them just after it, so arming is driven by the convar arriving
		// rather than by the level starting.
		Arm( nWanted );
		return;
	}

	if ( nPresent >= nWanted )
	{
		Release( "the roster is here" );
		for ( int i = 1; i <= gpGlobals->maxClients; ++i )
		{
			CBasePlayer *pPlayer = UTIL_PlayerByIndex( i );
			if ( pPlayer && pPlayer->IsConnected() && !pPlayer->IsFakeClient() )
			{
				ClientPrint( pPlayer, HUD_PRINTTALK, "#Greyline_Muster_Ready" );
			}
		}
		return;
	}

	if ( !pRules->IsInWaitingForPlayers() )
	{
		// The engine's own timer ran out — that is the muster timeout, and the
		// battle starts a body or two short rather than not at all.
		Release( "the muster timed out" );
		char szPresent[16], szWanted[16];
		V_snprintf( szPresent, sizeof( szPresent ), "%d", nPresent );
		V_snprintf( szWanted, sizeof( szWanted ), "%d", nWanted );
		for ( int i = 1; i <= gpGlobals->maxClients; ++i )
		{
			CBasePlayer *pPlayer = UTIL_PlayerByIndex( i );
			if ( pPlayer && pPlayer->IsConnected() && !pPlayer->IsFakeClient() )
			{
				ClientPrint( pPlayer, HUD_PRINTTALK, "#Greyline_Muster_Timeout", szPresent, szWanted );
			}
		}
		return;
	}

	AnnounceProgress( nPresent, nWanted );
}
