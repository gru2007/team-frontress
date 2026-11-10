//========= Copyright Team Frontress, All rights reserved. ====================//
//
// Purpose: The client half of Easy Anti-Cheat. See the header.
//
//=============================================================================//

#include "cbase.h"
#include "frontress_anticheat_client.h"
#include "frontress_anticheat_shared.h"
#include "frontress_eos.h"
#include "c_user_message_register.h"
#include "igamesystem.h"
#include "KeyValues.h"
#include "fmtstr.h"
#include "confirm_dialog.h"
#include "clientsteamcontext.h"
#include "tier3/tier3.h"
#include <vgui/ILocalize.h>

#ifdef FRONTRESS_EAC
#include "eos_anticheatclient.h"
#include "eos_reports.h"
#include "tf_gcmessages.h"
#endif

// memdbgon must be the last include file in a .cpp file!!!
#include "tier0/memdbgon.h"

static ConVar fr_eac_debug( "fr_eac_debug", "0", FCVAR_ARCHIVE, "Log Easy Anti-Cheat message traffic." );

#define ACDbg( ... ) do { if ( fr_eac_debug.GetBool() ) Msg( "[eac] " __VA_ARGS__ ); } while ( 0 )

//-----------------------------------------------------------------------------
// Sending to the server: base64 text in numbered KeyValues chunks.
//-----------------------------------------------------------------------------
static int s_nOutSeq = 0;

static void SendToServer( EFrontressACClientMsg eType, const uint8 *pData, int cubData )
{
	if ( !engine->IsConnected() )
		return;

	CUtlString strB64;
	FrontressAC_Base64Encode( pData, cubData, strB64 );
	const int nTotal = strB64.Length();
	const int nCount = MAX( 1, ( nTotal + FRONTRESS_AC_KV_CHUNK_BYTES - 1 ) / FRONTRESS_AC_KV_CHUNK_BYTES );
	if ( nCount > FRONTRESS_AC_MAX_CHUNKS )
	{
		Warning( "[eac] message of %d bytes is too large to send\n", cubData );
		return;
	}

	const int nSeq = ( ++s_nOutSeq ) & 0x7FFF;
	char szChunk[ FRONTRESS_AC_KV_CHUNK_BYTES + 1 ];
	for ( int i = 0; i < nCount; i++ )
	{
		const int nOffset = i * FRONTRESS_AC_KV_CHUNK_BYTES;
		const int nLen = MIN( FRONTRESS_AC_KV_CHUNK_BYTES, nTotal - nOffset );
		V_memcpy( szChunk, strB64.Get() + nOffset, MAX( nLen, 0 ) );
		szChunk[ MAX( nLen, 0 ) ] = '\0';

		KeyValues *pKV = new KeyValues( FRONTRESS_AC_KV_NAME );
		pKV->SetInt( "t", eType );
		pKV->SetInt( "s", nSeq );
		pKV->SetInt( "i", i );
		pKV->SetInt( "n", nCount );
		pKV->SetString( "d", szChunk );
		// The engine serialises and then deletes it.
		engine->ServerCmdKeyValues( pKV );
	}
	ACDbg( "-> server type %d, %d bytes in %d chunk(s)\n", eType, cubData, nCount );
}

static void SendUnavailable( const char *pszReason )
{
	if ( !engine->IsConnected() )
		return;
	KeyValues *pKV = new KeyValues( FRONTRESS_AC_KV_NAME );
	pKV->SetInt( "t", k_EFrontressACClientMsg_Unavailable );
	pKV->SetString( "r", pszReason );
	engine->ServerCmdKeyValues( pKV );
	Warning( "[eac] this server requires Easy Anti-Cheat, which is not available: %s\n", pszReason );
}

#ifdef FRONTRESS_EAC

//=============================================================================
// With the EOS SDK
//=============================================================================
class CFrontressACClient : public CAutoGameSystemPerFrame
{
public:
	CFrontressACClient()
		: CAutoGameSystemPerFrame( "CFrontressACClient" )
		, m_login( "eac" )
		, m_hPlatform( NULL )
		, m_hAntiCheat( NULL )
		, m_idMessageToServer( EOS_INVALID_NOTIFICATIONID )
		, m_idViolation( EOS_INVALID_NOTIFICATIONID )
		, m_bServerWantsSession( false )
		, m_bSessionActive( false )
		, m_bViolationPending( false )
		, m_flHelloTime( 0.0 )
	{
	}

	virtual bool Init() OVERRIDE
	{
		if ( CommandLine()->FindParm( "-noeac" ) )
			Msg( "[eac] -noeac: protected servers will refuse this client\n" );
		return true;
	}

	virtual void Shutdown() OVERRIDE
	{
		EndSession();
		if ( m_hAntiCheat )
		{
			if ( m_idMessageToServer != EOS_INVALID_NOTIFICATIONID )
				EOS_AntiCheatClient_RemoveNotifyMessageToServer( m_hAntiCheat, m_idMessageToServer );
			if ( m_idViolation != EOS_INVALID_NOTIFICATIONID )
				EOS_AntiCheatClient_RemoveNotifyClientIntegrityViolated( m_hAntiCheat, m_idViolation );
		}
		m_idMessageToServer = m_idViolation = EOS_INVALID_NOTIFICATIONID;
		m_hAntiCheat = NULL;
		m_login.SetPlatform( NULL );
		FrontressEOS::ReleasePlatform( m_hPlatform );
	}

	virtual void Update( float frametime ) OVERRIDE
	{
		EnsurePlatform();
		if ( !m_hPlatform )
			return;

		m_login.Think();
		EOS_Platform_Tick( m_hPlatform );

		// A server said hello before the login finished: start the session
		// the moment it does, or give up with a reason the server can log
		// well before its own handshake timeout.
		if ( m_bServerWantsSession && !m_bSessionActive )
		{
			if ( !engine->IsConnected() )
				m_bServerWantsSession = false;
			else if ( m_login.BLoggedIn() )
				BeginSession();
			else if ( Plat_FloatTime() - m_flHelloTime > 30.0 )
			{
				m_bServerWantsSession = false;
				SendUnavailable( FRONTRESS_AC_UNAVAILABLE_LOGIN_FAILED );
			}
		}

		if ( m_bViolationPending )
		{
			m_bViolationPending = false;
			ShowMessageBox( "Easy Anti-Cheat", m_strViolation.Get() );
		}
	}

	// Leaving the server or changing level ends the protected session; the
	// server ends its side at the same moment and says hello again after.
	virtual void LevelShutdownPreEntity() OVERRIDE
	{
		EndSession();
		m_bServerWantsSession = false;
		m_in.Reset();
	}

	void OnServerMessage( bf_read &msg )
	{
		const int nType = msg.ReadByte();
		if ( nType == k_EFrontressACServerMsg_Hello )
		{
			const int nProtocol = msg.ReadByte();
			msg.ReadByte(); // flags
			ACDbg( "<- server hello (protocol %d)\n", nProtocol );
			if ( CommandLine()->FindParm( "-noeac" ) )
			{
				SendUnavailable( FRONTRESS_AC_UNAVAILABLE_NOT_PROTECTED );
				return;
			}
			// A new hello on the same connection restarts the session.
			EndSession();
			m_in.Reset();
			m_bServerWantsSession = true;
			m_flHelloTime = Plat_FloatTime();
			if ( m_login.BLoggedIn() )
				BeginSession();
			return;
		}

		if ( nType == k_EFrontressACServerMsg_Data )
		{
			const int nSeq = msg.ReadShort();
			const int nIndex = msg.ReadByte();
			const int nCount = msg.ReadByte();
			const int nLen = msg.ReadByte();
			uint8 buf[ 256 ];
			if ( nLen > (int)sizeof( buf ) || !msg.ReadBytes( buf, nLen ) )
			{
				m_in.Reset();
				return;
			}
			const CFrontressACReassembly::EResult eResult = m_in.Add( nSeq, nIndex, nCount, buf, nLen, FRONTRESS_AC_MAX_DATA_BYTES );
			if ( eResult == CFrontressACReassembly::k_EComplete )
			{
				if ( m_bSessionActive && m_hAntiCheat )
				{
					EOS_AntiCheatClient_ReceiveMessageFromServerOptions opts;
					V_memset( &opts, 0, sizeof( opts ) );
					opts.ApiVersion = EOS_ANTICHEATCLIENT_RECEIVEMESSAGEFROMSERVER_API_LATEST;
					opts.DataLengthBytes = m_in.Data().Count();
					opts.Data = m_in.Data().Base();
					const EOS_EResult eRecv = EOS_AntiCheatClient_ReceiveMessageFromServer( m_hAntiCheat, &opts );
					if ( eRecv != EOS_EResult::EOS_Success )
						Warning( "[eac] ReceiveMessageFromServer: %s\n", FrontressEOS::ResultString( eRecv ) );
					ACDbg( "<- server data, %d bytes\n", m_in.Data().Count() );
				}
				m_in.Reset();
			}
			else if ( eResult == CFrontressACReassembly::k_EError )
			{
				Warning( "[eac] dropped a malformed message from the server\n" );
			}
		}
	}

	void ReportPlayer( const CSteamID &target, int nReason, uint32 unDetail, const char *pszComment, const char *pszMap, const char *pszServer );
	bool BCanReport() const { return m_hPlatform && m_login.BLoggedIn(); }

	bool BSessionActive() const { return m_bSessionActive; }

	// Whether the anti-cheat module the bootstrapper loads is in this process:
	// a protected session can only start when it is.
	CUtlString ModuleStatus() const
	{
		if ( !m_hPlatform )
			return CUtlString( m_bPlatformFailed ? "EOS is off" : "EOS not started yet" );
		if ( !m_hAntiCheat )
			return CUtlString( "no anti-cheat interface" );
		EOS_AntiCheatClient_GetModuleBuildIdOptions opts;
		V_memset( &opts, 0, sizeof( opts ) );
		opts.ApiVersion = EOS_ANTICHEATCLIENT_GETMODULEBUILDID_API_LATEST;
		uint32_t unBuild = 0;
		const EOS_EResult eResult = EOS_AntiCheatClient_GetModuleBuildId( m_hAntiCheat, &opts, &unBuild );
		CUtlString str;
		if ( eResult == EOS_EResult::EOS_Success )
			str.Format( "loaded (build %u)", unBuild );
		else if ( eResult == EOS_EResult::EOS_NotFound )
			str = "NOT loaded: the game was not started through start_protected_game, or EAC fell back to its null client";
		else
			str.Format( "unknown (%s)", FrontressEOS::ResultString( eResult ) );
		return str;
	}
	CUtlString ProductUserId() const { return m_login.BLoggedIn() ? FrontressEOS::ProductUserIdString( m_login.LocalUserId() ) : CUtlString(); }
	const char *LastViolation() const { return m_strViolation.Get(); }

private:
	void EnsurePlatform()
	{
		if ( m_hPlatform || m_bPlatformFailed )
			return;
		// Steam has to be up first: the login is a Steam ticket.
		if ( !steamapicontext || !steamapicontext->SteamUser() )
			return;

		FrontressEOS::Config_t config;
		FrontressEOS::GetClientConfig( config );
		m_hPlatform = FrontressEOS::CreatePlatform( config, false );
		if ( !m_hPlatform )
		{
			m_bPlatformFailed = true;
			return;
		}
		m_login.SetPlatform( m_hPlatform );

		// NULL when the game was not started through the anti-cheat
		// bootstrapper on some SDK versions; BeginSession reports it on others.
		m_hAntiCheat = EOS_Platform_GetAntiCheatClientInterface( m_hPlatform );
		if ( !m_hAntiCheat )
		{
			Msg( "[eac] the anti-cheat is not loaded; start the game from Steam to play on protected servers\n" );
			return;
		}

		EOS_AntiCheatClient_AddNotifyMessageToServerOptions msgOpts;
		V_memset( &msgOpts, 0, sizeof( msgOpts ) );
		msgOpts.ApiVersion = EOS_ANTICHEATCLIENT_ADDNOTIFYMESSAGETOSERVER_API_LATEST;
		m_idMessageToServer = EOS_AntiCheatClient_AddNotifyMessageToServer( m_hAntiCheat, &msgOpts, this, &OnMessageToServer );

		EOS_AntiCheatClient_AddNotifyClientIntegrityViolatedOptions violationOpts;
		V_memset( &violationOpts, 0, sizeof( violationOpts ) );
		violationOpts.ApiVersion = EOS_ANTICHEATCLIENT_ADDNOTIFYCLIENTINTEGRITYVIOLATED_API_LATEST;
		m_idViolation = EOS_AntiCheatClient_AddNotifyClientIntegrityViolated( m_hAntiCheat, &violationOpts, this, &OnIntegrityViolated );
	}

	void BeginSession()
	{
		if ( m_bSessionActive )
			return;
		m_bServerWantsSession = false;

		if ( !m_hAntiCheat )
		{
			SendUnavailable( FRONTRESS_AC_UNAVAILABLE_NOT_PROTECTED );
			return;
		}

		EOS_AntiCheatClient_BeginSessionOptions opts;
		V_memset( &opts, 0, sizeof( opts ) );
		opts.ApiVersion = EOS_ANTICHEATCLIENT_BEGINSESSION_API_LATEST;
		opts.LocalUserId = m_login.LocalUserId();
		opts.Mode = EOS_EAntiCheatClientMode::EOS_ACCM_ClientServer;
		const EOS_EResult eResult = EOS_AntiCheatClient_BeginSession( m_hAntiCheat, &opts );
		if ( eResult != EOS_EResult::EOS_Success )
		{
			Warning( "[eac] BeginSession: %s\n", FrontressEOS::ResultString( eResult ) );
			SendUnavailable( eResult == EOS_EResult::EOS_AntiCheat_ClientProtectionNotAvailable
			                     ? FRONTRESS_AC_UNAVAILABLE_NOT_PROTECTED
			                     : FRONTRESS_AC_UNAVAILABLE_SESSION_FAILED );
			return;
		}
		m_bSessionActive = true;

		// Who we are, in a form the server can check against our Steam
		// account without trusting us: "<platform>\n<product user id>\n<jwt>".
		const CUtlString strToken = m_login.CopyIdToken();
		const CUtlString strUserId = FrontressEOS::ProductUserIdString( m_login.LocalUserId() );
		if ( strToken.IsEmpty() || strUserId.IsEmpty() )
		{
			EndSession();
			SendUnavailable( FRONTRESS_AC_UNAVAILABLE_LOGIN_FAILED );
			return;
		}
#if defined( _WIN32 )
		const int nPlatform = (int)EOS_EAntiCheatCommonClientPlatform::EOS_ACCCP_Windows;
#elif defined( OSX )
		const int nPlatform = (int)EOS_EAntiCheatCommonClientPlatform::EOS_ACCCP_Mac;
#else
		const int nPlatform = (int)EOS_EAntiCheatCommonClientPlatform::EOS_ACCCP_Linux;
#endif
		CUtlString strPayload;
		strPayload.Format( "%d\n%s\n%s", nPlatform, strUserId.Get(), strToken.Get() );
		SendToServer( k_EFrontressACClientMsg_Token, (const uint8 *)strPayload.Get(), strPayload.Length() );
		Msg( "[eac] protected session started\n" );
	}

	void EndSession()
	{
		if ( !m_bSessionActive )
			return;
		m_bSessionActive = false;
		if ( !m_hAntiCheat )
			return;
		EOS_AntiCheatClient_EndSessionOptions opts;
		V_memset( &opts, 0, sizeof( opts ) );
		opts.ApiVersion = EOS_ANTICHEATCLIENT_ENDSESSION_API_LATEST;
		EOS_AntiCheatClient_EndSession( m_hAntiCheat, &opts );
		ACDbg( "protected session ended\n" );
	}

	static void EOS_CALL OnMessageToServer( const EOS_AntiCheatClient_OnMessageToServerCallbackInfo *pData )
	{
		CFrontressACClient *pThis = (CFrontressACClient *)pData->ClientData;
		if ( !pThis || !pThis->m_bSessionActive )
			return;
		SendToServer( k_EFrontressACClientMsg_Data, (const uint8 *)pData->MessageData, (int)pData->MessageDataSizeBytes );
	}

	static void EOS_CALL OnIntegrityViolated( const EOS_AntiCheatClient_OnClientIntegrityViolatedCallbackInfo *pData )
	{
		CFrontressACClient *pThis = (CFrontressACClient *)pData->ClientData;
		if ( !pThis )
			return;
		// EAC requires the message to reach the player as it is: it is
		// already localised and tells them why online play is refused.
		pThis->m_strViolation = pData->ViolationMessage ? pData->ViolationMessage : "Easy Anti-Cheat integrity violation";
		pThis->m_bViolationPending = true;
		Warning( "[eac] integrity violation (%d): %s\n", (int)pData->ViolationType, pThis->m_strViolation.Get() );
	}

	struct PendingReport_t
	{
		CFrontressACClient *m_pThis;
		CUtlString m_strTarget;
		EOS_EPlayerReportsCategory m_eCategory;
		CUtlString m_strMessage;
		CUtlString m_strContext;
	};

	static void EOS_CALL OnMappingsQueried( const EOS_Connect_QueryExternalAccountMappingsCallbackInfo *pData );
	static void EOS_CALL OnReportSent( const EOS_Reports_SendPlayerBehaviorReportCompleteCallbackInfo *pData );

	FrontressEOS::CSteamLogin m_login;
	EOS_HPlatform m_hPlatform;
	EOS_HAntiCheatClient m_hAntiCheat;
	EOS_NotificationId m_idMessageToServer;
	EOS_NotificationId m_idViolation;
	bool m_bPlatformFailed = false;
	bool m_bServerWantsSession;
	bool m_bSessionActive;
	bool m_bViolationPending;
	double m_flHelloTime;
	CUtlString m_strViolation;
	CFrontressACReassembly m_in;
};

static CFrontressACClient s_FrontressACClient;

//-----------------------------------------------------------------------------
// Player reports
//-----------------------------------------------------------------------------
// Keeps what may go into the report's JSON context as is: letters, digits
// and the punctuation of map names and addresses.
static void ACReportToken( const char *pszIn, char *pszOut, int nOut )
{
	int n = 0;
	for ( const char *p = pszIn ? pszIn : ""; *p && n < nOut - 1; ++p )
	{
		const char c = *p;
		if ( ( c >= 'a' && c <= 'z' ) || ( c >= 'A' && c <= 'Z' ) || ( c >= '0' && c <= '9' ) || V_strchr( "._-:[]", c ) )
			pszOut[ n++ ] = c;
	}
	pszOut[ n ] = 0;
}

void CFrontressACClient::ReportPlayer( const CSteamID &target, int nReason, uint32 unDetail, const char *pszComment, const char *pszMap, const char *pszServer )
{
	if ( !BCanReport() )
		return;

	// CMsgGC_ReportPlayer::EReason and EDetail.
	EOS_EPlayerReportsCategory eCategory = EOS_EPlayerReportsCategory::EOS_PRC_Other;
	const char *pszReason = "other";
	switch ( nReason )
	{
	case CMsgGC_ReportPlayer_EReason_kReason_CHEATING:
		pszReason = "cheating";
		eCategory = ( unDetail == CMsgGC_ReportPlayer_EDetail_kDetail_EXPLOIT ) ? EOS_EPlayerReportsCategory::EOS_PRC_Exploiting : EOS_EPlayerReportsCategory::EOS_PRC_Cheating;
		break;
	case CMsgGC_ReportPlayer_EReason_kReason_HARASSMENT:
		pszReason = "harassment";
		eCategory = ( unDetail == CMsgGC_ReportPlayer_EDetail_kDetail_NAME ) ? EOS_EPlayerReportsCategory::EOS_PRC_OffensiveProfile : EOS_EPlayerReportsCategory::EOS_PRC_VerbalAbuse;
		break;
	case CMsgGC_ReportPlayer_EReason_kReason_IDLE: pszReason = "idle"; break;
	case CMsgGC_ReportPlayer_EReason_kReason_GRIEFING: pszReason = "griefing"; break;
	default: break;
	}

	static const struct { uint32 unBit; const char *pszName; } s_Details[] = {
		{ CMsgGC_ReportPlayer_EDetail_kDetail_AIMBOT, "aimbot" }, { CMsgGC_ReportPlayer_EDetail_kDetail_WALLHACK, "wallhack" },
		{ CMsgGC_ReportPlayer_EDetail_kDetail_TRIGGERBOT, "triggerbot" }, { CMsgGC_ReportPlayer_EDetail_kDetail_MOVEMENT, "movement" },
		{ CMsgGC_ReportPlayer_EDetail_kDetail_EXPLOIT, "exploit" }, { CMsgGC_ReportPlayer_EDetail_kDetail_CHAT, "chat" },
		{ CMsgGC_ReportPlayer_EDetail_kDetail_VOICE, "voice" }, { CMsgGC_ReportPlayer_EDetail_kDetail_NAME, "name" },
	};
	CUtlString strDetails;
	for ( const auto &d : s_Details )
	{
		if ( unDetail & d.unBit )
			strDetails.Append( CFmtStr( "%s\"%s\"", strDetails.IsEmpty() ? "" : ",", d.pszName ).Get() );
	}
	char szMap[ 64 ], szServer[ 64 ];
	ACReportToken( pszMap, szMap, sizeof( szMap ) );
	ACReportToken( pszServer, szServer, sizeof( szServer ) );

	PendingReport_t *pReport = new PendingReport_t;
	pReport->m_pThis = this;
	pReport->m_strTarget.Format( "%llu", target.ConvertToUint64() );
	pReport->m_eCategory = eCategory;
	// The player's own words; EOS takes up to 512 characters (the menu
	// allows 200 bytes).
	pReport->m_strMessage = pszComment ? pszComment : "";
	pReport->m_strContext.Format( "{\"steamid\":\"%s\",\"reason\":\"%s\",\"detail\":[%s],\"map\":\"%s\",\"server\":\"%s\"}",
		pReport->m_strTarget.Get(), pszReason, strDetails.Get(), szMap, szServer );

	// EOS knows players by Product User ID; ask it for the one that belongs
	// to this Steam account.
	const char *rgpszIds[1] = { pReport->m_strTarget.Get() };
	EOS_Connect_QueryExternalAccountMappingsOptions opts;
	V_memset( &opts, 0, sizeof( opts ) );
	opts.ApiVersion = EOS_CONNECT_QUERYEXTERNALACCOUNTMAPPINGS_API_LATEST;
	opts.LocalUserId = m_login.LocalUserId();
	opts.AccountIdType = EOS_EExternalAccountType::EOS_EAT_STEAM;
	opts.ExternalAccountIds = rgpszIds;
	opts.ExternalAccountIdCount = 1;
	EOS_Connect_QueryExternalAccountMappings( EOS_Platform_GetConnectInterface( m_hPlatform ), &opts, pReport, &OnMappingsQueried );
}

void EOS_CALL CFrontressACClient::OnMappingsQueried( const EOS_Connect_QueryExternalAccountMappingsCallbackInfo *pData )
{
	PendingReport_t *pReport = (PendingReport_t *)pData->ClientData;
	if ( !pReport )
		return;
	CFrontressACClient *pThis = pReport->m_pThis;

	EOS_ProductUserId target = NULL;
	if ( pData->ResultCode == EOS_EResult::EOS_Success && pThis->m_hPlatform )
	{
		EOS_Connect_GetExternalAccountMappingsOptions opts;
		V_memset( &opts, 0, sizeof( opts ) );
		opts.ApiVersion = EOS_CONNECT_GETEXTERNALACCOUNTMAPPING_API_LATEST;
		opts.LocalUserId = pThis->m_login.LocalUserId();
		opts.AccountIdType = EOS_EExternalAccountType::EOS_EAT_STEAM;
		opts.TargetExternalUserId = pReport->m_strTarget.Get();
		target = EOS_Connect_GetExternalAccountMapping( EOS_Platform_GetConnectInterface( pThis->m_hPlatform ), &opts );
	}

	if ( !target || !EOS_ProductUserId_IsValid( target ) )
	{
		// The reported player never logged into EOS: the GC still has the
		// report; there is nothing to attach it to on Epic's side.
		ACDbg( "no EOS user for reported Steam account %s\n", pReport->m_strTarget.Get() );
		delete pReport;
		return;
	}

	EOS_Reports_SendPlayerBehaviorReportOptions opts;
	V_memset( &opts, 0, sizeof( opts ) );
	opts.ApiVersion = EOS_REPORTS_SENDPLAYERBEHAVIORREPORT_API_LATEST;
	opts.ReporterUserId = pThis->m_login.LocalUserId();
	opts.ReportedUserId = target;
	opts.Category = pReport->m_eCategory;
	opts.Message = pReport->m_strMessage.IsEmpty() ? NULL : pReport->m_strMessage.Get();
	opts.Context = pReport->m_strContext.Get();
	EOS_Reports_SendPlayerBehaviorReport( EOS_Platform_GetReportsInterface( pThis->m_hPlatform ), &opts, pReport, &OnReportSent );
}

void EOS_CALL CFrontressACClient::OnReportSent( const EOS_Reports_SendPlayerBehaviorReportCompleteCallbackInfo *pData )
{
	PendingReport_t *pReport = (PendingReport_t *)pData->ClientData;
	if ( pData->ResultCode != EOS_EResult::EOS_Success )
		Warning( "[eac] EOS player report failed: %s\n", FrontressEOS::ResultString( pData->ResultCode ) );
	else if ( pReport )
		ACDbg( "EOS player report sent for %s\n", pReport->m_strTarget.Get() );
	delete pReport;
}

void FrontressAC_Client_OnServerMessage( bf_read &msg ) { s_FrontressACClient.OnServerMessage( msg ); }
void FrontressAC_Client_ReportPlayer( const CSteamID &target, int nReason, uint32 unDetail, const char *pszComment, const char *pszMap, const char *pszServer )
{
	s_FrontressACClient.ReportPlayer( target, nReason, unDetail, pszComment, pszMap, pszServer );
}
bool FrontressAC_Client_CanReport() { return s_FrontressACClient.BCanReport(); }
bool FrontressAC_Client_BBuiltWithAntiCheat() { return true; }
bool FrontressAC_Client_BSessionActive() { return s_FrontressACClient.BSessionActive(); }
const char *FrontressAC_Client_LastViolation() { return s_FrontressACClient.LastViolation(); }
CUtlString FrontressAC_Client_ProductUserId() { return s_FrontressACClient.ProductUserId(); }
CUtlString FrontressAC_Client_ModuleStatus() { return s_FrontressACClient.ModuleStatus(); }

#else // !FRONTRESS_EAC

//=============================================================================
// Without the EOS SDK: answer a protected server's hello at once, so it can
// tell the player why instead of timing out.
//=============================================================================
void FrontressAC_Client_OnServerMessage( bf_read &msg )
{
	if ( msg.ReadByte() == k_EFrontressACServerMsg_Hello )
		SendUnavailable( FRONTRESS_AC_UNAVAILABLE_NOT_BUILT );
}

void FrontressAC_Client_ReportPlayer( const CSteamID &, int, uint32, const char *, const char *, const char * ) {}
bool FrontressAC_Client_CanReport() { return false; }
bool FrontressAC_Client_BBuiltWithAntiCheat() { return false; }
bool FrontressAC_Client_BSessionActive() { return false; }
const char *FrontressAC_Client_LastViolation() { return ""; }
CUtlString FrontressAC_Client_ProductUserId() { return CUtlString(); }
CUtlString FrontressAC_Client_ModuleStatus() { return CUtlString( "not in this build" ); }

#endif // FRONTRESS_EAC

//-----------------------------------------------------------------------------
// The Easy Anti-Cheat strings: the notice on the loading dialog
// (resource/loadingdialognobanner.res, in place of VAC's) and what a
// protected server prints. Every build loads them, because every build
// shows that dialog.
//-----------------------------------------------------------------------------
class CFrontressACLocalization : public CAutoGameSystem
{
public:
	CFrontressACLocalization() : CAutoGameSystem( "CFrontressACLocalization" ) {}

	virtual bool Init() OVERRIDE
	{
		if ( g_pVGuiLocalize )
			g_pVGuiLocalize->AddFile( "resource/frontress_eac_%language%.txt", "GAME", true );
		return true;
	}
};
static CFrontressACLocalization s_FrontressACLocalization;

static void __MsgFunc_FrontressAC( bf_read &msg )
{
	FrontressAC_Client_OnServerMessage( msg );
}
USER_MESSAGE_REGISTER( FrontressAC );

CON_COMMAND( fr_eac_status, "Show the Easy Anti-Cheat state of this client." )
{
	Msg( "Easy Anti-Cheat: %s\n", FrontressAC_Client_BBuiltWithAntiCheat() ? "built in" : "not in this build" );
	const CUtlString strUser = FrontressAC_Client_ProductUserId();
	Msg( "  EOS user: %s\n", strUser.IsEmpty() ? "not logged in" : strUser.Get() );
	Msg( "  anti-cheat module: %s\n", FrontressAC_Client_ModuleStatus().Get() );
	// A session starts only on a protected server, when it says hello.
	Msg( "  protected session: %s\n", FrontressAC_Client_BSessionActive() ? "active" : ( engine->IsConnected() ? "none" : "none (only on a protected server)" ) );
	const char *pszViolation = FrontressAC_Client_LastViolation();
	if ( pszViolation && pszViolation[0] )
		Msg( "  last violation: %s\n", pszViolation );
}
