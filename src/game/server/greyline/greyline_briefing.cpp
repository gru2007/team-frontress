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
//=========================================================================//

#include "cbase.h"
#include "greyline/greyline_shared.h"
#include "igamesystem.h"
#include "GameEventListener.h"
#include "eiface.h"
#include "tf_player.h"
#include "tf_shareddefs.h"

// memdbgon must be the last include file in a .cpp file!!!
#include "tier0/memdbgon.h"

//-----------------------------------------------------------------------------
// The battle's war context, written by the agent over RCON before the map
// loads. All of it is descriptive: nothing here changes how the game plays.
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
ConVar greyline_roster_kick_strangers( "greyline_roster_kick_strangers", "0", FCVAR_GAMEDLL,
	"Kick players who are not on the battle roster. The battle password already "
	"keeps them out, so this is a second line rather than the first." );
ConVar greyline_briefing_debug( "greyline_briefing_debug", "0", FCVAR_GAMEDLL,
	"Spew Greyline briefing and roster decisions." );

//-----------------------------------------------------------------------------
// Roster
//-----------------------------------------------------------------------------
struct GreylineRosterEntry_t
{
	uint64	m_ulSteamID;
	// The in-game team, which is what the player will see: their uniform, their
	// spawn, their half of the scoreboard.
	int		m_iTeam;		// TF_TEAM_RED / TF_TEAM_BLUE
	// The side of the war they are fighting for. On a payload or attack/defend
	// map these two disagree: those maps are built for BLU to attack, so a RED
	// offensive is fought in BLU colours. The player has to be told, or the game
	// is calling them BLU while the war calls them RED.
	int		m_iWarSide;
	bool	m_bContract;	// fighting for the side that is not their allegiance
};

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
	}

	virtual void FrameUpdatePostEntityThink() OVERRIDE;
	virtual void FireGameEvent( IGameEvent *pEvent ) OVERRIDE;

	void ClearRoster() { m_Roster.RemoveAll(); }
	void AddToRoster( uint64 ulSteamID, int iTeam, int iWarSide, bool bContract );

	// Returns NULL when the player is not on the roster, which is the normal
	// case on a server that is not currently hosting a Greyline battle.
	const GreylineRosterEntry_t *FindEntry( CBasePlayer *pPlayer ) const;

	// Announces to everyone. bIncludeHUD adds the single centre-screen notice,
	// which is deliberately once per battle rather than once per round.
	void AnnounceToAll( bool bIncludeHUD );
	void AnnounceToPlayer( CBasePlayer *pPlayer, bool bIncludeHUD );

	bool HasBattle() const { return greyline_front_name.GetString()[0] != '\0'; }

private:
	void EnforceTeams();

	CUtlVector< GreylineRosterEntry_t >	m_Roster;
	float								m_flNextEnforce;
	bool								m_bAnnouncedThisLevel;
};

static CGreylineBriefing g_GreylineBriefing;

//-----------------------------------------------------------------------------
// Purpose: maps the coordinator's side names onto in-game teams. The two are
//			not always the same thing: on a payload or attack/defend map the
//			attacking side plays as BLU whichever side it is in the war, and the
//			coordinator has already resolved that into the team it sends here.
//-----------------------------------------------------------------------------
static int GreylineTeamFromString( const char *pszTeam )
{
	if ( !pszTeam )
		return TEAM_UNASSIGNED;
	if ( !V_stricmp( pszTeam, "RED" ) )
		return TF_TEAM_RED;
	if ( !V_stricmp( pszTeam, "BLU" ) || !V_stricmp( pszTeam, "BLUE" ) )
		return TF_TEAM_BLUE;
	return TEAM_UNASSIGNED;
}

//-----------------------------------------------------------------------------
// Purpose: localization token for a side, so the client says it in its own
//			language.
//-----------------------------------------------------------------------------
static const char *GreylineSideToken( const char *pszSide )
{
	if ( pszSide && !V_stricmp( pszSide, "RED" ) )
		return "#Greyline_Side_RED";
	if ( pszSide && ( !V_stricmp( pszSide, "BLU" ) || !V_stricmp( pszSide, "BLUE" ) ) )
		return "#Greyline_Side_BLU";
	return "#Greyline_Side_NONE";
}

//-----------------------------------------------------------------------------
// Purpose: localization token for a stage kind. An unknown kind falls back to a
//			generic token rather than printing a raw English word.
//-----------------------------------------------------------------------------
static const char *GreylineStageToken( const char *pszKind )
{
	if ( !pszKind || !pszKind[0] )
		return "#Greyline_Stage_unknown";
	if ( !V_stricmp( pszKind, "skirmish" ) )
		return "#Greyline_Stage_skirmish";
	if ( !V_stricmp( pszKind, "breakthrough" ) )
		return "#Greyline_Stage_breakthrough";
	if ( !V_stricmp( pszKind, "advance" ) )
		return "#Greyline_Stage_advance";
	if ( !V_stricmp( pszKind, "assault" ) )
		return "#Greyline_Stage_assault";
	return "#Greyline_Stage_unknown";
}

//-----------------------------------------------------------------------------
// Purpose: the one line that says why this battle is being fought here.
//-----------------------------------------------------------------------------
static const char *GreylineReasonToken( const char *pszKind )
{
	if ( pszKind && !V_stricmp( pszKind, "skirmish" ) )
		return "#Greyline_Reason_skirmish";
	if ( pszKind && !V_stricmp( pszKind, "advance" ) )
		return "#Greyline_Reason_advance";
	if ( pszKind && !V_stricmp( pszKind, "assault" ) )
		return "#Greyline_Reason_assault";
	return "#Greyline_Reason_breakthrough";
}

//-----------------------------------------------------------------------------
void CGreylineBriefing::AddToRoster( uint64 ulSteamID, int iTeam, int iWarSide, bool bContract )
{
	if ( iWarSide == TEAM_UNASSIGNED )
	{
		// A coordinator that did not state a side is running a symmetric map,
		// where the two are the same thing.
		iWarSide = iTeam;
	}

	FOR_EACH_VEC( m_Roster, i )
	{
		if ( m_Roster[i].m_ulSteamID == ulSteamID )
		{
			m_Roster[i].m_iTeam = iTeam;
			m_Roster[i].m_iWarSide = iWarSide;
			m_Roster[i].m_bContract = bContract;
			return;
		}
	}

	GreylineRosterEntry_t entry;
	entry.m_ulSteamID = ulSteamID;
	entry.m_iTeam = iTeam;
	entry.m_iWarSide = iWarSide;
	entry.m_bContract = bContract;
	m_Roster.AddToTail( entry );
}

//-----------------------------------------------------------------------------
const GreylineRosterEntry_t *CGreylineBriefing::FindEntry( CBasePlayer *pPlayer ) const
{
	if ( !pPlayer || m_Roster.Count() == 0 )
		return NULL;

	const uint64 ulSteamID = pPlayer->GetSteamIDAsUInt64();
	if ( ulSteamID == 0 )
		return NULL;

	FOR_EACH_VEC( m_Roster, i )
	{
		if ( m_Roster[i].m_ulSteamID == ulSteamID )
			return &m_Roster[i];
	}
	return NULL;
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

		const GreylineRosterEntry_t *pEntry = FindEntry( pPlayer );
		if ( !pEntry )
		{
			if ( greyline_roster_kick_strangers.GetBool() && !pPlayer->IsFakeClient() )
			{
				engine->ServerCommand( UTIL_VarArgs( "kickid %d %s\n",
					pPlayer->GetUserID(), "not on this battle's roster" ) );
			}
			continue;
		}

		if ( pEntry->m_iTeam == TEAM_UNASSIGNED || pPlayer->GetTeamNumber() == pEntry->m_iTeam )
			continue;

		if ( greyline_briefing_debug.GetBool() )
		{
			Msg( "[greyline] moving %s to team %d (coordinator assignment)\n",
				pPlayer->GetPlayerName(), pEntry->m_iTeam );
		}
		pPlayer->ForceChangeTeam( pEntry->m_iTeam, false );
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
		// same briefing everyone else got.
		CBasePlayer *pPlayer = UTIL_PlayerByUserId( pEvent->GetInt( "userid" ) );
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
// Purpose: the briefing itself. Every sentence is a localization token with
//			substitutions, so the client renders it in the player's language.
//-----------------------------------------------------------------------------
void CGreylineBriefing::AnnounceToPlayer( CBasePlayer *pPlayer, bool bIncludeHUD )
{
	if ( !pPlayer || pPlayer->IsFakeClient() )
		return;
	if ( !greyline_briefing_enabled.GetBool() || !HasBattle() )
		return;

	const char *pszFront = greyline_front_name.GetString();
	const char *pszNode = greyline_node_name.GetString();
	if ( !pszNode[0] )
	{
		pszNode = greyline_node_id.GetString();
	}

	char szStage[8];
	char szStageCount[8];
	V_snprintf( szStage, sizeof( szStage ), "%d", greyline_stage.GetInt() );
	V_snprintf( szStageCount, sizeof( szStageCount ), "%d", greyline_stage_count.GetInt() );

	const char *pszStageToken = GreylineStageToken( greyline_stage_kind.GetString() );
	const char *pszAttackerToken = GreylineSideToken( greyline_attacker.GetString() );
	const GreylineRosterEntry_t *pEntry = FindEntry( pPlayer );

	// Line 1: which offensive this is.
	ClientPrint( pPlayer, HUD_PRINTTALK, "#Greyline_Chat_Front", pszFront );

	// Line 2: where in that offensive we are, and who is pushing.
	ClientPrint( pPlayer, HUD_PRINTTALK, "#Greyline_Chat_Stage",
		pszStageToken, szStage, szStageCount, pszAttackerToken );

	// Line 3, the one that stops a player thinking the game is broken.
	//
	// Payload and attack/defend maps are built for BLU to attack, so a RED
	// offensive on one of them is fought in BLU uniforms: the spawn, the
	// scoreboard and the end-of-round banner will all say BLU while the war
	// says RED. Nothing can repaint the map, so the game says it out loud
	// instead. On a symmetric map the two agree and this line is skipped.
	if ( pEntry && pEntry->m_iWarSide != TEAM_UNASSIGNED && pEntry->m_iTeam != pEntry->m_iWarSide )
	{
		ClientPrint( pPlayer, HUD_PRINTTALK, "#Greyline_Chat_Colours",
			GreylineSideToken( pEntry->m_iWarSide == TF_TEAM_RED ? "RED" : "BLU" ),
			GreylineSideToken( pEntry->m_iTeam == TF_TEAM_RED ? "RED" : "BLU" ) );
	}

	// Line 4: why this battle exists, in one sentence.
	ClientPrint( pPlayer, HUD_PRINTTALK,
		GreylineReasonToken( greyline_stage_kind.GetString() ), pszNode );

	// Line 5, only when it applies: this player is here on a contract for the
	// other side, which is worth saying out loud before they see the scoreboard.
	// The side named is the one they fight for in the war, not the colours they
	// happen to be wearing.
	if ( pEntry && pEntry->m_bContract )
	{
		ClientPrint( pPlayer, HUD_PRINTTALK, "#Greyline_Chat_Contract",
			GreylineSideToken( pEntry->m_iWarSide == TF_TEAM_RED ? "RED" : "BLU" ) );
	}

	// Line 6, only when it applies: the war is propping up the side that is
	// losing, and players should know why this defence is easier than usual.
	if ( greyline_mobilized.GetString()[0] )
	{
		ClientPrint( pPlayer, HUD_PRINTTALK, "#Greyline_Chat_Mobilized",
			GreylineSideToken( greyline_mobilized.GetString() ) );
	}

	if ( bIncludeHUD )
	{
		// One centre message, never two: a second would simply overwrite the
		// first. When the colours disagree with the war that fact replaces the
		// stage counter, because it is the more urgent of the two.
		if ( pEntry && pEntry->m_iWarSide != TEAM_UNASSIGNED && pEntry->m_iTeam != pEntry->m_iWarSide )
		{
			ClientPrint( pPlayer, HUD_PRINTCENTER, "#Greyline_Hud_Colours",
				pszFront, pszStageToken,
				GreylineSideToken( pEntry->m_iWarSide == TF_TEAM_RED ? "RED" : "BLU" ),
				GreylineSideToken( pEntry->m_iTeam == TF_TEAM_RED ? "RED" : "BLU" ) );
		}
		else
		{
			ClientPrint( pPlayer, HUD_PRINTCENTER, "#Greyline_Hud_Briefing",
				pszFront, pszStageToken, szStage, szStageCount );
		}
	}
}

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
	const int iTeam = GreylineTeamFromString( args[2] );
	const int iWarSide = ( args.ArgC() > 3 ) ? GreylineTeamFromString( args[3] ) : iTeam;
	const bool bContract = ( args.ArgC() > 4 ) && ( V_atoi( args[4] ) != 0 );

	if ( ulSteamID == 0 || iTeam == TEAM_UNASSIGNED )
	{
		Warning( "[greyline] refusing roster entry with steamid %s and team %s\n", args[1], args[2] );
		return;
	}

	g_GreylineBriefing.AddToRoster( ulSteamID, iTeam, iWarSide, bContract );
	if ( greyline_briefing_debug.GetBool() )
	{
		Msg( "[greyline] roster: %llu -> team %d, war side %d%s\n",
			ulSteamID, iTeam, iWarSide, bContract ? " (contract)" : "" );
	}
}

CON_COMMAND_F( greyline_announce_briefing,
	"Announce the current battle's place in the war to everybody on the server.",
	FCVAR_GAMEDLL )
{
	g_GreylineBriefing.AnnounceToAll( true );
}
