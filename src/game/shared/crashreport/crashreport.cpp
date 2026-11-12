//========= Copyright Valve Corporation, All rights reserved. ============//
//
// Purpose: Crash reporter
//
// Reports go to any server that speaks Sentry's protocol (Bugsink, GlitchTip,
// Sentry). What each report carries:
//  - a minidump of the crash (where every thread was, symbolicated on the
//    server from the PDBs / .dbg files CI uploads);
//  - the last ~64 KB of console output before it, in the event itself
//    (extra "console"): Bugsink drops every attachment but the minidump;
//  - the build, the platform and the player's SteamID.
//
// How the dump is made: crashpad, as Sentry ships it. Its handler, a second
// process, opens the game only once it has crashed, and uploads right away.
// On Windows the handler is named CrashReportHandler.exe: Easy Anti-Cheat
// lets a crash dumper that waits for the crash like this in only when its
// file name starts with "CrashReport" (Epic's "External Crash Dumpers").
// crashpad_wer.dll beside it gets the crashes Windows ends without asking
// the game (heap corruption, security checks) through WER. Crashes the
// engine's tier0 catches itself (CatchAndWriteMiniDump*) are handed to
// crashpad through tier0's SetMiniDumpFunction.
//
// Sessions that end with no crash report at all (the process killed from
// outside) are reported on the next launch, with their console output. So
// is the console of a session crashpad sent a minidump for, because the
// minidump goes up without it.
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

// Bytes of console output that go into a report.
static const size_t k_nConsoleBytes = 64 * 1024;

// The console output may hold bytes that are not UTF-8 (a name typed in a
// local code page); a JSON parser on the server would drop the whole event
// for them. Each bad byte becomes '?'.
static std::string ValidUTF8( const std::string &text )
{
	std::string out;
	out.reserve( text.size() );
	const unsigned char *p = (const unsigned char *)text.data();
	const size_t n = text.size();
	for ( size_t i = 0; i < n; )
	{
		const unsigned char c = p[ i ];
		size_t nLen = 0;
		if ( c < 0x80 )
			nLen = 1;
		else if ( c >= 0xC2 && c <= 0xDF )
			nLen = 2;
		else if ( c >= 0xE0 && c <= 0xEF )
			nLen = 3;
		else if ( c >= 0xF0 && c <= 0xF4 )
			nLen = 4;
		bool bOK = nLen > 0 && i + nLen <= n;
		for ( size_t k = 1; bOK && k < nLen; ++k )
			bOK = ( p[ i + k ] & 0xC0 ) == 0x80;
		// Overlong forms, surrogates and code points past U+10FFFF.
		if ( bOK && nLen == 3 )
			bOK = !( c == 0xE0 && p[ i + 1 ] < 0xA0 ) && !( c == 0xED && p[ i + 1 ] >= 0xA0 );
		if ( bOK && nLen == 4 )
			bOK = !( c == 0xF0 && p[ i + 1 ] < 0x90 ) && !( c == 0xF4 && p[ i + 1 ] >= 0x90 );
		if ( bOK )
		{
			out.append( text, i, nLen );
			i += nLen;
		}
		else
		{
			out += '?';
			++i;
		}
	}
	return out;
}

// The console output of a file goes into the reports sent until
// ClearConsole(), as the event's "console" extra. Attachments are not
// enough: Bugsink keeps none but the minidump.
static void SetConsole( const std::string &path )
{
	const std::string text = ValidUTF8( CrashNative::ReadFileTail( path.c_str(), k_nConsoleBytes ) );
	if ( text.empty() )
		sentry_remove_extra( "console" );
	else
		sentry_set_extra( "console", sentry_value_new_string( text.c_str() ) );
}

static void ClearConsole()
{
	sentry_remove_extra( "console" );
}

// Whether crashpad wrote a dump after this time: its database keeps reports
// in reports/ (Windows) or new/, pending/, completed/ (Linux, macOS) after
// they are sent. sentry's own crash marker is not written on macOS, nor for
// crashes that reach crashpad through WER.
static bool CrashpadDumpedSince( const std::string &database, double flTime )
{
	static const char *const s_Dirs[] = { "reports", "new", "pending", "completed" };
	for ( const char *pszDir : s_Dirs )
	{
		std::vector< std::string > dumps;
		CrashNative::ListFiles( ( database + PATH_SEP + pszDir ).c_str(), ".dmp", dumps );
		for ( const std::string &dump : dumps )
		{
			if ( CrashNative::FileTime( dump.c_str() ) >= flTime )
				return true;
		}
	}
	return false;
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
	m_DatabasePath = m_ReportsDir + PATH_SEP ".sentry";
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
	sentry_options_set_database_path( options, m_DatabasePath.c_str() );
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
	// This session's console output, saved on a timer, goes with every
	// report as a file too, for servers that keep attachments. Given before
	// sentry_init: crashpad on macOS takes no attachments added later. The
	// file still holds the last session's output until SendPendingReports()
	// is done with it.
	sentry_options_add_attachment( options, m_SessionPath.c_str() );
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
	// Crashes in code the engine runs under tier0's own __try/__except never
	// become unhandled; without this they end the game with no report.
	if ( !CrashNative::HookEngineMinidumps() )
		DevWarning( "Crash reporting: tier0 has no SetMiniDumpFunction; crashes it catches will not be reported.\n" );
#endif

	// From now on the file is this session's.
	CrashNative::LogWrite( m_SessionPath.c_str(), true );

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
	bool bCrashpadCrashed = sentry_get_crashed_last_run() == 1;
	if ( bCrashpadCrashed )
		sentry_clear_crashed_last_run();
	// The last session's file is still there when it did not get to
	// Shutdown().
	const double flSessionTime = CrashNative::FileTime( m_SessionPath.c_str() );
	const bool bSessionLeft = flSessionTime > 0.0;
	if ( !bCrashpadCrashed && bSessionLeft && !m_bInProcess )
		bCrashpadCrashed = CrashpadDumpedSince( m_DatabasePath, flSessionTime );
	if ( dumps.empty() && logs.empty() && !bSessionLeft )
		return;

	std::vector< std::string > sent;
	int nReports = 0;

	for ( const std::string &dump : dumps )
	{
		const std::string log = dump.substr( 0, dump.size() - 4 ) + ".log";
		SetConsole( log );
		sentry_capture_minidump( dump.c_str() );
		ClearConsole();
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
		SetConsole( log );
		sentry_capture_event( sentry_value_new_message_event( SENTRY_LEVEL_FATAL, "crash",
			"The game crashed, but its minidump could not be written" ) );
		ClearConsole();
		sent.push_back( log );
		++nReports;
	}

	// The last session did not get to Shutdown().
	if ( bSessionLeft && nReports == 0 )
	{
		SetConsole( m_SessionPath );
		if ( bCrashpadCrashed )
		{
			// crashpad has sent the minidump already, but a server that
			// takes it from crashpad's upload form (Bugsink) keeps no
			// console with it; this event carries it.
			sentry_capture_event( sentry_value_new_message_event( SENTRY_LEVEL_ERROR, "session",
				"Console output before the last crash (its minidump was sent separately)" ) );
		}
		else
		{
			// No crash was caught: the process was ended from outside or by
			// the runtime itself (heap corruption, a security check, the
			// anti-cheat, the task manager), or it hung and was killed.
			sentry_capture_event( sentry_value_new_message_event( SENTRY_LEVEL_ERROR, "session",
				"The previous session ended without a clean shutdown and without a crash dump" ) );
		}
		ClearConsole();
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
