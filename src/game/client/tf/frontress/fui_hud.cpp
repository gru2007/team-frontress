//========= Team Frontress ====================================================//
//
// The in-match HUD's state, and which stock HUD parts it stands in for.
// See fui_hud.h.
//
//=============================================================================
#include "cbase.h"

#pragma push_macro( "Assert" )
#undef Assert
#include "tier0/valve_minmax_off.h"
#include "fui.h"
#include "tier0/valve_minmax_on.h"
#pragma pop_macro( "Assert" )

#include "fui_hud.h"
#include "tf/frontress_uniform.h"

#include "c_tf_player.h"
#include "c_tf_team.h"
#include "c_playerresource.h"
#include "c_team_objectiveresource.h"
#include "c_team_train_watcher.h"
#include "entity_capture_flag.h"
#include "filesystem.h"
#include "GameEventListener.h"
#include "hud.h"
#include "teamplay_round_timer.h"
#include "tf_gamerules.h"
#include "tf_weapon_medigun.h"
#include "tf_weaponbase.h"
#include "vgui/ILocalize.h"

#include <cmath>
#include <deque>
#include <string>

// memdbgon must be the last include file in a .cpp file!!!
#include "tier0/memdbgon.h"

using fui::Node;

extern CUtlVector< CHandle< C_TeamTrainWatcher > > g_hTrainWatchers;

static ConVar fui_hud_style( "fui_hud_style", "auto", FCVAR_ARCHIVE,
	"Which HUD: a folder under resource/fui/huds/ (frontress, minimal, or your own), "
	"or auto: frontress, unless a classic custom HUD is installed in custom/." );

namespace
{

const float k_flRefresh = 1.f / 30.f;
const float k_flKillSeconds = 7.f;
const float k_flOwnKillSeconds = 11.f;
const int k_nMaxKills = 6;

std::string UTF8( const wchar_t *pwsz )
{
	if ( !pwsz )
		return std::string();
	char sz[ 256 ];
	g_pVGuiLocalize->ConvertUnicodeToANSI( pwsz, sz, sizeof( sz ) );
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

// The classic VGUI HUDs the community makes live in custom/ and override
// these files. If one is installed, the player chose it: keep it.
bool ClassicCustomHudInstalled()
{
	static float s_flNext = 0.f;
	static bool s_bInstalled = false;
	if ( Plat_FloatTime() < s_flNext )
		return s_bInstalled;
	s_flNext = (float)Plat_FloatTime() + 10.f;
	static const char *s_Files[] = { "resource/ui/hudplayerhealth.res", "resource/ui/hudammoweapons.res", "scripts/hudlayout.res" };
	s_bInstalled = false;
	for ( const char *pszFile : s_Files )
	{
		char szPath[ MAX_PATH ];
		if ( g_pFullFileSystem->RelativePathToFullPath( pszFile, "GAME", szPath, sizeof( szPath ) ) && V_stristr( szPath, "custom" ) )
			s_bInstalled = true;
	}
	return s_bInstalled;
}

//-----------------------------------------------------------------------------
// The kill feed, from player_death.
//-----------------------------------------------------------------------------
struct Kill_t
{
	std::string killer, victim, assist, icon;
	int iKillerTeam = 0, iVictimTeam = 0;
	bool bCrit = false, bMe = false;
	float flAt = 0.f;
};

class CKillFeed : public CGameEventListener
{
public:
	void Listen()
	{
		if ( !m_bListening )
		{
			ListenForGameEvent( "player_death" );
			m_bListening = true;
		}
	}

	virtual void FireGameEvent( IGameEvent *event ) OVERRIDE
	{
		if ( !g_PR )
			return;
		const int iVictim = engine->GetPlayerForUserID( event->GetInt( "userid" ) );
		const int iKiller = engine->GetPlayerForUserID( event->GetInt( "attacker" ) );
		const int iAssist = engine->GetPlayerForUserID( event->GetInt( "assister" ) );
		const int iLocal = GetLocalPlayerIndex();
		if ( iVictim <= 0 )
			return;

		Kill_t kill;
		kill.victim = g_PR->GetPlayerName( iVictim );
		kill.iVictimTeam = g_PR->GetTeam( iVictim );
		if ( iKiller > 0 && iKiller != iVictim )
		{
			kill.killer = g_PR->GetPlayerName( iKiller );
			kill.iKillerTeam = g_PR->GetTeam( iKiller );
		}
		if ( iAssist > 0 )
			kill.assist = g_PR->GetPlayerName( iAssist );
		kill.bMe = iLocal == iVictim || iLocal == iKiller || iLocal == iAssist;
		kill.bCrit = event->GetInt( "crit_type" ) == 2;

		// The stock kill icons: d_<weapon> (light) and dneg_<weapon> (dark,
		// for the player's own rows), from scripts/mod_textures.txt.
		const int iCustom = event->GetInt( "customkill" );
		std::string weapon = event->GetString( "weapon", "" );
		if ( iCustom == TF_DMG_CUSTOM_HEADSHOT )
			weapon = "headshot";
		else if ( iCustom == TF_DMG_CUSTOM_BACKSTAB )
			weapon = "backstab";
		if ( weapon.empty() || weapon == "world" || !gHUD.GetIcon( ( "d_" + weapon ).c_str() ) )
			weapon = "skull_tf";
		kill.icon = ( kill.bMe ? "hudicon:dneg_" : "hudicon:d_" ) + weapon;

		kill.flAt = gpGlobals->curtime;
		m_Kills.push_back( kill );
		while ( (int)m_Kills.size() > k_nMaxKills )
			m_Kills.pop_front();
		if ( iVictim == iLocal )
			m_strKilledBy = kill.killer;
	}

	void Write( Node &out )
	{
		while ( !m_Kills.empty() && ( m_Kills.front().flAt > gpGlobals->curtime ||
			gpGlobals->curtime - m_Kills.front().flAt > ( m_Kills.front().bMe ? k_flOwnKillSeconds : k_flKillSeconds ) ) )
			m_Kills.pop_front();
		out.SetArray();
		for ( const Kill_t &k : m_Kills )
		{
			Node &n = out.Append();
			n[ "killer" ] = Node( k.killer );
			n[ "killer_team" ] = Node( TeamName( k.iKillerTeam ) );
			n[ "victim" ] = Node( k.victim );
			n[ "victim_team" ] = Node( TeamName( k.iVictimTeam ) );
			n[ "assist" ] = Node( k.assist );
			n[ "icon" ] = Node( k.icon );
			n[ "crit" ] = Node( k.bCrit );
			n[ "me" ] = Node( k.bMe );
		}
	}

	void Clear()
	{
		m_Kills.clear();
		m_strKilledBy.clear();
	}
	const std::string &KilledBy() const { return m_strKilledBy; }

private:
	std::deque< Kill_t > m_Kills;
	std::string m_strKilledBy;
	bool m_bListening = false;
};

CKillFeed s_KillFeed;
CUtlStringList s_Replaces;	// tokens from the HUD document's data-replaces
std::string s_strMode;		// the objective our HUD draws ("" = leave it to the game)
float s_flNextRefresh = 0.f;

//-----------------------------------------------------------------------------
float TimerLeft( C_TeamRoundTimer *pTimer )
{
	return pTimer ? Max( 0.f, pTimer->GetTimeRemaining() ) : -1.f;
}

void WritePlayer( C_TFPlayer *pPlayer, Node &hud )
{
	const bool bAlive = pPlayer->IsAlive() && pPlayer->GetTeamNumber() >= FIRST_GAME_TEAM;
	const int iClass = pPlayer->GetPlayerClass() ? pPlayer->GetPlayerClass()->GetClassIndex() : 0;
	hud[ "alive" ] = Node( bAlive );
	hud[ "class" ] = Node( ClassName( iClass ) );
	hud[ "team" ] = Node( TeamName( pPlayer->GetTeamNumber() ) );

	const int iHealth = Max( 0, pPlayer->GetHealth() );
	const int iMax = Max( 1, pPlayer->GetMaxHealth() );
	const int iBuffed = Max( iMax + 1, pPlayer->m_Shared.GetMaxBuffedHealth() );
	hud[ "health" ] = Node( iHealth );
	hud[ "max" ] = Node( iMax );
	hud[ "frac" ] = Node( Min( 1.0, (double)iHealth / iMax ) );
	hud[ "overheal" ] = Node( Clamp( (double)( iHealth - iMax ) / ( iBuffed - iMax ), 0.0, 1.0 ) );
	hud[ "low" ] = Node( bAlive && iHealth <= iMax * 0.35 );

	// The weapon in hand.
	C_TFWeaponBase *pWeapon = pPlayer->GetActiveTFWeapon();
	std::string name, icon;
	int iClip = -1, iReserve = -1, iClipMax = 0;
	if ( pWeapon )
	{
		if ( CEconItemView *pItem = pWeapon->GetAttributeContainer()->GetItem() )
		{
			if ( pItem->IsValid() )
			{
				name = UTF8( pItem->GetItemName() );
				if ( const char *pszImage = pItem->GetStaticData()->GetInventoryImage() )
					icon = std::string( "tex:" ) + pszImage + "_large";
			}
		}
		if ( pWeapon->UsesPrimaryAmmo() )
		{
			iClip = pWeapon->Clip1();
			if ( iClip < 0 )
				iClip = pPlayer->GetAmmoCount( pWeapon->GetPrimaryAmmoType() );	// no clip: one number
			else
			{
				iReserve = pPlayer->GetAmmoCount( pWeapon->GetPrimaryAmmoType() );
				iClipMax = pWeapon->GetMaxClip1();
			}
		}
	}
	hud[ "weapon" ] = Node( name );
	hud[ "weapon_icon" ] = Node( icon );
	hud[ "clip" ] = Node( iClip );
	hud[ "reserve" ] = Node( iReserve );
	hud[ "clip_max" ] = Node( iClipMax );
	hud[ "clip_low" ] = Node( iClipMax > 0 ? iClip <= Max( 1, iClipMax / 4 ) : ( iClip >= 0 && iClip <= 5 ) );

	// A medic's charge.
	double flCharge = -1.0;
	if ( CWeaponMedigun *pMedigun = dynamic_cast< CWeaponMedigun * >( pPlayer->Weapon_OwnsThisID( TF_WEAPON_MEDIGUN ) ) )
		flCharge = pMedigun->GetChargeLevel();
	hud[ "charge" ] = Node( flCharge );
	hud[ "charge_ready" ] = Node( flCharge >= 1.0 );

	// Dead: who did it, and when we are back.
	int iRespawn = -1;
	if ( !bAlive && pPlayer->GetTeamNumber() >= FIRST_GAME_TEAM && TFGameRules() )
	{
		const float flAt = TFGameRules()->GetNextRespawnWave( pPlayer->GetTeamNumber(), pPlayer );
		if ( flAt > 0.f )
			iRespawn = Max( 0, (int)ceilf( flAt - gpGlobals->curtime ) );
	}
	hud[ "respawn" ] = Node( iRespawn );
	hud[ "killed_by" ] = Node( bAlive ? std::string() : s_KillFeed.KilledBy() );
}

void WriteObjective( Node &hud )
{
	CTFGameRules *pRules = TFGameRules();
	C_BaseTeamObjectiveResource *pObjectives = ObjectiveResource();
	std::string mode;
	if ( pRules )
	{
		switch ( pRules->GetGameType() )
		{
		case TF_GAMETYPE_CTF: mode = "ctf"; break;
		case TF_GAMETYPE_CP: mode = pRules->IsInKothMode() ? "koth" : "cp"; break;
		case TF_GAMETYPE_ESCORT: mode = pRules->HasMultipleTrains() ? "" : "pl"; break;
		case TF_GAMETYPE_ARENA: mode = "cp"; break;
		default: break;
		}
	}
	s_strMode = mode;
	hud[ "mode" ] = Node( mode );

	// Round timer: the one the map says to show.
	C_TeamRoundTimer *pTimer = NULL;
	if ( pObjectives && pObjectives->GetTimerToShowInHUD() > 0 )
		pTimer = dynamic_cast< C_TeamRoundTimer * >( ClientEntityList().GetEnt( pObjectives->GetTimerToShowInHUD() ) );
	if ( pTimer && ( pTimer->IsDisabled() || !pTimer->ShowInHud() ) )
		pTimer = NULL;
	hud[ "timer" ] = Node( (int)ceilf( TimerLeft( pTimer ) ) );
	hud[ "setup" ] = Node( pRules && pRules->InSetup() );
	hud[ "overtime" ] = Node( pRules && pRules->InOvertime() );
	hud[ "waiting" ] = Node( pRules && pRules->IsInWaitingForPlayers() );

	C_TFTeam *pRed = GetGlobalTFTeam( frontress::DisplayTeam( TF_TEAM_RED ) ), *pBlu = GetGlobalTFTeam( frontress::DisplayTeam( TF_TEAM_BLUE ) );
	const bool bFlags = mode == "ctf";
	hud[ "score_red" ] = Node( pRed ? ( bFlags ? pRed->GetFlagCaptures() : pRed->Get_Score() ) : 0 );
	hud[ "score_blu" ] = Node( pBlu ? ( bFlags ? pBlu->GetFlagCaptures() : pBlu->Get_Score() ) : 0 );

	// King of the hill: each side's clock.
	hud[ "koth_red" ] = Node( mode == "koth" && pRules ? (int)ceilf( TimerLeft( pRules->GetRedKothRoundTimer() ) ) : -1 );
	hud[ "koth_blu" ] = Node( mode == "koth" && pRules ? (int)ceilf( TimerLeft( pRules->GetBlueKothRoundTimer() ) ) : -1 );
	if ( frontress::UniformSwapped() )
	{
		const int redTime = mode == "koth" && pRules ? (int)ceilf( TimerLeft( pRules->GetBlueKothRoundTimer() ) ) : -1;
		const int bluTime = mode == "koth" && pRules ? (int)ceilf( TimerLeft( pRules->GetRedKothRoundTimer() ) ) : -1;
		hud[ "koth_red" ] = Node( redTime ); hud[ "koth_blu" ] = Node( bluTime );
	}

	// Control points, in the map's order.
	Node &points = hud[ "points" ];
	points.SetArray();
	for ( int i = 0; pObjectives && i < pObjectives->GetNumControlPoints(); ++i )
	{
		if ( !pObjectives->IsCPVisible( i ) )
			continue;
		Node &p = points.Append();
		const int iCapper = pObjectives->GetCappingTeam( i );
		p[ "owner" ] = Node( frontress::DisplayTeam( pObjectives->GetOwningTeam( i ) ) == TF_TEAM_RED ? "red" : frontress::DisplayTeam( pObjectives->GetOwningTeam( i ) ) == TF_TEAM_BLUE ? "blu" : "neutral" );
		p[ "capping" ] = Node( frontress::DisplayTeam( iCapper ) == TF_TEAM_RED ? "red" : frontress::DisplayTeam( iCapper ) == TF_TEAM_BLUE ? "blu" : "" );
		p[ "frac" ] = Node( iCapper >= FIRST_GAME_TEAM ? (double)pObjectives->GetCPCapPercentage( i ) : 0.0 );
		p[ "cappers" ] = Node( iCapper >= FIRST_GAME_TEAM ? pObjectives->GetNumPlayersInArea( i, iCapper ) : 0 );
		p[ "locked" ] = Node( pObjectives->GetCPLocked( i ) );
		p[ "blocked" ] = Node( pObjectives->CapIsBlocked( i ) );
	}

	// Payload: how far the cart has come.
	Node &cart = hud[ "cart" ];
	cart[ "progress" ] = Node( -1.0 );
	if ( mode == "pl" )
	{
		for ( int i = 0; i < g_hTrainWatchers.Count(); ++i )
		{
			C_TeamTrainWatcher *pWatcher = g_hTrainWatchers[ i ].Get();
			if ( !pWatcher || pWatcher->IsDormant() )
				continue;
			cart[ "progress" ] = Node( (double)Clamp( pWatcher->GetTotalProgress(), 0.f, 1.f ) );
			cart[ "speed" ] = Node( pWatcher->GetSpeedLevel() );
			cart[ "team" ] = Node( TeamName( pWatcher->GetTeamNumber() ) );
			break;
		}
	}

	// A war battle: what it is for and what it is fought under, as the
	// server has it from the coordinator (frontress_conditions.cpp).
	{
		static ConVarRef tf_war_battle( "tf_war_battle" ), tf_war_conditions( "tf_war_conditions" );
		Node &w = hud[ "war" ];
		CUtlStringList parts, conds;
		if ( tf_war_battle.IsValid() && tf_war_battle.GetString()[ 0 ] )
			V_SplitString( tf_war_battle.GetString(), "|", parts );
		w[ "active" ] = Node( parts.Count() >= 7 );
		if ( parts.Count() >= 7 )
		{
			w[ "stage" ] = Node( V_atoi( parts[ 0 ] ) );
			w[ "stages" ] = Node( V_atoi( parts[ 1 ] ) );
			w[ "kind" ] = Node( parts[ 2 ] );
			w[ "attacker" ] = Node( parts[ 3 ] );
			C_TFPlayer *pLocal = C_TFPlayer::GetLocalTFPlayer();
			w[ "attacking" ] = Node( pLocal && pLocal->GetTeamNumber() == V_atoi( parts[ 4 ] ) );
			w[ "order" ] = Node( !V_strcmp( parts[ 5 ], "true" ) );
			w[ "sector" ] = Node( parts[ 6 ] );
		}
		if ( tf_war_conditions.IsValid() && tf_war_conditions.GetString()[ 0 ] )
			V_SplitString( tf_war_conditions.GetString(), ",", conds );
		Node &list = w[ "conditions" ];
		list.SetArray();
		for ( int i = 0; i < conds.Count(); ++i )
			list.Append( Node( conds[ i ] ) );
	}

	// Capture the flag: where each team's intelligence is.
	Node &flags = hud[ "flags" ];
	flags.SetArray();
	for ( int i = 0; bFlags && i < ICaptureFlagAutoList::AutoList().Count(); ++i )
	{
		C_CaptureFlag *pFlag = static_cast< C_CaptureFlag * >( ICaptureFlagAutoList::AutoList()[ i ] );
		if ( !pFlag || pFlag->IsDormant() )
			continue;
		Node &f = flags.Append();
		f[ "team" ] = Node( TeamName( pFlag->GetTeamNumber() ) );
		f[ "status" ] = Node( pFlag->IsStolen() ? "stolen" : pFlag->IsDropped() ? "dropped" : "home" );
		f[ "return" ] = Node( pFlag->IsDropped() ? (double)pFlag->GetReturnProgress() : 0.0 );
	}
}

} // namespace

//-----------------------------------------------------------------------------
const char *FuiHudDocument()
{
	static CUtlString s_strPath;
	CUtlString style = fui_hud_style.GetString();
	if ( style.IsEmpty() || !V_stricmp( style, "auto" ) )
		style = ClassicCustomHudInstalled() ? "minimal" : "frontress";
	// A folder name, nothing else.
	for ( int i = 0; i < style.Length(); ++i )
	{
		const char c = style[ i ];
		if ( !V_isalnum( c ) && c != '_' && c != '-' )
		{
			style = "frontress";
			break;
		}
	}
	CFmtStr path( "huds/%s/hud.rml", style.Get() );
	if ( !g_pFullFileSystem->FileExists( CFmtStr( "resource/fui/%s", path.Get() ), "GAME" ) )
		path.sprintf( "huds/frontress/hud.rml" );
	s_strPath = path.Get();
	return s_strPath.Get();
}

void FuiHudUpdate( fui::Runtime &runtime, bool bShown )
{
	s_KillFeed.Listen();
	C_TFPlayer *pPlayer = C_TFPlayer::GetLocalTFPlayer();
	if ( !engine->IsInGame() || !pPlayer )
	{
		s_Replaces.PurgeAndDeleteElements();
		s_KillFeed.Clear();
		return;
	}

	// What the HUD document draws itself: kept for the whole match, so the
	// stock parts do not flash back while a menu is open over it.
	if ( runtime.IsHud() )
	{
		const std::string replaces = runtime.HudAttribute( "data-replaces" );
		s_Replaces.PurgeAndDeleteElements();
		V_SplitString( replaces.c_str(), " ", s_Replaces );
	}

	if ( !bShown || Plat_FloatTime() < s_flNextRefresh )
		return;
	s_flNextRefresh = (float)Plat_FloatTime() + k_flRefresh;

	static ConVarRef cl_drawhud( "cl_drawhud" );
	Node hud;
	hud.SetObject();
	hud[ "visible" ] = Node( !cl_drawhud.IsValid() || cl_drawhud.GetBool() );
	WritePlayer( pPlayer, hud );
	WriteObjective( hud );
	s_KillFeed.Write( hud[ "kills" ] );
	runtime.Set( "hud", std::move( hud ) );
}

bool FrontressHudReplaces( const char *pszElement )
{
	if ( !s_Replaces.Count() || !pszElement )
		return false;
	static const struct
	{
		const char *pszToken;
		const char *pszElement;
	} s_Parts[] = {
		{ "health", "CTFHudPlayerStatus" },
		{ "ammo", "CTFHudWeaponAmmo" },
		{ "medic", "CHudMedicChargeMeter" },
		{ "killfeed", "CTFHudDeathNotice" },
		{ "objective", "CTFHudObjectiveStatus" },
		{ "objective", "CTFHudKothTimeStatus" },
		{ "objective", "CTFHudMatchStatus" },
	};
	for ( const auto &part : s_Parts )
	{
		if ( V_strcmp( pszElement, part.pszElement ) )
			continue;
		// Modes our HUD does not draw keep the game's objective panel.
		if ( !V_strcmp( part.pszToken, "objective" ) && s_strMode.empty() )
			return false;
		for ( int i = 0; i < s_Replaces.Count(); ++i )
		{
			if ( !V_strcmp( s_Replaces[ i ], part.pszToken ) )
				return true;
		}
	}
	return false;
}

CON_COMMAND( fui_hud_list, "List the HUDs in resource/fui/huds/ (pick one with fui_hud_style)." )
{
	FileFindHandle_t h;
	for ( const char *pszName = g_pFullFileSystem->FindFirstEx( "resource/fui/huds/*", "GAME", &h ); pszName; pszName = g_pFullFileSystem->FindNext( h ) )
	{
		if ( pszName[ 0 ] != '.' && g_pFullFileSystem->FindIsDirectory( h ) )
			Msg( "  %s%s\n", pszName, !V_stricmp( pszName, fui_hud_style.GetString() ) ? "  (current)" : "" );
	}
	g_pFullFileSystem->FindClose( h );
	Msg( "Now showing: resource/fui/%s\n", FuiHudDocument() );
}

//-----------------------------------------------------------------------------
// fui_dev_goto: stand on the middle control point, facing the next one, so a
// HUD screenshot has the fight in it. Needs sv_cheats (it is setpos).
//-----------------------------------------------------------------------------
CON_COMMAND( fui_dev_goto, "Stand on the middle control point (sv_cheats). For HUD screenshots." )
{
	C_BaseTeamObjectiveResource *pObjectives = ObjectiveResource();
	if ( !pObjectives || pObjectives->GetNumControlPoints() <= 0 )
	{
		Msg( "fui_dev_goto: no control points on this map\n" );
		return;
	}
	const int n = pObjectives->GetNumControlPoints();
	const int iMiddle = n / 2;
	const Vector at = pObjectives->GetCPPosition( iMiddle );
	QAngle angles( 0, 0, 0 );
	if ( n > 1 )
	{
		VectorAngles( pObjectives->GetCPPosition( iMiddle + 1 < n ? iMiddle + 1 : iMiddle - 1 ) - at, angles );
		angles.x = 0;
	}
	engine->ClientCmd_Unrestricted( CFmtStr( "setpos %.0f %.0f %.0f; setang 0 %.0f 0\n", at.x, at.y, at.z + 20.f, angles.y ) );
}

//-----------------------------------------------------------------------------
// fui_dev_cam <n>: as a spectator, put the free camera high over the middle
// control point, looking down at it, from the n-th of eight directions --
// pulled in wherever the map is in the way. For screenshots of the fight.
//-----------------------------------------------------------------------------
CON_COMMAND( fui_dev_cam, "fui_dev_cam <n>: spectator camera over the middle control point, from direction n (0-7). For screenshots." )
{
	C_BaseTeamObjectiveResource *pObjectives = ObjectiveResource();
	if ( !pObjectives || pObjectives->GetNumControlPoints() <= 0 )
		return;
	const int n = pObjectives->GetNumControlPoints();
	const Vector target = pObjectives->GetCPPosition( n / 2 ) + Vector( 0, 0, 48 );
	Vector forward( 1, 0, 0 );
	if ( n > 1 )
	{
		forward = pObjectives->GetCPPosition( n / 2 + 1 < n ? n / 2 + 1 : n / 2 - 1 ) - target;
		forward.z = 0;
		VectorNormalize( forward );
	}
	const int i = args.ArgC() > 1 ? V_atoi( args[ 1 ] ) : 0;
	const float yaw = DEG2RAD( 25.f + 45.f * i );
	const Vector dir( forward.x * cosf( yaw ) - forward.y * sinf( yaw ), forward.x * sinf( yaw ) + forward.y * cosf( yaw ), 0 );
	// Keep it in the open: of a few heights (a hill under a roof needs a low
	// one), the one that gets furthest from the point before the map is in
	// the way, stopping a little short of it.
	Vector at = target;
	float best = -1.f;
	static const float s_Heights[] = { 420.f, 260.f, 140.f, 60.f };
	for ( float height : s_Heights )
	{
		trace_t tr;
		UTIL_TraceLine( target, target + dir * 820.f + Vector( 0, 0, height ), MASK_SOLID_BRUSHONLY, NULL, COLLISION_GROUP_NONE, &tr );
		const float reach = ( tr.endpos - tr.startpos ).Length();
		if ( reach > best + 80.f )
		{
			best = reach;
			at = tr.startpos + ( tr.endpos - tr.startpos ) * 0.92f;
		}
	}
	QAngle angles;
	VectorAngles( target - at, angles );
	engine->ClientCmd_Unrestricted( CFmtStr( "spec_mode 7; spec_goto %.0f %.0f %.0f %.1f %.1f\n", at.x, at.y, at.z, angles.x, angles.y ) );
}

// fui_dev_ground <x> <y> <z> <yaw>: the ground in front of a camera, on a grid
// (forward 150..2100, sideways -900..900), for placing puppet bots in store
// art (tools/steam-art). Prints "fwd right z" for points that hit solid ground
// within reach of the camera's height; "-" where there is none (a drop, water).
CON_COMMAND( fui_dev_ground, "fui_dev_ground <x> <y> <z> <yaw>: ground heights on a grid in front of a camera. For store art." )
{
	if ( args.ArgC() < 5 )
		return;
	const Vector cam( V_atof( args[ 1 ] ), V_atof( args[ 2 ] ), V_atof( args[ 3 ] ) );
	const float yaw = DEG2RAD( V_atof( args[ 4 ] ) );
	const Vector f( cosf( yaw ), sinf( yaw ), 0 ), r( sinf( yaw ), -cosf( yaw ), 0 );
	for ( int fwd = 150; fwd <= 2100; fwd += 150 )
	{
		CUtlString line;
		line.Format( "ground %5d:", fwd );
		for ( int right = -900; right <= 900; right += 150 )
		{
			const Vector top = cam + f * fwd + r * right + Vector( 0, 0, 300 );
			trace_t tr;
			UTIL_TraceLine( top, top - Vector( 0, 0, 2400 ), MASK_PLAYERSOLID, NULL, COLLISION_GROUP_NONE, &tr );
			const bool bGround = tr.fraction < 1.f && !tr.startsolid && !( tr.contents & ( CONTENTS_WATER | CONTENTS_SLIME ) ) &&
				!( tr.surface.flags & ( SURF_SKY | SURF_NODRAW ) ) && tr.plane.normal.z > 0.6f;
			line += bGround ? CFmtStr( " %5.0f", tr.endpos.z ).Access() : "     -";
		}
		Msg( "%s\n", line.Get() );
	}
}
