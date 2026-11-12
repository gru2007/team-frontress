//========= Team Frontress ====================================================//
//
// Purpose: Crash reporter, platform side. See crashreport_native.h.
//
// Deliberately free of engine headers (and so of the precompiled header): it
// needs the platform's own, which do not mix with the engine's.
//
//=============================================================================

#include "crashreport_native.h"

#include <algorithm>
#include <mutex>
#include <stdio.h>
#include <string.h>

#ifdef _WIN32
#define WIN32_LEAN_AND_MEAN
#ifndef NOMINMAX
#define NOMINMAX
#endif
#include <windows.h>
#include <dbghelp.h>
#include <signal.h>
#include <stdlib.h>
#else
#include <dirent.h>
#include <errno.h>
#include <limits.h>
#include <sys/stat.h>
#include <unistd.h>
#ifdef __APPLE__
#include <mach-o/dyld.h>
#include <stdlib.h>
#endif
#endif

namespace CrashNative
{

//-----------------------------------------------------------------------------
// Console ring
//-----------------------------------------------------------------------------

// Enough for the last few hundred lines.
static const size_t k_nLogBytes = 64 * 1024;

static char s_Log[ k_nLogBytes ];
static size_t s_nLogHead = 0;		// where the next byte goes
static bool s_bLogWrapped = false;
static unsigned s_nLogChanges = 0;	// bumped on every append
static unsigned s_nLogWritten = ~0u;	// s_nLogChanges at the last write
static std::mutex s_LogMutex;

void LogAppend( const char *pszText )
{
	if ( !pszText || !*pszText )
		return;
	size_t n = strlen( pszText );
	std::lock_guard< std::mutex > lock( s_LogMutex );
	if ( n > k_nLogBytes )
	{
		pszText += n - k_nLogBytes;
		n = k_nLogBytes;
	}
	while ( n )
	{
		const size_t nRoom = k_nLogBytes - s_nLogHead;
		const size_t nChunk = n < nRoom ? n : nRoom;
		memcpy( s_Log + s_nLogHead, pszText, nChunk );
		s_nLogHead += nChunk;
		pszText += nChunk;
		n -= nChunk;
		if ( s_nLogHead == k_nLogBytes )
		{
			s_nLogHead = 0;
			s_bLogWrapped = true;
		}
	}
	++s_nLogChanges;
}

// The ring in order: the older part, then the newer. No lock; the caller
// holds it, or is crashing and cannot wait for it.
static void LogParts( const char *&p1, size_t &n1, const char *&p2, size_t &n2 )
{
	if ( s_bLogWrapped )
	{
		p1 = s_Log + s_nLogHead;
		n1 = k_nLogBytes - s_nLogHead;
		// Start on a whole line.
		const char *pNewline = (const char *)memchr( p1, '\n', n1 );
		if ( pNewline )
		{
			n1 -= ( pNewline + 1 ) - p1;
			p1 = pNewline + 1;
		}
	}
	else
	{
		p1 = s_Log;
		n1 = 0;
	}
	p2 = s_Log;
	n2 = s_nLogHead;
}

#ifdef _WIN32

static std::wstring Widen( const char *psz )
{
	if ( !psz || !*psz )
		return std::wstring();
	const int n = MultiByteToWideChar( CP_UTF8, 0, psz, -1, NULL, 0 );
	if ( n <= 1 )
		return std::wstring();
	std::wstring w( n - 1, L'\0' );
	MultiByteToWideChar( CP_UTF8, 0, psz, -1, &w[ 0 ], n );
	return w;
}

static std::string Narrow( const wchar_t *pwsz )
{
	if ( !pwsz || !*pwsz )
		return std::string();
	const int n = WideCharToMultiByte( CP_UTF8, 0, pwsz, -1, NULL, 0, NULL, NULL );
	if ( n <= 1 )
		return std::string();
	std::string s( n - 1, '\0' );
	WideCharToMultiByte( CP_UTF8, 0, pwsz, -1, &s[ 0 ], n, NULL, NULL );
	return s;
}

// Crash-safe: no allocation, no locks.
static bool WriteLogToW( const wchar_t *pwszPath )
{
	HANDLE h = CreateFileW( pwszPath, GENERIC_WRITE, FILE_SHARE_READ, NULL, CREATE_ALWAYS, FILE_ATTRIBUTE_NORMAL, NULL );
	if ( h == INVALID_HANDLE_VALUE )
		return false;
	const char *p1, *p2;
	size_t n1, n2;
	LogParts( p1, n1, p2, n2 );
	DWORD dw = 0;
	bool bOK = true;
	if ( n1 )
		bOK = WriteFile( h, p1, (DWORD)n1, &dw, NULL ) && bOK;
	if ( n2 )
		bOK = WriteFile( h, p2, (DWORD)n2, &dw, NULL ) && bOK;
	CloseHandle( h );
	return bOK;
}

#endif

bool LogWrite( const char *pszPath, bool bForce )
{
	std::lock_guard< std::mutex > lock( s_LogMutex );
	if ( !bForce && s_nLogWritten == s_nLogChanges )
		return false;
	s_nLogWritten = s_nLogChanges;
#ifdef _WIN32
	const std::wstring path = Widen( pszPath );
	return !path.empty() && WriteLogToW( path.c_str() );
#else
	FILE *f = fopen( pszPath, "wb" );
	if ( !f )
		return false;
	const char *p1, *p2;
	size_t n1, n2;
	LogParts( p1, n1, p2, n2 );
	bool bOK = fwrite( p1, 1, n1, f ) == n1;
	bOK = fwrite( p2, 1, n2, f ) == n2 && bOK;
	return fclose( f ) == 0 && bOK;
#endif
}

//-----------------------------------------------------------------------------
// Files
//-----------------------------------------------------------------------------

#ifdef _WIN32

std::string ExeDir()
{
	wchar_t wsz[ 4096 ];
	const DWORD n = GetModuleFileNameW( NULL, wsz, (DWORD)( sizeof( wsz ) / sizeof( wsz[ 0 ] ) ) );
	if ( n == 0 || n >= sizeof( wsz ) / sizeof( wsz[ 0 ] ) )
		return std::string();
	wchar_t *pSlash = wcsrchr( wsz, L'\\' );
	if ( !pSlash )
		return std::string();
	*pSlash = 0;
	return Narrow( wsz );
}

bool FileExists( const char *pszPath )
{
	const DWORD dw = GetFileAttributesW( Widen( pszPath ).c_str() );
	return dw != INVALID_FILE_ATTRIBUTES && !( dw & FILE_ATTRIBUTE_DIRECTORY );
}

bool RemoveFile( const char *pszPath )
{
	return DeleteFileW( Widen( pszPath ).c_str() ) != 0;
}

double FileTime( const char *pszPath )
{
	WIN32_FILE_ATTRIBUTE_DATA fad;
	if ( !GetFileAttributesExW( Widen( pszPath ).c_str(), GetFileExInfoStandard, &fad ) )
		return 0.0;
	ULARGE_INTEGER t;
	t.LowPart = fad.ftLastWriteTime.dwLowDateTime;
	t.HighPart = fad.ftLastWriteTime.dwHighDateTime;
	return (double)t.QuadPart / 1e7;
}

void MakeDirs( const char *pszPath )
{
	const std::wstring path = Widen( pszPath );
	for ( size_t i = 1; i <= path.size(); ++i )
	{
		if ( i == path.size() || path[ i ] == L'\\' || path[ i ] == L'/' )
			CreateDirectoryW( path.substr( 0, i ).c_str(), NULL );
	}
}

void ListFiles( const char *pszDir, const char *pszSuffix, std::vector< std::string > &out )
{
	out.clear();
	const std::wstring dir = Widen( pszDir );
	const std::wstring suffix = Widen( pszSuffix );
	WIN32_FIND_DATAW fd;
	HANDLE h = FindFirstFileW( ( dir + L"\\*" + suffix ).c_str(), &fd );
	if ( h == INVALID_HANDLE_VALUE )
		return;
	do
	{
		if ( fd.dwFileAttributes & FILE_ATTRIBUTE_DIRECTORY )
			continue;
		const std::wstring name = fd.cFileName;
		// FindFirstFile also matches 8.3 names; check the real one.
		if ( name.size() < suffix.size() || name.compare( name.size() - suffix.size(), suffix.size(), suffix ) != 0 )
			continue;
		out.push_back( Narrow( ( dir + L"\\" + name ).c_str() ) );
	} while ( FindNextFileW( h, &fd ) );
	FindClose( h );
	// Names start with the time they were written.
	std::sort( out.begin(), out.end() );
}

#else // POSIX

std::string ExeDir()
{
#if defined( __linux__ )
	char sz[ PATH_MAX ];
	const ssize_t n = readlink( "/proc/self/exe", sz, sizeof( sz ) - 1 );
	if ( n <= 0 )
		return std::string();
	sz[ n ] = 0;
	char *pSlash = strrchr( sz, '/' );
	if ( !pSlash )
		return std::string();
	*pSlash = 0;
	return sz;
#elif defined( __APPLE__ )
	char szRaw[ PATH_MAX ];
	uint32_t nRaw = sizeof( szRaw );
	if ( _NSGetExecutablePath( szRaw, &nRaw ) != 0 )
		return std::string();
	// The path may be relative or go through symlinks.
	char sz[ PATH_MAX ];
	if ( !realpath( szRaw, sz ) )
		return std::string();
	char *pSlash = strrchr( sz, '/' );
	if ( !pSlash )
		return std::string();
	*pSlash = 0;
	return sz;
#else
	return std::string();
#endif
}

bool FileExists( const char *pszPath )
{
	struct stat st;
	return stat( pszPath, &st ) == 0 && S_ISREG( st.st_mode );
}

bool RemoveFile( const char *pszPath )
{
	return unlink( pszPath ) == 0;
}

double FileTime( const char *pszPath )
{
	struct stat st;
	if ( stat( pszPath, &st ) != 0 )
		return 0.0;
#if defined( __APPLE__ )
	return (double)st.st_mtimespec.tv_sec + st.st_mtimespec.tv_nsec / 1e9;
#else
	return (double)st.st_mtim.tv_sec + st.st_mtim.tv_nsec / 1e9;
#endif
}

void MakeDirs( const char *pszPath )
{
	const std::string path = pszPath;
	for ( size_t i = 1; i <= path.size(); ++i )
	{
		if ( i == path.size() || path[ i ] == '/' )
			mkdir( path.substr( 0, i ).c_str(), 0755 );
	}
}

void ListFiles( const char *pszDir, const char *pszSuffix, std::vector< std::string > &out )
{
	out.clear();
	DIR *d = opendir( pszDir );
	if ( !d )
		return;
	const size_t nSuffix = strlen( pszSuffix );
	while ( dirent *e = readdir( d ) )
	{
		const size_t n = strlen( e->d_name );
		if ( n >= nSuffix && strcmp( e->d_name + n - nSuffix, pszSuffix ) == 0 )
			out.push_back( std::string( pszDir ) + "/" + e->d_name );
	}
	closedir( d );
	std::sort( out.begin(), out.end() );
}

#endif

std::string ReadFileTail( const char *pszPath, size_t nMaxBytes )
{
#ifdef _WIN32
	FILE *f = _wfopen( Widen( pszPath ).c_str(), L"rb" );
#else
	FILE *f = fopen( pszPath, "rb" );
#endif
	if ( !f )
		return std::string();
	std::string text;
	if ( fseek( f, 0, SEEK_END ) == 0 )
	{
		const long nSize = ftell( f );
		const long nStart = nSize > (long)nMaxBytes ? nSize - (long)nMaxBytes : 0;
		if ( nSize > 0 && fseek( f, nStart, SEEK_SET ) == 0 )
		{
			text.resize( (size_t)( nSize - nStart ) );
			text.resize( fread( &text[ 0 ], 1, text.size(), f ) );
			// Start on a whole line.
			if ( nStart > 0 )
			{
				const size_t nNewline = text.find( '\n' );
				text.erase( 0, nNewline == std::string::npos ? 0 : nNewline + 1 );
			}
		}
	}
	fclose( f );
	return text;
}

//-----------------------------------------------------------------------------
// Windows minidumps
//
// The dump is written by a thread started ahead of time: the crashing thread
// may have no stack left (a stack overflow) or hold locks the writer needs.
// It only hands its exception over and waits.
//-----------------------------------------------------------------------------

#ifdef _WIN32

typedef BOOL( WINAPI *MiniDumpWriteDump_t )( HANDLE, DWORD, HANDLE, MINIDUMP_TYPE, PMINIDUMP_EXCEPTION_INFORMATION,
	PMINIDUMP_USER_STREAM_INFORMATION, PMINIDUMP_CALLBACK_INFORMATION );

// Our own codes for crashes that are not exceptions.
static const DWORD k_dwPureCall = 0xE0F70001;
static const DWORD k_dwInvalidParameter = 0xE0F70002;
static const DWORD k_dwAbort = 0xE0F70003;

static MiniDumpWriteDump_t s_pfnMiniDumpWriteDump = NULL;
static wchar_t s_wszDumpDir[ 4096 ];
static HANDLE s_hRequest = NULL, s_hDone = NULL, s_hThread = NULL;
static volatile LONG s_lHandling = 0;
static volatile bool s_bQuit = false;
static EXCEPTION_POINTERS *s_pException = NULL;
static DWORD s_dwCrashThread = 0;
static LPTOP_LEVEL_EXCEPTION_FILTER s_pfnPrevFilter = NULL;
static LPTOP_LEVEL_EXCEPTION_FILTER s_pfnPinnedFilter = NULL;
static bool s_bInstalled = false;

static void WriteDumpNow()
{
	SYSTEMTIME st;
	GetSystemTime( &st );
	wchar_t wszBase[ 4200 ], wszTmp[ 4300 ], wszDump[ 4300 ], wszLog[ 4300 ];
	_snwprintf( wszBase, 4200, L"%s\\%04u%02u%02u-%02u%02u%02u-%lu", s_wszDumpDir, st.wYear, st.wMonth, st.wDay, st.wHour,
		st.wMinute, st.wSecond, GetCurrentProcessId() );
	wszBase[ 4199 ] = 0;
	_snwprintf( wszTmp, 4300, L"%s.tmp", wszBase );
	_snwprintf( wszDump, 4300, L"%s.dmp", wszBase );
	_snwprintf( wszLog, 4300, L"%s.log", wszBase );
	wszTmp[ 4299 ] = wszDump[ 4299 ] = wszLog[ 4299 ] = 0;

	// The log first: it is the part most likely to be written at all.
	WriteLogToW( wszLog );

	HANDLE hFile = CreateFileW( wszTmp, GENERIC_WRITE, 0, NULL, CREATE_ALWAYS, FILE_ATTRIBUTE_NORMAL, NULL );
	if ( hFile == INVALID_HANDLE_VALUE )
		return;
	MINIDUMP_EXCEPTION_INFORMATION mei;
	mei.ThreadId = s_dwCrashThread;
	mei.ExceptionPointers = s_pException;
	mei.ClientPointers = FALSE;
	// Stacks, the memory they point at and thread details: enough to walk
	// every thread, a few megabytes at most.
	const MINIDUMP_TYPE type = (MINIDUMP_TYPE)( MiniDumpWithIndirectlyReferencedMemory | MiniDumpWithThreadInfo |
		MiniDumpWithUnloadedModules | MiniDumpWithProcessThreadData );
	const BOOL bOK = s_pfnMiniDumpWriteDump( GetCurrentProcess(), GetCurrentProcessId(), hFile, type,
		s_pException ? &mei : NULL, NULL, NULL );
	CloseHandle( hFile );
	// Renamed only once whole, so the next launch never sends half a dump.
	if ( bOK )
		MoveFileExW( wszTmp, wszDump, MOVEFILE_REPLACE_EXISTING );
	else
		DeleteFileW( wszTmp );
}

static DWORD WINAPI DumpThread( void * )
{
	for ( ;; )
	{
		WaitForSingleObject( s_hRequest, INFINITE );
		if ( s_bQuit )
			return 0;
		WriteDumpNow();
		SetEvent( s_hDone );
	}
}

// True if this call wrote (or tried to write) the dump; false if another
// crash got there first.
static bool DumpFromHere( EXCEPTION_POINTERS *pException )
{
	if ( InterlockedCompareExchange( &s_lHandling, 1, 0 ) != 0 )
		return false;
	s_pException = pException;
	s_dwCrashThread = GetCurrentThreadId();
	SetEvent( s_hRequest );
	// A dump that takes longer than this is stuck (a lock the crash holds).
	WaitForSingleObject( s_hDone, 30000 );
	return true;
}

static LONG WINAPI UnhandledFilter( EXCEPTION_POINTERS *pException )
{
	DumpFromHere( pException );
	// Not handed on to the filter we replaced: the dump is written, and a
	// second reporter (Steam's, the engine's) would only make another. This
	// ends the process without Windows' "stopped working" box; the report
	// goes out on the next launch.
	return EXCEPTION_EXECUTE_HANDLER;
}

// For crashes the CRT reports instead of raising: an exception record of our
// own around the current context, then the end of the process.
static void DumpAndTerminate( DWORD dwCode )
{
	CONTEXT ctx;
	memset( &ctx, 0, sizeof( ctx ) );
	RtlCaptureContext( &ctx );
	EXCEPTION_RECORD rec;
	memset( &rec, 0, sizeof( rec ) );
	rec.ExceptionCode = dwCode;
	rec.ExceptionFlags = EXCEPTION_NONCONTINUABLE;
#if defined( _M_X64 )
	rec.ExceptionAddress = (PVOID)ctx.Rip;
#elif defined( _M_IX86 )
	rec.ExceptionAddress = (PVOID)ctx.Eip;
#endif
	EXCEPTION_POINTERS ptrs;
	ptrs.ExceptionRecord = &rec;
	ptrs.ContextRecord = &ctx;
	DumpFromHere( &ptrs );
	TerminateProcess( GetCurrentProcess(), dwCode );
}

static void __cdecl OnPureCall()
{
	DumpAndTerminate( k_dwPureCall );
}

static void __cdecl OnInvalidParameter( const wchar_t *, const wchar_t *, const wchar_t *, unsigned int, uintptr_t )
{
	DumpAndTerminate( k_dwInvalidParameter );
}

static void __cdecl OnAbort( int )
{
	DumpAndTerminate( k_dwAbort );
}

bool InstallDumpHandler( const char *pszDumpDir )
{
	if ( s_bInstalled )
		return true;
	HMODULE hDbgHelp = LoadLibraryExW( L"dbghelp.dll", NULL, LOAD_LIBRARY_SEARCH_SYSTEM32 );
	if ( !hDbgHelp )
		return false;
	s_pfnMiniDumpWriteDump = (MiniDumpWriteDump_t)GetProcAddress( hDbgHelp, "MiniDumpWriteDump" );
	if ( !s_pfnMiniDumpWriteDump )
		return false;

	MakeDirs( pszDumpDir );
	const std::wstring dir = Widen( pszDumpDir );
	if ( dir.empty() || dir.size() >= sizeof( s_wszDumpDir ) / sizeof( s_wszDumpDir[ 0 ] ) )
		return false;
	memcpy( s_wszDumpDir, dir.c_str(), ( dir.size() + 1 ) * sizeof( wchar_t ) );

	s_hRequest = CreateEventW( NULL, FALSE, FALSE, NULL );
	s_hDone = CreateEventW( NULL, FALSE, FALSE, NULL );
	if ( !s_hRequest || !s_hDone )
		return false;
	// Its own stack, big enough for dbghelp, whatever state the game is in.
	s_hThread = CreateThread( NULL, 256 * 1024, DumpThread, NULL, 0, NULL );
	if ( !s_hThread )
		return false;

	s_pfnPrevFilter = SetUnhandledExceptionFilter( UnhandledFilter );
	// These belong to this module's CRT: our own code's pure virtual calls,
	// bad CRT arguments and aborts (std::terminate ends in abort too).
	_set_purecall_handler( OnPureCall );
	_set_invalid_parameter_handler( OnInvalidParameter );
	signal( SIGABRT, OnAbort );
	_set_abort_behavior( 0, _WRITE_ABORT_MSG | _CALL_REPORTFAULT );
	s_bInstalled = true;
	return true;
}

// tier0's CatchAndWriteMiniDump*() runs code under __try/__except: a crash
// there is caught before it is unhandled, written by tier0's own minidump
// function, and the process is ended. Neither our filter nor crashpad's
// would see it. tier0 lets that function be replaced (SetMiniDumpFunction),
// and calls it from the __except filter, with the crash still in place.
typedef void ( *EngineMiniDump_t )( unsigned int uCode, EXCEPTION_POINTERS *pException, const char *pszSuffix );
typedef EngineMiniDump_t ( *SetMiniDumpFunction_t )( EngineMiniDump_t pfn );

static SetMiniDumpFunction_t s_pfnSetMiniDumpFunction = NULL;
static EngineMiniDump_t s_pfnPrevEngineMiniDump = NULL;

// What tier0 also uses the function for, WriteMiniDump() on request, comes
// as a breakpoint; that, and anything else that is not a crash, keeps going
// to tier0's writer.
static bool IsCrash( DWORD dwCode )
{
	switch ( dwCode )
	{
	case EXCEPTION_ACCESS_VIOLATION:
	case EXCEPTION_ARRAY_BOUNDS_EXCEEDED:
	case EXCEPTION_DATATYPE_MISALIGNMENT:
	case EXCEPTION_FLT_DENORMAL_OPERAND:
	case EXCEPTION_FLT_DIVIDE_BY_ZERO:
	case EXCEPTION_FLT_INEXACT_RESULT:
	case EXCEPTION_FLT_INVALID_OPERATION:
	case EXCEPTION_FLT_OVERFLOW:
	case EXCEPTION_FLT_STACK_CHECK:
	case EXCEPTION_FLT_UNDERFLOW:
	case EXCEPTION_ILLEGAL_INSTRUCTION:
	case EXCEPTION_IN_PAGE_ERROR:
	case EXCEPTION_INT_DIVIDE_BY_ZERO:
	case EXCEPTION_INT_OVERFLOW:
	case EXCEPTION_INVALID_DISPOSITION:
	case EXCEPTION_NONCONTINUABLE_EXCEPTION:
	case EXCEPTION_PRIV_INSTRUCTION:
	case EXCEPTION_STACK_OVERFLOW:
	case 0xC0000374: // STATUS_HEAP_CORRUPTION
	case 0xC0000409: // STATUS_STACK_BUFFER_OVERRUN
	case 0xE06D7363: // a C++ exception nothing caught
		return true;
	default:
		return false;
	}
}

static void EngineMiniDump( unsigned int uCode, EXCEPTION_POINTERS *pException, const char *pszSuffix )
{
	if ( pException && IsCrash( uCode ) )
	{
		if ( s_bInstalled )
		{
			// tier0 ends the process once this returns.
			DumpFromHere( pException );
			return;
		}
		// crashpad's filter has the handler write the dump, then ends the
		// process itself.
		if ( s_pfnPinnedFilter )
			s_pfnPinnedFilter( pException );
	}
	if ( s_pfnPrevEngineMiniDump )
		s_pfnPrevEngineMiniDump( uCode, pException, pszSuffix );
}

bool HookEngineMinidumps()
{
	if ( !s_pfnSetMiniDumpFunction )
	{
		HMODULE hTier0 = GetModuleHandleW( L"tier0.dll" );
		if ( hTier0 )
			s_pfnSetMiniDumpFunction = (SetMiniDumpFunction_t)GetProcAddress( hTier0, "SetMiniDumpFunction" );
		if ( !s_pfnSetMiniDumpFunction )
			return false;
	}
	const EngineMiniDump_t pfnPrev = s_pfnSetMiniDumpFunction( EngineMiniDump );
	if ( pfnPrev != EngineMiniDump )
		s_pfnPrevEngineMiniDump = pfnPrev;
	return true;
}

void ReassertDumpHandler()
{
	if ( s_bInstalled )
		SetUnhandledExceptionFilter( UnhandledFilter );
	else if ( s_pfnPinnedFilter )
		SetUnhandledExceptionFilter( s_pfnPinnedFilter );
	if ( s_pfnSetMiniDumpFunction )
		HookEngineMinidumps();
}

void PinExceptionFilter()
{
	// Reading it means setting it; put it straight back.
	s_pfnPinnedFilter = SetUnhandledExceptionFilter( NULL );
	SetUnhandledExceptionFilter( s_pfnPinnedFilter );
}

void RemoveDumpHandler()
{
	if ( !s_bInstalled )
		return;
	SetUnhandledExceptionFilter( s_pfnPrevFilter );
	s_bQuit = true;
	SetEvent( s_hRequest );
	WaitForSingleObject( s_hThread, 1000 );
	CloseHandle( s_hThread );
	CloseHandle( s_hRequest );
	CloseHandle( s_hDone );
	s_hThread = s_hRequest = s_hDone = NULL;
	s_bInstalled = false;
}

#endif // _WIN32

} // namespace CrashNative
