//========= GREYLINE FRONTRESS ============================================//
//
// Purpose: tells the players in a battle which part of the war they are in.
//
// A GREYLINE battle is never just "a round of cp_process". It is stage two of
// RED's offensive at Foundry 17, and losing it pushes that offensive back. If
// the game does not say so, the whole strategic layer is invisible from inside
// the only place players actually spend their time — so this is not decoration,
// it is the feature.
//
// Where the context comes from:
//
//   coordinator --(HTTP)--> greyline-agent --(RCON)--> these convars
//
// The agent sets greyline_* before the changelevel, so by the time the map is
// up the server already knows what it is hosting. Nothing here talks to the
// coordinator, and nothing here decides anything about the war: this file only
// states what it was told, and puts each mercenary on the side the war put them
// on.
//
// Localization: the sentence structure is built from tokens the client resolves
// (resource/greyline_%language%.txt), so the briefing reads correctly in every
// language the game ships. Only proper nouns — the front and district names the
// coordinator generated — pass through as text.
//
// What the briefing says is decided in greyline_briefing_logic.cpp, which knows
// nothing about the engine and is unit-tested on its own (see
// src/game/shared/greyline/tests). This file is the half that needs a server:
// convars, players, teams and ClientPrint.
//
//=========================================================================//

#include "cbase.h"
#include "greyline/greyline_briefing_logic.h"
#include "greyline_roster_gate.h"
#include "greyline/greyline_uniform.h"
#include "igamesystem.h"
#include "GameEventListener.h"
#include "eiface.h"
#include "team.h"
#include "tf_player.h"
#include "tf_shareddefs.h"

// memdbgon must be the last include file in a .cpp file!!!
#include "tier0/memdbgon.h"

//-----------------------------------------------------------------------------
// The logic layer carries its own copy of the team numbers because it cannot
// include tf_shareddefs.h. If those ever diverge every roster assignment would
// silently put players on the wrong team, so the build stops instead.
//-----------------------------------------------------------------------------
COMPILE_TIME_ASSERT( greyline::kTeamNone == TEAM_UNASSIGNED );
COMPILE_TIME_ASSERT( greyline::kTeamRed == TF_TEAM_RED );
COMPILE_TIME_ASSERT( greyline::kTeamBlu == TF_TEAM_BLUE );

//-----------------------------------------------------------------------------
// The battle's war context, written by the agent over RCON before the map
// loads. All of it is descriptive: nothing here changes how the game plays.
//
// Renaming any of these breaks the agent, which sets them by name. The names
// are checked against the agent's in tools/greyline_integration_check.py.
//-----------------------------------------------------------------------------
ConVar greyline_battle_id( "greyline_battle_id", "", FCVAR_GAMEDLL,
	"Coordinator battle id for the battle this server is hosting." );
ConVar greyline_front_id( "greyline_front_id", "", FCVAR_GAMEDLL,
	"Coordinator front id this battle belongs to." );
ConVar greyline_front_name( "greyline_front_name", "", FCVAR_GAMEDLL,
	"Name of the offensive this battle is part of, e.g. BATTLE FOR FOUNDRY 17." );
ConVar greyline_node_id( "greyline_node_id", "", FCVAR_GAMEDLL,
	"Strategic node being fought over." );
ConVar greyline_node_name( "greyline_node_name", "", FCVAR_GAMEDLL,
	"Display name of the strategic node being fought over." );
ConVar greyline_campaign( "greyline_campaign", "", FCVAR_GAMEDLL,
	"Name of the campaign this battle belongs to." );
ConVar greyline_attacker( "greyline_attacker", "", FCVAR_GAMEDLL,
	"Side on the offensive: RED or BLU." );
ConVar greyline_defender( "greyline_defender", "", FCVAR_GAMEDLL,
	"Side defending: RED or BLU." );
ConVar greyline_stage( "greyline_stage", "0", FCVAR_GAMEDLL,
	"Which stage of the offensive this battle is, 1-based." );
ConVar greyline_stage_count( "greyline_stage_count", "0", FCVAR_GAMEDLL,
	"How many stages the offensive has." );
ConVar greyline_stage_kind( "greyline_stage_kind", "", FCVAR_GAMEDLL,
	"Stage kind: skirmish, breakthrough, advance or assault." );
ConVar greyline_mobilized( "greyline_mobilized", "", FCVAR_GAMEDLL,
	"Side under defensive mobilization, if any." );

ConVar greyline_briefing_enabled( "greyline_briefing_enabled", "1", FCVAR_GAMEDLL,
	"Announce the battle's place in the war to players." );
ConVar greyline_roster_enforce( "greyline_roster_enforce", "1", FCVAR_GAMEDLL,
	"Put players on the team the coordinator assigned them." );
ConVar greyline_roster_gate( "greyline_roster_gate", "0", FCVAR_GAMEDLL,
	"Refuse connections from players who are not on this battle's roster.\n"
	"OFF by default, and the reason is the whole point: this gate is worth exactly as "
	"much as the identities on the roster are. Under auth.mode=dev a client states "
	"whichever SteamID it likes, so the roster can be full of accounts the game server "
	"will never see — and turning strangers away then means turning everybody away. "
	"The coordinator switches it on for itself when it is running verified Steam auth "
	"(assignment field verified_identities); the battle password is what gates a "
	"battle until then." );
ConVar greyline_roster_kick_strangers( "greyline_roster_kick_strangers", "0", FCVAR_GAMEDLL,
	"Kick players who are not on the battle roster. With greyline_roster_gate on they "
	"could not have connected in the first place, so this is a third line rather than "
	"a second." );
ConVar greyline_briefing_debug( "greyline_briefing_debug", "0", FCVAR_GAMEDLL,
	"Spew Greyline briefing and roster decisions." );

//-----------------------------------------------------------------------------
// Purpose: keeps the roster, enforces teams, and announces the briefing.
//-----------------------------------------------------------------------------
class CGreylineBriefing : public CAutoGameSystemPerFrame, public CGameEventListener
{
public:
	CGreylineBriefing() : CAutoGameSystemPerFrame( "CGreylineBriefing" )
	{
		m_flNextEnforce = 0.0f;
		m_bAnnouncedThisLevel = false;
		V_memset( m_bAssignmentAnnounced, 0, sizeof( m_bAssignmentAnnounced ) );
	}

	virtual bool Init() OVERRIDE
	{
		ListenForGameEvent( "teamplay_round_start" );
		ListenForGameEvent( "player_activate" );
		return true;
	}

	virtual void Shutdown() OVERRIDE
	{
		StopListeningForAllEvents();
	}

	virtual void LevelInitPostEntity() OVERRIDE
	{
		m_bAnnouncedThisLevel = false;
		m_flNextEnforce = 0.0f;
		V_memset( m_bAssignmentAnnounced, 0, sizeof( m_bAssignmentAnnounced ) );
	}

	virtual void FrameUpdatePostEntityThink() OVERRIDE;
	virtual void FireGameEvent( IGameEvent *pEvent ) OVERRIDE;

	void ClearRoster()
	{
		m_Roster.Clear();
		V_memset( m_bAssignmentAnnounced, 0, sizeof( m_bAssignmentAnnounced ) );
		greyline::RecomputeUniformSwap();
	}
	bool AddToRoster( uint64 ulSteamID, int iTeam, int iWarSide, bool bContract )
	{
		if ( !m_Roster.Add( ulSteamID, iTeam, iWarSide, bContract ) )
			return false;
		greyline::RecomputeUniformSwap();
		return true;
	}
	bool RemoveFromRoster( uint64 ulSteamID )
	{
		if ( !m_Roster.Remove( ulSteamID ) )
			return false;
		greyline::RecomputeUniformSwap();
		return true;
	}

	// Whether this battle puts the war's sides in each other's in-game
	// colours. The rule itself lives in the logic layer, where it is unit
	// tested without an engine.
	bool RosterIsColourSwapped() const { return greyline::RosterColoursSwapped( m_Roster ); }

	// Returns NULL when the player is not on the roster, which is the normal
	// case on a server that is not currently hosting a Greyline battle.
	const greyline::RosterEntry_t *FindEntry( CBasePlayer *pPlayer ) const;

	// Announces to everyone. bIncludeHUD adds the single centre-screen notice,
	// which is deliberately once per battle rather than once per round.
	void AnnounceToAll( bool bIncludeHUD );
	void AnnounceToPlayer( CBasePlayer *pPlayer, bool bIncludeHUD );

	// Puts one player straight onto the team the war put them on, the moment
	// they arrive, and opens class selection there. Without it they are shown
	// the team menu, pick a side the war did not give them, and get moved off
	// it a second later by EnforceTeams — which reads as the server taking
	// something away rather than as never having been theirs to choose.
	void SeatPlayer( CBasePlayer *pPlayer );

	bool HasBattle() const { return greyline_front_name.GetString()[0] != '\0'; }

	int RosterCount() const { return m_Roster.Count(); }
	void PrintRoster() const;

	// The one-line answer to "what is going on" for a player who asks in the
	// middle of a battle: how much of the roster made it, and the score the
	// war will be given.
	void PrintStatusTo( CBasePlayer *pPlayer ) const;
	bool IsOnRoster( uint64 ulSteamID ) const { return m_Roster.Find( ulSteamID ) != NULL; }

private:
	void EnforceTeams();

	// Reads the convars the agent set into the shape the logic layer wants.
	static greyline::BattleContext_t CurrentContext();

	greyline::CRoster	m_Roster;
	float				m_flNextEnforce;
	bool				m_bAnnouncedThisLevel;
	bool				m_bAssignmentAnnounced[MAX_PLAYERS + 1];
};

static CGreylineBriefing g_GreylineBriefing;

//-----------------------------------------------------------------------------
greyline::BattleContext_t CGreylineBriefing::CurrentContext()
{
	greyline::BattleContext_t ctx;
	ctx.m_pszFrontName = greyline_front_name.GetString();
	ctx.m_pszNodeName = greyline_node_name.GetString();
	ctx.m_pszNodeID = greyline_node_id.GetString();
	ctx.m_pszAttacker = greyline_attacker.GetString();
	ctx.m_pszStageKind = greyline_stage_kind.GetString();
	ctx.m_pszMobilized = greyline_mobilized.GetString();
	ctx.m_nStage = greyline_stage.GetInt();
	ctx.m_nStageCount = greyline_stage_count.GetInt();
	// What the player can actually see decides which half of the colour
	// explanation is true — see greyline_uniform.h.
	ctx.m_bUniformsShowWarSide = greyline::UniformSwapped();
	return ctx;
}

//-----------------------------------------------------------------------------
const greyline::RosterEntry_t *CGreylineBriefing::FindEntry( CBasePlayer *pPlayer ) const
{
	if ( !pPlayer )
		return NULL;

	// Zero means Steam has not told the server who this is yet — a player who
	// activated a frame too early. Find() treats it as "not on the roster",
	// which is right: guessing would put somebody on the wrong team.
	return m_Roster.Find( pPlayer->GetSteamIDAsUInt64() );
}

//-----------------------------------------------------------------------------
// Purpose: puts every mercenary on the side the war put them on.
//
//			The coordinator balanced these teams around the war's own sides and,
//			on a directional map, around which team the map lets attack. A
//			player who switches out of that is not making a personal choice, they
//			are breaking the battle the war is counting, so they get moved back.
//-----------------------------------------------------------------------------
void CGreylineBriefing::EnforceTeams()
{
	if ( !greyline_roster_enforce.GetBool() || m_Roster.Count() == 0 )
		return;

	for ( int i = 1; i <= gpGlobals->maxClients; ++i )
	{
		CTFPlayer *pPlayer = ToTFPlayer( UTIL_PlayerByIndex( i ) );
		if ( !pPlayer || !pPlayer->IsConnected() )
			continue;

		const greyline::RosterEntry_t *pEntry = FindEntry( pPlayer );
		if ( !pEntry )
		{
			const uint64 ulSteamID = pPlayer->GetSteamIDAsUInt64();
			if ( ( greyline_roster_gate.GetBool() || greyline_roster_kick_strangers.GetBool() ) &&
				ulSteamID != 0 && !pPlayer->IsFakeClient() )
			{
				engine->ServerCommand( UTIL_VarArgs( "kickid %d %s\n",
					pPlayer->GetUserID(), "not on this battle's roster" ) );
			}
			continue;
		}

		// Same path as on arrival, so a player the backstop catches — one
		// whose SteamID had not reached the server yet when they activated —
		// gets the class menu too rather than a silent yank.
		SeatPlayer( pPlayer );
		if ( i <= MAX_PLAYERS && !m_bAssignmentAnnounced[i] )
		{
			AnnounceToPlayer( pPlayer, false );
		}
	}
}

//-----------------------------------------------------------------------------
void CGreylineBriefing::FrameUpdatePostEntityThink()
{
	// Once a second is enough: a player who picks the wrong team is corrected
	// before they can spawn into it, and nothing here is worth doing per tick.
	if ( gpGlobals->curtime < m_flNextEnforce )
		return;
	m_flNextEnforce = gpGlobals->curtime + 1.0f;

	EnforceTeams();
}

//-----------------------------------------------------------------------------
void CGreylineBriefing::FireGameEvent( IGameEvent *pEvent )
{
	if ( !pEvent )
		return;

	const char *pszName = pEvent->GetName();
	if ( !V_strcmp( pszName, "teamplay_round_start" ) )
	{
		// The HUD notice goes out once per battle, not once per round: a line
		// on the screen every round would be wallpaper by the third one.
		AnnounceToAll( !m_bAnnouncedThisLevel );
		m_bAnnouncedThisLevel = true;
		return;
	}

	if ( !V_strcmp( pszName, "player_activate" ) )
	{
		// Somebody joined late, or reconnected after a crash. They deserve the
		// same briefing everyone else got — and to be put where the war wants
		// them before they are asked to choose.
		CBasePlayer *pPlayer = UTIL_PlayerByUserId( pEvent->GetInt( "userid" ) );
		SeatPlayer( pPlayer );
		AnnounceToPlayer( pPlayer, true );
	}
}

//-----------------------------------------------------------------------------
void CGreylineBriefing::AnnounceToAll( bool bIncludeHUD )
{
	if ( !greyline_briefing_enabled.GetBool() || !HasBattle() )
		return;

	for ( int i = 1; i <= gpGlobals->maxClients; ++i )
	{
		CBasePlayer *pPlayer = UTIL_PlayerByIndex( i );
		if ( pPlayer && pPlayer->IsConnected() && !pPlayer->IsFakeClient() )
		{
			AnnounceToPlayer( pPlayer, bIncludeHUD );
		}
	}
}

//-----------------------------------------------------------------------------
// Purpose: sends the briefing this player should get.
//
//			Which lines those are is decided in the logic layer; all that happens
//			here is putting them on the wire. ClientPrint carries the token and
//			its substitutions untranslated, and the client resolves both against
//			its own language file — see CBaseHudChat::MsgFunc_TextMsg, which
//			looks up every parameter that starts with '#' before it builds the
//			sentence. That is what lets one server brief a Russian and an English
//			player correctly at the same time.
//-----------------------------------------------------------------------------
void CGreylineBriefing::AnnounceToPlayer( CBasePlayer *pPlayer, bool bIncludeHUD )
{
	if ( !pPlayer || pPlayer->IsFakeClient() )
		return;
	if ( !greyline_briefing_enabled.GetBool() || !HasBattle() )
		return;

	greyline::Briefing_t briefing;
	const greyline::RosterEntry_t *pEntry = FindEntry( pPlayer );
	greyline::BuildBriefing( CurrentContext(), pEntry, bIncludeHUD, &briefing );

	for ( int i = 0; i < briefing.m_nLines; ++i )
	{
		const greyline::BriefingLine_t &line = briefing.m_Lines[i];
		const int iDest = ( line.m_iDest == greyline::kDestCenter ) ? HUD_PRINTCENTER : HUD_PRINTTALK;

		// ClientPrint takes exactly four optional substitutions and ignores
		// trailing empties, which is why unused ones are "" rather than NULL.
		ClientPrint( pPlayer, iDest, line.m_pszToken,
			line.m_pszParams[0], line.m_pszParams[1],
			line.m_pszParams[2], line.m_pszParams[3] );
	}
	if ( pEntry )
	{
		const int iIndex = pPlayer->entindex();
		if ( iIndex > 0 && iIndex <= MAX_PLAYERS )
			m_bAssignmentAnnounced[iIndex] = true;
	}
}

//-----------------------------------------------------------------------------
// Purpose: the standing of the battle, in the asking player's own language.
//-----------------------------------------------------------------------------
void CGreylineBriefing::PrintStatusTo( CBasePlayer *pPlayer ) const
{
	if ( !pPlayer || pPlayer->IsFakeClient() )
		return;

	int nPresent = 0;
	for ( int i = 1; i <= gpGlobals->maxClients; ++i )
	{
		CBasePlayer *pOther = UTIL_PlayerByIndex( i );
		if ( pOther && pOther->IsConnected() && !pOther->IsFakeClient() )
			++nPresent;
	}

	CTeam *pRed = GetGlobalTeam( TF_TEAM_RED );
	CTeam *pBlu = GetGlobalTeam( TF_TEAM_BLUE );

	char szPresent[16], szRoster[16], szRed[16], szBlu[16];
	V_snprintf( szPresent, sizeof( szPresent ), "%d", nPresent );
	V_snprintf( szRoster, sizeof( szRoster ), "%d", m_Roster.Count() );
	V_snprintf( szRed, sizeof( szRed ), "%d", pRed ? pRed->GetScore() : 0 );
	V_snprintf( szBlu, sizeof( szBlu ), "%d", pBlu ? pBlu->GetScore() : 0 );

	ClientPrint( pPlayer, HUD_PRINTTALK, "#Greyline_Status_Roster",
		szPresent, szRoster, szRed, szBlu );
}

//-----------------------------------------------------------------------------
// Purpose: what this server thinks the battle is, for whoever is looking at a
//			disconnect box that says they are not on the roster.
//-----------------------------------------------------------------------------
void CGreylineBriefing::PrintRoster() const
{
	Msg( "[greyline] battle %s on front %s: %d on the roster, gate %s, enforce %s\n",
		greyline_battle_id.GetString()[0] ? greyline_battle_id.GetString() : "(none)",
		greyline_front_name.GetString()[0] ? greyline_front_name.GetString() : "(none)",
		m_Roster.Count(),
		greyline_roster_gate.GetBool() ? "on" : "off",
		greyline_roster_enforce.GetBool() ? "on" : "off" );

	for ( int i = 0; i < m_Roster.Count(); ++i )
	{
		const greyline::RosterEntry_t &e = m_Roster[i];
		Msg( "  %llu  team %d  war side %d%s\n",
			e.m_ulSteamID, e.m_iTeam, e.m_iWarSide, e.m_bContract ? "  (contract)" : "" );
	}

	// The other half of the answer: who is here that the roster does not know.
	for ( int i = 1; i <= gpGlobals->maxClients; ++i )
	{
		CBasePlayer *pPlayer = UTIL_PlayerByIndex( i );
		if ( !pPlayer || !pPlayer->IsConnected() || pPlayer->IsFakeClient() )
			continue;
		const uint64 ulSteamID = pPlayer->GetSteamIDAsUInt64();
		Msg( "  connected: %s  %llu  %s\n", pPlayer->GetPlayerName(), ulSteamID,
			m_Roster.Find( ulSteamID ) ? "on the roster" : "NOT on the roster" );
	}
}

//-----------------------------------------------------------------------------
void CGreylineBriefing::SeatPlayer( CBasePlayer *pPlayer )
{
	if ( !greyline_roster_enforce.GetBool() || !pPlayer || pPlayer->IsFakeClient() )
		return;

	const greyline::RosterEntry_t *pEntry = FindEntry( pPlayer );
	if ( !pEntry || pEntry->m_iTeam == TEAM_UNASSIGNED )
		return;
	if ( pPlayer->GetTeamNumber() == pEntry->m_iTeam )
		return;

	CTFPlayer *pTFPlayer = ToTFPlayer( pPlayer );
	if ( !pTFPlayer )
		return;

	pTFPlayer->ForceChangeTeam( pEntry->m_iTeam, false );
	// Straight to class selection: the team menu behind it has one legal
	// answer, and showing it anyway is how a player ends up believing they
	// picked something.
	pTFPlayer->ShowViewPortPanel(
		( pEntry->m_iTeam == TF_TEAM_RED ) ? PANEL_CLASS_RED : PANEL_CLASS_BLUE );

	if ( greyline_briefing_debug.GetBool() )
	{
		Msg( "[greyline] seated %s on team %d\n",
			pPlayer->GetPlayerName(), pEntry->m_iTeam );
	}
}

//-----------------------------------------------------------------------------
// What the stock TF code asks the war before it lets somebody play. See
// greyline_roster_gate.h for why this is three free functions.
//-----------------------------------------------------------------------------
namespace greyline
{

bool HandleClientCommand( CBasePlayer *pPlayer, const CCommand &args )
{
	if ( !pPlayer || args.ArgC() < 1 || !FStrEq( args[0], "greyline_briefing" ) )
		return false;

	if ( !g_GreylineBriefing.HasBattle() )
	{
		ClientPrint( pPlayer, HUD_PRINTTALK, "#Greyline_Status_NoBattle" );
		return true;
	}

	g_GreylineBriefing.AnnounceToPlayer( pPlayer, true );
	g_GreylineBriefing.PrintStatusTo( pPlayer );
	return true;
}

bool BattleHasRoster()
{
	return g_GreylineBriefing.RosterCount() > 0;
}

bool IsRosterMember( unsigned long long ulSteamID )
{
	return ulSteamID != 0 && g_GreylineBriefing.IsOnRoster( ulSteamID );
}

bool MayJoinBattle( unsigned long long ulSteamID, const char **ppszReason )
{
	if ( !greyline_roster_gate.GetBool() || !BattleHasRoster() )
		return true;

	// Steam has not identified this client yet. Refusing here would lock out
	// an offline or LAN client outright, and the roster still decides their
	// team once they are in — so this is deliberately a soft edge, and the
	// only one.
	if ( ulSteamID == 0 )
	{
		Warning( "[greyline] admitting a client Steam has not identified; the roster gate cannot judge it\n" );
		return true;
	}

	if ( g_GreylineBriefing.IsOnRoster( ulSteamID ) )
		return true;

	// Loud, because the only way this is ever wrong is that the roster on this
	// server is not the roster the coordinator thinks it sent — and a silent
	// refusal gives whoever is debugging it nothing but a disconnect box.
	Warning( "[greyline] refusing %llu: not among the %d players on this battle's roster. "
		"Run greyline_roster_status to see who is on it, or greyline_roster_gate 0 to let "
		"the battle password decide on its own.\n",
		ulSteamID, g_GreylineBriefing.RosterCount() );

	if ( ppszReason )
	{
		*ppszReason = "You are not on this battle's roster. Deploy from the GREYLINE menu to be sent to one.";
	}
	return false;
}

void RecomputeUniformSwap()
{
	// Published as a replicated convar because the client is what draws the
	// players and it cannot see the roster. See greyline_uniform.h.
	ConVarRef swap( "greyline_uniform_swap" );
	if ( swap.IsValid() )
	{
		swap.SetValue( g_GreylineBriefing.RosterIsColourSwapped() ? 1 : 0 );
	}
}

int RosterTeamFor( CBasePlayer *pPlayer )
{
	if ( !greyline_roster_enforce.GetBool() )
		return TEAM_UNASSIGNED;
	const greyline::RosterEntry_t *pEntry = g_GreylineBriefing.FindEntry( pPlayer );
	return pEntry ? pEntry->m_iTeam : TEAM_UNASSIGNED;
}

} // namespace greyline

//-----------------------------------------------------------------------------
// Console commands the agent drives over RCON.
//-----------------------------------------------------------------------------
CON_COMMAND_F( greyline_roster_clear, "Clear the Greyline battle roster.", FCVAR_GAMEDLL )
{
	g_GreylineBriefing.ClearRoster();
	if ( greyline_briefing_debug.GetBool() )
	{
		Msg( "[greyline] roster cleared\n" );
	}
}

CON_COMMAND_F( greyline_roster_add,
	"greyline_roster_add <steamid64> <game team RED|BLU> <war side RED|BLU> <contract 0|1> — "
	"add one player to the battle roster.",
	FCVAR_GAMEDLL )
{
	if ( args.ArgC() < 3 )
	{
		Msg( "usage: greyline_roster_add <steamid64> <RED|BLU> [war side] [contract]\n" );
		return;
	}

	const uint64 ulSteamID = V_atoui64( args[1] );
	const int iTeam = greyline::TeamFromName( args[2] );
	const int iWarSide = ( args.ArgC() > 3 ) ? greyline::TeamFromName( args[3] ) : iTeam;
	const bool bContract = ( args.ArgC() > 4 ) && ( V_atoi( args[4] ) != 0 );

	if ( !g_GreylineBriefing.AddToRoster( ulSteamID, iTeam, iWarSide, bContract ) )
	{
		Warning( "[greyline] refusing roster entry with steamid %s and team %s\n", args[1], args[2] );
		return;
	}

	if ( greyline_briefing_debug.GetBool() )
	{
		Msg( "[greyline] roster: %llu -> team %d, war side %d%s\n",
			ulSteamID, iTeam, iWarSide, bContract ? " (contract)" : "" );
	}
}

CON_COMMAND_F( greyline_roster_remove,
	"greyline_roster_remove <steamid64> — take one player off the battle roster. "
	"The coordinator sends this with a kick when a player is banned mid-battle: "
	"a kick on its own only sends them to the connect screen they came in through.",
	FCVAR_GAMEDLL )
{
	if ( args.ArgC() < 2 )
	{
		Msg( "usage: greyline_roster_remove <steamid64>\n" );
		return;
	}

	const uint64 ulSteamID = V_atoui64( args[1] );
	if ( !g_GreylineBriefing.RemoveFromRoster( ulSteamID ) )
	{
		// Not a warning: the coordinator sends this for a player who may
		// already have left, and that is the outcome it wanted anyway.
		if ( greyline_briefing_debug.GetBool() )
		{
			Msg( "[greyline] %llu was not on the roster\n", ulSteamID );
		}
		return;
	}

	Msg( "[greyline] %llu removed from the battle roster\n", ulSteamID );
}

CON_COMMAND_F( greyline_roster_status,
	"Print the battle roster this server is actually holding.",
	FCVAR_GAMEDLL )
{
	g_GreylineBriefing.PrintRoster();
}

CON_COMMAND_F( greyline_announce_briefing,
	"Announce the current battle's place in the war to everybody on the server.",
	FCVAR_GAMEDLL )
{
	g_GreylineBriefing.AnnounceToAll( true );
}
