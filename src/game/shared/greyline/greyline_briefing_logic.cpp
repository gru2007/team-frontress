//========= GREYLINE FRONTRESS ============================================//
//
// Purpose: the briefing's decisions. See greyline_briefing_logic.h.
//
// Nothing from Source is included on purpose — not even tier1 — so this builds
// under the test harness as well as under the game. That rules out V_stricmp
// and CUtlVector, which is why the small equivalents below exist.
//
//=========================================================================//

#include "greyline_briefing_logic.h"

namespace greyline
{

//-----------------------------------------------------------------------------
// tier1 without tier1. Both handle NULL, because every string here arrives from
// a convar the agent may never have set.
//-----------------------------------------------------------------------------
static inline char LowerASCII( char c )
{
	return ( c >= 'A' && c <= 'Z' ) ? (char)( c - 'A' + 'a' ) : c;
}

static bool EqualsNoCase( const char *a, const char *b )
{
	if ( !a || !b )
		return false;
	while ( *a && *b )
	{
		if ( LowerASCII( *a ) != LowerASCII( *b ) )
			return false;
		++a;
		++b;
	}
	return *a == *b;
}

static bool IsEmpty( const char *s ) { return !s || s[0] == '\0'; }

// Writes a non-negative int as decimal. Negative values would mean the
// coordinator sent a nonsense stage, and printing "-1 of 3" tells the player
// more than silently clamping would.
static void IntToString( int value, char *pOut, int nOutLen )
{
	if ( nOutLen <= 0 )
		return;

	char szTmp[12];
	int nTmp = 0;
	bool bNegative = value < 0;
	unsigned int uValue = bNegative ? (unsigned int)( -(long long)value ) : (unsigned int)value;

	do
	{
		szTmp[nTmp++] = (char)( '0' + ( uValue % 10 ) );
		uValue /= 10;
	} while ( uValue != 0 && nTmp < (int)sizeof( szTmp ) );

	int nPos = 0;
	if ( bNegative && nPos < nOutLen - 1 )
		pOut[nPos++] = '-';
	while ( nTmp > 0 && nPos < nOutLen - 1 )
		pOut[nPos++] = szTmp[--nTmp];
	pOut[nPos] = '\0';
}

//-----------------------------------------------------------------------------
// Tokens
//-----------------------------------------------------------------------------
int TeamFromName( const char *pszTeam )
{
	if ( EqualsNoCase( pszTeam, "RED" ) )
		return kTeamRed;
	if ( EqualsNoCase( pszTeam, "BLU" ) || EqualsNoCase( pszTeam, "BLUE" ) )
		return kTeamBlu;
	return kTeamNone;
}

const char *SideTokenFromTeam( int iTeam )
{
	if ( iTeam == kTeamRed )
		return "#Greyline_Side_RED";
	if ( iTeam == kTeamBlu )
		return "#Greyline_Side_BLU";
	return "#Greyline_Side_NONE";
}

const char *SideTokenFromName( const char *pszSide )
{
	return SideTokenFromTeam( TeamFromName( pszSide ) );
}

const char *StageToken( const char *pszKind )
{
	if ( EqualsNoCase( pszKind, "skirmish" ) )
		return "#Greyline_Stage_skirmish";
	if ( EqualsNoCase( pszKind, "breakthrough" ) )
		return "#Greyline_Stage_breakthrough";
	if ( EqualsNoCase( pszKind, "advance" ) )
		return "#Greyline_Stage_advance";
	if ( EqualsNoCase( pszKind, "assault" ) )
		return "#Greyline_Stage_assault";
	return "#Greyline_Stage_unknown";
}

const char *ReasonToken( const char *pszKind )
{
	if ( EqualsNoCase( pszKind, "skirmish" ) )
		return "#Greyline_Reason_skirmish";
	if ( EqualsNoCase( pszKind, "advance" ) )
		return "#Greyline_Reason_advance";
	if ( EqualsNoCase( pszKind, "assault" ) )
		return "#Greyline_Reason_assault";
	return "#Greyline_Reason_breakthrough";
}

//-----------------------------------------------------------------------------
// Roster
//-----------------------------------------------------------------------------
bool CRoster::Add( unsigned long long ulSteamID, int iTeam, int iWarSide, bool bContract )
{
	if ( ulSteamID == 0 )
		return false;
	if ( iTeam != kTeamRed && iTeam != kTeamBlu )
		return false;
	if ( iWarSide != kTeamRed && iWarSide != kTeamBlu )
	{
		// A coordinator that named no side is running a symmetric map, where the
		// uniform and the war side are the same thing.
		iWarSide = iTeam;
	}

	for ( int i = 0; i < m_nCount; ++i )
	{
		if ( m_Entries[i].m_ulSteamID == ulSteamID )
		{
			m_Entries[i].m_iTeam = iTeam;
			m_Entries[i].m_iWarSide = iWarSide;
			m_Entries[i].m_bContract = bContract;
			return true;
		}
	}

	if ( m_nCount >= kMaxRoster )
		return false;

	RosterEntry_t &e = m_Entries[m_nCount++];
	e.m_ulSteamID = ulSteamID;
	e.m_iTeam = iTeam;
	e.m_iWarSide = iWarSide;
	e.m_bContract = bContract;
	return true;
}

const RosterEntry_t *CRoster::Find( unsigned long long ulSteamID ) const
{
	if ( ulSteamID == 0 )
		return 0;

	for ( int i = 0; i < m_nCount; ++i )
	{
		if ( m_Entries[i].m_ulSteamID == ulSteamID )
			return &m_Entries[i];
	}
	return 0;
}

//-----------------------------------------------------------------------------
// The briefing
//-----------------------------------------------------------------------------
bool ColoursDisagree( const RosterEntry_t *pEntry )
{
	if ( !pEntry )
		return false;
	if ( pEntry->m_iWarSide == kTeamNone || pEntry->m_iTeam == kTeamNone )
		return false;
	return pEntry->m_iTeam != pEntry->m_iWarSide;
}

static void AddLine( Briefing_t *pOut, int iDest, const char *pszToken,
					 const char *p1 = 0, const char *p2 = 0,
					 const char *p3 = 0, const char *p4 = 0 )
{
	if ( pOut->m_nLines >= kMaxBriefingLines )
		return;

	BriefingLine_t &line = pOut->m_Lines[pOut->m_nLines++];
	line.m_iDest = iDest;
	line.m_pszToken = pszToken;
	line.m_nParams = 0;

	const char *params[kMaxParams] = { p1, p2, p3, p4 };
	for ( int i = 0; i < kMaxParams; ++i )
	{
		line.m_pszParams[i] = "";
		if ( params[i] )
			line.m_pszParams[line.m_nParams++] = params[i];
	}
}

void BuildBriefing( const BattleContext_t &ctx, const RosterEntry_t *pEntry,
					bool bIncludeHUD, Briefing_t *pOut )
{
	if ( !pOut )
		return;

	pOut->m_nLines = 0;
	IntToString( ctx.m_nStage, pOut->m_szStage, sizeof( pOut->m_szStage ) );
	IntToString( ctx.m_nStageCount, pOut->m_szStageCount, sizeof( pOut->m_szStageCount ) );

	if ( !ctx.HasBattle() )
		return;

	// A node with no display name is still a place. Falling back to the id keeps
	// the sentence true instead of leaving a hole in it.
	const char *pszNode = IsEmpty( ctx.m_pszNodeName ) ? ctx.m_pszNodeID : ctx.m_pszNodeName;
	if ( IsEmpty( pszNode ) )
		pszNode = "";

	const char *pszStageToken = StageToken( ctx.m_pszStageKind );

	// Line 1: which offensive this is.
	AddLine( pOut, kDestChat, "#Greyline_Chat_Front", ctx.m_pszFrontName );

	// Line 2: where in that offensive we are, and who is pushing.
	AddLine( pOut, kDestChat, "#Greyline_Chat_Stage",
			 pszStageToken, pOut->m_szStage, pOut->m_szStageCount,
			 SideTokenFromName( ctx.m_pszAttacker ) );

	// Line 3, the one that stops a player thinking the game is broken. Payload
	// and attack/defend maps are built for BLU to attack, so a RED offensive is
	// fought in BLU colours. Said once, plainly, or the scoreboard lies.
	if ( ColoursDisagree( pEntry ) )
	{
		AddLine( pOut, kDestChat, "#Greyline_Chat_Colours",
				 SideTokenFromTeam( pEntry->m_iWarSide ),
				 SideTokenFromTeam( pEntry->m_iTeam ) );
	}

	// Line 4: why this battle exists.
	AddLine( pOut, kDestChat, ReasonToken( ctx.m_pszStageKind ), pszNode );

	// Line 5, only when it applies: this player is here under contract for the
	// other side. The side named is the one they fight for, not the colours they
	// happen to be wearing.
	if ( pEntry && pEntry->m_bContract )
	{
		AddLine( pOut, kDestChat, "#Greyline_Chat_Contract",
				 SideTokenFromTeam( pEntry->m_iWarSide ) );
	}

	// Line 6, only when it applies: the war is propping up the losing side, and
	// players deserve to know why this defence is harder to break than usual.
	if ( !IsEmpty( ctx.m_pszMobilized ) )
	{
		AddLine( pOut, kDestChat, "#Greyline_Chat_Mobilized",
				 SideTokenFromName( ctx.m_pszMobilized ) );
	}

	if ( !bIncludeHUD )
		return;

	// One centre message, never two: the second would only overwrite the first.
	// When the colours disagree with the war, that fact replaces the stage
	// counter, because it is the more urgent of the two.
	if ( ColoursDisagree( pEntry ) )
	{
		AddLine( pOut, kDestCenter, "#Greyline_Hud_Colours",
				 ctx.m_pszFrontName, pszStageToken,
				 SideTokenFromTeam( pEntry->m_iWarSide ),
				 SideTokenFromTeam( pEntry->m_iTeam ) );
	}
	else
	{
		AddLine( pOut, kDestCenter, "#Greyline_Hud_Briefing",
				 ctx.m_pszFrontName, pszStageToken,
				 pOut->m_szStage, pOut->m_szStageCount );
	}
}

} // namespace greyline
