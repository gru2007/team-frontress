//========= Team Frontress ====================================================//
//
// A war battle, on both ends of the wire.
//
// Server: the coordinator tells a server what its match is (msg 51004, JSON in
// a CMsgPartyChatMsg; services/gc/internal/gc/warorder.go): the conditions it
// is fought under, which sector, which stage, who attacks. Once that match is
// up, the server execs cfg/frontress_cond_<id>.cfg for each condition (after
// the match's own config) and puts the rest in replicated convars; when the
// match is over, cfg/frontress_cond_reset.cfg puts everything back.
//
// Client: the HUD reads those convars (tf/frontress/fui_hud.cpp) to show what
// the fight is for and what it is fought under.
//
//=============================================================================
#include "cbase.h"

// memdbgon must be the last include file in a .cpp file!!!
#include "tier0/memdbgon.h"

// "lowgrav,nocrits": what this battle is fought under.
ConVar tf_war_conditions( "tf_war_conditions", "", FCVAR_REPLICATED | FCVAR_HIDDEN, "The war battle's conditions (set by the server from the coordinator)." );
// Respawn wave length a condition imposes ("longwaves", "quickwaves"), in
// seconds; -1 leaves it to the map and mp_respawnwavetime. Most maps set their
// own team wave times, which mp_respawnwavetime cannot override; this does
// (CTeamplayRoundBasedRules::GetRespawnWaveMaxLength).
ConVar tf_war_respawnwave( "tf_war_respawnwave", "-1", FCVAR_REPLICATED | FCVAR_HIDDEN, "Respawn wave length a war battle condition imposes, in seconds (-1: the map's)." );
// "<stage>|<stages>|<kind>|<attacker side>|<attacker team>|<order>|<sector>"
ConVar tf_war_battle( "tf_war_battle", "", FCVAR_REPLICATED | FCVAR_HIDDEN, "The war battle being fought (set by the server from the coordinator)." );

#ifdef GAME_DLL
#include "gc_clientsystem.h"
#include "gcsdk/gcsdk_auto.h"
#include "tf_gcmessages.h"
#include "tf_gcmessages.pb.h"
#include "tf_gc_server.h"
extern ConVar tf_war_uniform_swap;

static const uint32 k_EMsgGCFrontress_MatchConditions = 51004;

namespace
{

// A small reader for the one JSON shape we get: string and number fields,
// and one array of strings.
CUtlString JsonString( const char *pszJSON, const char *pszKey )
{
	const char *p = V_strstr( pszJSON, CFmtStr( "\"%s\":", pszKey ) );
	if ( !p )
		return CUtlString();
	p += V_strlen( pszKey ) + 3;
	while ( *p == ' ' )
		++p;
	if ( *p != '"' )
	{
		// A number or a bool, as text.
		const char *end = p;
		while ( *end && *end != ',' && *end != '}' )
			++end;
		return CUtlString( p, end - p );
	}
	++p;
	CUtlString out;
	for ( ; *p && *p != '"'; ++p )
	{
		if ( *p == '\\' && p[ 1 ] )
			++p;
		out += *p;
	}
	return out;
}

void JsonStrings( const char *pszJSON, const char *pszKey, CUtlStringList &out )
{
	const char *p = V_strstr( pszJSON, CFmtStr( "\"%s\":", pszKey ) );
	if ( !p || !( p = strchr( p, '[' ) ) )
		return;
	for ( ++p; *p && *p != ']'; ++p )
	{
		if ( *p != '"' )
			continue;
		const char *end = strchr( p + 1, '"' );
		if ( !end )
			return;
		out.CopyAndAddToTail( CUtlString( p + 1, end - p - 1 ).Get() );
		p = end;
	}
}

// A condition names a cfg file: letters only.
bool ValidCondition( const char *psz )
{
	if ( !*psz || V_strlen( psz ) > 24 )
		return false;
	for ( ; *psz; ++psz )
	{
		if ( !( *psz >= 'a' && *psz <= 'z' ) )
			return false;
	}
	return true;
}

struct PendingBattle_t
{
	uint64 m_nMatchID = 0;
	CUtlStringList m_Conditions;
	CUtlString m_strBattle;
	bool m_bApplied = false;
	bool m_bUniformSwap = false;
};
PendingBattle_t s_Battle;

} // namespace

//-----------------------------------------------------------------------------
class CGCFrontressMatchConditions : public GCSDK::CGCClientJob
{
public:
	CGCFrontressMatchConditions( GCSDK::CGCClient *pClient ) : GCSDK::CGCClientJob( pClient ) {}
	virtual bool BYieldingRunGCJob( GCSDK::IMsgNetPacket *pNetPacket ) OVERRIDE
	{
		GCSDK::CProtoBufMsg< CMsgPartyChatMsg > msg( pNetPacket );
		const char *pszJSON = msg.Body().msg().c_str();

		PendingBattle_t battle;
		battle.m_nMatchID = V_atoui64( JsonString( pszJSON, "match_id" ) );
		battle.m_bUniformSwap = V_strcmp( JsonString( pszJSON, "uniform_swap" ).Get(), "true" ) == 0;
		CUtlStringList conditions;
		JsonStrings( pszJSON, "conditions", conditions );
		for ( int i = 0; i < conditions.Count(); ++i )
		{
			if ( ValidCondition( conditions[ i ] ) )
				battle.m_Conditions.CopyAndAddToTail( conditions[ i ] );
		}
		CUtlString sector = JsonString( pszJSON, "sector" );
		sector = sector.Replace( '|', '/' );
		battle.m_strBattle.Format( "%s|%s|%s|%s|%s|%s|%s",
			JsonString( pszJSON, "stage" ).Get(), JsonString( pszJSON, "stages" ).Get(), JsonString( pszJSON, "kind" ).Get(),
			JsonString( pszJSON, "attacker" ).Get(), JsonString( pszJSON, "attacker_team" ).Get(),
			JsonString( pszJSON, "order" ).Get(), sector.Get() );

		if ( battle.m_nMatchID == s_Battle.m_nMatchID && s_Battle.m_bApplied )
			return true;	// told again after a reconnect: already in force
		s_Battle.m_nMatchID = battle.m_nMatchID;
		s_Battle.m_Conditions.PurgeAndDeleteElements();
		for ( int i = 0; i < battle.m_Conditions.Count(); ++i )
			s_Battle.m_Conditions.CopyAndAddToTail( battle.m_Conditions[ i ] );
		s_Battle.m_strBattle = battle.m_strBattle;
		s_Battle.m_bUniformSwap = battle.m_bUniformSwap;
		s_Battle.m_bApplied = false;
		Msg( "War battle for match %llu: %s, conditions: %d\n", (unsigned long long)battle.m_nMatchID, battle.m_strBattle.Get(), battle.m_Conditions.Count() );
		return true;
	}
};
GC_REG_JOB( GCSDK::CGCClient, CGCFrontressMatchConditions, "CGCFrontressMatchConditions", k_EMsgGCFrontress_MatchConditions, GCSDK::k_EServerTypeGCClient );

//-----------------------------------------------------------------------------
// Puts the conditions in force once the match they are for is up, and takes
// them out when it is over.
//-----------------------------------------------------------------------------
class CFrontressConditions : public CAutoGameSystemPerFrame
{
public:
	CFrontressConditions() : CAutoGameSystemPerFrame( "CFrontressConditions" ) {}

	virtual void LevelInitPostEntity() OVERRIDE { m_flLevelStart = gpGlobals->curtime; }

	virtual void LevelShutdownPreEntity() OVERRIDE
	{
		if ( s_Battle.m_bApplied )
			Reset();
	}

	virtual void FrameUpdatePostEntityThink() OVERRIDE
	{
		const CMatchInfo *pMatch = GTFGCClientSystem() ? GTFGCClientSystem()->GetMatch() : NULL;
		const uint64 nMatch = pMatch ? pMatch->m_nMatchID : 0;
		if ( s_Battle.m_bApplied && nMatch != s_Battle.m_nMatchID )
		{
			Reset();
			return;
		}
		// A moment into the level, so the match's own config has run first.
		if ( s_Battle.m_bApplied || !nMatch || nMatch != s_Battle.m_nMatchID || gpGlobals->curtime - m_flLevelStart < 2.f )
			return;

		CUtlString list;
		for ( int i = 0; i < s_Battle.m_Conditions.Count(); ++i )
		{
			engine->ServerCommand( CFmtStr( "exec frontress_cond_%s.cfg\n", s_Battle.m_Conditions[ i ] ) );
			list += ( i ? "," : "" );
			list += s_Battle.m_Conditions[ i ];
		}
		tf_war_conditions.SetValue( list.Get() );
		tf_war_battle.SetValue( s_Battle.m_strBattle.Get() );
		tf_war_uniform_swap.SetValue( s_Battle.m_bUniformSwap ? 1 : 0 );
		s_Battle.m_bApplied = true;
	}

private:
	void Reset()
	{
		engine->ServerCommand( "exec frontress_cond_reset.cfg\n" );
		tf_war_conditions.SetValue( "" );
		tf_war_battle.SetValue( "" );
		tf_war_uniform_swap.SetValue( 0 );
		s_Battle.m_bApplied = false;
		s_Battle.m_nMatchID = 0;
	}

	float m_flLevelStart = 0.f;
};
static CFrontressConditions s_FrontressConditions;

#endif // GAME_DLL
