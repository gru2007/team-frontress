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
#include "fui_ingame.h"
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
#include "tf_weapon_pipebomblauncher.h"
#include "tf_hud_itemeffectmeter.h"
#include "c_baseobject.h"
#include "c_obj_sentrygun.h"
#include "c_obj_dispenser.h"
#include "c_obj_teleporter.h"
#include "cdll_util.h"
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
	char sz[ 512 ];
	V_UnicodeToUTF8( pwsz, sz, sizeof( sz ) );
	return sz;
}

// A game string ("#TF_Cloak"), in the player's language, key bindings filled
// in as the stock HUD does. The token itself (without '#') if there is none.
std::string Localized( const char *pszToken )
{
	if ( !pszToken || !pszToken[ 0 ] )
		return std::string();
	const wchar_t *pwsz = g_pVGuiLocalize->Find( pszToken );
	if ( !pwsz )
		return pszToken[ 0 ] == '#' ? std::string( pszToken + 1 ) : std::string( pszToken );
	wchar_t wszBound[ 256 ];
	UTIL_ReplaceKeyBindings( pwsz, 0, wszBound, sizeof( wszBound ) );
	return UTF8( wszBound );
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

void WriteConditions( C_TFPlayer *pPlayer, Node &out );
void WriteDisguise( C_TFPlayer *pPlayer, Node &out );

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

	// The class you come back as, when it is not the one you play now.
	const int iDesired = pPlayer->m_Shared.GetDesiredPlayerClassIndex();
	hud[ "next_class" ] = Node( iDesired != iClass && iDesired > TF_CLASS_UNDEFINED && iDesired <= TF_LAST_NORMAL_CLASS ? ClassName( iDesired ) : "" );

	WriteConditions( pPlayer, hud[ "conds" ] );
	WriteDisguise( pPlayer, hud[ "disguise" ] );
}

//-----------------------------------------------------------------------------
// What is being done to you: the stock health panel's buff and debuff icons
// (CTFHudPlayerHealth), plus what TF2 showed only as a screen effect
// (burning, Jarate, Übercharge, crits). icon: a texture, "" for none; a "%s"
// in it is the team (red / blue), as the stock panel picks.
//-----------------------------------------------------------------------------
struct Cond_t
{
	ETFCond eCond;
	const char *pszID;
	const char *pszIcon;
	bool bBad;
};

const Cond_t k_Conds[] = {
	{ TF_COND_MEDIGUN_UBER_BULLET_RESIST, "resist_bullet", "hud/defense_buff_bullet_%s", false },
	{ TF_COND_MEDIGUN_UBER_BLAST_RESIST, "resist_blast", "hud/defense_buff_explosion_%s", false },
	{ TF_COND_MEDIGUN_UBER_FIRE_RESIST, "resist_fire", "hud/defense_buff_fire_%s", false },
	{ TF_COND_MEDIGUN_SMALL_BULLET_RESIST, "resist_bullet", "hud/defense_buff_bullet_%s", false },
	{ TF_COND_MEDIGUN_SMALL_BLAST_RESIST, "resist_blast", "hud/defense_buff_explosion_%s", false },
	{ TF_COND_MEDIGUN_SMALL_FIRE_RESIST, "resist_fire", "hud/defense_buff_fire_%s", false },
	{ TF_COND_OFFENSEBUFF, "buff_offense", "effects/soldier_buff_offense_%s", false },
	{ TF_COND_DEFENSEBUFF, "buff_defense", "effects/soldier_buff_defense_%s", false },
	{ TF_COND_REGENONDAMAGEBUFF, "buff_heal", "effects/soldier_buff_healonhit_%s", false },
	{ TF_COND_RUNE_STRENGTH, "rune_strength", "effects/powerup_strength_hud", false },
	{ TF_COND_RUNE_HASTE, "rune_haste", "effects/powerup_haste_hud", false },
	{ TF_COND_RUNE_REGEN, "rune_regen", "effects/powerup_regen_hud", false },
	{ TF_COND_RUNE_RESIST, "rune_resist", "effects/powerup_resist_hud", false },
	{ TF_COND_RUNE_VAMPIRE, "rune_vampire", "effects/powerup_vampire_hud", false },
	{ TF_COND_RUNE_REFLECT, "rune_reflect", "effects/powerup_reflect_hud", false },
	{ TF_COND_RUNE_PRECISION, "rune_precision", "effects/powerup_precision_hud", false },
	{ TF_COND_RUNE_AGILITY, "rune_agility", "effects/powerup_agility_hud", false },
	{ TF_COND_RUNE_KNOCKOUT, "rune_knockout", "effects/powerup_knockout_hud", false },
	{ TF_COND_RUNE_KING, "rune_king", "effects/powerup_king_hud", false },
	{ TF_COND_RUNE_PLAGUE, "rune_plague", "effects/powerup_plague_hud", false },
	{ TF_COND_RUNE_SUPERNOVA, "rune_supernova", "effects/powerup_supernova_hud", false },
	{ TF_COND_PARACHUTE_ACTIVE, "parachute", "hud/hud_parachute_active", false },
	{ TF_COND_SPEED_BOOST, "speed", "", false },
	{ TF_COND_BURNING, "burning", "", true },
	{ TF_COND_BLEEDING, "bleeding", "vgui/bleed_drop", true },
	{ TF_COND_GRAPPLINGHOOK_BLEEDING, "bleeding", "vgui/bleed_drop_grapple", true },
	{ TF_COND_URINE, "jarate", "", true },
	{ TF_COND_MAD_MILK, "milk", "", true },
	{ TF_COND_GAS, "gas", "vgui/covered_in_gas", true },
	{ TF_COND_MARKEDFORDEATH, "marked", "vgui/marked_for_death", true },
	{ TF_COND_MARKEDFORDEATH_SILENT, "marked", "vgui/marked_for_death", true },
	{ TF_COND_PASSTIME_PENALTY_DEBUFF, "marked", "vgui/marked_for_death", true },
	{ TF_COND_STUNNED, "slowed", "", true },
};

void WriteConditions( C_TFPlayer *pPlayer, Node &out )
{
	out.SetArray();
	if ( !pPlayer->IsAlive() )
		return;
	CTFPlayerShared &shared = pPlayer->m_Shared;
	const char *pszTeam = frontress::DisplayTeam( pPlayer->GetTeamNumber() ) == TF_TEAM_BLUE ? "blue" : "red";
	CUtlVector< const char * > seen;
	auto add = [ & ]( const char *pszID, const char *pszIcon, bool bBad )
	{
		FOR_EACH_VEC( seen, i )
		{
			if ( !V_strcmp( seen[ i ], pszID ) )
				return;
		}
		seen.AddToTail( pszID );
		Node &c = out.Append();
		c[ "id" ] = Node( pszID );
		c[ "icon" ] = Node( pszIcon && pszIcon[ 0 ] ? std::string( "tex:" ) + CFmtStr( pszIcon, pszTeam ).Get() : std::string() );
		c[ "bad" ] = Node( bBad );
	};

	if ( shared.IsInvulnerable() )
		add( "uber", "", false );
	if ( shared.IsCritBoosted() )
		add( "crits", "", false );
	else if ( shared.InCond( TF_COND_MINICRITBOOSTED_ON_KILL ) || shared.InCond( TF_COND_ENERGY_BUFF ) || shared.InCond( TF_COND_NOHEALINGDAMAGEBUFF ) )
		add( "minicrits", "", false );
	for ( const Cond_t &cond : k_Conds )
	{
		if ( shared.InCond( cond.eCond ) )
			add( cond.pszID, cond.pszIcon, cond.bBad );
	}
	if ( shared.GetNumHealers() > 0 )
		add( "healed", "", false );
	if ( shared.IsStealthed() )
		add( "cloaked", "", false );
}

//-----------------------------------------------------------------------------
// A spy's disguise (the stock CDisguiseStatus): as whom, on which team, with
// what in hand.
//-----------------------------------------------------------------------------
void WriteDisguise( C_TFPlayer *pPlayer, Node &out )
{
	CTFPlayerShared &shared = pPlayer->m_Shared;
	const bool bOn = pPlayer->IsAlive() && shared.InCond( TF_COND_DISGUISED );
	out[ "on" ] = Node( bOn );
	out[ "disguising" ] = Node( pPlayer->IsAlive() && shared.InCond( TF_COND_DISGUISING ) );
	out[ "class" ] = Node( bOn ? ClassName( shared.GetDisguiseClass() ) : "" );
	out[ "team" ] = Node( bOn ? TeamName( shared.GetDisguiseTeam() ) : "" );
	std::string name, weapon;
	if ( bOn )
	{
		C_TFPlayer *pTarget = shared.GetDisguiseTarget();
		name = pTarget ? pTarget->GetPlayerName() : pPlayer->GetPlayerName();
		if ( CTFWeaponBase *pWeapon = shared.GetDisguiseWeapon() )
		{
			CEconItemView *pItem = pWeapon->GetAttributeContainer() ? pWeapon->GetAttributeContainer()->GetItem() : NULL;
			weapon = pItem && pItem->IsValid() ? UTF8( pItem->GetItemName() ) : Localized( pWeapon->GetTFWpnData().szPrintName );
		}
	}
	out[ "name" ] = Node( name );
	out[ "weapon" ] = Node( weapon );
}

//-----------------------------------------------------------------------------
// Everything a class carries besides health and ammo, the way the stock HUD
// counts it: the item meters (cloak, drinks, banners, the Eyelander's heads,
// revenge crits, the killstreak...), stickybombs out, the shield's charge, a
// weapon being charged, metal, the medigun's kind.
//-----------------------------------------------------------------------------
void WriteClassInfo( fui::Runtime &runtime, C_TFPlayer *pPlayer, Node &hud )
{
	const bool bAlive = pPlayer->IsAlive() && pPlayer->GetTeamNumber() >= FIRST_GAME_TEAM;
	CTFPlayerShared &shared = pPlayer->m_Shared;

	Node &meters = hud[ "meters" ];
	meters.SetArray();
	if ( bAlive && !shared.InCond( TF_COND_HALLOWEEN_GHOST_MODE ) )
	{
		const CUtlVector< IHudItemEffectMeterAutoList * > &list = IHudItemEffectMeterAutoList::AutoList();
		for ( int i = list.Count() - 1; i >= 0; --i )
		{
			CHudItemEffectMeter *pMeter = static_cast< CHudItemEffectMeter * >( list[ i ] );
			if ( !pMeter || !pMeter->IsEnabled() || !pMeter->ShouldDraw() )
				continue;
			Node &m = meters.Append();
			const int iCount = pMeter->GetCount();
			m[ "label" ] = Node( Localized( pMeter->GetLabelText() ) );
			m[ "frac" ] = Node( (double)Clamp( pMeter->GetProgress(), 0.f, 1.f ) );
			m[ "count" ] = Node( iCount );
			m[ "percent" ] = Node( iCount >= 0 && pMeter->ShowPercentSymbol() );
			m[ "bars" ] = Node( Max( 1, pMeter->GetNumProgressBar() ) );
			m[ "flash" ] = Node( pMeter->ShouldFlash() );
			m[ "streak" ] = Node( pMeter->IsKillstreakMeter() );
		}
		// A demoman's shield (the stock pipes panel shows it in their place).
		if ( shared.IsShieldEquipped() )
		{
			Node &m = meters.Append();
			const float flCharge = shared.GetDemomanChargeMeter() / 100.f;
			m[ "label" ] = Node( runtime.Translate( "fui_hud_shield" ) );
			m[ "frac" ] = Node( (double)Clamp( flCharge, 0.f, 1.f ) );
			m[ "count" ] = Node( -1 );
			m[ "percent" ] = Node( false );
			m[ "bars" ] = Node( 1 );
			m[ "flash" ] = Node( shared.InCond( TF_COND_SHIELD_CHARGE ) && flCharge <= 0.4f );
			m[ "streak" ] = Node( false );
		}
	}

	// Stickybombs out.
	int iStickies = -1;
	if ( bAlive && pPlayer->IsPlayerClass( TF_CLASS_DEMOMAN ) && pPlayer->Weapon_OwnsThisID( TF_WEAPON_PIPEBOMBLAUNCHER ) )
		iStickies = pPlayer->GetNumActivePipebombs();
	hud[ "stickies" ] = Node( iStickies );

	// A weapon that charges while held: the stickybomb launcher, the
	// Huntsman, a charged airblast (the stock charge meters).
	double flPower = -1.0;
	C_TFWeaponBase *pWeapon = bAlive ? pPlayer->GetActiveTFWeapon() : NULL;
	if ( ITFChargeUpWeapon *pCharge = dynamic_cast< ITFChargeUpWeapon * >( pWeapon ) )
	{
		if ( pCharge->CanCharge() && pCharge->GetChargeMaxTime() > 0.f )
		{
			const float flBegin = pCharge->GetChargeBeginTime();
			flPower = flBegin > 0.f ? Clamp( ( gpGlobals->curtime - flBegin ) / pCharge->GetChargeMaxTime(), 0.f, 1.f ) : 0.0;
		}
	}
	hud[ "power" ] = Node( flPower );

	// An engineer's metal.
	hud[ "metal" ] = Node( bAlive && pPlayer->IsPlayerClass( TF_CLASS_ENGINEER ) ? pPlayer->GetAmmoCount( TF_AMMO_METAL ) : -1 );
	hud[ "metal_max" ] = Node( pPlayer->GetMaxAmmo( TF_AMMO_METAL ) );

	// The medigun: which kind (the Vaccinator counts charges and has a resist).
	std::string kind, resist;
	int nCharges = -1;
	bool bReleasing = false;
	if ( CWeaponMedigun *pMedigun = dynamic_cast< CWeaponMedigun * >( pPlayer->Weapon_OwnsThisID( TF_WEAPON_MEDIGUN ) ) )
	{
		switch ( pMedigun->GetMedigunType() )
		{
		case MEDIGUN_UBER: kind = "kritz"; break;
		case MEDIGUN_QUICKFIX: kind = "quickfix"; break;
		case MEDIGUN_RESIST: kind = "vaccinator"; break;
		default: kind = "uber"; break;
		}
		bReleasing = pMedigun->IsReleasingCharge();
		if ( pMedigun->GetMedigunType() == MEDIGUN_RESIST )
		{
			static ConVarRef weapon_medigun_resist_num_chunks( "weapon_medigun_resist_num_chunks" );
			const int nChunks = Max( 1, weapon_medigun_resist_num_chunks.IsValid() ? weapon_medigun_resist_num_chunks.GetInt() : 4 );
			nCharges = (int)floorf( pMedigun->GetChargeLevel() * nChunks + 0.001f );
			static const char *s_Resists[] = { "bullet", "blast", "fire" };
			const int iResist = pMedigun->GetResistType();
			resist = iResist >= 0 && iResist < (int)ARRAYSIZE( s_Resists ) ? s_Resists[ iResist ] : "";
		}
	}
	hud[ "charge_kind" ] = Node( kind );
	hud[ "charge_resist" ] = Node( resist );
	hud[ "charges" ] = Node( nCharges );
	hud[ "charge_active" ] = Node( bReleasing );
}

//-----------------------------------------------------------------------------
// An engineer's buildings (the stock building status panel): a slot for each
// of the four, built or not.
//-----------------------------------------------------------------------------
void WriteBuildings( C_TFPlayer *pPlayer, Node &out )
{
	out.SetArray();
	if ( !pPlayer->IsPlayerClass( TF_CLASS_ENGINEER ) || pPlayer->GetTeamNumber() < FIRST_GAME_TEAM )
		return;
	static const struct
	{
		const char *pszKind;
		int iType, iMode;
	} s_Slots[] = {
		{ "sentry", OBJ_SENTRYGUN, 0 },
		{ "dispenser", OBJ_DISPENSER, 0 },
		{ "entrance", OBJ_TELEPORTER, MODE_TELEPORTER_ENTRANCE },
		{ "exit", OBJ_TELEPORTER, MODE_TELEPORTER_EXIT },
	};
	for ( const auto &slot : s_Slots )
	{
		C_BaseObject *pObj = pPlayer->GetObjectOfType( slot.iType, slot.iMode );
		if ( pObj && pObj->IsDisposableBuilding() )
			pObj = NULL;
		Node &b = out.Append();
		b[ "kind" ] = Node( slot.pszKind );
		b[ "built" ] = Node( pObj != NULL );
		if ( !pObj )
			continue;
		const int iLevel = Clamp( pObj->GetUpgradeLevel(), 1, 3 );
		b[ "level" ] = Node( iLevel );
		b[ "mini" ] = Node( pObj->IsMiniBuilding() );
		b[ "health" ] = Node( (double)pObj->GetHealth() / Max( 1, pObj->GetMaxHealth() ) );
		b[ "building" ] = Node( pObj->IsBuilding() );
		b[ "progress" ] = Node( (double)pObj->GetPercentageConstructed() );
		b[ "carried" ] = Node( pObj->IsCarried() );
		b[ "sapped" ] = Node( pObj->HasSapper() );
		b[ "disabled" ] = Node( pObj->IsDisabled() );
		const BuildingHudAlert_t eAlert = pObj->GetBuildingAlertLevel();
		b[ "alert" ] = Node( eAlert == BUILDING_HUD_ALERT_SAPPER ? "sapper" :
			( eAlert == BUILDING_HUD_ALERT_LOW_AMMO || eAlert == BUILDING_HUD_ALERT_VERY_LOW_AMMO ) ? "ammo" :
			( eAlert == BUILDING_HUD_ALERT_LOW_HEALTH || eAlert == BUILDING_HUD_ALERT_VERY_LOW_HEALTH ) ? "health" : "" );
		b[ "upgrade" ] = Node( iLevel < 3 && !pObj->IsMiniBuilding() ? (double)pObj->GetUpgradeMetal() / Max( 1, pObj->GetUpgradeMetalRequired() ) : -1.0 );
		b[ "ammo" ] = Node( -1.0 );
		b[ "rockets" ] = Node( -1.0 );
		b[ "kills" ] = Node( -1 );
		b[ "charge" ] = Node( -1.0 );
		if ( C_ObjectSentrygun *pSentry = dynamic_cast< C_ObjectSentrygun * >( pObj ) )
		{
			int iShells, iMaxShells, iRockets, iMaxRockets;
			pSentry->GetAmmoCount( iShells, iMaxShells, iRockets, iMaxRockets );
			b[ "ammo" ] = Node( (double)iShells / Max( 1, iMaxShells ) );
			if ( iLevel >= 3 )
				b[ "rockets" ] = Node( (double)iRockets / Max( 1, iMaxRockets ) );
			b[ "kills" ] = Node( pSentry->GetKills() );
			b[ "assists" ] = Node( pSentry->GetAssists() );
		}
		else if ( C_ObjectDispenser *pDispenser = dynamic_cast< C_ObjectDispenser * >( pObj ) )
		{
			const float flMax = pDispenser->IsMiniBuilding() ? MINI_DISPENSER_MAX_METAL : DISPENSER_MAX_METAL_AMMO;
			b[ "ammo" ] = Node( (double)Clamp( pDispenser->GetMetalAmmoCount() / flMax, 0.f, 1.f ) );
		}
		else if ( C_ObjectTeleporter *pTeleporter = dynamic_cast< C_ObjectTeleporter * >( pObj ) )
		{
			if ( pTeleporter->GetState() == TELEPORTER_STATE_RECHARGING && pTeleporter->GetCurrentRechargeDuration() > 0.f )
				b[ "charge" ] = Node( (double)Clamp( 1.f - ( pTeleporter->GetChargeTime() / pTeleporter->GetCurrentRechargeDuration() ), 0.f, 1.f ) );
			else
				b[ "charge" ] = Node( 1.0 );
			b[ "used" ] = Node( pTeleporter->GetTimesUsed() );
		}
	}
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
	// Before a round (and a match) starts: the countdown the stock match
	// status panel showed.
	const float flRestart = pRules ? pRules->GetRoundRestartTime() : -1.f;
	hud[ "restart" ] = Node( flRestart > gpGlobals->curtime ? (int)ceilf( flRestart - gpGlobals->curtime ) : -1 );

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
		FuiHudOverlayReset();
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
	WriteClassInfo( runtime, pPlayer, hud );
	WriteBuildings( pPlayer, hud[ "buildings" ] );
	WriteObjective( hud );
	s_KillFeed.Write( hud[ "kills" ] );
	FuiHudOverlayUpdate( runtime, hud );
	FuiWriteClassMenu( hud[ "menu" ] );
	runtime.Set( "hud", std::move( hud ) );
}

bool FrontressHudReplaces( const char *pszPart )
{
	if ( !pszPart )
		return false;
	for ( int i = 0; i < s_Replaces.Count(); ++i )
	{
		if ( !V_strcmp( s_Replaces[ i ], pszPart ) )
			return true;
	}
	return false;
}

bool FrontressHudReplaces( CHudElement *pElement )
{
	if ( !s_Replaces.Count() || !pElement )
		return false;

	// The item meters (cloak, drinks, banners...) are made per weapon, all
	// under one name.
	if ( dynamic_cast< CHudItemEffectMeter * >( pElement ) )
		return FrontressHudReplaces( "class" );

	static const struct
	{
		const char *pszToken;
		const char *pszElement;
	} s_Parts[] = {
		{ "health", "CTFHudPlayerStatus" },
		{ "health", "CHealthAccountPanel" },
		{ "ammo", "CTFHudWeaponAmmo" },
		{ "medic", "CHudMedicChargeMeter" },
		{ "class", "CHudDemomanPipes" },
		{ "class", "CHudDemomanChargeMeter" },
		{ "class", "CHudBowChargeMeter" },
		{ "class", "CHudFlameRocketChargeMeter" },
		{ "class", "CDisguiseStatus" },
		{ "class", "CHudAccountPanel" },
		{ "class", "BuildingStatus_Engineer" },	// CHudBuildingStatusContainer_Engineer
		{ "killfeed", "CTFHudDeathNotice" },
		{ "objective", "CTFHudObjectiveStatus" },
		{ "objective", "CTFHudKothTimeStatus" },
		{ "objective", "CTFHudMatchStatus" },
		{ "goal", "CHudTeamGoal" },
		{ "freezecam", "CTFFreezePanel" },
		{ "winpanel", "CTFWinPanel" },
		{ "winpanel", "CTFMatchSummary" },
		{ "chat", "CHudChat" },
		{ "quests", "CHudItemAttributeTracker" },
		{ "quests", "CQuestNotificationPanel" },
	};
	const char *pszElement = pElement->GetName();
	for ( const auto &part : s_Parts )
	{
		if ( V_strcmp( pszElement, part.pszElement ) )
			continue;
		// Modes our HUD does not draw keep the game's objective panel.
		if ( !V_strcmp( part.pszToken, "objective" ) && s_strMode.empty() )
			return false;
		// MvM keeps its own end of match.
		if ( !V_strcmp( part.pszToken, "winpanel" ) && TFGameRules() && TFGameRules()->IsMannVsMachineMode() )
			return false;
		if ( FrontressHudReplaces( part.pszToken ) )
			return true;
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
