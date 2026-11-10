//========= Team Frontress ====================================================//
//
// In-match windows of the Frontress menu and the class menus of its HUD.
// See fui_ingame.h.
//
//=============================================================================
#include "cbase.h"

#pragma push_macro( "Assert" )
#undef Assert
#include "tier0/valve_minmax_off.h"
#include "fui.h"
#include "tier0/valve_minmax_on.h"
#pragma pop_macro( "Assert" )

#include "fui_ingame.h"
#include "fui_host.h"
#include "fui_hud.h"
#include "tf/frontress_uniform.h"

#include "c_tf_player.h"
#include "c_tf_playerresource.h"
#include "hud.h"
#include "hud_vote.h"
#include "ienginevgui.h"
#include "tf_gamerules.h"
#include "tf_hud_menu_engy_build.h"
#include "tf_hud_menu_engy_destroy.h"
#include "tf_hud_menu_spy_disguise.h"
#include "steam/steam_api.h"
#include "vgui/ILocalize.h"
#include "voice_status.h"

#include <string>

// memdbgon must be the last include file in a .cpp file!!!
#include "tier0/memdbgon.h"

using fui::Node;

namespace
{

std::string UTF8( const wchar_t *pwsz )
{
	if ( !pwsz )
		return std::string();
	char sz[ 512 ];
	V_UnicodeToUTF8( pwsz, sz, sizeof( sz ) );
	return sz;
}

std::string Localized( const char *pszToken )
{
	if ( !pszToken || !pszToken[ 0 ] )
		return std::string();
	const wchar_t *pwsz = g_pVGuiLocalize->Find( pszToken );
	return pwsz ? UTF8( pwsz ) : std::string( pszToken[ 0 ] == '#' ? pszToken + 1 : pszToken );
}

const char *ClassName( int iClass )
{
	static const char *s_Names[] = { "", "scout", "sniper", "soldier", "demoman", "medic", "heavy", "pyro", "spy", "engineer" };
	return iClass > 0 && iClass < (int)ARRAYSIZE( s_Names ) ? s_Names[ iClass ] : "";
}

const char *TeamName( int iTeam )
{
	iTeam = frontress::DisplayTeam( iTeam );
	return iTeam == TF_TEAM_BLUE ? "blu" : iTeam == TF_TEAM_RED ? "red" : "spec";
}

std::string Avatar( int iPlayer )
{
	player_info_t info;
	ISteamFriends *pFriends = steamapicontext ? steamapicontext->SteamFriends() : NULL;
	if ( !pFriends || !engine->GetPlayerInfo( iPlayer, &info ) || !info.friendsID || info.fakeplayer )
		return std::string();
	const CSteamID id( info.friendsID, steamapicontext->SteamUtils() ? steamapicontext->SteamUtils()->GetConnectedUniverse() : k_EUniversePublic, k_EAccountTypeIndividual );
	if ( pFriends->GetLargeFriendAvatar( id ) <= 0 )
		return std::string();
	return "avatar:" + std::to_string( id.ConvertToUint64() );
}

// A server-sent name that goes into a console command: a map, a mission.
bool SafeName( const std::string &name )
{
	if ( name.empty() || name.size() > 96 )
		return false;
	for ( char c : name )
	{
		if ( !V_isalnum( c ) && c != '_' && c != '-' && c != '.' && c != '/' )
			return false;
	}
	return true;
}

std::string Str( fui::Runtime &ui, const char *pszPath )
{
	const Node *p = ui.Get( pszPath );
	return p ? p->AsString() : std::string();
}

//-----------------------------------------------------------------------------
// Calling a vote.
//-----------------------------------------------------------------------------
// Who a kick vote may name: your own team, as the stock dialog lists them.
void WriteKickable( Node &players )
{
	players.SetArray();
	const int iLocal = GetLocalPlayerIndex();
	const int iLocalTeam = GetLocalPlayerTeam();
	if ( !iLocal || iLocalTeam == TEAM_UNASSIGNED || !g_TF_PR )
		return;
	const bool bMvM = TFGameRules() && TFGameRules()->IsMannVsMachineMode();
	for ( int i = 1; i <= gpGlobals->maxClients; ++i )
	{
		if ( i == iLocal || !g_PR->IsConnected( i ) || g_TF_PR->GetPlayerConnectionState( i ) != MM_CONNECTED )
			continue;
		const int iTeam = g_PR->GetTeam( i );
		const bool bLoose = bMvM && ( iTeam == TEAM_UNASSIGNED || iTeam == TEAM_SPECTATOR ) && !g_PR->IsFakePlayer( i );
		if ( iTeam != iLocalTeam && !bLoose )
			continue;
		Node &p = players.Append();
		p[ "idx" ] = i;
		p[ "name" ] = g_PR->GetPlayerName( i );
		p[ "cls" ] = ClassName( g_TF_PR->GetPlayerClass( i ) );
		p[ "avatar" ] = Avatar( i );
		p[ "bot" ] = g_PR->IsFakePlayer( i );
	}
}

//-----------------------------------------------------------------------------
// Muting players.
//-----------------------------------------------------------------------------
void RefreshMute( fui::Runtime &ui )
{
	Node mute;
	Node &players = mute[ "players" ];
	players.SetArray();
	CVoiceStatus *pVoice = GetClientVoiceMgr();
	const int iLocal = GetLocalPlayerIndex();
	for ( int i = 1; i <= gpGlobals->maxClients && g_PR; ++i )
	{
		if ( i == iLocal || !g_PR->IsConnected( i ) || g_PR->IsFakePlayer( i ) )
			continue;
		Node &p = players.Append();
		p[ "idx" ] = i;
		p[ "name" ] = g_PR->GetPlayerName( i );
		p[ "team" ] = TeamName( g_PR->GetTeam( i ) );
		p[ "avatar" ] = Avatar( i );
		p[ "muted" ] = pVoice && pVoice->IsPlayerBlocked( i );
		p[ "talking" ] = pVoice && pVoice->IsPlayerSpeaking( i );
	}
	static ConVarRef cl_mute_all_comms( "cl_mute_all_comms" );
	mute[ "all_comms" ] = cl_mute_all_comms.IsValid() && cl_mute_all_comms.GetBool();
	ui.Set( "mute", std::move( mute ) );
}

const char *BuildingKind( int iType, int iMode )
{
	switch ( iType )
	{
	case OBJ_SENTRYGUN: return iMode == MODE_SENTRYGUN_DISPOSABLE ? "mini" : "sentry";
	case OBJ_DISPENSER: return "dispenser";
	case OBJ_TELEPORTER: return iMode == MODE_TELEPORTER_EXIT ? "exit" : "entrance";
	default: return "";
	}
}

} // namespace

//-----------------------------------------------------------------------------
// The server's answer to "callvote": what may be voted on here
// (CHudVote::MsgFunc_VoteSetup). True: the Frontress window shows it, not the
// stock dialog.
//-----------------------------------------------------------------------------
bool FuiVoteSetup( const CUtlVector< VoteIssue_t > &issues, const CUtlStringList &maps, const CUtlStringList &missions )
{
	fui::Runtime *pUI = FrontressUI();
	if ( !pUI || !FrontressUIReplacesMenu() )
		return false;

	Node vs;
	vs[ "loading" ] = false;
	bool bAny = false;
	Node &list = vs[ "issues" ];
	list.SetArray();
	FOR_EACH_VEC( issues, i )
	{
		const VoteIssue_t &issue = issues[ i ];
		Node &n = list.Append();
		n[ "id" ] = issue.szName;
		n[ "label" ] = Localized( issue.szNameString );
		n[ "active" ] = issue.bIsActive;
		const char *pszParam = "";
		if ( !V_stricmp( issue.szName, "ChangeLevel" ) || !V_stricmp( issue.szName, "NextLevel" ) )
			pszParam = "map";
		else if ( !V_stricmp( issue.szName, "Kick" ) )
			pszParam = "player";
		else if ( !V_stricmp( issue.szName, "ChangeMission" ) )
			pszParam = "mission";
		n[ "param" ] = pszParam;
		bAny = bAny || issue.bIsActive;
	}
	vs[ "enabled" ] = bAny;

	char szCurrent[ MAX_PATH ];
	V_FileBase( engine->GetLevelName(), szCurrent, sizeof( szCurrent ) );
	Node &mapList = vs[ "maps" ];
	mapList.SetArray();
	for ( int i = 0; i < maps.Count(); ++i )
	{
		if ( !SafeName( maps[ i ] ) || !V_stricmp( maps[ i ], szCurrent ) )
			continue;
		Node &m = mapList.Append();
		m[ "id" ] = maps[ i ];
		m[ "name" ] = GetMapDisplayName( maps[ i ] );
	}
	Node &missionList = vs[ "missions" ];
	missionList.SetArray();
	for ( int i = 0; i < missions.Count(); ++i )
	{
		if ( !SafeName( missions[ i ] ) )
			continue;
		Node &m = missionList.Append();
		m[ "id" ] = missions[ i ];
		m[ "name" ] = missions[ i ];
	}
	WriteKickable( vs[ "players" ] );
	vs[ "issue" ] = "";
	vs[ "param" ] = "";
	vs[ "reason" ] = "other";
	pUI->Set( "votesetup", std::move( vs ) );

	if ( !enginevgui->IsGameUIVisible() )
		engine->ClientCmd_Unrestricted( "gameui_activate\n" );
	pUI->Set( "ui.modal", Node( "vote" ) );
	return true;
}

bool FuiIngameCommand( fui::Runtime &ui, const std::string &name, const std::vector< std::string > &args )
{
	const std::string arg = args.empty() ? std::string() : args[ 0 ];
	if ( name == "open_vote" )
	{
		// The server says what can be voted on (FuiVoteSetup above).
		Node vs;
		vs[ "loading" ] = true;
		vs[ "enabled" ] = true;
		vs[ "issues" ].SetArray();
		vs[ "issue" ] = "";
		ui.Set( "votesetup", std::move( vs ) );
		ui.Set( "ui.modal", Node( "vote" ) );
		engine->ClientCmd( "callvote\n" );
		return true;
	}
	if ( name == "call_vote" )
	{
		const std::string issue = Str( ui, "votesetup.issue" ), param = Str( ui, "votesetup.param" );
		std::string reason = Str( ui, "votesetup.reason" );
		if ( !SafeName( issue ) )
			return true;
		CFmtStr1024 cmd;
		const Node *pIssues = ui.Get( "votesetup.issues" );
		std::string kind;
		for ( size_t i = 0; pIssues && i < pIssues->Size(); ++i )
		{
			const Node &n = *pIssues->At( i );
			if ( n.Find( "id" ) && n.Find( "id" )->AsString() == issue )
				kind = n.Find( "param" ) ? n.Find( "param" )->AsString() : "";
		}
		if ( kind == "player" )
		{
			const int iPlayer = V_atoi( param.c_str() );
			if ( iPlayer <= 0 || iPlayer > MAX_PLAYERS || !g_PR || !g_PR->IsConnected( iPlayer ) )
				return true;
			if ( reason != "cheating" && reason != "idle" && reason != "scamming" )
				reason = "other";
			cmd.sprintf( "callvote %s \"%d %s\"\n", issue.c_str(), g_PR->GetUserID( iPlayer ), reason.c_str() );
		}
		else if ( kind == "map" || kind == "mission" )
		{
			if ( !SafeName( param ) )
				return true;
			cmd.sprintf( "callvote %s %s\n", issue.c_str(), param.c_str() );
		}
		else
			cmd.sprintf( "callvote %s\n", issue.c_str() );
		engine->ClientCmd( cmd );
		// Back to the match, where the vote shows.
		ui.Set( "ui.modal", Node( "" ) );
		engine->ClientCmd_Unrestricted( "gameui_hide\n" );
		return true;
	}
	if ( name == "open_mute" )
	{
		RefreshMute( ui );
		ui.Set( "ui.modal", Node( "mute" ) );
		return true;
	}
	if ( name == "mute_toggle" )
	{
		const int iPlayer = V_atoi( arg.c_str() );
		CVoiceStatus *pVoice = GetClientVoiceMgr();
		if ( pVoice && iPlayer > 0 && iPlayer <= MAX_PLAYERS && iPlayer != GetLocalPlayerIndex() )
			pVoice->SetPlayerBlockedState( iPlayer, !pVoice->IsPlayerBlocked( iPlayer ) );
		RefreshMute( ui );
		return true;
	}
	if ( name == "mute_all_comms" )
	{
		// Muted players are kept out of text chat too (cl_mute_all_comms).
		static ConVarRef cl_mute_all_comms( "cl_mute_all_comms" );
		if ( cl_mute_all_comms.IsValid() )
			cl_mute_all_comms.SetValue( !cl_mute_all_comms.GetBool() );
		RefreshMute( ui );
		return true;
	}
	return false;
}

//-----------------------------------------------------------------------------
// The class menu up now: an engineer's build or destroy menu, a spy's
// disguise menu. Each stock menu still works out its slots and takes the
// keys; this reads what it would show.
//-----------------------------------------------------------------------------
void FuiWriteClassMenu( Node &out )
{
	out[ "shown" ] = false;
	out[ "kind" ] = "";
	out[ "slots" ].SetArray();
	C_TFPlayer *pLocal = C_TFPlayer::GetLocalTFPlayer();
	if ( !pLocal || !FrontressHudReplaces( "buildmenu" ) )
		return;

	CHudMenuEngyBuild *pBuild = GET_HUDELEMENT( CHudMenuEngyBuild );
	CHudMenuEngyDestroy *pDestroy = GET_HUDELEMENT( CHudMenuEngyDestroy );
	CHudMenuSpyDisguise *pDisguise = GET_HUDELEMENT( CHudMenuSpyDisguise );
	Node &slots = out[ "slots" ];
	if ( pBuild && pBuild->IsVisible() )
	{
		out[ "shown" ] = true;
		out[ "kind" ] = "build";
		out[ "metal" ] = pLocal->GetAmmoCount( TF_AMMO_METAL );
		static const char *s_States[] = { "", "ready", "built", "short", "off" };
		for ( int i = 0; i < NUM_ENGY_BUILDINGS; ++i )
		{
			int iType, iMode, iCost;
			const int iState = pBuild->FrontressSlot( i, iType, iMode, iCost );
			Node &s = slots.Append();
			s[ "key" ] = i + 1;
			s[ "kind" ] = BuildingKind( iType, iMode );
			s[ "state" ] = s_States[ Clamp( iState, 0, 4 ) ];
			s[ "cost" ] = iCost;
		}
	}
	else if ( pDestroy && pDestroy->IsVisible() )
	{
		out[ "shown" ] = true;
		out[ "kind" ] = "destroy";
		static const char *s_States[] = { "", "built", "none", "off" };
		for ( int i = 0; i < NUM_ENGY_BUILDINGS; ++i )
		{
			int iType, iMode;
			const int iState = pDestroy->FrontressSlot( i, iType, iMode );
			Node &s = slots.Append();
			s[ "key" ] = i + 1;
			s[ "kind" ] = BuildingKind( iType, iMode );
			s[ "state" ] = s_States[ Clamp( iState, 0, 3 ) ];
		}
	}
	else if ( pDisguise && pDisguise->IsVisible() )
	{
		out[ "shown" ] = true;
		out[ "kind" ] = "disguise";
		int iTeam, iGroup, iSelected;
		pDisguise->FrontressState( iTeam, iGroup, iSelected );
		out[ "team" ] = TeamName( iTeam );
		out[ "group" ] = iGroup;
		static ConVarRef tf_simple_disguise_menu( "tf_simple_disguise_menu" );
		out[ "simple" ] = tf_simple_disguise_menu.IsValid() && tf_simple_disguise_menu.GetBool();
		// The menu's order (the number keys), not the class numbers.
		static const int s_Order[] = { TF_CLASS_SCOUT, TF_CLASS_SOLDIER, TF_CLASS_PYRO, TF_CLASS_DEMOMAN, TF_CLASS_HEAVYWEAPONS,
			TF_CLASS_ENGINEER, TF_CLASS_MEDIC, TF_CLASS_SNIPER, TF_CLASS_SPY };
		for ( int i = 0; i < 9; ++i )
		{
			Node &s = slots.Append();
			s[ "key" ] = i + 1;
			s[ "cls" ] = ClassName( s_Order[ i ] );
			s[ "group" ] = i / 3;
			s[ "selected" ] = iSelected == i + 1;
		}
	}
}
