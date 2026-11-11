//========= Team Frontress ====================================================//
//
// The in-match overlays of the Frontress HUD: the scoreboard, the end of a
// round and of the match, chat, votes, the spectator view, who killed you,
// and the game's notices. See fui_hud.h.
//
// Each reads the game the way its stock panel does (the same events, the
// same player resource, the same strings), so the information is TF2's; only
// the drawing is ours. The stock panels stay hidden while the HUD on screen
// names their part in data-replaces.
//
//=============================================================================
#include "cbase.h"

#pragma push_macro( "Assert" )
#undef Assert
#include "tier0/valve_minmax_off.h"
#include "fui.h"
#include <RmlUi/Core/Context.h>
#include <RmlUi/Core/Element.h>
#include <RmlUi/Core/ElementDocument.h>
#include "tier0/valve_minmax_on.h"
#pragma pop_macro( "Assert" )

#include "fui_hud.h"
#include "fui_host.h"
#include "baseviewport.h"
#include "iclientmode.h"
#include "ienginevgui.h"
#include "tf/frontress_uniform.h"

#include "c_tf_player.h"
#include "c_tf_team.h"
#include "c_tf_playerresource.h"
#include "c_baseobject.h"
#include "GameEventListener.h"
#include "hud.h"
#include "hud_basechat.h"
#include "hud_vote.h"
#include "tf_gamerules.h"
#include "econ_item_system.h"
#include "steam/steam_api.h"
#include "vgui/ILocalize.h"
#include "vgui_controls/Label.h"
#include "voice_status.h"

#include <cmath>
#include <deque>
#include <string>

// memdbgon must be the last include file in a .cpp file!!!
#include "tier0/memdbgon.h"

using fui::Node;

extern ConVar tf_scoreboard_mouse_mode;
extern bool AreEnemyTeams( int iTeam1, int iTeam2 );	// tf_clientscoreboard.cpp

namespace
{

std::string UTF8( const wchar_t *pwsz )
{
	if ( !pwsz )
		return std::string();
	char sz[ 1024 ];
	V_UnicodeToUTF8( pwsz, sz, sizeof( sz ) );
	return sz;
}

const char *TeamName( int iTeam )
{
	iTeam = frontress::DisplayTeam( iTeam );
	return iTeam == TF_TEAM_BLUE ? "blu" : iTeam == TF_TEAM_RED ? "red" : "spec";
}

const char *ClassName( int iClass )
{
	static const char *s_Names[] = { "", "scout", "sniper", "soldier", "demoman", "medic", "heavy", "pyro", "spy", "engineer" };
	return iClass > 0 && iClass < (int)ARRAYSIZE( s_Names ) ? s_Names[ iClass ] : "";
}

// A localized game string with up to two arguments (%s1 %s2).
std::string Construct( const char *pszToken, const wchar_t *pwszArg1 = NULL, const wchar_t *pwszArg2 = NULL )
{
	const wchar_t *pwszFormat = g_pVGuiLocalize->Find( pszToken );
	if ( !pwszFormat )
		return std::string();
	wchar_t wszOut[ 512 ];
	const int nArgs = pwszArg2 ? 2 : pwszArg1 ? 1 : 0;
	g_pVGuiLocalize->ConstructString_safe( wszOut, pwszFormat, nArgs, pwszArg1, pwszArg2 );
	return UTF8( wszOut );
}

std::string SteamID( int iPlayer )
{
	player_info_t info;
	if ( iPlayer <= 0 || !engine->GetPlayerInfo( iPlayer, &info ) || !info.friendsID || info.fakeplayer )
		return std::string();
	const CSteamID id( info.friendsID, steamapicontext && steamapicontext->SteamUtils() ? steamapicontext->SteamUtils()->GetConnectedUniverse() : k_EUniversePublic, k_EAccountTypeIndividual );
	return std::to_string( id.ConvertToUint64() );
}

// "avatar:<id>" once Steam has the picture (the renderer caches a miss).
std::string Avatar( int iPlayer )
{
	const std::string id = SteamID( iPlayer );
	ISteamFriends *pFriends = steamapicontext ? steamapicontext->SteamFriends() : NULL;
	if ( id.empty() || !pFriends || pFriends->GetLargeFriendAvatar( CSteamID( (uint64)V_atoui64( id.c_str() ) ) ) <= 0 )
		return std::string();
	return "avatar:" + id;
}

bool IsMvM()
{
	return TFGameRules() && TFGameRules()->IsMannVsMachineMode();
}

//=============================================================================
// The scoreboard (CTFClientScoreBoardDialog).
//=============================================================================
bool s_bScores = false;
bool s_bScoresMouse = false;

void WritePlayerRow( int i, int iLocalTeam, C_TFPlayer *pLocal, Node &row )
{
	const int iTeam = g_PR->GetTeam( i );
	bool bAlive = g_TF_PR->IsAlive( i );
	C_TFPlayer *pPlayer = ToTFPlayer( UTIL_PlayerByIndex( i ) );
	// A spy feigning death is dead on the scoreboard too.
	if ( pPlayer && g_TF_PR->GetPlayerClass( i ) == TF_CLASS_SPY && pPlayer->m_Shared.InCond( TF_COND_FEIGN_DEATH ) )
		bAlive = false;

	row[ "idx" ] = Node( i );
	row[ "id" ] = Node( SteamID( i ) );
	row[ "name" ] = Node( g_TF_PR->GetPlayerName( i ) );
	row[ "team" ] = Node( TeamName( iTeam ) );
	row[ "score" ] = Node( g_TF_PR->GetTotalScore( i ) );
	row[ "kills" ] = Node( g_TF_PR->GetPlayerScore( i ) );
	row[ "deaths" ] = Node( g_TF_PR->GetDeaths( i ) );
	row[ "damage" ] = Node( g_TF_PR->GetDamage( i ) );
	row[ "healing" ] = Node( g_TF_PR->GetHealing( i ) );
	row[ "alive" ] = Node( bAlive );
	row[ "me" ] = Node( i == GetLocalPlayerIndex() );
	row[ "bot" ] = Node( g_PR->IsFakePlayer( i ) );
	row[ "ping" ] = Node( g_PR->IsFakePlayer( i ) ? -1 : g_PR->GetPing( i ) );
	row[ "doms" ] = Node( g_TF_PR->GetActiveDominations( i ) );
	row[ "streak" ] = Node( g_TF_PR->GetStreak( i, CTFPlayerShared::kTFStreak_Kills ) );
	row[ "avatar" ] = Node( Avatar( i ) );
	row[ "muted" ] = Node( GetClientVoiceMgr() && GetClientVoiceMgr()->IsPlayerBlocked( i ) );

	// Classes of the other team stay hidden, as in TF2.
	std::string cls;
	if ( !AreEnemyTeams( iTeam, iLocalTeam ) && iLocalTeam != TEAM_UNASSIGNED )
	{
		int iClass = g_TF_PR->GetPlayerClass( i );
		// Dead and about to come back as something else: show that.
		if ( i == GetLocalPlayerIndex() && !bAlive && pLocal && pLocal->m_Shared.GetDesiredPlayerClassIndex() != TF_CLASS_RANDOM )
			iClass = pLocal->m_Shared.GetDesiredPlayerClassIndex();
		cls = ClassName( iClass );
	}
	row[ "cls" ] = Node( cls );

	// Who dominates whom, against you.
	std::string rivalry;
	if ( pLocal && pPlayer && pPlayer != pLocal )
	{
		if ( pPlayer->m_Shared.IsPlayerDominated( pLocal->entindex() ) )
			rivalry = "nemesis";
		else if ( pLocal->m_Shared.IsPlayerDominated( i ) )
			rivalry = "dominated";
	}
	row[ "rivalry" ] = Node( rivalry );
}

void WriteScores( Node &out, bool bSummary )
{
	out[ "shown" ] = Node( s_bScores || bSummary );
	out[ "summary" ] = Node( bSummary );
	out[ "mouse" ] = Node( s_bScoresMouse && !bSummary );
	// The match is over: who won it.
	const int iWinner = bSummary && TFGameRules() ? TFGameRules()->GetWinningTeam() : TEAM_UNASSIGNED;
	out[ "winner" ] = Node( iWinner >= FIRST_GAME_TEAM ? TeamName( iWinner ) : "" );
	out[ "mine" ] = Node( iWinner >= FIRST_GAME_TEAM && iWinner == GetLocalPlayerTeam() );
	if ( !( s_bScores || bSummary ) || !g_TF_PR )
		return;

	C_TFPlayer *pLocal = C_TFPlayer::GetLocalTFPlayer();
	const int iLocalTeam = GetLocalPlayerTeam();
	Node &red = out[ "red" ], &blu = out[ "blu" ], &spec = out[ "spec" ];
	red[ "players" ].SetArray();
	blu[ "players" ].SetArray();
	spec.SetArray();
	for ( int i = 1; i <= gpGlobals->maxClients; ++i )
	{
		if ( !g_PR->IsConnected( i ) )
			continue;
		const int iTeam = frontress::DisplayTeam( g_PR->GetTeam( i ) );
		if ( iTeam == TF_TEAM_RED || iTeam == TF_TEAM_BLUE )
			WritePlayerRow( i, iLocalTeam, pLocal, ( iTeam == TF_TEAM_RED ? red : blu )[ "players" ].Append() );
		else if ( iTeam == TEAM_SPECTATOR || g_TF_PR->IsArenaSpectator( i ) )
			spec.Append( Node( g_TF_PR->GetPlayerName( i ) ) );
	}
	// Best first, like the stock lists.
	for ( Node *pList : { &red[ "players" ], &blu[ "players" ] } )
	{
		std::vector< Node > rows;
		for ( size_t r = 0; r < pList->Size(); ++r )
			rows.push_back( std::move( *pList->At( r ) ) );
		std::stable_sort( rows.begin(), rows.end(), []( const Node &a, const Node &b ) {
			return a.Find( "score" )->AsNumber() > b.Find( "score" )->AsNumber();
		} );
		pList->SetArray();
		for ( Node &row : rows )
			pList->Append( std::move( row ) );
	}
	for ( int iTeam : { TF_TEAM_RED, TF_TEAM_BLUE } )
	{
		C_TFTeam *pTeam = GetGlobalTFTeam( frontress::DisplayTeam( iTeam ) );
		Node &t = iTeam == TF_TEAM_RED ? red : blu;
		t[ "score" ] = Node( pTeam ? pTeam->Get_Score() : 0 );
		t[ "count" ] = Node( (int)t[ "players" ].Size() );
	}

	// Time left on the map.
	int iTimeLeft = -1;
	static ConVarRef mp_timelimit( "mp_timelimit" );
	if ( TFGameRules() && mp_timelimit.IsValid() && mp_timelimit.GetInt() > 0 )
		iTimeLeft = Max( 0, TFGameRules()->GetTimeLeft() );
	out[ "time_left" ] = Node( iTimeLeft );

	// Your own round, as the stock panel's bottom row has it.
	Node &me = out[ "me" ];
	const int iMe = GetLocalPlayerIndex();
	if ( pLocal )
	{
		CTFPlayerShared &s = pLocal->m_Shared;
		me[ "kills" ] = Node( g_TF_PR->GetPlayerScore( iMe ) );
		me[ "deaths" ] = Node( g_TF_PR->GetDeaths( iMe ) );
		me[ "assists" ] = Node( s.GetKillAssists( iMe ) );
		me[ "destruction" ] = Node( s.GetBuildingsDestroyed( iMe ) );
		me[ "captures" ] = Node( s.GetCaptures( iMe ) );
		me[ "defenses" ] = Node( s.GetDefenses( iMe ) );
		me[ "dominations" ] = Node( s.GetDominations( iMe ) );
		me[ "revenge" ] = Node( s.GetRevenge( iMe ) );
		me[ "healing" ] = Node( s.GetHealPoints( iMe ) );
		me[ "invulns" ] = Node( s.GetInvulns( iMe ) );
		me[ "teleports" ] = Node( s.GetTeleports( iMe ) );
		me[ "headshots" ] = Node( s.GetHeadshots( iMe ) );
		me[ "backstabs" ] = Node( s.GetBackstabs( iMe ) );
		me[ "bonus" ] = Node( s.GetBonusPoints( iMe ) );
		me[ "damage" ] = Node( g_TF_PR->GetDamage( iMe ) );
	}
}

//=============================================================================
// The end of a round (CTFWinPanel, CTFArenaWinPanel) and of the match
// (CTFMatchSummary): who won, why, and who carried it.
//=============================================================================
class CRoundEnd : public CGameEventListener
{
public:
	void Listen()
	{
		if ( m_bListening )
			return;
		m_bListening = true;
		ListenForGameEvent( "teamplay_win_panel" );
		ListenForGameEvent( "arena_win_panel" );
		ListenForGameEvent( "teamplay_round_start" );
		ListenForGameEvent( "teamplay_game_over" );
		ListenForGameEvent( "tf_game_over" );
		ListenForGameEvent( "training_complete" );
		ListenForGameEvent( "show_match_summary" );
	}

	virtual void FireGameEvent( IGameEvent *event ) OVERRIDE
	{
		const char *pszName = event->GetName();
		if ( V_strcmp( pszName, "teamplay_win_panel" ) && V_strcmp( pszName, "arena_win_panel" ) )
		{
			m_bShown = false;
			return;
		}
		if ( IsMvM() || !g_TF_PR )
			return;

		m_bShown = true;
		const int iWinner = event->GetInt( "winning_team" );
		const int iReason = event->GetInt( "winreason" );
		m_iWinner = iWinner;
		m_bComplete = V_strcmp( pszName, "arena_win_panel" ) ? event->GetInt( "round_complete" ) != 0 : true;
		m_strReason = Reason( iReason, iWinner, event->GetInt( "flagcaplimit" ) );
		m_iScoreRed = event->GetInt( frontress::UniformSwapped() ? "blue_score" : "red_score" );
		m_iScoreBlu = event->GetInt( frontress::UniformSwapped() ? "red_score" : "blue_score" );

		// Who took the last points or flags.
		m_strCappers.clear();
		if ( iReason == WINREASON_ALL_POINTS_CAPTURED || iReason == WINREASON_FLAG_CAPTURE_LIMIT )
		{
			const char *pszCappers = event->GetString( "cappers" );
			for ( int i = 0; pszCappers && pszCappers[ i ]; ++i )
			{
				const int iPlayer = (unsigned char)pszCappers[ i ];
				if ( iPlayer <= 0 || iPlayer > MAX_PLAYERS || !g_PR->IsConnected( iPlayer ) )
					continue;
				if ( !m_strCappers.empty() )
					m_strCappers += ", ";
				m_strCappers += g_PR->GetPlayerName( iPlayer );
			}
		}

		// The best three of the round, and the longest streak.
		m_Top.clear();
		for ( int i = 1; i <= 3; ++i )
		{
			const int iPlayer = event->GetInt( CFmtStr( "player_%d", i ), 0 );
			const int iPoints = event->GetInt( CFmtStr( "player_%d_points", i ), 0 );
			if ( iPoints <= 0 || iPlayer <= 0 || !g_PR->IsConnected( iPlayer ) )
				continue;
			Top_t top;
			top.iPlayer = iPlayer;
			top.iPoints = iPoints;
			top.strName = g_PR->GetPlayerName( iPlayer );
			top.iTeam = g_PR->GetTeam( iPlayer );
			top.iClass = g_TF_PR->GetPlayerClass( iPlayer );
			m_Top.push_back( top );
		}
		m_strStreak.clear();
		m_nStreak = event->GetInt( "killstreak_player_1_count", 0 );
		const int iStreaker = event->GetInt( "killstreak_player_1", 0 );
		if ( m_nStreak > 0 && iStreaker > 0 && g_PR->IsConnected( iStreaker ) )
			m_strStreak = g_PR->GetPlayerName( iStreaker );
	}

	void Write( Node &out )
	{
		const bool bShown = m_bShown && !( TFGameRules() && TFGameRules()->ShowMatchSummary() );
		out[ "shown" ] = Node( bShown );
		if ( !bShown )
			return;
		out[ "winner" ] = Node( m_iWinner >= FIRST_GAME_TEAM ? TeamName( m_iWinner ) : "" );
		out[ "mine" ] = Node( m_iWinner >= FIRST_GAME_TEAM && m_iWinner == GetLocalPlayerTeam() );
		out[ "complete" ] = Node( m_bComplete );
		out[ "reason" ] = Node( m_strReason );
		out[ "cappers" ] = Node( m_strCappers );
		out[ "score_red" ] = Node( m_iScoreRed );
		out[ "score_blu" ] = Node( m_iScoreBlu );
		Node &top = out[ "top" ];
		top.SetArray();
		for ( const Top_t &t : m_Top )
		{
			Node &n = top.Append();
			n[ "name" ] = Node( t.strName );
			n[ "team" ] = Node( TeamName( t.iTeam ) );
			n[ "cls" ] = Node( ClassName( t.iClass ) );
			n[ "points" ] = Node( t.iPoints );
			n[ "avatar" ] = Node( Avatar( t.iPlayer ) );
		}
		out[ "streak_name" ] = Node( m_strStreak );
		out[ "streak" ] = Node( m_nStreak );
	}

	void Clear() { m_bShown = false; }

private:
	// Why the round went the way it did, in the game's own words.
	static std::string Reason( int iReason, int iWinner, int iFlagCapLimit )
	{
		const wchar_t *pwszTeam = g_pVGuiLocalize->Find( frontress::DisplayTeam( iWinner ) == TF_TEAM_BLUE ? "#TF_BlueTeam_Name" : "#TF_RedTeam_Name" );
		wchar_t wszCount[ 16 ];
		V_snwprintf( wszCount, ARRAYSIZE( wszCount ), L"%d", iFlagCapLimit );
		switch ( iReason )
		{
		case WINREASON_ALL_POINTS_CAPTURED:
			return Construct( TFGameRules() && TFGameRules()->HasMultipleTrains() ? "#Winreason_PayloadRace" : "#Winreason_AllPointsCaptured", pwszTeam );
		case WINREASON_FLAG_CAPTURE_LIMIT:
			return Construct( iFlagCapLimit == 1 ? "#Winreason_FlagCaptureLimit_One" : "#Winreason_FlagCaptureLimit", pwszTeam, wszCount );
		case WINREASON_OPPONENTS_DEAD: return Construct( "#Winreason_OpponentsDead", pwszTeam );
		case WINREASON_DEFEND_UNTIL_TIME_LIMIT: return Construct( "#Winreason_DefendedUntilTimeLimit", pwszTeam );
		case WINREASON_STALEMATE: return Construct( "#Winreason_Stalemate" );
		case WINREASON_TIMELIMIT: return Construct( "#Winreason_TimeLimit", pwszTeam );
		case WINREASON_WINLIMIT: return Construct( "#Winreason_WinLimit", pwszTeam );
		case WINREASON_WINDIFFLIMIT: return Construct( "#Winreason_WinDiffLimit", pwszTeam );
		case WINREASON_RD_REACTOR_CAPTURED: return Construct( "#Winreason_ReactorCaptured", pwszTeam );
		case WINREASON_RD_CORES_COLLECTED: return Construct( "#Winreason_CoresCollected", pwszTeam );
		case WINREASON_RD_REACTOR_RETURNED: return Construct( "#Winreason_ReactorReturned", pwszTeam );
		case WINREASON_PD_POINTS: return Construct( "#Winreason_PlayerDestructionPoints", pwszTeam );
		case WINREASON_SCORED:
			return Construct( iFlagCapLimit == 1 ? "#Winreason_ScoreLimit_One" : "#Winreason_ScoreLimit", pwszTeam, wszCount );
		default: return std::string();
		}
	}

	struct Top_t
	{
		int iPlayer, iPoints, iTeam, iClass;
		std::string strName;
	};
	std::vector< Top_t > m_Top;
	std::string m_strReason, m_strCappers, m_strStreak;
	int m_iWinner = 0, m_iScoreRed = 0, m_iScoreBlu = 0, m_nStreak = 0;
	bool m_bComplete = false, m_bShown = false, m_bListening = false;
};

CRoundEnd s_RoundEnd;

//=============================================================================
// Who killed you (CTFFreezePanel): shown over the freeze frame.
//=============================================================================
class CKiller : public CGameEventListener
{
public:
	void Listen()
	{
		if ( m_bListening )
			return;
		m_bListening = true;
		ListenForGameEvent( "player_death" );
		ListenForGameEvent( "show_freezepanel" );
		ListenForGameEvent( "hide_freezepanel" );
		ListenForGameEvent( "teamplay_win_panel" );
		ListenForGameEvent( "training_complete" );
		ListenForGameEvent( "localplayer_respawn" );
	}

	virtual void FireGameEvent( IGameEvent *event ) OVERRIDE
	{
		const char *pszName = event->GetName();
		if ( !V_strcmp( pszName, "player_death" ) )
		{
			C_TFPlayer *pLocal = C_TFPlayer::GetLocalTFPlayer();
			if ( !pLocal || engine->GetPlayerForUserID( event->GetInt( "userid" ) ) != pLocal->entindex() )
				return;
			const int iFlags = event->GetInt( "death_flags" );
			m_strRivalry = ( iFlags & TF_DEATH_DOMINATION ) ? "nemesis" : ( iFlags & TF_DEATH_REVENGE ) ? "revenge" : "";
			m_strWeapon.clear();
			m_strWeaponIcon.clear();
			const int iDef = event->GetInt( "weapon_def_index", INVALID_ITEM_DEF_INDEX );
			if ( const CEconItemDefinition *pDef = iDef != INVALID_ITEM_DEF_INDEX ? GetItemSchema()->GetItemDefinition( iDef ) : NULL )
			{
				m_strWeapon = UTF8( g_pVGuiLocalize->Find( pDef->GetItemBaseName() ) );
				if ( pDef->GetInventoryImage() && pDef->GetInventoryImage()[ 0 ] )
					m_strWeaponIcon = std::string( "tex:" ) + pDef->GetInventoryImage() + "_large";
			}
		}
		else if ( !V_strcmp( pszName, "show_freezepanel" ) )
		{
			m_iKiller = event->GetInt( "killer" );
			m_bShown = m_iKiller > 0;
		}
		else
		{
			m_bShown = false;
		}
	}

	void Write( Node &out )
	{
		C_TFPlayer *pLocal = C_TFPlayer::GetLocalTFPlayer();
		const bool bShown = m_bShown && pLocal && !pLocal->IsAlive();
		out[ "shown" ] = Node( bShown );
		if ( !bShown || !g_TF_PR )
			return;

		// The killer, or the owner of the building that did it.
		C_BaseEntity *pKiller = ClientEntityList().GetBaseEntity( m_iKiller );
		std::string object;
		int iPlayer = m_iKiller;
		if ( pKiller && pKiller->IsBaseObject() )
		{
			C_BaseObject *pObj = assert_cast< C_BaseObject * >( pKiller );
			object = pObj->GetType() == OBJ_SENTRYGUN ? "sentry" : pObj->GetType() == OBJ_DISPENSER ? "dispenser" : "teleporter";
			iPlayer = pObj->GetOwner() ? pObj->GetOwner()->entindex() : 0;
		}
		const bool bPlayer = iPlayer > 0 && iPlayer <= MAX_PLAYERS && g_PR->IsConnected( iPlayer );
		out[ "self" ] = Node( iPlayer == pLocal->entindex() );
		out[ "object" ] = Node( object );
		out[ "name" ] = Node( bPlayer ? g_PR->GetPlayerName( iPlayer ) : "" );
		out[ "team" ] = Node( bPlayer ? TeamName( g_PR->GetTeam( iPlayer ) ) : "" );
		out[ "cls" ] = Node( bPlayer ? ClassName( g_TF_PR->GetPlayerClass( iPlayer ) ) : "" );
		out[ "avatar" ] = Node( bPlayer ? Avatar( iPlayer ) : "" );
		const int iHealth = pKiller ? Max( 0, pKiller->GetHealth() ) : 0;
		const int iMax = pKiller ? Max( 1, pKiller->GetMaxHealth() ) : 1;
		out[ "health" ] = Node( iHealth );
		out[ "max" ] = Node( iMax );
		out[ "frac" ] = Node( Min( 1.0, (double)iHealth / iMax ) );
		out[ "weapon" ] = Node( object.empty() ? m_strWeapon : std::string() );
		out[ "weapon_icon" ] = Node( object.empty() ? m_strWeaponIcon : std::string() );
		out[ "rivalry" ] = Node( m_strRivalry );
	}

	void Clear() { m_bShown = false; }

private:
	std::string m_strRivalry, m_strWeapon, m_strWeaponIcon;
	int m_iKiller = 0;
	bool m_bShown = false, m_bListening = false;
};

CKiller s_Killer;

//=============================================================================
// The spectator view (CTFSpectatorGUI): whom you watch and how.
//=============================================================================
bool s_bSpectator = false;

void WriteSpectator( Node &out )
{
	C_TFPlayer *pLocal = C_TFPlayer::GetLocalTFPlayer();
	const bool bShown = s_bSpectator && pLocal && pLocal->IsObserver() &&
		pLocal->GetObserverMode() != OBS_MODE_FREEZECAM && pLocal->GetObserverMode() != OBS_MODE_DEATHCAM;
	out[ "shown" ] = Node( bShown );
	if ( !bShown || !g_TF_PR )
		return;

	const char *pszMode = "";
	switch ( pLocal->GetObserverMode() )
	{
	case OBS_MODE_IN_EYE: pszMode = "eyes"; break;
	case OBS_MODE_CHASE: pszMode = "chase"; break;
	case OBS_MODE_ROAMING: pszMode = "free"; break;
	case OBS_MODE_FIXED: pszMode = "fixed"; break;
	default: break;
	}
	out[ "mode" ] = Node( pszMode );
	out[ "team_spec" ] = Node( pLocal->GetTeamNumber() == TEAM_SPECTATOR );

	C_BaseEntity *pTarget = pLocal->GetObserverTarget();
	Node &t = out[ "target" ];
	std::string object;
	int iPlayer = 0;
	if ( pTarget && pTarget->IsPlayer() && pTarget != pLocal )
		iPlayer = pTarget->entindex();
	else if ( pTarget && pTarget->IsBaseObject() )
	{
		C_BaseObject *pObj = assert_cast< C_BaseObject * >( pTarget );
		object = pObj->GetType() == OBJ_SENTRYGUN ? "sentry" : pObj->GetType() == OBJ_DISPENSER ? "dispenser" : "teleporter";
		iPlayer = pObj->GetOwner() ? pObj->GetOwner()->entindex() : 0;
	}
	const bool bPlayer = iPlayer > 0 && iPlayer <= MAX_PLAYERS && g_PR->IsConnected( iPlayer );
	t[ "on" ] = Node( bPlayer );
	t[ "object" ] = Node( object );
	t[ "name" ] = Node( bPlayer ? g_PR->GetPlayerName( iPlayer ) : "" );
	t[ "team" ] = Node( bPlayer ? TeamName( g_PR->GetTeam( iPlayer ) ) : "" );
	// Classes of the other team are no secret to a spectator, as in TF2.
	t[ "cls" ] = Node( bPlayer ? ClassName( g_TF_PR->GetPlayerClass( iPlayer ) ) : "" );
	t[ "avatar" ] = Node( bPlayer ? Avatar( iPlayer ) : "" );
	const int iHealth = pTarget ? Max( 0, pTarget->GetHealth() ) : 0;
	const int iMax = pTarget ? Max( 1, pTarget->GetMaxHealth() ) : 1;
	t[ "health" ] = Node( iHealth );
	t[ "max" ] = Node( iMax );
	t[ "frac" ] = Node( Min( 1.0, (double)iHealth / iMax ) );
}

//=============================================================================
// The game's notices: "you can't change class now", teams switched, sudden
// death, arena, streaks... Each stock notice panel still decides what to say
// and when (its events, its strings); its text is read off its labels.
//=============================================================================
void CollectLabels( vgui::Panel *pPanel, Node &lines )
{
	for ( int i = 0; i < pPanel->GetChildCount(); ++i )
	{
		vgui::Panel *pChild = pPanel->GetChild( i );
		if ( !pChild || !pChild->IsVisible() )
			continue;
		if ( vgui::Label *pLabel = dynamic_cast< vgui::Label * >( pChild ) )
		{
			wchar_t wsz[ 512 ];
			pLabel->GetText( wsz, sizeof( wsz ) );
			std::string text = UTF8( wsz );
			// Trim; skip empty labels and repeats (a text and its shadow).
			while ( !text.empty() && ( text.back() == ' ' || text.back() == '\n' ) )
				text.pop_back();
			size_t nStart = text.find_first_not_of( " \n" );
			if ( nStart == std::string::npos )
				continue;
			text = text.substr( nStart );
			bool bSeen = false;
			for ( size_t n = 0; n < lines.Size() && !bSeen; ++n )
				bSeen = lines.At( n )->AsString() == text;
			if ( !bSeen )
				lines.Append( Node( text ) );
		}
		CollectLabels( pChild, lines );
	}
}

void WriteNotices( Node &out )
{
	out.SetArray();
	if ( !FrontressHudReplaces( "notices" ) )
		return;
	static const struct
	{
		const char *pszElement;
		const char *pszKind;
	} s_Notices[] = {
		{ "CGamePausePanel", "pause" },
		{ "CHudStalemate", "stalemate" },
		{ "CHudTeamSwitch", "teamswitch" },
		{ "CHudArenaCapPointCountdown", "countdown" },
		{ "CHudArenaNotification", "arena" },
		{ "CHudAlert", "alert" },
		{ "CHudNotificationPanel", "notify" },
		{ "CStrandedSpawnPanel", "notify" },
		{ "CTFStreakNotice", "streak" },
		{ "CTFStatPanel", "record" },
	};
	for ( const auto &notice : s_Notices )
	{
		// The stock panel still shows and hides itself (it only does not
		// paint, fui_paint.h): it is up when it says it is visible.
		CHudElement *pElement = gHUD.FindElement( notice.pszElement );
		vgui::Panel *pPanel = dynamic_cast< vgui::Panel * >( pElement );
		if ( !pElement || !pPanel || !pPanel->IsVisible() )
			continue;
		Node n;
		n[ "kind" ] = Node( notice.pszKind );
		Node &lines = n[ "lines" ];
		lines.SetArray();
		CollectLabels( pPanel, lines );
		if ( lines.Size() )
			out.Append( std::move( n ) );
	}
}

//=============================================================================
// Votes (CHudVotePanel keeps the vote; this reads it).
//=============================================================================
void WriteVotes( Node &out )
{
	out.SetArray();
	CHudVote *pHudVote = GET_HUDELEMENT( CHudVote );
	if ( !pHudVote || !FrontressHudReplaces( "vote" ) )
		return;
	for ( int i = 0; i < NUM_VOTE_PANELS; ++i )
	{
		CHudVotePanel *pPanel = pHudVote->GetVotePanel( i );
		FrontressVote_t vote;
		if ( !pPanel || !pPanel->GetFrontressVote( vote ) )
			continue;
		Node &v = out.Append();
		v[ "header" ] = Node( UTF8( vote.wszHeader ) );
		v[ "issue" ] = Node( UTF8( vote.wszIssue ) );
		v[ "result" ] = Node( vote.iResult == 1 ? "passed" : vote.iResult == 2 ? "failed" : "" );
		v[ "result_text" ] = Node( UTF8( vote.wszResult ) );
		v[ "voted" ] = Node( vote.bVoted );
		v[ "input" ] = Node( vote.bInput );
		v[ "yesno" ] = Node( vote.bYesNo );
		v[ "needed" ] = Node( vote.nPotential );
		Node &options = v[ "options" ];
		options.SetArray();
		for ( int o = 0; o < vote.nOptions; ++o )
		{
			Node &opt = options.Append();
			opt[ "label" ] = Node( UTF8( vote.wszOptions[ o ] ) );
			opt[ "count" ] = Node( vote.nCounts[ o ] );
			opt[ "key" ] = Node( CFmtStr( "F%d", o + 1 ).Get() );
		}
	}
}

//=============================================================================
// Chat (CHudChat). Lines come from CBaseHudChat::ChatPrintf, which still does
// the filtering and muting; typing is a text field in the HUD.
//=============================================================================
struct ChatSeg_t
{
	std::string text, color;
};
struct ChatLine_t
{
	std::vector< ChatSeg_t > segs;
	double flAt;
};
std::deque< ChatLine_t > s_Chat;
int s_iChatMode = 0;			// 0: closed, else MM_SAY / MM_SAY_TEAM / MM_SAY_PARTY
bool s_bChatFocus = false;		// the text field still has to take the keyboard
const size_t k_nChatKeep = 60;

const char *TeamChatColor( int iPlayer )
{
	if ( iPlayer <= 0 || !g_PR )
		return "#99ff99";	// the console
	switch ( frontress::DisplayTeam( g_PR->GetTeam( iPlayer ) ) )
	{
	case TF_TEAM_RED: return "#ff3f3f";
	case TF_TEAM_BLUE: return "#99ccff";
	default: return "#cccccc";
	}
}

// ChatPrintf's colour codes: 1 normal, 3 the player's team, 4 location,
// 5 achievement, 7RRGGBB and 8RRGGBBAA any colour.
void ParseChat( int iPlayer, const char *psz, std::vector< ChatSeg_t > &segs )
{
	std::string color, text;
	auto flush = [ & ]()
	{
		if ( !text.empty() )
			segs.push_back( { text, color } );
		text.clear();
	};
	for ( const char *p = psz; *p; ++p )
	{
		const unsigned char c = (unsigned char)*p;
		if ( c == COLOR_NORMAL || c == 2 || c == COLOR_CUSTOM )
		{
			flush();
			color.clear();
		}
		else if ( c == COLOR_PLAYERNAME )
		{
			flush();
			color = TeamChatColor( iPlayer );
		}
		else if ( c == COLOR_LOCATION )
		{
			flush();
			color = "#99ff99";
		}
		else if ( c == COLOR_ACHIEVEMENT )
		{
			flush();
			color = "#cfb53b";
		}
		else if ( ( c == COLOR_HEXCODE || c == COLOR_HEXCODE_ALPHA ) )
		{
			const int nLen = c == COLOR_HEXCODE ? 6 : 8;
			bool bOK = true;
			for ( int i = 1; i <= nLen && bOK; ++i )
				bOK = p[ i ] && V_isxdigit( p[ i ] );
			if ( !bOK )
				continue;
			flush();
			color = "#" + std::string( p + 1, 6 );
			p += nLen;
		}
		else if ( c == '\n' )
			text += ' ';
		else if ( c >= 32 || c >= 0x80 )
			text += (char)c;
	}
	flush();
}

void WriteChat( Node &out )
{
	static ConVarRef hud_saytext_time( "hud_saytext_time" );
	const double flShowFor = hud_saytext_time.IsValid() ? hud_saytext_time.GetFloat() : 12.0;
	const double flNow = Plat_FloatTime();
	out[ "open" ] = Node( s_iChatMode != 0 );
	out[ "mode" ] = Node( s_iChatMode == MM_SAY_TEAM ? "team" : s_iChatMode == MM_SAY_PARTY ? "party" : "all" );
	Node &lines = out[ "lines" ];
	lines.SetArray();
	// Open: the history; closed: what was said lately, fading out.
	const size_t nFirst = s_iChatMode ? ( s_Chat.size() > 30 ? s_Chat.size() - 30 : 0 ) : ( s_Chat.size() > 8 ? s_Chat.size() - 8 : 0 );
	for ( size_t i = nFirst; i < s_Chat.size(); ++i )
	{
		const ChatLine_t &line = s_Chat[ i ];
		const double flAge = flNow - line.flAt;
		if ( !s_iChatMode && flAge > flShowFor )
			continue;
		Node &l = lines.Append();
		l[ "fade" ] = Node( !s_iChatMode && flAge > flShowFor - 2.0 );
		Node &segs = l[ "segs" ];
		segs.SetArray();
		for ( const ChatSeg_t &seg : line.segs )
		{
			Node &n = segs.Append();
			n[ "t" ] = Node( seg.text );
			n[ "c" ] = Node( seg.color );
		}
	}
}

void SendChat( const std::string &raw )
{
	// One line, no quotes (they would end the say argument early).
	std::string text;
	for ( char c : raw )
		text += ( c == '"' ) ? '\'' : ( c == '\n' || c == '\r' ) ? ' ' : c;
	size_t nStart = text.find_first_not_of( ' ' );
	if ( nStart == std::string::npos )
		return;
	text = text.substr( nStart );
	const char *pszCmd = s_iChatMode == MM_SAY_TEAM ? "say_team" : s_iChatMode == MM_SAY_PARTY ? "say_party" : "say";
	engine->ClientCmd_Unrestricted( CFmtStr1024( "%s \"%s\"\n", pszCmd, text.substr( 0, 127 ).c_str() ) );
}

} // namespace

//=============================================================================
// fui_hud.h
//=============================================================================
bool FuiHudShowScores( bool bShow )
{
	if ( !FrontressUI() || !FrontressHudReplaces( "scoreboard" ) || IsMvM() )
	{
		s_bScores = s_bScoresMouse = false;
		return false;
	}
	s_bScores = bShow;
	if ( !bShow )
		s_bScoresMouse = false;
	return true;
}

bool FuiHudScoresShown()
{
	return s_bScores;
}

bool FuiHudScoresClick( int down, ButtonCode_t keynum )
{
	if ( !s_bScores || !down || s_bScoresMouse || tf_scoreboard_mouse_mode.GetInt() != 2 )
		return false;
	if ( keynum != MOUSE_LEFT && keynum != MOUSE_RIGHT )
		return false;
	s_bScoresMouse = true;
	return true;
}

bool FuiHudShowSpectator( bool bShow )
{
	if ( !FrontressUI() || !FrontressHudReplaces( "spectator" ) || IsMvM() )
	{
		s_bSpectator = false;
		return false;
	}
	s_bSpectator = bShow;
	return true;
}

bool FuiHudStartChat( int iMode )
{
	if ( !FrontressUI() || !FrontressHudReplaces( "chat" ) || enginevgui->IsGameUIVisible() )
		return false;
	s_iChatMode = iMode;
	s_bChatFocus = true;
	if ( fui::Runtime *pRuntime = FrontressUI() )
		pRuntime->Set( "ui.say_draft", Node( "" ) );
	return true;
}

void FuiHudChatLine( int iPlayer, const char *pszLine )
{
	if ( !pszLine || !pszLine[ 0 ] )
		return;
	ChatLine_t line;
	line.flAt = Plat_FloatTime();
	ParseChat( iPlayer, pszLine, line.segs );
	if ( line.segs.empty() )
		return;
	s_Chat.push_back( std::move( line ) );
	while ( s_Chat.size() > k_nChatKeep )
		s_Chat.pop_front();
}

bool FuiHudWantsKeyboard()
{
	return s_iChatMode != 0;
}

bool FuiHudWantsMouse()
{
	return s_iChatMode != 0 || ( s_bScores && s_bScoresMouse );
}

bool FuiHudCloseChat()
{
	if ( !s_iChatMode )
		return false;
	s_iChatMode = 0;
	s_bChatFocus = false;
	if ( fui::Runtime *pRuntime = FrontressUI() )
	{
		pRuntime->Set( "ui.say_draft", Node( "" ) );
		if ( pRuntime->Context() && pRuntime->Context()->GetFocusElement() )
			pRuntime->Context()->GetFocusElement()->Blur();
	}
	return true;
}

bool FuiHudCommand( fui::Runtime &runtime, const std::string &name, const std::vector< std::string > &args )
{
	const std::string arg = args.empty() ? std::string() : args[ 0 ];
	if ( name == "say_input" )
	{
		// The text field's change event: Enter sends.
		if ( arg == "1" || arg == "true" )
		{
			const Node *pDraft = runtime.Get( "ui.say_draft" );
			SendChat( args.size() > 1 ? args[ 1 ] : pDraft ? pDraft->AsString() : std::string() );
			FuiHudCloseChat();
		}
		return true;
	}
	if ( name == "say_close" )
	{
		FuiHudCloseChat();
		return true;
	}
	if ( name == "scores_pick" )
	{
		// A player picked on the scoreboard: the report dialog, with them.
		if ( !arg.empty() )
			engine->ClientCmd_Unrestricted( CFmtStr( "fr_report %s 1\n", arg.c_str() ) );
		s_bScores = s_bScoresMouse = false;
		return true;
	}
	if ( name == "scores_mute" )
	{
		const int iPlayer = V_atoi( arg.c_str() );
		if ( iPlayer > 0 && iPlayer <= MAX_PLAYERS && GetClientVoiceMgr() && iPlayer != GetLocalPlayerIndex() )
			GetClientVoiceMgr()->SetPlayerBlockedState( iPlayer, !GetClientVoiceMgr()->IsPlayerBlocked( iPlayer ) );
		return true;
	}
	return false;
}

void FuiHudOverlayUpdate( fui::Runtime &runtime, Node &hud )
{
	s_RoundEnd.Listen();
	s_Killer.Listen();

	const bool bSummary = TFGameRules() && TFGameRules()->ShowMatchSummary() && FrontressHudReplaces( "winpanel" ) && !IsMvM();
	WriteScores( hud[ "scores" ], bSummary );
	s_RoundEnd.Write( hud[ "round" ] );
	s_Killer.Write( hud[ "killer" ] );
	WriteSpectator( hud[ "spec" ] );
	WriteNotices( hud[ "notices" ] );
	WriteVotes( hud[ "votes" ] );
	WriteChat( hud[ "chat" ] );

	// The chat field takes the keyboard once it is on screen.
	if ( s_bChatFocus && runtime.FocusHud( "say" ) )
		s_bChatFocus = false;
}

//-----------------------------------------------------------------------------
// Stock viewport panels that opened before the HUD could say it draws them
// (joining, a map change): asked to show again, they go through the hooks
// (FuiHudShowSpectator, FuiHudShowScores) and hide.
//-----------------------------------------------------------------------------
void FuiHudOverlayHideStock()
{
	if ( !gViewPortInterface || IsMvM() )
		return;
	static const struct
	{
		const char *pszPanel;
		const char *pszPart;
	} s_Panels[] = {
		{ PANEL_SPECGUI, "spectator" },
		{ PANEL_SCOREBOARD, "scoreboard" },
	};
	for ( const auto &p : s_Panels )
	{
		IViewPortPanel *pPanel = gViewPortInterface->FindPanelByName( p.pszPanel );
		if ( pPanel && pPanel->IsVisible() && FrontressHudReplaces( p.pszPart ) )
			gViewPortInterface->ShowPanel( pPanel, true );
	}
}

void FuiHudOverlayReset()
{
	s_bScores = s_bScoresMouse = false;
	s_bSpectator = false;
	s_iChatMode = 0;
	s_bChatFocus = false;
	s_RoundEnd.Clear();
	s_Killer.Clear();
	s_Chat.clear();
}
