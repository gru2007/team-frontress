//========= Copyright Valve Corporation, All rights reserved. ============//
//
// Purpose: Crash reporter
//
// Reports go to any server that speaks Sentry's protocol (Bugsink, GlitchTip,
// Sentry). What each report carries:
//  - a minidump of the crash (where every thread was, symbolicated on the
//    server from the PDBs / .dbg files CI uploads);
//  - the last ~64 KB of console output before it;
//  - the build, the platform and the player's SteamID.
//
// How the dump is made: crashpad, as Sentry ships it. Its handler, a second
// process, opens the game only once it has crashed, and uploads right away.
// On Windows the handler is named CrashReportHandler.exe: Easy Anti-Cheat
// lets a crash dumper that waits for the crash like this in only when its
// file name starts with "CrashReport" (Epic's "External Crash Dumpers").
// crashpad_wer.dll beside it gets the crashes Windows ends without asking
// the game (heap corruption, security checks) through WER.
//
// Sessions that end with no crash report at all (the process killed from
// outside) are reported on the next launch, with their console output.
//
// Without the handler (or with -crashinprocess) the Windows game writes its
// own dumps instead (crashreport_native.cpp) and uploads them on the next
// launch. -nosentry turns all of it off; -sentrydsn <dsn> sends one run
// elsewhere.
//
//===========================================================================//

// The standard library and sentry before Source's headers, which define
// min/max.
#include <string>
#include <vector>
#include "sentry.h"
#include "crashreport_native.h"

#include "cbase.h"

#include "crashreport.h"

#include <tier0/platform.h>
#include "filesystem.h"
#include "steam/steam_api.h"

// memdbgon.h must be the last include in a cpp file!!!!
#include "tier0/memdbgon.h"

//=============================================================================
// WHERE REPORTS GO: the DSN of the project on the crash server (in Bugsink,
// the project's page shows it). The only line to change. Empty sends nothing.
//=============================================================================
#ifndef FRONTRESS_SENTRY_DSN
#define FRONTRESS_SENTRY_DSN "https://7187ea260d984dd8be067c6aee64eb39@crash.team-frontress.org/1"
#endif

#define SENTRY_ENVIRONMENT "prod"
#define SENTRY_RELEASE_PREFIX "frontress@"

#if defined( _WIN32 )
	#define PATH_SEP			"\\"
	// crashpad_handler, renamed for Easy Anti-Cheat (see the top).
	#define CRASHPAD_HANDLER	"CrashReportHandler.exe"
#else
	#define PATH_SEP			"/"
	#define CRASHPAD_HANDLER	"crashpad_handler"
#endif

#ifdef CLIENT_DLL
	#define SESSION_FILE		"session_client.txt"
#else
	#define SESSION_FILE		"session_server.txt"
#endif

// Pending reports kept at most; a game that crashes at every start must not
// fill the disk or send the same crash a hundred times.
static const int k_nMaxPendingReports = 5;
// How often the console output is saved for a session that dies without a
// dump. Only written when something was printed.
static const float k_flSessionSaveInterval = 2.0f;

static CCrashReporter s_CrashReporter;
CCrashReporter* GetCrashReporter() { return &s_CrashReporter; }

// The mod's version from steam.inf (PatchVersion), as Steam and the
// engine know it.
static std::string ProductVersion()
{
	CUtlBuffer buf( 0, 0, CUtlBuffer::TEXT_BUFFER );
	if ( !g_pFullFileSystem || !g_pFullFileSystem->ReadFile( "steam.inf", "MOD", buf ) )
		return "unknown";
	char szLine[ 256 ];
	while ( buf.IsValid() && buf.GetBytesRemaining() > 0 )
	{
		szLine[ 0 ] = 0;
		buf.GetLine( szLine, sizeof( szLine ) );
		if ( V_strnicmp( szLine, "PatchVersion=", 13 ) == 0 )
		{
			char *pszValue = szLine + 13;
			V_StripTrailingWhitespace( pszValue );
			if ( *pszValue )
				return pszValue;
		}
	}
	return "unknown";
}

static void AttachIfExists( const std::string &path, std::vector< sentry_attachment_t * > &attached )
{
	if ( CrashNative::FileExists( path.c_str() ) )
	{
		if ( sentry_attachment_t *p = sentry_attach_file( path.c_str() ) )
			attached.push_back( p );
	}
}

static void DetachAll( std::vector< sentry_attachment_t * > &attached )
{
	for ( sentry_attachment_t *p : attached )
		sentry_remove_attachment( p );
	attached.clear();
}

CCrashReporter::CCrashReporter()
	: CAutoGameSystemPerFrame("crashreport")
{
}

bool CCrashReporter::Init()
{
#ifdef GAME_DLL
	if ( !engine->IsDedicatedServer() )
		return true;
#endif

	if ( CommandLine()->FindParm( "-nosentry" ) )
		return true;

	if ( m_bInit )
		return true;

	const char *pszDSN = CommandLine()->ParmValue( "-sentrydsn", FRONTRESS_SENTRY_DSN );
	if ( !pszDSN || !pszDSN[ 0 ] )
	{
		DevMsg( "Crash reporting is off: no DSN in this build.\n" );
		return true;
	}

	// Paths from the folder of the game's executable: the Linux client runs
	// from Team Fortress 2's folder, so the working directory would miss.
	// What we write goes in crashreports/ there, not in bin/<platform>,
	// which Easy Anti-Cheat's integrity catalog covers whole.
	const std::string exeDir = CrashNative::ExeDir();
	const std::string rootDir = exeDir.empty() ? std::string( "." ) : exeDir;
	const std::string handlerPath = rootDir + PATH_SEP PLATFORM_BIN_DIR PATH_SEP CRASHPAD_HANDLER;
	m_ReportsDir = rootDir + PATH_SEP "crashreports";
	const std::string databasePath = m_ReportsDir + PATH_SEP ".sentry";
	m_SessionPath = m_ReportsDir + PATH_SEP SESSION_FILE;
	CrashNative::MakeDirs( m_ReportsDir.c_str() );

#if defined( _WIN32 )
	m_bInProcess = CommandLine()->FindParm( "-crashinprocess" ) || !CrashNative::FileExists( handlerPath.c_str() );
#else
	m_bInProcess = false;
#endif

	const std::string release = SENTRY_RELEASE_PREFIX + ProductVersion();

	sentry_options_t* options = sentry_options_new();
	sentry_options_set_dsn( options, pszDSN );
	sentry_options_set_database_path( options, databasePath.c_str() );
	if ( m_bInProcess )
	{
		// No crashpad: the game writes its own dumps (see the top).
		sentry_options_set_backend( options, NULL );
	}
	else
	{
		sentry_options_set_handler_path( options, handlerPath.c_str() );
	}
	sentry_options_set_release( options, release.c_str() );
	sentry_options_set_environment( options, SENTRY_ENVIRONMENT );
	if ( CommandLine()->FindParm( "-sentrydebug" ) )
		sentry_options_set_debug( options, 1 );

	const int ret = sentry_init( options );
	if ( ret != 0 )
	{
		Warning( "Crash reporting did not start (%d).\n", ret );
		return true;
	}
	m_bInit = true;

#ifdef CLIENT_DLL
	sentry_set_tag( "side", "client" );
#else
	sentry_set_tag( "side", "dedicated" );
#endif
#if defined( FRONTRESS_EAC )
	sentry_set_tag( "eac_build", "1" );
#else
	sentry_set_tag( "eac_build", "0" );
#endif
	sentry_set_tag( "crash_backend", m_bInProcess ? "in-process" : "crashpad" );

	SetAssertFailedNotifyFunc( AssertCallbackFunc );
	m_OutputFuncPrev = GetSpewOutputFunc();
	SpewOutputFunc( &SpewFunc );

	SendPendingReports();
#if defined( _WIN32 )
	if ( m_bInProcess )
	{
		if ( !CrashNative::InstallDumpHandler( m_ReportsDir.c_str() ) )
			Warning( "Crash reporting: could not install the dump writer.\n" );
	}
	else
	{
		// crashpad's filter, to put back if something loaded later replaces it.
		CrashNative::PinExceptionFilter();
	}
#endif

	// Every report from now on carries this session's console output; it
	// is saved on a timer, and kept after a death nothing else saw.
	CrashNative::LogWrite( m_SessionPath.c_str(), true );
	sentry_attach_file( m_SessionPath.c_str() );

	Msg( "Crash reporting on (%s, %s).\n", release.c_str(), m_bInProcess ? "in-process dumps" : "crashpad" );
	return true;
}

// Reports from earlier runs: the game's own dumps with their console output
// and console output whose dump could not be written (Windows without
// crashpad), and a session that just ended with no report at all.
void CCrashReporter::SendPendingReports()
{
	std::vector< std::string > dumps, logs;
	CrashNative::ListFiles( m_ReportsDir.c_str(), ".dmp", dumps );
	CrashNative::ListFiles( m_ReportsDir.c_str(), ".log", logs );

	// Only the newest few.
	while ( (int)dumps.size() > k_nMaxPendingReports )
	{
		CrashNative::RemoveFile( dumps.front().c_str() );
		dumps.erase( dumps.begin() );
	}
	while ( (int)logs.size() > k_nMaxPendingReports )
	{
		CrashNative::RemoveFile( logs.front().c_str() );
		logs.erase( logs.begin() );
	}

	// crashpad has already sent the last run's crash, if there was one.
	const bool bCrashpadCrashed = sentry_get_crashed_last_run() == 1;
	if ( bCrashpadCrashed )
		sentry_clear_crashed_last_run();
	const bool bSessionLeft = CrashNative::FileExists( m_SessionPath.c_str() ) && !bCrashpadCrashed;
	if ( !bSessionLeft )
		CrashNative::RemoveFile( m_SessionPath.c_str() );
	if ( dumps.empty() && logs.empty() && !bSessionLeft )
		return;

	std::vector< std::string > sent;
	std::vector< sentry_attachment_t * > attached;
	int nReports = 0;

	for ( const std::string &dump : dumps )
	{
		const std::string log = dump.substr( 0, dump.size() - 4 ) + ".log";
		AttachIfExists( log, attached );
		sentry_capture_minidump( dump.c_str() );
		DetachAll( attached );
		sent.push_back( dump );
		sent.push_back( log );
		++nReports;
	}

	for ( const std::string &log : logs )
	{
		const std::string dump = log.substr( 0, log.size() - 4 ) + ".dmp";
		bool bHasDump = false;
		for ( const std::string &d : dumps )
			bHasDump = bHasDump || d == dump;
		if ( bHasDump )
			continue;
		AttachIfExists( log, attached );
		sentry_capture_event( sentry_value_new_message_event( SENTRY_LEVEL_FATAL, "crash",
			"The game crashed, but its minidump could not be written" ) );
		DetachAll( attached );
		sent.push_back( log );
		++nReports;
	}

	// The last session's file is still there when it did not get to
	// Shutdown(). If no crash was caught either, the process was ended from
	// outside or by the runtime itself (heap corruption, a security check,
	// the anti-cheat, the task manager).
	if ( bSessionLeft && nReports == 0 )
	{
		AttachIfExists( m_SessionPath, attached );
		sentry_value_t event = sentry_value_new_message_event( SENTRY_LEVEL_ERROR, "session",
			"The previous session ended without a clean shutdown and without a crash dump" );
		sentry_capture_event( event );
		DetachAll( attached );
		++nReports;
	}

	// Sent before anything else happens: a game that crashes again within
	// seconds would otherwise never get them out. Kept for the next launch
	// if the upload has not finished.
	if ( sentry_flush( 8000 ) == 0 )
	{
		for ( const std::string &path : sent )
			CrashNative::RemoveFile( path.c_str() );
		Msg( "Crash reporting: sent %d report(s) from earlier runs.\n", nReports );
	}
	else
	{
		Warning( "Crash reporting: %d report(s) from earlier runs are still uploading; will retry next launch.\n", nReports );
	}
}

void CCrashReporter::Shutdown()
{
#ifdef GAME_DLL
	if ( !engine->IsDedicatedServer() )
		return;
#endif

	if ( !m_bInit )
		return;

#if defined( _WIN32 )
	CrashNative::RemoveDumpHandler();
#endif
	SpewOutputFunc( m_OutputFuncPrev );
	sentry_close();
	// A clean exit: nothing to report next time.
	CrashNative::RemoveFile( m_SessionPath.c_str() );
	m_bInit = false;
}

void CCrashReporter::Tick()
{
	if ( !m_bInit )
		return;

	const double flNow = Plat_FloatTime();
	if ( flNow < m_flNextSave )
		return;
	m_flNextSave = flNow + k_flSessionSaveInterval;

	CrashNative::LogWrite( m_SessionPath.c_str() );
#if defined( _WIN32 )
	// Something loaded after us may have put its own filter on top of ours
	// or crashpad's.
	CrashNative::ReassertDumpHandler();
#endif

	if ( !m_bUserSet && steamapicontext && steamapicontext->SteamUser() )
	{
		const CSteamID steamID = steamapicontext->SteamUser()->GetSteamID();
		if ( steamID.IsValid() )
		{
			char szID[ 32 ];
			V_snprintf( szID, sizeof( szID ), "%llu", steamID.ConvertToUint64() );
			sentry_value_t user = sentry_value_new_object();
			sentry_value_set_by_key( user, "id", sentry_value_new_string( szID ) );
			sentry_set_user( user );
			m_bUserSet = true;
		}
	}
}

#ifdef CLIENT_DLL
void CCrashReporter::Update( float frametime )
{
	Tick();
}
#else
void CCrashReporter::FrameUpdatePostEntityThink()
{
	Tick();
}
#endif

void CCrashReporter::AssertCallbackFunc( const char* pchFile, int nLine, const char* pchMessage )
{
	sentry_value_t event = sentry_value_new_message_event( SENTRY_LEVEL_ERROR, "assert", pchMessage );
	sentry_value_set_stacktrace(event, nullptr, 0);
	sentry_capture_event( event );
}

SpewRetval_t CCrashReporter::SpewFunc(SpewType_t type, const tchar* pMsg)
{
	CrashNative::LogAppend( pMsg );
	if ( type == SPEW_ERROR )
	{
		// Error() ends the game, possibly inside the engine's own spew
		// function: send it first, with the console output that led to it,
		// and leave nothing for the next launch.
		CrashNative::LogWrite( s_CrashReporter.m_SessionPath.c_str(), true );
		s_CrashReporter.ReportError( "spew", pMsg );
		sentry_flush( 5000 );
		CrashNative::RemoveFile( s_CrashReporter.m_SessionPath.c_str() );
	}
	return s_CrashReporter.m_OutputFuncPrev( type, pMsg );
}

// capture events
void CCrashReporter::ReportInfo( const char* logger, const char* message )
{
	sentry_capture_event( sentry_value_new_message_event( SENTRY_LEVEL_INFO, logger, message ) );
}

void CCrashReporter::ReportWarning( const char* logger, const char* message )
{
	sentry_capture_event( sentry_value_new_message_event( SENTRY_LEVEL_WARNING, logger, message ) );
}

void CCrashReporter::ReportError( const char* logger, const char* message )
{
	sentry_value_t event = sentry_value_new_message_event( SENTRY_LEVEL_ERROR, logger, message );
	sentry_value_set_stacktrace(event, nullptr, 0);
	sentry_capture_event( event );
}
