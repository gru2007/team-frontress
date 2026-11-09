//========= Copyright Team Frontress, All rights reserved. ====================//
//
// Purpose: Epic Online Services, as much of it as Easy Anti-Cheat needs: the
//			platform, logging, and an EOS Connect login with the player's
//			Steam account.
//
// Compiled into client.dll and server.dll alike. Each module owns its own
// EOS platform. On a listen server both live in one process, which EOS
// allows; EOS_Initialize then succeeds for whichever module comes first and
// reports EOS_AlreadyConfigured to the other.
//
// Everything here exists only with FRONTRESS_EAC, which the build defines
// when the EOS SDK is present (src/game/shared/frontress_eac.vpc). Without
// it, FrontressEOS::BAvailable() is false and nothing else is declared.
//
// Product identity -- product, sandbox, deployment and the game client
// credentials -- comes from frontress_eos_config.h, which CI generates from
// repository variables (tools/eac/stage_eos_sdk.sh) and which is never
// committed. Any value can be overridden from the environment, under the
// same names prefixed FRONTRESS_EOS_. A dedicated server's own credentials
// (the TrustedServer policy) are never compiled in; they only come from its
// environment.
//
//=============================================================================//

#ifndef FRONTRESS_EOS_H
#define FRONTRESS_EOS_H
#ifdef _WIN32
#pragma once
#endif

#include "utlstring.h"

#ifdef FRONTRESS_EAC

#include "eos_sdk.h"
#include "eos_connect.h"
#include "steam/steam_api.h"

namespace FrontressEOS
{
	inline bool BAvailable() { return true; }

	struct Config_t
	{
		CUtlString m_strProductId;
		CUtlString m_strSandboxId;
		CUtlString m_strDeploymentId;
		CUtlString m_strClientId;
		CUtlString m_strClientSecret;

		bool BComplete() const;
	};

	// The game client identity: compiled in, overridable from the environment.
	void GetClientConfig( Config_t &config );

	// A dedicated server: the product from the build, credentials only from
	// FRONTRESS_EOS_SERVER_CLIENT_ID / FRONTRESS_EOS_SERVER_CLIENT_SECRET.
	void GetServerConfig( Config_t &config );

	// EOS_Initialize once per process, then a platform. NULL on failure, with
	// the reason already logged.
	EOS_HPlatform CreatePlatform( const Config_t &config, bool bIsServer );
	void ReleasePlatform( EOS_HPlatform &hPlatform );

	const char *ResultString( EOS_EResult eResult );

	// A line in tc2_launcher.log (Windows; TC2_LAUNCHER_LOG names it), written
	// and closed at once: it survives the process being closed from outside,
	// which console.log does not.
	void Trace( PRINTF_FORMAT_STRING const char *pszFormat, ... ) FMTFUNCTION( 1, 2 );
	CUtlString ProductUserIdString( EOS_ProductUserId id );

	//-------------------------------------------------------------------------
	// An EOS Connect login as the local Steam user. Steam issues a web API
	// ticket for the "epiconlineservices" identity; EOS exchanges it for the
	// player's Product User ID, creating the user on first login.
	//-------------------------------------------------------------------------
	class CSteamLogin
	{
	public:
		CSteamLogin( const char *pszLogTag );
		~CSteamLogin();

		void SetPlatform( EOS_HPlatform hPlatform );

		// Call every frame. Starts the login when it is not running and
		// retries failed ones with a back-off.
		void Think();

		bool BLoggedIn() const { return m_eState == k_eLoggedIn; }
		bool BFailed() const { return m_eState == k_eBackoff; }
		EOS_ProductUserId LocalUserId() const { return m_localUserId; }
		const char *LastError() const { return m_strLastError.Get(); }

		// The EOS ID token for the logged in user, for a game server to
		// verify. Empty when not logged in.
		CUtlString CopyIdToken() const;

	private:
		enum EState
		{
			k_eIdle,
			k_eWaitingTicket,
			k_eLoggingIn,
			k_eCreatingUser,
			k_eLoggedIn,
			k_eBackoff,
		};

		void Start();
		void Fail( const char *pszWhy );

		static void EOS_CALL OnLogin( const EOS_Connect_LoginCallbackInfo *pData );
		static void EOS_CALL OnCreateUser( const EOS_Connect_CreateUserCallbackInfo *pData );
		static void EOS_CALL OnAuthExpiration( const EOS_Connect_AuthExpirationCallbackInfo *pData );

		STEAM_CALLBACK( CSteamLogin, OnWebApiTicket, GetTicketForWebApiResponse_t );

		const char *m_pszLogTag;
		EOS_HPlatform m_hPlatform;
		EOS_NotificationId m_idAuthExpiration;
		EState m_eState;
		HAuthTicket m_hTicket;
		EOS_ProductUserId m_localUserId;
		double m_flNextAttempt;
		double m_flBackoff;
		CUtlString m_strLastError;
	};
}

#else // !FRONTRESS_EAC

namespace FrontressEOS
{
	inline bool BAvailable() { return false; }
}

#endif // FRONTRESS_EAC

#endif // FRONTRESS_EOS_H
