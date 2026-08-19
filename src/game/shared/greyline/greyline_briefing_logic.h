//========= GREYLINE FRONTRESS ============================================//
//
// Purpose: the briefing's decisions, with no engine underneath them.
//
// Everything here is a pure function of what the coordinator said: which
// localization token names a stage, which team a mercenary belongs on, whether
// their uniform disagrees with the side they fight for, and therefore which
// lines the briefing is made of.
//
// It is a separate translation unit for one reason: it is the half worth
// testing, and it cannot be tested through the game. This file includes nothing
// from Source, so src/game/shared/greyline/tests builds it with a plain g++ on
// Linux and asserts the exact briefing a given battle produces. greyline_briefing.cpp
// keeps the half that genuinely needs the engine — players, convars, ClientPrint.
//
//=========================================================================//

#ifndef GREYLINE_BRIEFING_LOGIC_H
#define GREYLINE_BRIEFING_LOGIC_H
#ifdef _WIN32
#pragma once
#endif

namespace greyline
{

//-----------------------------------------------------------------------------
// Teams, with the engine's own numbering.
//
// These must equal TEAM_UNASSIGNED / TF_TEAM_RED / TF_TEAM_BLUE, which they
// cannot include from here. greyline_briefing.cpp static_asserts the equality,
// so a change to tf_shareddefs.h fails the build rather than the battle.
//-----------------------------------------------------------------------------
enum Team_t
{
	kTeamNone = 0,
	kTeamRed  = 2,
	kTeamBlu  = 3,
};

// How many mercenaries one battle's roster can hold. A Source server caps at 32
// slots; the spare room costs nothing and means a roster is never silently cut.
enum { kMaxRoster = 64 };

// The largest number of substitutions a TextMsg can carry. The engine's own
// limit (CBaseHudChat::MsgFunc_TextMsg reads exactly four), not ours.
enum { kMaxParams = 4 };

//-----------------------------------------------------------------------------
// Token lookups. Every one returns a localization token, never English: the
// server does not know what language the player reads.
//-----------------------------------------------------------------------------

// "RED"/"BLU"/"BLUE" -> kTeamRed/kTeamBlu. Anything else is kTeamNone, which is
// how a coordinator says "this map is symmetric, the sides are the teams".
int TeamFromName( const char *pszTeam );

// kTeamRed -> "#Greyline_Side_RED". Unknown teams name nobody rather than
// guessing, because a briefing that says the wrong side is worse than one that
// says none.
const char *SideTokenFromTeam( int iTeam );
const char *SideTokenFromName( const char *pszSide );

// An unrecognised stage falls back to a generic token. A raw English word
// leaking into a Russian client is the exact failure the tokens exist to stop.
const char *StageToken( const char *pszKind );

// The one line that says why this battle is being fought here. Breakthrough is
// the fallback: it is the stage every plan starts with.
const char *ReasonToken( const char *pszKind );

//-----------------------------------------------------------------------------
// The roster the coordinator assigned.
//-----------------------------------------------------------------------------
struct RosterEntry_t
{
	unsigned long long	m_ulSteamID;
	// The in-game team: the player's uniform, spawn, and half of the scoreboard.
	int					m_iTeam;
	// The side of the war they fight for. On a payload or attack/defend map the
	// two disagree, because those maps are built for BLU to attack.
	int					m_iWarSide;
	bool				m_bContract;	// fighting for a side that is not theirs
};

class CRoster
{
public:
	CRoster() : m_nCount( 0 ) {}

	void Clear() { m_nCount = 0; }
	int Count() const { return m_nCount; }
	const RosterEntry_t &operator[]( int i ) const { return m_Entries[i]; }

	// Adds or updates one mercenary. Returns false when the entry is unusable —
	// no SteamID or no team — because a roster entry the server cannot act on is
	// worse than an absent one: it looks like the player was assigned.
	//
	// A war side of kTeamNone means the map is symmetric, so the side and the
	// team are the same thing.
	bool Add( unsigned long long ulSteamID, int iTeam, int iWarSide, bool bContract );

	// NULL when this player is not in this battle, which is the ordinary case on
	// a server that is not hosting one.
	const RosterEntry_t *Find( unsigned long long ulSteamID ) const;

private:
	RosterEntry_t	m_Entries[kMaxRoster];
	int				m_nCount;
};

//-----------------------------------------------------------------------------
// What the agent set over RCON before the map loaded. Pointers are borrowed
// from the convars and must outlive the briefing built from them.
//-----------------------------------------------------------------------------
struct BattleContext_t
{
	const char *m_pszFrontName;
	const char *m_pszNodeName;
	const char *m_pszNodeID;		// used when the display name is missing
	const char *m_pszAttacker;
	const char *m_pszStageKind;
	const char *m_pszMobilized;		// empty when nobody is mobilized
	int			m_nStage;			// 1-based, as the coordinator sends it
	int			m_nStageCount;
	// True when the player bodies are being drawn in the war's colours rather
	// than the in-game team's — see greyline_uniform.h. It changes what the
	// briefing has to explain, not whether it explains anything: with it off
	// the player is confused by wearing the wrong colour, and with it on by a
	// scoreboard that names a team they do not look like. Telling them the
	// wrong one of those two is worse than telling them nothing.
	bool		m_bUniformsShowWarSide;

	BattleContext_t()
		: m_pszFrontName( "" ), m_pszNodeName( "" ), m_pszNodeID( "" ),
		  m_pszAttacker( "" ), m_pszStageKind( "" ), m_pszMobilized( "" ),
		  m_nStage( 0 ), m_nStageCount( 0 ), m_bUniformsShowWarSide( false ) {}

	// A server with no front name is not hosting a Greyline battle, and must say
	// nothing at all rather than briefing players on an empty war.
	bool HasBattle() const { return m_pszFrontName && m_pszFrontName[0] != '\0'; }
};

//-----------------------------------------------------------------------------
// One printed line: a destination, a token, and its substitutions.
//-----------------------------------------------------------------------------
enum LineDest_t
{
	kDestChat = 0,		// HUD_PRINTTALK
	kDestCenter,		// HUD_PRINTCENTER
};

struct BriefingLine_t
{
	int			m_iDest;
	const char *m_pszToken;
	const char *m_pszParams[kMaxParams];
	int			m_nParams;
};

enum { kMaxBriefingLines = 8 };

//-----------------------------------------------------------------------------
// The whole briefing, built in one place so it can be asserted in one place.
//
// It owns the storage its lines point into, so it must outlive them — which is
// why BuildBriefing fills a caller-owned Briefing_t rather than returning one.
//-----------------------------------------------------------------------------
struct Briefing_t
{
	BriefingLine_t	m_Lines[kMaxBriefingLines];
	int				m_nLines;

	char			m_szStage[12];
	char			m_szStageCount[12];

	Briefing_t() : m_nLines( 0 ) { m_szStage[0] = '\0'; m_szStageCount[0] = '\0'; }
};

// True when this player's uniform disagrees with the side they fight for, which
// is the one fact a player must be told or the game is calling them BLU while
// the war calls them RED.
bool ColoursDisagree( const RosterEntry_t *pEntry );

// True when this battle as a whole is being fought in swapped colours: the
// coordinator mapped the war's sides onto the opposite in-game teams, which is
// what a directional map forces whenever the side on the offensive is not the
// one the map lets attack.
//
// It is a property of the battle rather than of a player because the mapping
// is decided once, for everybody — which is exactly why it is safe to redraw
// the teams in the war's colours (see greyline_uniform.h) and would not be if
// it were per player.
bool RosterColoursSwapped( const CRoster &roster );

// Builds the briefing for one player. pEntry may be NULL — a spectator, a late
// joiner whose SteamID has not arrived yet, or somebody the coordinator never
// assigned — in which case the battle is still described, just not their part
// in it.
//
// bIncludeHUD adds the single centre-screen line, which belongs once per battle
// rather than once per round.
void BuildBriefing( const BattleContext_t &ctx, const RosterEntry_t *pEntry,
					bool bIncludeHUD, Briefing_t *pOut );

} // namespace greyline

#endif // GREYLINE_BRIEFING_LOGIC_H
