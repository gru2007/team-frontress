//========= GREYLINE FRONTRESS ============================================//
//
// Purpose: what the briefing says, asserted without a game.
//
// These cover the decisions a player actually notices when they are wrong: the
// team the coordinator put them on, whether they are told their uniform
// disagrees with their side, and the exact set of lines a given battle produces.
//
//=========================================================================//

#include "greyline_test.h"
#include "../greyline_briefing_logic.h"

#include <string>
#include <vector>

using namespace greyline;

namespace
{

// A battle the way the agent would have set it up: RED pushing the second of
// three stages into Foundry 17, on a payload map, so RED wears BLU.
BattleContext_t AdvanceAtFoundry()
{
	BattleContext_t ctx;
	ctx.m_pszFrontName = "BATTLE FOR FOUNDRY 17";
	ctx.m_pszNodeName = "Foundry 17";
	ctx.m_pszNodeID = "foundry_17";
	ctx.m_pszAttacker = "RED";
	ctx.m_pszStageKind = "advance";
	ctx.m_nStage = 2;
	ctx.m_nStageCount = 3;
	return ctx;
}

RosterEntry_t Entry( int iTeam, int iWarSide, bool bContract = false )
{
	RosterEntry_t e;
	e.m_ulSteamID = 76561198000000001ull;
	e.m_iTeam = iTeam;
	e.m_iWarSide = iWarSide;
	e.m_bContract = bContract;
	return e;
}

// The tokens of a briefing, in order, so a test can state the whole thing.
std::vector<std::string> Tokens( const Briefing_t &b )
{
	std::vector<std::string> out;
	for ( int i = 0; i < b.m_nLines; ++i )
		out.push_back( b.m_Lines[i].m_pszToken );
	return out;
}

bool HasToken( const Briefing_t &b, const char *pszToken )
{
	for ( int i = 0; i < b.m_nLines; ++i )
	{
		if ( std::string( b.m_Lines[i].m_pszToken ) == pszToken )
			return true;
	}
	return false;
}

const BriefingLine_t *LineWith( const Briefing_t &b, const char *pszToken )
{
	for ( int i = 0; i < b.m_nLines; ++i )
	{
		if ( std::string( b.m_Lines[i].m_pszToken ) == pszToken )
			return &b.m_Lines[i];
	}
	return 0;
}

std::string Joined( const std::vector<std::string> &v )
{
	std::string out;
	for ( size_t i = 0; i < v.size(); ++i )
	{
		if ( i )
			out += ", ";
		out += v[i];
	}
	return out;
}

} // namespace

// ---------------------------------------------------------------------------
// Sides and teams
// ---------------------------------------------------------------------------

GREYLINE_TEST( TeamFromName_accepts_what_the_coordinator_sends )
{
	CHECK_EQ( TeamFromName( "RED" ), (int)kTeamRed );
	CHECK_EQ( TeamFromName( "BLU" ), (int)kTeamBlu );
	// The coordinator says BLU; a human typing the roster command says BLUE.
	CHECK_EQ( TeamFromName( "BLUE" ), (int)kTeamBlu );
	CHECK_EQ( TeamFromName( "red" ), (int)kTeamRed );
	CHECK_EQ( TeamFromName( "bLu" ), (int)kTeamBlu );
}

GREYLINE_TEST( TeamFromName_refuses_everything_else )
{
	// Not a team: these must not resolve to one, or a typo in the agent would
	// silently put a whole roster on RED.
	CHECK_EQ( TeamFromName( "" ), (int)kTeamNone );
	CHECK_EQ( TeamFromName( 0 ), (int)kTeamNone );
	CHECK_EQ( TeamFromName( "SPECTATOR" ), (int)kTeamNone );
	CHECK_EQ( TeamFromName( "R" ), (int)kTeamNone );
	CHECK_EQ( TeamFromName( "REDD" ), (int)kTeamNone );
	CHECK_EQ( TeamFromName( "2" ), (int)kTeamNone );
}

GREYLINE_TEST( SideTokens_never_guess )
{
	CHECK_EQ( SideTokenFromTeam( kTeamRed ), "#Greyline_Side_RED" );
	CHECK_EQ( SideTokenFromTeam( kTeamBlu ), "#Greyline_Side_BLU" );
	CHECK_EQ( SideTokenFromTeam( kTeamNone ), "#Greyline_Side_NONE" );
	CHECK_EQ( SideTokenFromTeam( 99 ), "#Greyline_Side_NONE" );
	CHECK_EQ( SideTokenFromName( "BLUE" ), "#Greyline_Side_BLU" );
	CHECK_EQ( SideTokenFromName( "nonsense" ), "#Greyline_Side_NONE" );
}

// ---------------------------------------------------------------------------
// Stage and reason tokens
// ---------------------------------------------------------------------------

GREYLINE_TEST( StageToken_covers_every_stage_the_war_engine_can_produce )
{
	// These four strings are the war engine's StageKind values. If a new one is
	// added there and not here, players see "BATTLE" instead of its name.
	CHECK_EQ( StageToken( "skirmish" ), "#Greyline_Stage_skirmish" );
	CHECK_EQ( StageToken( "breakthrough" ), "#Greyline_Stage_breakthrough" );
	CHECK_EQ( StageToken( "advance" ), "#Greyline_Stage_advance" );
	CHECK_EQ( StageToken( "assault" ), "#Greyline_Stage_assault" );
}

GREYLINE_TEST( StageToken_falls_back_rather_than_leaking_english )
{
	CHECK_EQ( StageToken( "" ), "#Greyline_Stage_unknown" );
	CHECK_EQ( StageToken( 0 ), "#Greyline_Stage_unknown" );
	CHECK_EQ( StageToken( "encirclement" ), "#Greyline_Stage_unknown" );
}

GREYLINE_TEST( ReasonToken_defaults_to_breakthrough )
{
	CHECK_EQ( ReasonToken( "skirmish" ), "#Greyline_Reason_skirmish" );
	CHECK_EQ( ReasonToken( "advance" ), "#Greyline_Reason_advance" );
	CHECK_EQ( ReasonToken( "assault" ), "#Greyline_Reason_assault" );
	CHECK_EQ( ReasonToken( "breakthrough" ), "#Greyline_Reason_breakthrough" );
	// Every plan starts with a breakthrough, so it is the honest fallback.
	CHECK_EQ( ReasonToken( "" ), "#Greyline_Reason_breakthrough" );
	CHECK_EQ( ReasonToken( 0 ), "#Greyline_Reason_breakthrough" );
}

// ---------------------------------------------------------------------------
// The roster
// ---------------------------------------------------------------------------

GREYLINE_TEST( Roster_keeps_what_the_coordinator_assigned )
{
	CRoster roster;
	CHECK( roster.Add( 111, kTeamRed, kTeamRed, false ) );
	CHECK( roster.Add( 222, kTeamBlu, kTeamRed, true ) );
	CHECK_EQ( roster.Count(), 2 );

	const RosterEntry_t *pA = roster.Find( 111 );
	CHECK( pA != 0 );
	CHECK_EQ( pA->m_iTeam, (int)kTeamRed );
	CHECK_EQ( pA->m_bContract, false );

	const RosterEntry_t *pB = roster.Find( 222 );
	CHECK( pB != 0 );
	CHECK_EQ( pB->m_iTeam, (int)kTeamBlu );
	CHECK_EQ( pB->m_iWarSide, (int)kTeamRed );
	CHECK_EQ( pB->m_bContract, true );
}

GREYLINE_TEST( Roster_reassigns_rather_than_duplicating )
{
	// The agent re-sends a roster when a battle is reassigned. The second entry
	// must replace the first, or enforcement flips between two answers.
	CRoster roster;
	roster.Add( 111, kTeamRed, kTeamRed, false );
	roster.Add( 111, kTeamBlu, kTeamRed, true );

	CHECK_EQ( roster.Count(), 1 );
	CHECK_EQ( roster.Find( 111 )->m_iTeam, (int)kTeamBlu );
	CHECK_EQ( roster.Find( 111 )->m_bContract, true );
}

GREYLINE_TEST( Roster_refuses_entries_the_server_cannot_act_on )
{
	CRoster roster;
	// No SteamID: nothing to match a player against.
	CHECK_EQ( roster.Add( 0, kTeamRed, kTeamRed, false ), false );
	// No team: an entry that exists but assigns nobody looks like an assignment.
	CHECK_EQ( roster.Add( 111, kTeamNone, kTeamRed, false ), false );
	CHECK_EQ( roster.Add( 111, 42, kTeamRed, false ), false );
	CHECK_EQ( roster.Count(), 0 );
}

GREYLINE_TEST( Roster_treats_a_missing_war_side_as_a_symmetric_map )
{
	// koth and 5cp have no attacking team, so the coordinator sends no war side
	// and the uniform is the side.
	CRoster roster;
	CHECK( roster.Add( 111, kTeamBlu, kTeamNone, false ) );
	CHECK_EQ( roster.Find( 111 )->m_iWarSide, (int)kTeamBlu );
	CHECK_EQ( ColoursDisagree( roster.Find( 111 ) ), false );
}

GREYLINE_TEST( Roster_find_misses_are_not_matches )
{
	CRoster roster;
	roster.Add( 111, kTeamRed, kTeamRed, false );
	CHECK( roster.Find( 222 ) == 0 );
	// A player whose SteamID has not arrived yet must never match an entry.
	CHECK( roster.Find( 0 ) == 0 );
}

GREYLINE_TEST( Roster_survives_more_players_than_a_server_has_slots )
{
	CRoster roster;
	for ( int i = 0; i < kMaxRoster + 10; ++i )
		roster.Add( 1000ull + i, kTeamRed, kTeamRed, false );

	CHECK_EQ( roster.Count(), (int)kMaxRoster );
	// The ones that fit are still correct, and nothing wrote past the end.
	CHECK( roster.Find( 1000 ) != 0 );
	CHECK( roster.Find( 1000ull + kMaxRoster - 1 ) != 0 );
	CHECK( roster.Find( 1000ull + kMaxRoster ) == 0 );
}

GREYLINE_TEST( Roster_remove_takes_one_player_off )
{
	CRoster roster;
	roster.Add( 111, kTeamRed, kTeamRed, false );
	roster.Add( 222, kTeamBlu, kTeamBlu, false );
	roster.Add( 333, kTeamRed, kTeamRed, false );

	CHECK( roster.Remove( 222 ) );
	CHECK_EQ( roster.Count(), 2 );
	CHECK( roster.Find( 222 ) == 0 );
	// The survivors are untouched: the hole is filled by the last entry, so a
	// removal in the middle must not lose the tail.
	CHECK( roster.Find( 111 ) != 0 );
	CHECK( roster.Find( 333 ) != 0 );
}

GREYLINE_TEST( Roster_remove_misses_report_nothing_was_removed )
{
	CRoster roster;
	roster.Add( 111, kTeamRed, kTeamRed, false );

	CHECK( !roster.Remove( 222 ) );
	CHECK( !roster.Remove( 0 ) );
	CHECK_EQ( roster.Count(), 1 );

	// A player removed twice — a coordinator retrying a kick — is not an error
	// the second time, just a miss.
	CHECK( roster.Remove( 111 ) );
	CHECK( !roster.Remove( 111 ) );
	CHECK_EQ( roster.Count(), 0 );
}

GREYLINE_TEST( Roster_can_be_refilled_after_a_removal )
{
	CRoster roster;
	for ( int i = 0; i < (int)kMaxRoster; ++i )
		roster.Add( 1000ull + i, kTeamRed, kTeamRed, false );

	CHECK( roster.Remove( 1000 ) );
	// The slot the removal freed is usable again, rather than the roster being
	// permanently one short for the rest of the battle.
	CHECK( roster.Add( 9999, kTeamBlu, kTeamBlu, false ) );
	CHECK_EQ( roster.Count(), (int)kMaxRoster );
	CHECK( roster.Find( 9999 ) != 0 );
}

GREYLINE_TEST( Roster_clear_empties_it )
{
	CRoster roster;
	roster.Add( 111, kTeamRed, kTeamRed, false );
	roster.Clear();
	CHECK_EQ( roster.Count(), 0 );
	CHECK( roster.Find( 111 ) == 0 );
}

// ---------------------------------------------------------------------------
// Colours
// ---------------------------------------------------------------------------

GREYLINE_TEST( ColoursDisagree_only_when_the_player_would_be_confused )
{
	// RED fighting in BLU colours on a payload map: the one case worth a line.
	RosterEntry_t redInBlu = Entry( kTeamBlu, kTeamRed );
	CHECK_EQ( ColoursDisagree( &redInBlu ), true );

	RosterEntry_t bluInBlu = Entry( kTeamBlu, kTeamBlu );
	CHECK_EQ( ColoursDisagree( &bluInBlu ), false );

	RosterEntry_t redInRed = Entry( kTeamRed, kTeamRed );
	CHECK_EQ( ColoursDisagree( &redInRed ), false );

	// Unknown either way is not a disagreement, it is an absence.
	RosterEntry_t noSide = Entry( kTeamBlu, kTeamNone );
	CHECK_EQ( ColoursDisagree( &noSide ), false );
	CHECK_EQ( ColoursDisagree( 0 ), false );
}

// ---------------------------------------------------------------------------
// The briefing
// ---------------------------------------------------------------------------

GREYLINE_TEST( Briefing_says_nothing_when_no_battle_is_running )
{
	// An ordinary server that never got an assignment must stay quiet.
	BattleContext_t ctx;
	Briefing_t b;
	BuildBriefing( ctx, 0, true, &b );
	CHECK_EQ( b.m_nLines, 0 );
}

GREYLINE_TEST( Briefing_on_a_symmetric_map_is_front_stage_reason_and_the_hud )
{
	BattleContext_t ctx = AdvanceAtFoundry();
	ctx.m_pszStageKind = "breakthrough";
	RosterEntry_t entry = Entry( kTeamRed, kTeamRed );

	Briefing_t b;
	BuildBriefing( ctx, &entry, true, &b );

	std::vector<std::string> want;
	want.push_back( "#Greyline_Chat_Front" );
	want.push_back( "#Greyline_Chat_Stage" );
	want.push_back( "#Greyline_Chat_TeamIsAssigned" );
	want.push_back( "#Greyline_Reason_breakthrough" );
	want.push_back( "#Greyline_Hud_Briefing" );
	CHECK_EQ( Joined( Tokens( b ) ), Joined( want ) );

	// No colours warning: the fixed-assignment line is enough when both agree.
	CHECK_EQ( HasToken( b, "#Greyline_Chat_Colours" ), false );
}

GREYLINE_TEST( Briefing_explains_the_uniform_when_red_attacks_in_blu )
{
	BattleContext_t ctx = AdvanceAtFoundry();
	RosterEntry_t entry = Entry( kTeamBlu, kTeamRed );

	Briefing_t b;
	BuildBriefing( ctx, &entry, true, &b );

	CHECK( HasToken( b, "#Greyline_Chat_Colours" ) );

	const BriefingLine_t *pLine = LineWith( b, "#Greyline_Chat_Colours" );
	CHECK( pLine != 0 );
	// Order matters: first the side they fight for, then the colours they wear.
	// Swapped, the line tells them the exact opposite of the truth.
	CHECK_EQ( pLine->m_pszParams[0], "#Greyline_Side_RED" );
	CHECK_EQ( pLine->m_pszParams[1], "#Greyline_Side_BLU" );
	CHECK_EQ( pLine->m_nParams, 2 );
}

GREYLINE_TEST( Briefing_hud_line_prefers_the_colours_over_the_stage_counter )
{
	BattleContext_t ctx = AdvanceAtFoundry();
	RosterEntry_t entry = Entry( kTeamBlu, kTeamRed );

	Briefing_t b;
	BuildBriefing( ctx, &entry, true, &b );

	// One centre message, never two: the second would only overwrite the first.
	CHECK( HasToken( b, "#Greyline_Hud_Colours" ) );
	CHECK_EQ( HasToken( b, "#Greyline_Hud_Briefing" ), false );

	int nCentre = 0;
	for ( int i = 0; i < b.m_nLines; ++i )
	{
		if ( b.m_Lines[i].m_iDest == kDestCenter )
			++nCentre;
	}
	CHECK_EQ( nCentre, 1 );
}

GREYLINE_TEST( Briefing_without_the_hud_prints_only_chat )
{
	BattleContext_t ctx = AdvanceAtFoundry();
	RosterEntry_t entry = Entry( kTeamBlu, kTeamRed );

	Briefing_t b;
	BuildBriefing( ctx, &entry, false, &b );

	for ( int i = 0; i < b.m_nLines; ++i )
		CHECK_EQ( b.m_Lines[i].m_iDest, (int)kDestChat );
}

GREYLINE_TEST( Briefing_stage_line_carries_the_numbers_the_coordinator_sent )
{
	BattleContext_t ctx = AdvanceAtFoundry();
	Briefing_t b;
	BuildBriefing( ctx, 0, false, &b );

	const BriefingLine_t *pLine = LineWith( b, "#Greyline_Chat_Stage" );
	CHECK( pLine != 0 );
	CHECK_EQ( pLine->m_pszParams[0], "#Greyline_Stage_advance" );
	// The coordinator sends 1-based stages (plan.Stage + 1), so this reads
	// "stage 2 of 3" and never "stage 0 of 3".
	CHECK_EQ( pLine->m_pszParams[1], "2" );
	CHECK_EQ( pLine->m_pszParams[2], "3" );
	CHECK_EQ( pLine->m_pszParams[3], "#Greyline_Side_RED" );
	CHECK_EQ( pLine->m_nParams, 4 );
}

GREYLINE_TEST( Briefing_falls_back_to_the_node_id_when_it_has_no_name )
{
	BattleContext_t ctx = AdvanceAtFoundry();
	ctx.m_pszNodeName = "";

	Briefing_t b;
	BuildBriefing( ctx, 0, false, &b );

	const BriefingLine_t *pLine = LineWith( b, "#Greyline_Reason_advance" );
	CHECK( pLine != 0 );
	CHECK_EQ( pLine->m_pszParams[0], "foundry_17" );
}

GREYLINE_TEST( Briefing_mentions_a_contract_only_for_the_player_under_one )
{
	BattleContext_t ctx = AdvanceAtFoundry();

	RosterEntry_t hired = Entry( kTeamBlu, kTeamRed, true );
	Briefing_t b;
	BuildBriefing( ctx, &hired, false, &b );
	const BriefingLine_t *pLine = LineWith( b, "#Greyline_Chat_Contract" );
	CHECK( pLine != 0 );
	// The side named is the one they fight for, not the colours they wear.
	CHECK_EQ( pLine->m_pszParams[0], "#Greyline_Side_RED" );

	RosterEntry_t regular = Entry( kTeamBlu, kTeamRed, false );
	Briefing_t b2;
	BuildBriefing( ctx, &regular, false, &b2 );
	CHECK_EQ( HasToken( b2, "#Greyline_Chat_Contract" ), false );
}

GREYLINE_TEST( Briefing_mentions_mobilization_when_the_war_is_propping_a_side_up )
{
	BattleContext_t ctx = AdvanceAtFoundry();
	ctx.m_pszMobilized = "BLU";

	Briefing_t b;
	BuildBriefing( ctx, 0, false, &b );
	const BriefingLine_t *pLine = LineWith( b, "#Greyline_Chat_Mobilized" );
	CHECK( pLine != 0 );
	CHECK_EQ( pLine->m_pszParams[0], "#Greyline_Side_BLU" );

	ctx.m_pszMobilized = "";
	Briefing_t b2;
	BuildBriefing( ctx, 0, false, &b2 );
	CHECK_EQ( HasToken( b2, "#Greyline_Chat_Mobilized" ), false );
}

GREYLINE_TEST( Briefing_still_describes_the_battle_for_a_player_it_does_not_know )
{
	// A spectator, or somebody whose SteamID has not arrived yet. They get the
	// war context; only the lines about their own part are missing.
	BattleContext_t ctx = AdvanceAtFoundry();
	Briefing_t b;
	BuildBriefing( ctx, 0, true, &b );

	CHECK( HasToken( b, "#Greyline_Chat_Front" ) );
	CHECK( HasToken( b, "#Greyline_Chat_Stage" ) );
	CHECK( HasToken( b, "#Greyline_Hud_Briefing" ) );
	CHECK_EQ( HasToken( b, "#Greyline_Chat_Colours" ), false );
	CHECK_EQ( HasToken( b, "#Greyline_Chat_Contract" ), false );
}

GREYLINE_TEST( Briefing_never_exceeds_what_it_can_hold )
{
	// Every optional line at once: contract, colours and mobilization.
	BattleContext_t ctx = AdvanceAtFoundry();
	ctx.m_pszMobilized = "BLU";
	RosterEntry_t entry = Entry( kTeamBlu, kTeamRed, true );

	Briefing_t b;
	BuildBriefing( ctx, &entry, true, &b );

	// front, stage, assignment, colours, reason, contract, mobilization, and the
	// one HUD line — the most a player can ever be told at once.
	CHECK( b.m_nLines <= (int)kMaxBriefingLines );
	CHECK_EQ( b.m_nLines, 8 );
	for ( int i = 0; i < b.m_nLines; ++i )
	{
		CHECK( b.m_Lines[i].m_nParams <= (int)kMaxParams );
		CHECK( b.m_Lines[i].m_pszToken != 0 );
		// Unused parameters are empty strings, never null: ClientPrint writes
		// all four either way, and a null would be a crash rather than a blank.
		for ( int p = 0; p < (int)kMaxParams; ++p )
			CHECK( b.m_Lines[i].m_pszParams[p] != 0 );
	}
}

GREYLINE_TEST( Briefing_survives_a_coordinator_that_sends_rubbish )
{
	// Nothing here should be possible, but a briefing that crashes the server
	// is worse than one that reads oddly.
	BattleContext_t ctx;
	ctx.m_pszFrontName = "BATTLE FOR NOWHERE";
	ctx.m_pszNodeName = "";
	ctx.m_pszNodeID = "";
	ctx.m_pszAttacker = "";
	ctx.m_pszStageKind = "";
	ctx.m_pszMobilized = "";
	ctx.m_nStage = -1;
	ctx.m_nStageCount = 0;

	Briefing_t b;
	BuildBriefing( ctx, 0, true, &b );

	CHECK( b.m_nLines > 0 );
	CHECK( HasToken( b, "#Greyline_Chat_Stage" ) );
	CHECK( HasToken( b, "#Greyline_Reason_breakthrough" ) );
	CHECK_EQ( b.m_szStage, "-1" );
	CHECK_EQ( b.m_szStageCount, "0" );

	const BriefingLine_t *pStage = LineWith( b, "#Greyline_Chat_Stage" );
	CHECK_EQ( pStage->m_pszParams[0], "#Greyline_Stage_unknown" );
	CHECK_EQ( pStage->m_pszParams[3], "#Greyline_Side_NONE" );
}

GREYLINE_TEST( Briefing_renders_large_stage_numbers )
{
	BattleContext_t ctx = AdvanceAtFoundry();
	ctx.m_nStage = 12;
	ctx.m_nStageCount = 100;

	Briefing_t b;
	BuildBriefing( ctx, 0, false, &b );
	CHECK_EQ( b.m_szStage, "12" );
	CHECK_EQ( b.m_szStageCount, "100" );
}

// ---------------------------------------------------------------------------
// Which battles are fought in swapped colours. This is what decides whether
// the player bodies are redrawn in the war's colours — see greyline_uniform.h
// — so it has to be right about the battles that are swapped and, more
// importantly, about the ones that are not.
// ---------------------------------------------------------------------------

GREYLINE_TEST( A_directional_map_with_RED_attacking_is_a_swapped_battle )
{
	// The attacking side always wears BLU, so a RED offensive puts every
	// RED-allegiance player in BLU and every BLU one in RED.
	CRoster roster;
	CHECK( roster.Add( 111, kTeamBlu, kTeamRed, false ) );	// RED side, BLU uniform
	CHECK( roster.Add( 222, kTeamRed, kTeamBlu, false ) );	// BLU side, RED uniform
	CHECK_EQ( RosterColoursSwapped( roster ), true );
}

GREYLINE_TEST( A_directional_map_with_BLU_attacking_is_not_swapped )
{
	// Nothing to put back: the colours already say what the war says.
	CRoster roster;
	CHECK( roster.Add( 111, kTeamBlu, kTeamBlu, false ) );
	CHECK( roster.Add( 222, kTeamRed, kTeamRed, false ) );
	CHECK_EQ( RosterColoursSwapped( roster ), false );
}

GREYLINE_TEST( A_symmetric_map_is_never_a_swapped_battle )
{
	// koth and 5cp send no war side at all; the uniform is the side.
	CRoster roster;
	CHECK( roster.Add( 111, kTeamRed, kTeamNone, false ) );
	CHECK( roster.Add( 222, kTeamBlu, kTeamNone, false ) );
	CHECK_EQ( RosterColoursSwapped( roster ), false );
}

GREYLINE_TEST( A_contract_alone_does_not_make_a_battle_swapped )
{
	// A mercenary under contract fights for the other side this battle — that
	// is a fact about one player's allegiance, not about the colours the
	// battle is being fought in, and redrawing everybody over it would be
	// wrong.
	CRoster roster;
	CHECK( roster.Add( 111, kTeamRed, kTeamRed, true ) );
	CHECK( roster.Add( 222, kTeamBlu, kTeamBlu, true ) );
	CHECK_EQ( RosterColoursSwapped( roster ), false );
}

GREYLINE_TEST( An_empty_roster_is_not_a_swapped_battle )
{
	// A server between battles must not redraw anybody.
	CRoster roster;
	CHECK_EQ( RosterColoursSwapped( roster ), false );
}

int main()
{
	return greylinetest::RunAll( "greyline_logic" );
}
