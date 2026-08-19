//========= GREYLINE FRONTRESS ============================================//
//
// Purpose: the briefing actually renders, in every language it ships in.
//
// The server sends tokens and the client resolves them, which means the two
// halves can drift without either one failing to build: a token nobody
// translated prints as "#Greyline_Chat_Stage" in somebody's chat, and a
// substitution the server does not send prints as a literal "%s3". Both are
// invisible until a player sees them, and both are cheap to catch here.
//
// So this reads the shipped language files the way the engine does, replays the
// real briefings through them, and asserts the sentence that comes out.
//
//   * every token the server can emit is translated in every language
//   * every %sN in a translation is a substitution the server actually sends
//   * nothing renders longer than the client's buffer, which would truncate it
//
// The engine side of this is CBaseHudChat::MsgFunc_TextMsg: it resolves the
// message and all four parameters through g_pVGuiLocalize->Find — so a
// parameter starting with '#' is itself translated — then builds the sentence
// with ConstructString_safe into a wchar_t[256].
//
//=========================================================================//

#include "greyline_test.h"
#include "../greyline_briefing_logic.h"

#include <cstdio>
#include <cstdlib>
#include <map>
#include <set>
#include <string>
#include <vector>

using namespace greyline;

namespace
{

// The client's buffer in CBaseHudChat::MsgFunc_TextMsg, in UTF-16 units. On
// Windows wchar_t is 16 bits, so this is the real limit; the last one is the
// terminator.
const size_t kClientBufferChars = 256;

// A generous stand-in for the proper nouns the coordinator generates. Front
// names are built as "BATTLE FOR " + a node name, and node names come from the
// theater file, so this is longer than anything theater.industrial.json can
// produce — which is the point: the check should fail before a player does.
const char *kLongFrontName = "BATTLE FOR THE NORTHERN RESERVOIR PUMPING STATION";
const char *kLongNodeName = "The Northern Reservoir Pumping Station";

std::string g_strResourceDir;

// ---------------------------------------------------------------------------
// Reading a Source localization file: UCS-2 (UTF-16LE) with a BOM.
// ---------------------------------------------------------------------------

typedef std::vector<unsigned short> U16String;

bool ReadUTF16File( const std::string &strPath, U16String *pOut, std::string *pErr )
{
	std::FILE *f = std::fopen( strPath.c_str(), "rb" );
	if ( !f )
	{
		*pErr = "cannot open " + strPath;
		return false;
	}

	std::vector<unsigned char> bytes;
	unsigned char buf[4096];
	size_t n;
	while ( ( n = std::fread( buf, 1, sizeof( buf ), f ) ) > 0 )
		bytes.insert( bytes.end(), buf, buf + n );
	std::fclose( f );

	if ( bytes.size() < 2 || bytes[0] != 0xFF || bytes[1] != 0xFE )
	{
		*pErr = strPath + " is not UTF-16LE with a BOM, which is what the engine reads";
		return false;
	}
	if ( bytes.size() % 2 != 0 )
	{
		*pErr = strPath + " has an odd byte count and cannot be UTF-16";
		return false;
	}
	// A second BOM is what an edit that re-encoded an already-decoded file
	// leaves behind. Every parser here would skip it as whitespace and every
	// token would still be found, so nothing would notice until the engine
	// read the file and briefed nobody.
	if ( bytes.size() >= 4 && bytes[2] == 0xFF && bytes[3] == 0xFE )
	{
		*pErr = strPath + " starts with two BOMs — it was re-encoded on top of its own BOM";
		return false;
	}

	pOut->clear();
	for ( size_t i = 2; i + 1 < bytes.size(); i += 2 )
		pOut->push_back( (unsigned short)( bytes[i] | ( bytes[i + 1] << 8 ) ) );
	return true;
}

std::string ToASCII( const U16String &s )
{
	std::string out;
	for ( size_t i = 0; i < s.size(); ++i )
		out += ( s[i] < 128 ) ? (char)s[i] : '?';
	return out;
}

// Pulls "key" "value" pairs out of the Tokens block. Comments run to the end of
// the line and never appear inside a quoted string in these files.
bool ParseTokens( const U16String &text, std::map<std::string, U16String> *pOut, std::string *pErr )
{
	std::vector<U16String> quoted;
	bool bInString = false;
	bool bInComment = false;
	U16String current;

	for ( size_t i = 0; i < text.size(); ++i )
	{
		const unsigned short c = text[i];

		if ( bInComment )
		{
			if ( c == '\n' )
				bInComment = false;
			continue;
		}
		if ( bInString )
		{
			if ( c == '"' )
			{
				quoted.push_back( current );
				current.clear();
				bInString = false;
			}
			else
			{
				current.push_back( c );
			}
			continue;
		}
		if ( c == '/' && i + 1 < text.size() && text[i + 1] == '/' )
		{
			bInComment = true;
			continue;
		}
		if ( c == '"' )
			bInString = true;
	}

	if ( bInString )
	{
		*pErr = "unterminated string";
		return false;
	}

	// Pair up everything after the "Tokens" key rather than assuming how many
	// strings precede it.
	size_t nStart = quoted.size();
	for ( size_t i = 0; i < quoted.size(); ++i )
	{
		if ( ToASCII( quoted[i] ) == "Tokens" )
		{
			nStart = i + 1;
			break;
		}
	}
	if ( nStart >= quoted.size() )
	{
		*pErr = "no Tokens block";
		return false;
	}

	pOut->clear();
	for ( size_t i = nStart; i + 1 < quoted.size(); i += 2 )
		( *pOut )[ToASCII( quoted[i] )] = quoted[i + 1];
	return true;
}

// ---------------------------------------------------------------------------
// Substitution, the way vgui::ILocalize::ConstructString does it: %s1..%s4 are
// replaced by the corresponding parameter, wherever and however often they
// appear, and everything else is literal.
// ---------------------------------------------------------------------------

U16String ConstructString( const U16String &format, const U16String *pParams, int nParams )
{
	U16String out;
	for ( size_t i = 0; i < format.size(); ++i )
	{
		if ( format[i] == '%' && i + 2 < format.size() &&
			 format[i + 1] == 's' && format[i + 2] >= '1' && format[i + 2] <= '9' )
		{
			const int nIndex = format[i + 2] - '1';
			if ( nIndex < nParams )
			{
				out.insert( out.end(), pParams[nIndex].begin(), pParams[nIndex].end() );
			}
			else
			{
				// The engine leaves an unsupplied substitution empty. The test
				// that matters flags it separately; here we just keep going.
			}
			i += 2;
			continue;
		}
		out.push_back( format[i] );
	}
	return out;
}

// Highest N used by any %sN in the string, or 0 for none.
int MaxPlaceholder( const U16String &s )
{
	int nMax = 0;
	for ( size_t i = 0; i + 2 < s.size(); ++i )
	{
		if ( s[i] == '%' && s[i + 1] == 's' && s[i + 2] >= '1' && s[i + 2] <= '9' )
		{
			const int n = s[i + 2] - '0';
			if ( n > nMax )
				nMax = n;
		}
	}
	return nMax;
}

// Any '%' that is not part of a well-formed %sN. A stray %s or %d is a literal
// here but a loaded gun for anything that ever formats these with printf.
bool HasMalformedPercent( const U16String &s )
{
	for ( size_t i = 0; i < s.size(); ++i )
	{
		if ( s[i] != '%' )
			continue;
		const bool bWellFormed = ( i + 2 < s.size() && s[i + 1] == 's' &&
								   s[i + 2] >= '1' && s[i + 2] <= '9' );
		if ( !bWellFormed )
			return true;
	}
	return false;
}

U16String FromASCII( const std::string &s )
{
	U16String out;
	for ( size_t i = 0; i < s.size(); ++i )
		out.push_back( (unsigned char)s[i] );
	return out;
}

// ---------------------------------------------------------------------------
// The language files under test.
// ---------------------------------------------------------------------------

struct Language_t
{
	std::string							m_strName;
	std::map<std::string, U16String>	m_Tokens;
};

std::vector<Language_t> &Languages()
{
	static std::vector<Language_t> s_Languages;
	return s_Languages;
}

bool LoadLanguages( std::string *pErr )
{
	// Every language the game ships a Greyline file for. Adding one here is what
	// makes it checked; a language file nobody lists is a language nobody tests.
	const char *pszNames[] = { "english", "russian" };

	for ( size_t i = 0; i < sizeof( pszNames ) / sizeof( pszNames[0] ); ++i )
	{
		const std::string strPath = g_strResourceDir + "/greyline_" + pszNames[i] + ".txt";

		U16String text;
		if ( !ReadUTF16File( strPath, &text, pErr ) )
			return false;

		Language_t lang;
		lang.m_strName = pszNames[i];
		if ( !ParseTokens( text, &lang.m_Tokens, pErr ) )
		{
			*pErr = strPath + ": " + *pErr;
			return false;
		}
		Languages().push_back( lang );
	}
	return true;
}

// ---------------------------------------------------------------------------
// Every briefing the server can produce, so the checks below run over the real
// thing rather than a list somebody has to remember to update.
// ---------------------------------------------------------------------------

struct Scenario_t
{
	std::string		m_strName;
	BattleContext_t	m_Context;
	RosterEntry_t	m_Entry;
	bool			m_bHasEntry;
	bool			m_bHUD;
};

std::vector<Scenario_t> AllScenarios()
{
	const char *pszStages[] = { "skirmish", "breakthrough", "advance", "assault", "" };
	std::vector<Scenario_t> out;

	// Both halves of every scenario: with the player bodies drawn in the
	// war's colours and without. The two produce different sentences about
	// what the player is looking at, and a language that translates only one
	// of them leaves somebody reading a raw token in the case that actually
	// happens to them.
	for ( int nUniform = 0; nUniform < 2; ++nUniform )
	for ( size_t s = 0; s < sizeof( pszStages ) / sizeof( pszStages[0] ); ++s )
	{
		for ( int nVariant = 0; nVariant < 4; ++nVariant )
		{
			Scenario_t sc;
			sc.m_Context.m_bUniformsShowWarSide = ( nUniform != 0 );
			sc.m_Context.m_pszFrontName = kLongFrontName;
			sc.m_Context.m_pszNodeName = kLongNodeName;
			sc.m_Context.m_pszNodeID = "reservoir";
			sc.m_Context.m_pszAttacker = ( nVariant % 2 ) ? "BLU" : "RED";
			sc.m_Context.m_pszStageKind = pszStages[s];
			sc.m_Context.m_nStage = 2;
			sc.m_Context.m_nStageCount = 3;
			// Mobilization and a contract are the lines that only sometimes
			// appear, so half the scenarios carry them.
			sc.m_Context.m_pszMobilized = ( nVariant >= 2 ) ? "BLU" : "";

			sc.m_Entry.m_ulSteamID = 76561198000000001ull;
			// The interesting case: RED fighting in BLU colours.
			sc.m_Entry.m_iTeam = kTeamBlu;
			sc.m_Entry.m_iWarSide = ( nVariant % 2 ) ? kTeamBlu : kTeamRed;
			sc.m_Entry.m_bContract = ( nVariant >= 2 );
			sc.m_bHasEntry = true;
			sc.m_bHUD = true;

			sc.m_strName = std::string( "stage=" ) +
				( pszStages[s][0] ? pszStages[s] : "(empty)" ) +
				" variant=" + std::to_string( nVariant ) +
				" uniforms=" + ( nUniform ? "war" : "team" );
			out.push_back( sc );
		}
	}

	// And the same battle for somebody the coordinator never assigned.
	Scenario_t anon;
	anon.m_Context.m_pszFrontName = kLongFrontName;
	anon.m_Context.m_pszNodeName = kLongNodeName;
	anon.m_Context.m_pszNodeID = "reservoir";
	anon.m_Context.m_pszAttacker = "RED";
	anon.m_Context.m_pszStageKind = "breakthrough";
	anon.m_Context.m_pszMobilized = "RED";
	anon.m_Context.m_nStage = 1;
	anon.m_Context.m_nStageCount = 3;
	anon.m_bHasEntry = false;
	anon.m_bHUD = true;
	anon.m_strName = "unassigned player";
	out.push_back( anon );

	return out;
}

// Resolves one parameter the way the client does: a '#' token is looked up, and
// anything else is literal text.
U16String ResolveParam( const Language_t &lang, const char *pszParam )
{
	const std::string strParam = pszParam ? pszParam : "";
	if ( strParam.empty() )
		return U16String();
	if ( strParam[0] != '#' )
		return FromASCII( strParam );

	std::map<std::string, U16String>::const_iterator it =
		lang.m_Tokens.find( strParam.substr( 1 ) );
	if ( it == lang.m_Tokens.end() )
		return FromASCII( strParam );	// untranslated; a separate test says so
	return it->second;
}

} // namespace

// ---------------------------------------------------------------------------

GREYLINE_TEST( Language_files_parse_and_agree_on_what_they_define )
{
	CHECK_EQ( (int)Languages().size(), 2 );
	if ( Languages().size() < 2 )
		return;

	for ( size_t i = 0; i < Languages().size(); ++i )
		CHECK( Languages()[i].m_Tokens.size() > 0 );

	// A token translated in one language and not another is how a Russian
	// player ends up reading "#Greyline_Chat_Stage" while everyone else reads a
	// sentence.
	const Language_t &base = Languages()[0];
	for ( size_t i = 1; i < Languages().size(); ++i )
	{
		const Language_t &other = Languages()[i];

		for ( std::map<std::string, U16String>::const_iterator it = base.m_Tokens.begin();
			  it != base.m_Tokens.end(); ++it )
		{
			if ( other.m_Tokens.find( it->first ) == other.m_Tokens.end() )
			{
				greylinetest::Fail( __FILE__, __LINE__,
					other.m_strName + " is missing token " + it->first +
					", which " + base.m_strName + " defines" );
			}
		}
		for ( std::map<std::string, U16String>::const_iterator it = other.m_Tokens.begin();
			  it != other.m_Tokens.end(); ++it )
		{
			if ( base.m_Tokens.find( it->first ) == base.m_Tokens.end() )
			{
				greylinetest::Fail( __FILE__, __LINE__,
					other.m_strName + " defines token " + it->first +
					", which " + base.m_strName + " does not" );
			}
		}
	}
}

GREYLINE_TEST( Every_token_the_server_can_emit_is_translated )
{
	const std::vector<Scenario_t> scenarios = AllScenarios();
	std::set<std::string> used;

	for ( size_t i = 0; i < scenarios.size(); ++i )
	{
		Briefing_t b;
		BuildBriefing( scenarios[i].m_Context,
			scenarios[i].m_bHasEntry ? &scenarios[i].m_Entry : 0,
			scenarios[i].m_bHUD, &b );

		for ( int l = 0; l < b.m_nLines; ++l )
		{
			used.insert( b.m_Lines[l].m_pszToken );
			for ( int p = 0; p < (int)kMaxParams; ++p )
			{
				const std::string strParam = b.m_Lines[l].m_pszParams[p];
				if ( !strParam.empty() && strParam[0] == '#' )
					used.insert( strParam );
			}
		}
	}

	// Sanity: the scenarios really do exercise the whole briefing. If this drops
	// the tests below would still pass while covering less.
	CHECK( used.size() >= 17 );

	for ( std::set<std::string>::const_iterator it = used.begin(); it != used.end(); ++it )
	{
		const std::string strName = it->substr( 1 );	// drop the '#'
		for ( size_t i = 0; i < Languages().size(); ++i )
		{
			if ( Languages()[i].m_Tokens.find( strName ) == Languages()[i].m_Tokens.end() )
			{
				greylinetest::Fail( __FILE__, __LINE__,
					"the server can emit " + *it + " but " + Languages()[i].m_strName +
					" does not translate it — the player would see the raw token" );
			}
		}
	}
}

GREYLINE_TEST( No_translation_asks_for_a_substitution_the_server_does_not_send )
{
	const std::vector<Scenario_t> scenarios = AllScenarios();

	// Token -> the most parameters the server ever sends with it.
	std::map<std::string, int> sent;

	for ( size_t i = 0; i < scenarios.size(); ++i )
	{
		Briefing_t b;
		BuildBriefing( scenarios[i].m_Context,
			scenarios[i].m_bHasEntry ? &scenarios[i].m_Entry : 0,
			scenarios[i].m_bHUD, &b );

		for ( int l = 0; l < b.m_nLines; ++l )
		{
			const std::string strToken = std::string( b.m_Lines[l].m_pszToken ).substr( 1 );
			const int nParams = b.m_Lines[l].m_nParams;
			if ( sent.find( strToken ) == sent.end() || sent[strToken] < nParams )
				sent[strToken] = nParams;
		}
	}

	for ( std::map<std::string, int>::const_iterator it = sent.begin(); it != sent.end(); ++it )
	{
		for ( size_t i = 0; i < Languages().size(); ++i )
		{
			std::map<std::string, U16String>::const_iterator tok =
				Languages()[i].m_Tokens.find( it->first );
			if ( tok == Languages()[i].m_Tokens.end() )
				continue;	// reported by the test above

			const int nWanted = MaxPlaceholder( tok->second );
			if ( nWanted > it->second )
			{
				greylinetest::Fail( __FILE__, __LINE__,
					Languages()[i].m_strName + "'s " + it->first + " uses %s" +
					std::to_string( nWanted ) + " but the server sends only " +
					std::to_string( it->second ) + " substitution(s) — it would render empty" );
			}
			// The engine reads exactly four. A fifth is unreachable by design.
			if ( nWanted > (int)kMaxParams )
			{
				greylinetest::Fail( __FILE__, __LINE__,
					Languages()[i].m_strName + "'s " + it->first + " uses %s" +
					std::to_string( nWanted ) + "; TextMsg carries only four" );
			}
		}
	}
}

GREYLINE_TEST( No_translation_contains_a_malformed_substitution )
{
	for ( size_t i = 0; i < Languages().size(); ++i )
	{
		for ( std::map<std::string, U16String>::const_iterator it = Languages()[i].m_Tokens.begin();
			  it != Languages()[i].m_Tokens.end(); ++it )
		{
			if ( HasMalformedPercent( it->second ) )
			{
				greylinetest::Fail( __FILE__, __LINE__,
					Languages()[i].m_strName + "'s " + it->first +
					" contains a '%' that is not a %s1..%s9 substitution" );
			}
		}
	}
}

GREYLINE_TEST( Every_briefing_line_fits_the_clients_buffer )
{
	const std::vector<Scenario_t> scenarios = AllScenarios();

	for ( size_t i = 0; i < scenarios.size(); ++i )
	{
		Briefing_t b;
		BuildBriefing( scenarios[i].m_Context,
			scenarios[i].m_bHasEntry ? &scenarios[i].m_Entry : 0,
			scenarios[i].m_bHUD, &b );

		for ( int l = 0; l < b.m_nLines; ++l )
		{
			const std::string strToken = std::string( b.m_Lines[l].m_pszToken ).substr( 1 );

			for ( size_t g = 0; g < Languages().size(); ++g )
			{
				const Language_t &lang = Languages()[g];
				std::map<std::string, U16String>::const_iterator tok = lang.m_Tokens.find( strToken );
				if ( tok == lang.m_Tokens.end() )
					continue;

				U16String params[kMaxParams];
				for ( int p = 0; p < (int)kMaxParams; ++p )
					params[p] = ResolveParam( lang, b.m_Lines[l].m_pszParams[p] );

				const U16String rendered = ConstructString( tok->second, params, kMaxParams );

				if ( rendered.size() >= kClientBufferChars )
				{
					greylinetest::Fail( __FILE__, __LINE__,
						lang.m_strName + "'s " + strToken + " renders " +
						std::to_string( rendered.size() ) + " characters (limit " +
						std::to_string( kClientBufferChars - 1 ) +
						") in scenario [" + scenarios[i].m_strName +
						"] — the client truncates it mid-sentence" );
				}

				// Nothing may survive substitution: a leftover %sN means the
				// player reads the placeholder instead of the value.
				CHECK_EQ( MaxPlaceholder( rendered ), 0 );
			}
		}
	}
}

GREYLINE_TEST( Translations_are_not_accidentally_empty )
{
	for ( size_t i = 0; i < Languages().size(); ++i )
	{
		for ( std::map<std::string, U16String>::const_iterator it = Languages()[i].m_Tokens.begin();
			  it != Languages()[i].m_Tokens.end(); ++it )
		{
			if ( it->second.empty() )
			{
				greylinetest::Fail( __FILE__, __LINE__,
					Languages()[i].m_strName + "'s " + it->first + " is empty" );
			}
		}
	}
}

int main( int argc, char **argv )
{
	// The resource directory, so the test can be run from anywhere. The Makefile
	// passes it; the default is where it lives relative to the repository root.
	g_strResourceDir = ( argc > 1 ) ? argv[1] : "game/tc2/loose/resource";

	std::string strErr;
	if ( !LoadLanguages( &strErr ) )
	{
		std::fprintf( stderr, "greyline_localization: %s\n", strErr.c_str() );
		std::fprintf( stderr, "  (pass the resource directory as the first argument)\n" );
		return 1;
	}

	return greylinetest::RunAll( "greyline_localization" );
}
