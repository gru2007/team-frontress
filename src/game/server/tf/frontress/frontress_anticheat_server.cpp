//========= Copyright Team Frontress, All rights reserved. ====================//
//
// Purpose: The server half of Easy Anti-Cheat. See the header.
//
//=============================================================================//

#include "cbase.h"
#include "frontress_anticheat_server.h"
#include "frontress_anticheat_shared.h"
#include "frontress_eos.h"
#include "KeyValues.h"

#ifdef FRONTRESS_EAC
#include "eos_anticheatserver.h"
#include "fmtstr.h"
#include "igamesystem.h"
#include "inetchannelinfo.h"
#include "recipientfilter.h"
#include "takedamageinfo.h"
#include "tf_player.h"
#include "tf_gamerules.h"
#include "tf_weaponbase.h"
#include "tf_gc_server.h"
#include "gc_clientsystem.h"
#include "gcsdk/gcsdk_auto.h"
#include "tf_gcmessages.h"
#include "tf_gcmessages.pb.h"
#include "utlmap.h"
#include "viewport_panel_names.h"
#endif

// memdbgon must be the last include file in a .cpp file!!!
#include "tier0/memdbgon.h"

#ifdef FRONTRESS_EAC

//-----------------------------------------------------------------------------
// Settings
//-----------------------------------------------------------------------------
static ConVar fr_eac_enable( "fr_eac_enable", "1", FCVAR_NOTIFY,
	"Run Easy Anti-Cheat on this server. Takes effect on the next map." );
static ConVar fr_eac_enforce( "fr_eac_enforce", "1", FCVAR_NOTIFY,
	"Remove players without working anti-cheat. 0 only logs what would have happened (a soft rollout)." );
static ConVar fr_eac_listen( "fr_eac_listen", "1", 0,
	"Protect a listen server with the host's own EOS login." );
static ConVar fr_eac_handshake_timeout( "fr_eac_handshake_timeout", "45", 0,
	"Seconds a joining player has to start anti-cheat before removal.", true, 10.f, true, 120.f );
static ConVar fr_eac_gameplay_data( "fr_eac_gameplay_data", "0", 0,
	"Opt in to EAC gameplay telemetry after enabling it for this EOS deployment. Client authentication and enforcement stay enabled independently." );
static ConVar fr_eac_tick_interval( "fr_eac_tick_interval", "2", 0,
	"Server ticks between player state samples sent to EAC.", true, 1.f, true, 66.f );
static ConVar fr_eac_debug( "fr_eac_debug", "0", 0, "Log Easy Anti-Cheat message traffic." );

#define ACDbg( ... ) do { if ( fr_eac_debug.GetBool() ) Msg( "[eac] " __VA_ARGS__ ); } while ( 0 )

// Anything bigger is not a message our client sends.
#define BASE64_LEN( n )				( ( ( ( n ) + 2 ) / 3 ) * 4 )
#define MAX_CLIENT_KV_PER_SECOND	120

//-----------------------------------------------------------------------------
// What a removed player is told. The details EAC adds are written for players.
//-----------------------------------------------------------------------------
static const char *ActionReasonText( EOS_EAntiCheatCommonClientActionReason eReason )
{
	switch ( eReason )
	{
	case EOS_EAntiCheatCommonClientActionReason::EOS_ACCCAR_InternalError:			return "internal error";
	case EOS_EAntiCheatCommonClientActionReason::EOS_ACCCAR_InvalidMessage:			return "invalid anti-cheat message";
	case EOS_EAntiCheatCommonClientActionReason::EOS_ACCCAR_AuthenticationFailed:	return "anti-cheat authentication failed; start the game from Steam";
	case EOS_EAntiCheatCommonClientActionReason::EOS_ACCCAR_NullClient:				return "anti-cheat is not running; start the game from Steam";
	case EOS_EAntiCheatCommonClientActionReason::EOS_ACCCAR_HeartbeatTimeout:		return "anti-cheat heartbeat timed out";
	case EOS_EAntiCheatCommonClientActionReason::EOS_ACCCAR_ClientViolation:		return "client integrity violation";
	case EOS_EAntiCheatCommonClientActionReason::EOS_ACCCAR_BackendViolation:		return "anti-cheat backend violation";
	case EOS_EAntiCheatCommonClientActionReason::EOS_ACCCAR_TemporaryCooldown:		return "temporary cooldown";
	case EOS_EAntiCheatCommonClientActionReason::EOS_ACCCAR_TemporaryBanned:		return "temporarily banned";
	case EOS_EAntiCheatCommonClientActionReason::EOS_ACCCAR_PermanentBanned:		return "permanently banned";
	default:								return "removed by anti-cheat";
	}
}

static const char *UnavailableText( const char *pszReason )
{
	if ( !V_strcmp( pszReason, FRONTRESS_AC_UNAVAILABLE_NOT_BUILT ) )
		return "This server requires Easy Anti-Cheat, and your game build does not include it";
	if ( !V_strcmp( pszReason, FRONTRESS_AC_UNAVAILABLE_NOT_PROTECTED ) )
		return "This server requires Easy Anti-Cheat. Start Team Frontress from Steam with Play";
	if ( !V_strcmp( pszReason, FRONTRESS_AC_UNAVAILABLE_LOGIN_FAILED ) )
		return "Easy Anti-Cheat could not sign you in to Epic Online Services. Check your connection and try again";
	return "Easy Anti-Cheat failed to start. Restart the game from Steam";
}

// kickid takes the rest of the line as the reason; keep it to one clean line.
static CUtlString SanitizeReason( const char *psz )
{
	CUtlString str;
	char buf[ 200 ];
	int n = 0;
	for ( const char *p = psz ? psz : ""; *p && n < (int)sizeof( buf ) - 1; ++p )
	{
		const char c = *p;
		buf[ n++ ] = ( c == ';' || c == '"' || c == '\n' || c == '\r' || c == '\t' ) ? ' ' : c;
	}
	buf[ n ] = '\0';
	str = buf;
	return str;
}

static void ToVec( const Vector &v, EOS_AntiCheatCommon_Vec3f &out )
{
	out.x = v.x;
	out.y = v.y;
	out.z = v.z;
}

static void ToQuat( const QAngle &ang, EOS_AntiCheatCommon_Quat &out )
{
	Quaternion q;
	AngleQuaternion( ang, q );
	out.w = q.w;
	out.x = q.x;
	out.y = q.y;
	out.z = q.z;
}

//=============================================================================
// CFrontressACServer
//=============================================================================
class CFrontressACServer : public CAutoGameSystemPerFrame
{
public:
	CFrontressACServer()
		: CAutoGameSystemPerFrame( "CFrontressACServer" )
		, m_login( "eac-server" )
		, m_hPlatform( NULL )
		, m_hAntiCheat( NULL )
		, m_idMessageToClient( EOS_INVALID_NOTIFICATIONID )
		, m_idActionRequired( EOS_INVALID_NOTIFICATIONID )
		, m_idAuthStatus( EOS_INVALID_NOTIFICATIONID )
		, m_bPlatformTried( false )
		, m_bListen( false )
		, m_bWantSession( false )
		, m_bSessionActive( false )
		, m_bGameplayData( false )
		, m_nSessionSerial( 0 )
		, m_nTick( 0 )
		, m_clients( DefLessFunc( int ) )
	{
	}

	virtual void Shutdown() OVERRIDE
	{
		EndSession();
		if ( m_hAntiCheat )
		{
			if ( m_idMessageToClient != EOS_INVALID_NOTIFICATIONID )
				EOS_AntiCheatServer_RemoveNotifyMessageToClient( m_hAntiCheat, m_idMessageToClient );
			if ( m_idActionRequired != EOS_INVALID_NOTIFICATIONID )
				EOS_AntiCheatServer_RemoveNotifyClientActionRequired( m_hAntiCheat, m_idActionRequired );
			if ( m_idAuthStatus != EOS_INVALID_NOTIFICATIONID )
				EOS_AntiCheatServer_RemoveNotifyClientAuthStatusChanged( m_hAntiCheat, m_idAuthStatus );
		}
		m_hAntiCheat = NULL;
		m_login.SetPlatform( NULL );
		FrontressEOS::ReleasePlatform( m_hPlatform );
		// A dedicated server is the only module in its process using EOS.
		if ( m_bPlatformTried && engine->IsDedicatedServer() )
			EOS_Shutdown();
	}

	virtual void LevelInitPostEntity() OVERRIDE
	{
		m_bWantSession = fr_eac_enable.GetBool();
		if ( m_bWantSession )
		{
			EnsurePlatform();
			TryBeginSession();
		}
	}

	virtual void LevelShutdownPreEntity() OVERRIDE
	{
		m_bWantSession = false;
		EndSession();
	}

	virtual void FrameUpdatePreEntityThink() OVERRIDE
	{
		if ( !m_hPlatform )
			return;
		if ( m_bListen )
			m_login.Think();
		EOS_Platform_Tick( m_hPlatform );
		TryBeginSession();
		if ( m_bSessionActive )
			CheckClients();
	}

	virtual void FrameUpdatePostEntityThink() OVERRIDE
	{
		if ( !m_bSessionActive || !m_bGameplayData )
			return;
		if ( ( ++m_nTick % fr_eac_tick_interval.GetInt() ) != 0 )
			return;
		FOR_EACH_MAP_FAST( m_clients, i )
		{
			Client_t *pClient = m_clients[i];
			if ( pClient->m_eState != k_eRegistered )
				continue;
			CTFPlayer *pPlayer = ToTFPlayer( UTIL_PlayerByUserId( pClient->m_nUserId ) );
			if ( pPlayer && pPlayer->IsAlive() )
				LogTick( pClient, pPlayer );
		}
	}

	// Player lifecycle ------------------------------------------------------

	void ClientActive( CBasePlayer *pPlayer )
	{
		if ( !pPlayer || pPlayer->IsFakeClient() || pPlayer->IsHLTV() || pPlayer->IsReplay() || !m_bWantSession )
			return;
		const int nUserId = pPlayer->GetUserID();
		RemoveClient( nUserId, false );

		Client_t *pClient = new Client_t;
		pClient->m_nUserId = nUserId;
		pPlayer->GetSteamID( &pClient->m_steamID );
		pClient->m_flActiveTime = Plat_FloatTime();
		m_clients.Insert( nUserId, pClient );

		if ( m_bSessionActive )
			SendHello( pClient );
	}

	void ClientDisconnect( CBasePlayer *pPlayer )
	{
		if ( pPlayer )
			RemoveClient( pPlayer->GetUserID(), true );
	}

	bool CanPlay( CBasePlayer *pPlayer )
	{
		if ( !m_bSessionActive || !fr_eac_enforce.GetBool() || !pPlayer )
			return true;
		const int idx = m_clients.Find( pPlayer->GetUserID() );
		if ( idx == m_clients.InvalidIndex() )
			return pPlayer->IsFakeClient() || pPlayer->IsHLTV() || pPlayer->IsReplay();
		const Client_t *pClient = m_clients[idx];
		return pClient->m_eState == k_eRegistered && pClient->m_eAuthStatus != EOS_EAntiCheatCommonClientAuthStatus::EOS_ACCCAS_Invalid;
	}

	bool BProtected() const { return m_bSessionActive; }

	// Client messages -------------------------------------------------------

	void HandleKeyValues( edict_t *pEdict, KeyValues *pKV )
	{
		CBasePlayer *pPlayer = (CBasePlayer *)CBaseEntity::Instance( pEdict );
		if ( !pPlayer )
			return;
		const int idx = m_clients.Find( pPlayer->GetUserID() );
		if ( idx == m_clients.InvalidIndex() )
			return;
		Client_t *pClient = m_clients[idx];

		// Rate limit: our client sends a handful per second at most.
		const double flNow = Plat_FloatTime();
		if ( flNow - pClient->m_flRateWindow >= 1.0 )
		{
			pClient->m_flRateWindow = flNow;
			pClient->m_nRateCount = 0;
		}
		if ( ++pClient->m_nRateCount > MAX_CLIENT_KV_PER_SECOND )
		{
			if ( pClient->m_nRateCount == MAX_CLIENT_KV_PER_SECOND + 1 )
				Remove( pClient, "Easy Anti-Cheat: too many anti-cheat messages", CMsgGC_Frontress_AntiCheatEvent::kEvent_KICKED, 0, "flood" );
			return;
		}

		const int nType = pKV->GetInt( "t" );
		if ( nType == k_EFrontressACClientMsg_Unavailable )
		{
			const char *pszReason = pKV->GetString( "r", "unknown" );
			Warning( "[eac] %s (%s) cannot run anti-cheat: %s\n", pPlayer->GetPlayerName(), pClient->m_steamID.Render(), pszReason );
			if ( pClient->m_eState == k_eAwaitingToken )
				Remove( pClient, UnavailableText( pszReason ), CMsgGC_Frontress_AntiCheatEvent::kEvent_NOT_PROTECTED, 0, pszReason );
			return;
		}

		const char *pszData = pKV->GetString( "d", "" );
		const int nLen = V_strlen( pszData );
		if ( nLen > FRONTRESS_AC_KV_CHUNK_BYTES )
			return;

		if ( nType == k_EFrontressACClientMsg_Token )
		{
			if ( pClient->m_eState != k_eAwaitingToken )
				return;
			const CFrontressACReassembly::EResult eResult = pClient->m_token.Add( pKV->GetInt( "s" ), pKV->GetInt( "i" ), pKV->GetInt( "n" ),
				(const uint8 *)pszData, nLen, BASE64_LEN( FRONTRESS_AC_MAX_TOKEN_BYTES ) );
			if ( eResult == CFrontressACReassembly::k_EComplete )
				OnToken( pClient );
		}
		else if ( nType == k_EFrontressACClientMsg_Data )
		{
			if ( pClient->m_eState != k_eRegistered )
				return;
			const CFrontressACReassembly::EResult eResult = pClient->m_data.Add( pKV->GetInt( "s" ), pKV->GetInt( "i" ), pKV->GetInt( "n" ),
				(const uint8 *)pszData, nLen, BASE64_LEN( FRONTRESS_AC_MAX_DATA_BYTES ) );
			if ( eResult == CFrontressACReassembly::k_EComplete )
				OnData( pClient );
		}
	}

	// Gameplay data ---------------------------------------------------------

	void OnRoundStart()
	{
		if ( !BGameplayData() )
			return;
		CUtlString strSession = SessionIdentifier();
		EOS_AntiCheatCommon_LogGameRoundStartOptions opts;
		V_memset( &opts, 0, sizeof( opts ) );
		opts.ApiVersion = EOS_ANTICHEATCOMMON_LOGGAMEROUNDSTART_API_LATEST;
		opts.SessionIdentifier = strSession.Get();
		opts.LevelName = STRING( gpGlobals->mapname );
		opts.ModeName = TFGameRules() ? TFGameRules()->GetGameTypeName() : "";
		opts.RoundTimeSeconds = 0;
		opts.CompetitionType = ( GTFGCClientSystem() && GTFGCClientSystem()->GetMatch() ) ? EOS_EAntiCheatCommonGameRoundCompetitionType::EOS_ACCGRCT_Ranked : EOS_EAntiCheatCommonGameRoundCompetitionType::EOS_ACCGRCT_Casual;
		EOS_AntiCheatServer_LogGameRoundStart( m_hAntiCheat, &opts );
	}

	void OnRoundEnd( int iWinningTeam )
	{
		if ( !BGameplayData() )
			return;
		EOS_AntiCheatCommon_LogGameRoundEndOptions opts;
		V_memset( &opts, 0, sizeof( opts ) );
		opts.ApiVersion = EOS_ANTICHEATCOMMON_LOGGAMEROUNDEND_API_LATEST;
		opts.WinningTeamId = (uint32_t)MAX( iWinningTeam, 0 );
		EOS_AntiCheatServer_LogGameRoundEnd( m_hAntiCheat, &opts );
	}

	void OnSpawned( CTFPlayer *pPlayer )
	{
		Client_t *pClient = RegisteredClient( pPlayer );
		if ( !pClient || !BGameplayData() )
			return;
		EOS_AntiCheatCommon_LogPlayerSpawnOptions opts;
		V_memset( &opts, 0, sizeof( opts ) );
		opts.ApiVersion = EOS_ANTICHEATCOMMON_LOGPLAYERSPAWN_API_LATEST;
		opts.SpawnedPlayerHandle = Handle( pClient );
		opts.TeamId = (uint32_t)pPlayer->GetTeamNumber();
		opts.CharacterId = (uint32_t)pPlayer->GetPlayerClass()->GetClassIndex();
		EOS_AntiCheatServer_LogPlayerSpawn( m_hAntiCheat, &opts );
	}

	void OnKilled( CBasePlayer *pPlayer )
	{
		Client_t *pClient = RegisteredClient( pPlayer );
		if ( !pClient || !BGameplayData() )
			return;
		EOS_AntiCheatCommon_LogPlayerDespawnOptions opts;
		V_memset( &opts, 0, sizeof( opts ) );
		opts.ApiVersion = EOS_ANTICHEATCOMMON_LOGPLAYERDESPAWN_API_LATEST;
		opts.DespawnedPlayerHandle = Handle( pClient );
		EOS_AntiCheatServer_LogPlayerDespawn( m_hAntiCheat, &opts );
	}

	void OnWeaponFired( CTFPlayer *pPlayer )
	{
		Client_t *pClient = RegisteredClient( pPlayer );
		if ( !pClient || !BGameplayData() )
			return;
		CTFWeaponBase *pWeapon = pPlayer->GetActiveTFWeapon();
		if ( !pWeapon )
			return;

		pClient->m_flLastFireTime = Plat_FloatTime();
		pClient->m_bLastFireMelee = pWeapon->IsMeleeWeapon();
		V_strncpy( pClient->m_szLastWeapon, pWeapon->GetClassname(), sizeof( pClient->m_szLastWeapon ) );

		EOS_AntiCheatCommon_Vec3f pos;
		EOS_AntiCheatCommon_Quat rot;
		ToVec( pPlayer->GetAbsOrigin(), pos );
		ToQuat( pPlayer->EyeAngles(), rot );

		EOS_AntiCheatCommon_LogPlayerUseWeaponData data;
		V_memset( &data, 0, sizeof( data ) );
		data.PlayerHandle = Handle( pClient );
		data.PlayerPosition = &pos;
		data.PlayerViewRotation = &rot;
		data.bIsPlayerViewZoomed = pPlayer->m_Shared.InCond( TF_COND_ZOOMED ) ? EOS_TRUE : EOS_FALSE;
		data.bIsMeleeAttack = pClient->m_bLastFireMelee ? EOS_TRUE : EOS_FALSE;
		data.WeaponName = pClient->m_szLastWeapon;

		EOS_AntiCheatCommon_LogPlayerUseWeaponOptions opts;
		V_memset( &opts, 0, sizeof( opts ) );
		opts.ApiVersion = EOS_ANTICHEATCOMMON_LOGPLAYERUSEWEAPON_API_LATEST;
		opts.UseWeaponData = &data;
		EOS_AntiCheatServer_LogPlayerUseWeapon( m_hAntiCheat, &opts );
	}

	void OnDamaged( CBasePlayer *pVictimBase, const CTakeDamageInfo &info, int iDamageTaken )
	{
		if ( !BGameplayData() || iDamageTaken <= 0 )
			return;
		CTFPlayer *pVictim = ToTFPlayer( pVictimBase );
		Client_t *pVictimClient = RegisteredClient( pVictim );
		// EAC wants damage to registered players; damage dealt to bots tells
		// it nothing it could hold against the victim.
		if ( !pVictimClient )
			return;

		CTFPlayer *pAttacker = ToTFPlayer( info.GetAttacker() );
		Client_t *pAttackerClient = RegisteredClient( pAttacker );

		EOS_AntiCheatCommon_Vec3f victimPos, attackerPos, attackerEye, damagePos;
		EOS_AntiCheatCommon_Quat victimRot, attackerRot;
		ToVec( pVictim->GetAbsOrigin(), victimPos );
		ToQuat( pVictim->EyeAngles(), victimRot );
		ToVec( info.GetDamagePosition(), damagePos );

		EOS_AntiCheatCommon_LogPlayerTakeDamageOptions opts;
		V_memset( &opts, 0, sizeof( opts ) );
		opts.ApiVersion = EOS_ANTICHEATCOMMON_LOGPLAYERTAKEDAMAGE_API_LATEST;
		opts.VictimPlayerHandle = Handle( pVictimClient );
		opts.VictimPlayerPosition = &victimPos;
		opts.VictimPlayerViewRotation = &victimRot;
		opts.DamagePosition = &damagePos;

		const int bitsDamage = info.GetDamageType();
		const int iCustom = info.GetDamageCustom();
		opts.bIsHitscanAttack = ( bitsDamage & ( DMG_BULLET | DMG_BUCKSHOT ) ) ? EOS_TRUE : EOS_FALSE;
		opts.bIsCriticalHit = ( bitsDamage & DMG_CRITICAL ) ? EOS_TRUE : EOS_FALSE;
		opts.DamageTaken = (float)iDamageTaken;
		opts.HealthRemaining = (float)MAX( pVictim->GetHealth(), 0 );
		opts.DamageResult = pVictim->IsAlive() ? EOS_EAntiCheatCommonPlayerTakeDamageResult::EOS_ACCPTDR_None : EOS_EAntiCheatCommonPlayerTakeDamageResult::EOS_ACCPTDR_NormalToEliminated;
		if ( iCustom == TF_DMG_CUSTOM_BURNING || iCustom == TF_DMG_CUSTOM_BLEEDING )
			opts.DamageType = EOS_EAntiCheatCommonPlayerTakeDamageType::EOS_ACCPTDT_DamageOverTime;
		else if ( bitsDamage & DMG_BLAST )
			opts.DamageType = EOS_EAntiCheatCommonPlayerTakeDamageType::EOS_ACCPTDT_RadialDamage;
		else
			opts.DamageType = EOS_EAntiCheatCommonPlayerTakeDamageType::EOS_ACCPTDT_PointDamage;

		EOS_AntiCheatCommon_LogPlayerUseWeaponData weapon;
		if ( pAttacker && pAttacker != pVictim )
		{
			opts.DamageSource = pAttackerClient ? EOS_EAntiCheatCommonPlayerTakeDamageSource::EOS_ACCPTDS_Player : EOS_EAntiCheatCommonPlayerTakeDamageSource::EOS_ACCPTDS_NonPlayerCharacter;
			ToVec( pAttacker->GetAbsOrigin(), attackerPos );
			ToVec( pAttacker->EyePosition(), attackerEye );
			ToQuat( pAttacker->EyeAngles(), attackerRot );
			opts.AttackerPlayerPosition = &attackerPos;
			opts.AttackerPlayerViewRotation = &attackerRot;
			opts.AttackerPlayerViewPosition = &attackerEye;
			opts.bHasLineOfSight = pAttacker->FVisible( pVictim, MASK_SHOT ) ? EOS_TRUE : EOS_FALSE;

			if ( pAttackerClient )
			{
				opts.AttackerPlayerHandle = Handle( pAttackerClient );
				if ( pAttackerClient->m_szLastWeapon[0] )
				{
					V_memset( &weapon, 0, sizeof( weapon ) );
					weapon.PlayerHandle = opts.AttackerPlayerHandle;
					weapon.PlayerPosition = &attackerPos;
					weapon.PlayerViewRotation = &attackerRot;
					weapon.bIsPlayerViewZoomed = pAttacker->m_Shared.InCond( TF_COND_ZOOMED ) ? EOS_TRUE : EOS_FALSE;
					weapon.bIsMeleeAttack = pAttackerClient->m_bLastFireMelee ? EOS_TRUE : EOS_FALSE;
					weapon.WeaponName = pAttackerClient->m_szLastWeapon;
					opts.PlayerUseWeaponData = &weapon;
					opts.TimeSincePlayerUseWeaponMs = (uint32_t)MAX( 0.0, ( Plat_FloatTime() - pAttackerClient->m_flLastFireTime ) * 1000.0 );
				}
			}
		}
		else
		{
			opts.DamageSource = ( info.GetAttacker() && info.GetAttacker() != pVictim ) ? EOS_EAntiCheatCommonPlayerTakeDamageSource::EOS_ACCPTDS_NonPlayerCharacter : EOS_EAntiCheatCommonPlayerTakeDamageSource::EOS_ACCPTDS_World;
		}

		EOS_AntiCheatServer_LogPlayerTakeDamage( m_hAntiCheat, &opts );
	}

private:
	enum EClientState
	{
		k_eAwaitingHello,	// joined before the session started
		k_eAwaitingToken,
		k_eVerifying,
		k_eRegistered,
		k_eRemoved,
	};

	struct Client_t
	{
		Client_t()
			: m_nUserId( 0 ), m_eState( k_eAwaitingHello ), m_eAuthStatus( EOS_EAntiCheatCommonClientAuthStatus::EOS_ACCCAS_Invalid )
			, m_flActiveTime( 0.0 ), m_flRateWindow( 0.0 ), m_nRateCount( 0 ), m_nOutSeq( 0 )
			, m_bNetworkActive( true ), m_bReportedVerified( false )
			, m_flLastFireTime( 0.0 ), m_bLastFireMelee( false )
		{
			m_szLastWeapon[0] = '\0';
		}

		int m_nUserId;
		CSteamID m_steamID;
		EClientState m_eState;
		EOS_EAntiCheatCommonClientAuthStatus m_eAuthStatus;
		double m_flActiveTime;
		double m_flRateWindow;
		int m_nRateCount;
		int m_nOutSeq;
		bool m_bNetworkActive;
		bool m_bReportedVerified;
		CUtlString m_strProductUserId;
		CFrontressACReassembly m_token;
		CFrontressACReassembly m_data;
		double m_flLastFireTime;
		bool m_bLastFireMelee;
		char m_szLastWeapon[ EOS_ANTICHEATCOMMON_LOGPLAYERUSEWEAPON_WEAPONNAME_MAX_LENGTH ];
	};

	// The EAC client handle is the engine user ID: unique for the life of
	// the server, never reused for another player, never zero.
	static EOS_AntiCheatCommon_ClientHandle Handle( const Client_t *pClient ) { return (EOS_AntiCheatCommon_ClientHandle)(intptr_t)pClient->m_nUserId; }
	static int UserIdFromHandle( EOS_AntiCheatCommon_ClientHandle h ) { return (int)(intptr_t)h; }

	Client_t *FindClient( int nUserId )
	{
		const int idx = m_clients.Find( nUserId );
		return idx == m_clients.InvalidIndex() ? NULL : m_clients[idx];
	}

	Client_t *RegisteredClient( CBasePlayer *pPlayer )
	{
		if ( !pPlayer || !m_bSessionActive )
			return NULL;
		Client_t *pClient = FindClient( pPlayer->GetUserID() );
		return ( pClient && pClient->m_eState == k_eRegistered ) ? pClient : NULL;
	}

	bool BGameplayData() const { return m_bSessionActive && m_bGameplayData && m_hAntiCheat; }

	// Platform and session --------------------------------------------------

	void EnsurePlatform()
	{
		if ( m_bPlatformTried )
			return;
		m_bPlatformTried = true;

		FrontressEOS::Config_t config;
		if ( engine->IsDedicatedServer() )
		{
			FrontressEOS::GetServerConfig( config );
			if ( config.m_strClientId.IsEmpty() )
			{
				Warning( "[eac] *** This server is NOT protected by Easy Anti-Cheat: set FRONTRESS_EOS_SERVER_CLIENT_ID and FRONTRESS_EOS_SERVER_CLIENT_SECRET. ***\n" );
				return;
			}
			m_hPlatform = FrontressEOS::CreatePlatform( config, true );
		}
		else
		{
			if ( !fr_eac_listen.GetBool() )
				return;
			FrontressEOS::GetClientConfig( config );
			m_hPlatform = FrontressEOS::CreatePlatform( config, false );
			if ( m_hPlatform )
			{
				m_bListen = true;
				m_login.SetPlatform( m_hPlatform );
			}
		}
		if ( !m_hPlatform )
			return;
		m_strDeploymentId = config.m_strDeploymentId;

		m_hAntiCheat = EOS_Platform_GetAntiCheatServerInterface( m_hPlatform );
		if ( !m_hAntiCheat )
		{
			Warning( "[eac] the EOS anti-cheat server interface is not available\n" );
			return;
		}

		EOS_AntiCheatServer_AddNotifyMessageToClientOptions msgOpts;
		V_memset( &msgOpts, 0, sizeof( msgOpts ) );
		msgOpts.ApiVersion = EOS_ANTICHEATSERVER_ADDNOTIFYMESSAGETOCLIENT_API_LATEST;
		m_idMessageToClient = EOS_AntiCheatServer_AddNotifyMessageToClient( m_hAntiCheat, &msgOpts, this, &OnMessageToClient );

		EOS_AntiCheatServer_AddNotifyClientActionRequiredOptions actionOpts;
		V_memset( &actionOpts, 0, sizeof( actionOpts ) );
		actionOpts.ApiVersion = EOS_ANTICHEATSERVER_ADDNOTIFYCLIENTACTIONREQUIRED_API_LATEST;
		m_idActionRequired = EOS_AntiCheatServer_AddNotifyClientActionRequired( m_hAntiCheat, &actionOpts, this, &OnActionRequired );

		EOS_AntiCheatServer_AddNotifyClientAuthStatusChangedOptions authOpts;
		V_memset( &authOpts, 0, sizeof( authOpts ) );
		authOpts.ApiVersion = EOS_ANTICHEATSERVER_ADDNOTIFYCLIENTAUTHSTATUSCHANGED_API_LATEST;
		m_idAuthStatus = EOS_AntiCheatServer_AddNotifyClientAuthStatusChanged( m_hAntiCheat, &authOpts, this, &OnAuthStatusChanged );
	}

	void TryBeginSession()
	{
		if ( m_bSessionActive || !m_bWantSession || !m_hAntiCheat )
			return;
		// A listen server's session belongs to the host's EOS user.
		if ( m_bListen && !m_login.BLoggedIn() )
			return;

		static ConVarRef hostname( "hostname" );
		m_bGameplayData = fr_eac_gameplay_data.GetBool() && !m_bListen;

		EOS_AntiCheatServer_BeginSessionOptions opts;
		V_memset( &opts, 0, sizeof( opts ) );
		opts.ApiVersion = EOS_ANTICHEATSERVER_BEGINSESSION_API_LATEST;
		opts.RegisterTimeoutSeconds = 60;
		opts.ServerName = hostname.IsValid() ? hostname.GetString() : "Team Frontress";
		opts.bEnableGameplayData = m_bGameplayData ? EOS_TRUE : EOS_FALSE;
		opts.LocalUserId = m_bListen ? m_login.LocalUserId() : NULL;
		const EOS_EResult eResult = EOS_AntiCheatServer_BeginSession( m_hAntiCheat, &opts );
		if ( eResult != EOS_EResult::EOS_Success )
		{
			Warning( "[eac] BeginSession: %s -- this map runs unprotected\n", FrontressEOS::ResultString( eResult ) );
			m_bWantSession = false;
			return;
		}
		m_bSessionActive = true;
		m_nSessionSerial++;
		Msg( "[eac] protected session started (%s%s)\n", m_bListen ? "listen server" : "dedicated",
		     m_bGameplayData ? ", gameplay data on" : "" );

		// Ties EAC's view of this map to the GC's match.
		CUtlString strSession = SessionIdentifier();
		EOS_AntiCheatCommon_SetGameSessionIdOptions sessionOpts;
		V_memset( &sessionOpts, 0, sizeof( sessionOpts ) );
		sessionOpts.ApiVersion = EOS_ANTICHEATCOMMON_SETGAMESESSIONID_API_LATEST;
		sessionOpts.GameSessionId = strSession.Get();
		EOS_AntiCheatServer_SetGameSessionId( m_hAntiCheat, &sessionOpts );

		// Anyone already in the game (a listen server's host) hears hello now.
		FOR_EACH_MAP_FAST( m_clients, i )
		{
			if ( m_clients[i]->m_eState == k_eAwaitingHello )
				SendHello( m_clients[i] );
		}
	}

	void EndSession()
	{
		FOR_EACH_MAP_FAST( m_clients, i )
		{
			UnregisterFromEAC( m_clients[i] );
			delete m_clients[i];
		}
		m_clients.RemoveAll();

		if ( !m_bSessionActive )
			return;
		m_bSessionActive = false;
		EOS_AntiCheatServer_EndSessionOptions opts;
		V_memset( &opts, 0, sizeof( opts ) );
		opts.ApiVersion = EOS_ANTICHEATSERVER_ENDSESSION_API_LATEST;
		EOS_AntiCheatServer_EndSession( m_hAntiCheat, &opts );
		Msg( "[eac] protected session ended\n" );
	}

	CUtlString SessionIdentifier() const
	{
		CUtlString str;
		const CMatchInfo *pMatch = GTFGCClientSystem() ? GTFGCClientSystem()->GetMatch() : NULL;
		if ( pMatch && pMatch->m_nMatchID )
			str.Format( "match-%llu", (unsigned long long)pMatch->m_nMatchID );
		else
			str.Format( "%s-%u", STRING( gpGlobals->mapname ), m_nSessionSerial );
		return str;
	}

	// Per client ------------------------------------------------------------

	void SendHello( Client_t *pClient )
	{
		CBasePlayer *pPlayer = UTIL_PlayerByUserId( pClient->m_nUserId );
		if ( !pPlayer )
			return;
		pClient->m_eState = k_eAwaitingToken;
		pClient->m_flActiveTime = Plat_FloatTime();
		pClient->m_token.Reset();

		CSingleUserRecipientFilter filter( pPlayer );
		filter.MakeReliable();
		UserMessageBegin( filter, FRONTRESS_AC_USERMSG );
			WRITE_BYTE( k_EFrontressACServerMsg_Hello );
			WRITE_BYTE( FRONTRESS_AC_PROTOCOL );
			WRITE_BYTE( fr_eac_enforce.GetBool() ? FRONTRESS_AC_HELLO_ENFORCED : 0 );
		MessageEnd();
		ACDbg( "-> %s hello\n", pClient->m_steamID.Render() );
	}

	void SendData( Client_t *pClient, const uint8 *pData, int cubData )
	{
		CBasePlayer *pPlayer = UTIL_PlayerByUserId( pClient->m_nUserId );
		if ( !pPlayer )
			return;
		const int nCount = MAX( 1, ( cubData + FRONTRESS_AC_USERMSG_CHUNK_BYTES - 1 ) / FRONTRESS_AC_USERMSG_CHUNK_BYTES );
		if ( nCount > FRONTRESS_AC_MAX_CHUNKS )
			return;
		const int nSeq = ( ++pClient->m_nOutSeq ) & 0x7FFF;

		CSingleUserRecipientFilter filter( pPlayer );
		filter.MakeReliable();
		for ( int i = 0; i < nCount; i++ )
		{
			const int nOffset = i * FRONTRESS_AC_USERMSG_CHUNK_BYTES;
			const int nLen = MIN( FRONTRESS_AC_USERMSG_CHUNK_BYTES, cubData - nOffset );
			UserMessageBegin( filter, FRONTRESS_AC_USERMSG );
				WRITE_BYTE( k_EFrontressACServerMsg_Data );
				WRITE_SHORT( nSeq );
				WRITE_BYTE( i );
				WRITE_BYTE( nCount );
				WRITE_BYTE( nLen );
				WRITE_BITS( pData + nOffset, nLen * 8 );
			MessageEnd();
		}
		ACDbg( "-> %s data, %d bytes\n", pClient->m_steamID.Render(), cubData );
	}

	struct PendingVerify_t
	{
		CFrontressACServer *m_pThis;
		int m_nUserId;
		uint32 m_nSessionSerial;
		int m_nPlatform;
		CUtlString m_strProductUserId;
		CUtlString m_strToken;
	};

	void OnToken( Client_t *pClient )
	{
		// The reassembly buffer is not NUL-terminated; decode a copy that is.
		CUtlVector< char > text;
		text.AddMultipleToTail( pClient->m_token.Data().Count(), (const char *)pClient->m_token.Data().Base() );
		text.AddToTail( '\0' );
		pClient->m_token.Reset();
		CUtlVector< uint8 > raw;
		if ( !FrontressAC_Base64Decode( text.Base(), raw ) || raw.Count() == 0 || raw.Count() > FRONTRESS_AC_MAX_TOKEN_BYTES )
			return;
		raw.AddToTail( '\0' );

		// "<platform>\n<product user id>\n<jwt>"
		char *pszPlatform = (char *)raw.Base();
		char *pszUserId = V_strstr( pszPlatform, "\n" );
		char *pszJwt = pszUserId ? V_strstr( pszUserId + 1, "\n" ) : NULL;
		if ( !pszUserId || !pszJwt )
			return;
		*pszUserId++ = '\0';
		*pszJwt++ = '\0';

		PendingVerify_t *pVerify = new PendingVerify_t;
		pVerify->m_pThis = this;
		pVerify->m_nUserId = pClient->m_nUserId;
		pVerify->m_nSessionSerial = m_nSessionSerial;
		pVerify->m_nPlatform = V_atoi( pszPlatform );
		pVerify->m_strProductUserId = pszUserId;
		pVerify->m_strToken = pszJwt;

		EOS_Connect_IdToken token;
		V_memset( &token, 0, sizeof( token ) );
		token.ApiVersion = EOS_CONNECT_IDTOKEN_API_LATEST;
		token.ProductUserId = EOS_ProductUserId_FromString( pVerify->m_strProductUserId.Get() );
		token.JsonWebToken = pVerify->m_strToken.Get();

		EOS_Connect_VerifyIdTokenOptions opts;
		V_memset( &opts, 0, sizeof( opts ) );
		opts.ApiVersion = EOS_CONNECT_VERIFYIDTOKEN_API_LATEST;
		opts.IdToken = &token;

		pClient->m_eState = k_eVerifying;
		EOS_Connect_VerifyIdToken( EOS_Platform_GetConnectInterface( m_hPlatform ), &opts, pVerify, &OnTokenVerified );
		ACDbg( "verifying %s as %s\n", pClient->m_steamID.Render(), pVerify->m_strProductUserId.Get() );
	}

	static void EOS_CALL OnTokenVerified( const EOS_Connect_VerifyIdTokenCallbackInfo *pData )
	{
		PendingVerify_t *pVerify = (PendingVerify_t *)pData->ClientData;
		if ( !pVerify )
			return;
		CFrontressACServer *pThis = pVerify->m_pThis;
		Client_t *pClient = pThis->FindClient( pVerify->m_nUserId );
		if ( pClient && pVerify->m_nSessionSerial == pThis->m_nSessionSerial && pThis->m_bSessionActive && pClient->m_eState == k_eVerifying )
			pThis->FinishVerify( pClient, pVerify, pData );
		delete pVerify;
	}

	void FinishVerify( Client_t *pClient, const PendingVerify_t *pVerify, const EOS_Connect_VerifyIdTokenCallbackInfo *pData )
	{
		if ( pData->ResultCode != EOS_EResult::EOS_Success )
		{
			Warning( "[eac] %s: ID token rejected: %s\n", pClient->m_steamID.Render(), FrontressEOS::ResultString( pData->ResultCode ) );
			Remove( pClient, "Easy Anti-Cheat could not verify your identity. Restart the game from Steam",
			        CMsgGC_Frontress_AntiCheatEvent::kEvent_IDENTITY_MISMATCH, 0, FrontressEOS::ResultString( pData->ResultCode ) );
			return;
		}

		// The token must be this connection's Steam account, not a token
		// borrowed from someone else's EOS login.
		CFmtStr strSteamID( "%llu", pClient->m_steamID.ConvertToUint64() );
		const bool bSteam = pData->bIsAccountInfoPresent && pData->AccountIdType == EOS_EExternalAccountType::EOS_EAT_STEAM &&
		                    pData->AccountId && !V_strcmp( pData->AccountId, strSteamID.Get() );
		const bool bDeployment = !pData->DeploymentId || !pData->DeploymentId[0] || !V_strcmp( pData->DeploymentId, m_strDeploymentId.Get() );
		if ( !bSteam || !bDeployment )
		{
			Warning( "[eac] %s presented an EOS identity of %s (%s)\n", pClient->m_steamID.Render(),
			         pData->AccountId ? pData->AccountId : "?", bDeployment ? "another account" : "another deployment" );
			Remove( pClient, "Easy Anti-Cheat identity does not match your Steam account",
			        CMsgGC_Frontress_AntiCheatEvent::kEvent_IDENTITY_MISMATCH, 0, "account mismatch" );
			return;
		}

		CBasePlayer *pPlayer = UTIL_PlayerByUserId( pClient->m_nUserId );
		CUtlString strIP;
		if ( pPlayer )
		{
			INetChannelInfo *pNet = engine->GetPlayerNetInfo( pPlayer->entindex() );
			const char *pszAddress = pNet ? pNet->GetAddress() : NULL;
			if ( pszAddress && V_strcmp( pszAddress, "loopback" ) )
			{
				strIP = pszAddress;
				const char *pszColon = V_strrchr( strIP.Get(), ':' );
				if ( pszColon && V_strchr( strIP.Get(), ':' ) == pszColon )
					strIP.SetLength( pszColon - strIP.Get() );
			}
		}

		// Every PC client is a protected client. A platform outside the PC
		// set is not something our game runs on, and must not become a way to
		// register unprotected.
		EOS_EAntiCheatCommonClientPlatform ePlatform = EOS_EAntiCheatCommonClientPlatform::EOS_ACCCP_Unknown;
		if ( pVerify->m_nPlatform == (int)EOS_EAntiCheatCommonClientPlatform::EOS_ACCCP_Windows || pVerify->m_nPlatform == (int)EOS_EAntiCheatCommonClientPlatform::EOS_ACCCP_Mac || pVerify->m_nPlatform == (int)EOS_EAntiCheatCommonClientPlatform::EOS_ACCCP_Linux )
			ePlatform = (EOS_EAntiCheatCommonClientPlatform)pVerify->m_nPlatform;

		EOS_AntiCheatServer_RegisterClientOptions opts;
		V_memset( &opts, 0, sizeof( opts ) );
		opts.ApiVersion = EOS_ANTICHEATSERVER_REGISTERCLIENT_API_LATEST;
		opts.ClientHandle = Handle( pClient );
		opts.ClientType = EOS_EAntiCheatCommonClientType::EOS_ACCCT_ProtectedClient;
		opts.ClientPlatform = ePlatform;
		opts.IpAddress = strIP.IsEmpty() ? NULL : strIP.Get();
		opts.UserId = pData->ProductUserId;
		const EOS_EResult eResult = EOS_AntiCheatServer_RegisterClient( m_hAntiCheat, &opts );
		if ( eResult != EOS_EResult::EOS_Success )
		{
			Warning( "[eac] RegisterClient %s: %s\n", pClient->m_steamID.Render(), FrontressEOS::ResultString( eResult ) );
			Remove( pClient, "Easy Anti-Cheat could not register you on this server", CMsgGC_Frontress_AntiCheatEvent::kEvent_KICKED, 0, "register failed" );
			return;
		}
		pClient->m_eState = k_eRegistered;
		pClient->m_strProductUserId = FrontressEOS::ProductUserIdString( pData->ProductUserId );
		pClient->m_data.Reset();
		Msg( "[eac] %s registered (EOS %s)\n", pClient->m_steamID.Render(), pClient->m_strProductUserId.Get() );
	}

	void OnData( Client_t *pClient )
	{
		CUtlVector< char > text;
		text.AddMultipleToTail( pClient->m_data.Data().Count(), (const char *)pClient->m_data.Data().Base() );
		text.AddToTail( '\0' );
		pClient->m_data.Reset();

		CUtlVector< uint8 > raw;
		if ( !FrontressAC_Base64Decode( text.Base(), raw ) || raw.Count() == 0 || raw.Count() > FRONTRESS_AC_MAX_DATA_BYTES )
			return;

		EOS_AntiCheatServer_ReceiveMessageFromClientOptions opts;
		V_memset( &opts, 0, sizeof( opts ) );
		opts.ApiVersion = EOS_ANTICHEATSERVER_RECEIVEMESSAGEFROMCLIENT_API_LATEST;
		opts.ClientHandle = Handle( pClient );
		opts.DataLengthBytes = raw.Count();
		opts.Data = raw.Base();
		const EOS_EResult eResult = EOS_AntiCheatServer_ReceiveMessageFromClient( m_hAntiCheat, &opts );
		if ( eResult != EOS_EResult::EOS_Success )
			ACDbg( "ReceiveMessageFromClient %s: %s\n", pClient->m_steamID.Render(), FrontressEOS::ResultString( eResult ) );
	}

	void UnregisterFromEAC( Client_t *pClient )
	{
		if ( pClient->m_eState != k_eRegistered || !m_hAntiCheat || !m_bSessionActive )
			return;
		EOS_AntiCheatServer_UnregisterClientOptions opts;
		V_memset( &opts, 0, sizeof( opts ) );
		opts.ApiVersion = EOS_ANTICHEATSERVER_UNREGISTERCLIENT_API_LATEST;
		opts.ClientHandle = Handle( pClient );
		EOS_AntiCheatServer_UnregisterClient( m_hAntiCheat, &opts );
		pClient->m_eState = k_eRemoved;
	}

	void RemoveClient( int nUserId, bool bLog )
	{
		const int idx = m_clients.Find( nUserId );
		if ( idx == m_clients.InvalidIndex() )
			return;
		UnregisterFromEAC( m_clients[idx] );
		if ( bLog )
			ACDbg( "%s left\n", m_clients[idx]->m_steamID.Render() );
		delete m_clients[idx];
		m_clients.RemoveAt( idx );
	}

	// Removes the player from the server (when enforcing) and tells the GC.
	void Remove( Client_t *pClient, const char *pszWhy, CMsgGC_Frontress_AntiCheatEvent::EEvent eEvent, int nActionReason, const char *pszDetails )
	{
		ReportToGC( pClient, eEvent, nActionReason, pszDetails );
		if ( !fr_eac_enforce.GetBool() )
		{
			Warning( "[eac] would remove %s: %s (fr_eac_enforce 0)\n", pClient->m_steamID.Render(), pszWhy );
			return;
		}
		Msg( "[eac] removing %s: %s\n", pClient->m_steamID.Render(), pszWhy );
		const int nUserId = pClient->m_nUserId;
		UnregisterFromEAC( pClient );
		pClient->m_eState = k_eRemoved;
		engine->ServerCommand( CFmtStr( "kickid %d %s\n", nUserId, SanitizeReason( pszWhy ).Get() ) );
	}

	void ReportToGC( Client_t *pClient, CMsgGC_Frontress_AntiCheatEvent::EEvent eEvent, int nActionReason, const char *pszDetails )
	{
		if ( !GCClientSystem() )
			return;
		GCSDK::CProtoBufMsg< CMsgGC_Frontress_AntiCheatEvent > msg( k_EMsgGC_Frontress_AntiCheatEvent );
		msg.Body().set_steam_id( pClient->m_steamID.ConvertToUint64() );
		msg.Body().set_product_user_id( pClient->m_strProductUserId.Get() );
		msg.Body().set_event( eEvent );
		msg.Body().set_action_reason( nActionReason );
		msg.Body().set_details( pszDetails ? pszDetails : "" );
		msg.Body().set_enforced( fr_eac_enforce.GetBool() );
		const CMatchInfo *pMatch = GTFGCClientSystem() ? GTFGCClientSystem()->GetMatch() : NULL;
		if ( pMatch )
			msg.Body().set_match_id( pMatch->m_nMatchID );
		GCClientSystem()->BSendMessage( msg );
	}

	void CheckClients()
	{
		const double flNow = Plat_FloatTime();
		FOR_EACH_MAP_FAST( m_clients, i )
		{
			Client_t *pClient = m_clients[i];
			if ( pClient->m_eState == k_eAwaitingToken && flNow - pClient->m_flActiveTime > fr_eac_handshake_timeout.GetFloat() )
			{
				// Removing may not kick (fr_eac_enforce 0); never ask twice.
				pClient->m_flActiveTime = flNow + 1e9;
				Remove( pClient, "Easy Anti-Cheat did not start in time. Start Team Frontress from Steam with Play",
				        CMsgGC_Frontress_AntiCheatEvent::kEvent_NOT_PROTECTED, 0, "handshake timeout" );
				continue;
			}

			// A player whose connection stalls should not be removed by a
			// heartbeat timeout EAC can be told to expect.
			if ( pClient->m_eState == k_eRegistered )
			{
				CBasePlayer *pPlayer = UTIL_PlayerByUserId( pClient->m_nUserId );
				INetChannelInfo *pNet = pPlayer ? engine->GetPlayerNetInfo( pPlayer->entindex() ) : NULL;
				const bool bActive = !pNet || !pNet->IsTimingOut();
				if ( bActive != pClient->m_bNetworkActive )
				{
					pClient->m_bNetworkActive = bActive;
					EOS_AntiCheatServer_SetClientNetworkStateOptions opts;
					V_memset( &opts, 0, sizeof( opts ) );
					opts.ApiVersion = EOS_ANTICHEATSERVER_SETCLIENTNETWORKSTATE_API_LATEST;
					opts.ClientHandle = Handle( pClient );
					opts.bIsNetworkActive = bActive ? EOS_TRUE : EOS_FALSE;
					EOS_AntiCheatServer_SetClientNetworkState( m_hAntiCheat, &opts );
				}
			}
		}
	}

	void LogTick( Client_t *pClient, CTFPlayer *pPlayer )
	{
		EOS_AntiCheatCommon_Vec3f pos, eye;
		EOS_AntiCheatCommon_Quat rot;
		ToVec( pPlayer->GetAbsOrigin(), pos );
		ToVec( pPlayer->EyePosition(), eye );
		ToQuat( pPlayer->EyeAngles(), rot );

		EOS_EAntiCheatCommonPlayerMovementState eMovement = EOS_EAntiCheatCommonPlayerMovementState::EOS_ACCPMS_None;
		if ( pPlayer->GetMoveType() == MOVETYPE_LADDER )
			eMovement = EOS_EAntiCheatCommonPlayerMovementState::EOS_ACCPMS_OnLadder;
		else if ( pPlayer->GetMoveType() == MOVETYPE_NOCLIP || pPlayer->GetMoveType() == MOVETYPE_FLY )
			eMovement = EOS_EAntiCheatCommonPlayerMovementState::EOS_ACCPMS_Flying;
		else if ( pPlayer->GetWaterLevel() >= WL_Waist )
			eMovement = EOS_EAntiCheatCommonPlayerMovementState::EOS_ACCPMS_Swimming;
		else if ( pPlayer->GetFlags() & FL_DUCKING )
			eMovement = EOS_EAntiCheatCommonPlayerMovementState::EOS_ACCPMS_Crouching;
		else if ( !( pPlayer->GetFlags() & FL_ONGROUND ) )
			eMovement = EOS_EAntiCheatCommonPlayerMovementState::EOS_ACCPMS_Falling;

		EOS_AntiCheatCommon_LogPlayerTickOptions opts;
		V_memset( &opts, 0, sizeof( opts ) );
		opts.ApiVersion = EOS_ANTICHEATCOMMON_LOGPLAYERTICK_API_LATEST;
		opts.PlayerHandle = Handle( pClient );
		opts.PlayerPosition = &pos;
		opts.PlayerViewRotation = &rot;
		opts.PlayerViewPosition = &eye;
		opts.bIsPlayerViewZoomed = pPlayer->m_Shared.InCond( TF_COND_ZOOMED ) ? EOS_TRUE : EOS_FALSE;
		opts.PlayerHealth = (float)pPlayer->GetHealth();
		opts.PlayerMovementState = eMovement;
		EOS_AntiCheatServer_LogPlayerTick( m_hAntiCheat, &opts );
	}

	// EOS callbacks -----------------------------------------------------------

	static void EOS_CALL OnMessageToClient( const EOS_AntiCheatCommon_OnMessageToClientCallbackInfo *pData )
	{
		CFrontressACServer *pThis = (CFrontressACServer *)pData->ClientData;
		Client_t *pClient = pThis ? pThis->FindClient( UserIdFromHandle( pData->ClientHandle ) ) : NULL;
		if ( pClient && pClient->m_eState == k_eRegistered )
			pThis->SendData( pClient, (const uint8 *)pData->MessageData, (int)pData->MessageDataSizeBytes );
	}

	static void EOS_CALL OnActionRequired( const EOS_AntiCheatCommon_OnClientActionRequiredCallbackInfo *pData )
	{
		CFrontressACServer *pThis = (CFrontressACServer *)pData->ClientData;
		Client_t *pClient = pThis ? pThis->FindClient( UserIdFromHandle( pData->ClientHandle ) ) : NULL;
		if ( !pClient || pData->ClientAction != EOS_EAntiCheatCommonClientAction::EOS_ACCCA_RemovePlayer )
			return;

		const EOS_EAntiCheatCommonClientActionReason eReason = pData->ActionReasonCode;
		CMsgGC_Frontress_AntiCheatEvent::EEvent eEvent = CMsgGC_Frontress_AntiCheatEvent::kEvent_KICKED;
		if ( eReason == EOS_EAntiCheatCommonClientActionReason::EOS_ACCCAR_TemporaryBanned )
			eEvent = CMsgGC_Frontress_AntiCheatEvent::kEvent_TEMP_BANNED;
		else if ( eReason == EOS_EAntiCheatCommonClientActionReason::EOS_ACCCAR_PermanentBanned )
			eEvent = CMsgGC_Frontress_AntiCheatEvent::kEvent_PERM_BANNED;
		else if ( eReason == EOS_EAntiCheatCommonClientActionReason::EOS_ACCCAR_ClientViolation || eReason == EOS_EAntiCheatCommonClientActionReason::EOS_ACCCAR_BackendViolation )
			eEvent = CMsgGC_Frontress_AntiCheatEvent::kEvent_VIOLATION;
		else if ( eReason == EOS_EAntiCheatCommonClientActionReason::EOS_ACCCAR_NullClient || eReason == EOS_EAntiCheatCommonClientActionReason::EOS_ACCCAR_AuthenticationFailed )
			eEvent = CMsgGC_Frontress_AntiCheatEvent::kEvent_NOT_PROTECTED;

		const char *pszDetails = pData->ActionReasonDetailsString ? pData->ActionReasonDetailsString : "";
		CFmtStr strWhy( "Easy Anti-Cheat: %s%s%s", ActionReasonText( eReason ), pszDetails[0] ? " - " : "", pszDetails );
		pThis->Remove( pClient, strWhy.Get(), eEvent, (int)eReason, pszDetails );
	}

	static void EOS_CALL OnAuthStatusChanged( const EOS_AntiCheatCommon_OnClientAuthStatusChangedCallbackInfo *pData )
	{
		CFrontressACServer *pThis = (CFrontressACServer *)pData->ClientData;
		Client_t *pClient = pThis ? pThis->FindClient( UserIdFromHandle( pData->ClientHandle ) ) : NULL;
		if ( !pClient )
			return;
		const bool bFirst = pClient->m_eAuthStatus == EOS_EAntiCheatCommonClientAuthStatus::EOS_ACCCAS_Invalid;
		pClient->m_eAuthStatus = pData->ClientAuthStatus;
		ACDbg( "%s auth status %d\n", pClient->m_steamID.Render(), (int)pData->ClientAuthStatus );

		if ( pData->ClientAuthStatus == EOS_EAntiCheatCommonClientAuthStatus::EOS_ACCCAS_RemoteAuthComplete && !pClient->m_bReportedVerified )
		{
			pClient->m_bReportedVerified = true;
			pThis->ReportToGC( pClient, CMsgGC_Frontress_AntiCheatEvent::kEvent_VERIFIED, 0, "" );
		}

		// Held out of the team menu until now: let them in.
		if ( bFirst && pClient->m_eAuthStatus != EOS_EAntiCheatCommonClientAuthStatus::EOS_ACCCAS_Invalid )
		{
			CBasePlayer *pPlayer = UTIL_PlayerByUserId( pClient->m_nUserId );
			if ( pPlayer )
			{
				ClientPrint( pPlayer, HUD_PRINTCENTER, "#Frontress_EAC_Protected" );
				const bool bMatch = GTFGCClientSystem() && GTFGCClientSystem()->GetMatch();
				if ( !bMatch && pPlayer->GetTeamNumber() == TEAM_UNASSIGNED && fr_eac_enforce.GetBool() )
					pPlayer->ShowViewPortPanel( PANEL_TEAM, true );
			}
		}
	}

	// Remembered for token checks; the platform copied its own.
	CUtlString m_strDeploymentId;
	FrontressEOS::CSteamLogin m_login;
	EOS_HPlatform m_hPlatform;
	EOS_HAntiCheatServer m_hAntiCheat;
	EOS_NotificationId m_idMessageToClient;
	EOS_NotificationId m_idActionRequired;
	EOS_NotificationId m_idAuthStatus;
	bool m_bPlatformTried;
	bool m_bListen;
	bool m_bWantSession;
	bool m_bSessionActive;
	bool m_bGameplayData;
	uint32 m_nSessionSerial;
	uint32 m_nTick;
	CUtlMap< int, Client_t * > m_clients;
};

static CFrontressACServer s_FrontressACServer;

static CFrontressACServer *ACServer() { return &s_FrontressACServer; }

void FrontressAC_Server_ClientActive( CBasePlayer *pPlayer ) { ACServer()->ClientActive( pPlayer ); }
void FrontressAC_Server_ClientDisconnect( CBasePlayer *pPlayer ) { ACServer()->ClientDisconnect( pPlayer ); }
bool FrontressAC_Server_CanPlay( CBasePlayer *pPlayer ) { return ACServer()->CanPlay( pPlayer ); }
bool FrontressAC_Server_IsProtected() { return ACServer()->BProtected(); }
void FrontressAC_Server_OnRoundStart() { ACServer()->OnRoundStart(); }
void FrontressAC_Server_OnRoundEnd( int iWinningTeam ) { ACServer()->OnRoundEnd( iWinningTeam ); }
void FrontressAC_Server_OnPlayerSpawned( CTFPlayer *pPlayer ) { ACServer()->OnSpawned( pPlayer ); }
void FrontressAC_Server_OnPlayerKilled( CBasePlayer *pPlayer ) { ACServer()->OnKilled( pPlayer ); }
void FrontressAC_Server_OnWeaponFired( CTFPlayer *pPlayer ) { ACServer()->OnWeaponFired( pPlayer ); }
void FrontressAC_Server_OnPlayerDamaged( CBasePlayer *pVictim, const CTakeDamageInfo &info, int iDamageTaken ) { ACServer()->OnDamaged( pVictim, info, iDamageTaken ); }

bool FrontressAC_Server_HandleKeyValues( edict_t *pEdict, KeyValues *pKeyValues )
{
	if ( !pKeyValues || V_stricmp( pKeyValues->GetName(), FRONTRESS_AC_KV_NAME ) )
		return false;
	ACServer()->HandleKeyValues( pEdict, pKeyValues );
	return true;
}

CON_COMMAND( fr_eac_status, "Show the Easy Anti-Cheat state of this server." )
{
	if ( !UTIL_IsCommandIssuedByServerAdmin() )
		return;
	Msg( "Easy Anti-Cheat: %s\n", FrontressAC_Server_IsProtected() ? "protected session active" : "NOT protected" );
}

#else // !FRONTRESS_EAC

//=============================================================================
// Without the EOS SDK this server is unprotected. It still owns the
// "FrontressAC" command so clients' messages never reach the game rules.
//=============================================================================
void FrontressAC_Server_ClientActive( CBasePlayer * ) {}
void FrontressAC_Server_ClientDisconnect( CBasePlayer * ) {}
bool FrontressAC_Server_CanPlay( CBasePlayer * ) { return true; }
bool FrontressAC_Server_IsProtected() { return false; }
void FrontressAC_Server_OnRoundStart() {}
void FrontressAC_Server_OnRoundEnd( int ) {}
void FrontressAC_Server_OnPlayerSpawned( CTFPlayer * ) {}
void FrontressAC_Server_OnPlayerKilled( CBasePlayer * ) {}
void FrontressAC_Server_OnWeaponFired( CTFPlayer * ) {}
void FrontressAC_Server_OnPlayerDamaged( CBasePlayer *, const CTakeDamageInfo &, int ) {}

bool FrontressAC_Server_HandleKeyValues( edict_t *, KeyValues *pKeyValues )
{
	return pKeyValues && !V_stricmp( pKeyValues->GetName(), FRONTRESS_AC_KV_NAME );
}

#endif // FRONTRESS_EAC
