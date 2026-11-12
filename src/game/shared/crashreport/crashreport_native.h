//========= Team Frontress ====================================================//
//
// Purpose: The parts of the crash reporter that need the platform's own
// headers, kept free of the engine's (which do not mix with windows.h).
//
//  - A ring of the latest console output. It goes along with every crash
//    report, because a crash is usually explained by what was printed just
//    before it.
//  - On Windows, a fallback for when crashpad cannot run (its handler is
//    missing, or -crashinprocess): minidumps written from inside the game,
//    uploaded on the next launch.
//  - On Windows, keeping crashpad's exception filter on top, and getting the
//    crashes the engine's tier0 catches itself.
//
//=============================================================================

#ifndef CRASHREPORT_NATIVE_H
#define CRASHREPORT_NATIVE_H
#ifdef _WIN32
#pragma once
#endif

#include <string>
#include <vector>

namespace CrashNative
{
	// Console output. Any thread; never blocks for long.
	void LogAppend( const char *pszText );
	// Writes what the ring holds to a file (UTF-8 path). False when nothing
	// changed since the last write, or it could not be written.
	bool LogWrite( const char *pszPath, bool bForce = false );

	// The folder the game's executable is in (UTF-8), without a trailing
	// separator. Empty if it cannot be told.
	std::string ExeDir();

	// Plain file helpers on UTF-8 paths.
	bool FileExists( const char *pszPath );
	bool RemoveFile( const char *pszPath );
	// When the file was last written, in seconds of some fixed epoch; 0 if
	// it does not exist.
	double FileTime( const char *pszPath );
	void MakeDirs( const char *pszPath );
	// Files in a folder whose names end in pszSuffix, full paths, oldest first.
	void ListFiles( const char *pszDir, const char *pszSuffix, std::vector< std::string > &out );
	// The last nMaxBytes of a file, from the start of a line. Empty if it
	// cannot be read.
	std::string ReadFileTail( const char *pszPath, size_t nMaxBytes );

#ifdef _WIN32
	// Unhandled exceptions, pure virtual calls, invalid CRT parameters and
	// abort() write <pszDumpDir>/<time>-<pid>.dmp and, beside it, the console
	// ring as <same name>.log. Returns false if dbghelp is missing.
	bool InstallDumpHandler( const char *pszDumpDir );
	// Takes the top-level exception filter back if something replaced it:
	// ours when installed, otherwise the one PinExceptionFilter() saw.
	void ReassertDumpHandler();
	void RemoveDumpHandler();
	// Remembers the current top-level exception filter (crashpad's, right
	// after sentry_init) for ReassertDumpHandler().
	void PinExceptionFilter();
	// Has crashes that tier0's CatchAndWriteMiniDump*() catches go to our
	// dump writer or crashpad too. False if tier0 has no SetMiniDumpFunction.
	bool HookEngineMinidumps();
#endif
}

#endif // CRASHREPORT_NATIVE_H
