//========= Copyright Valve Corporation, All rights reserved. ============//
//
// Purpose: A redirection tool that allows the DLLs to reside elsewhere.
//
//=====================================================================================//

#if defined( _WIN32 )
#include <windows.h>
#include <stdio.h>
#include <assert.h>
#include <direct.h>
#endif

#ifdef POSIX
#include <stdio.h>
#include <stdlib.h>
#include <dlfcn.h>
#include <limits.h>
#include <string.h>
#include <sys/stat.h>
#define MAX_PATH PATH_MAX
#endif

#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <array>
#include <string>
#include <vector>

#include "tier0/basetypes.h"

#ifdef WIN32
typedef int (*LauncherMain_t)( HINSTANCE hInstance, HINSTANCE hPrevInstance, 
							  LPSTR lpCmdLine, int nCmdShow );
#elif POSIX
typedef int (*LauncherMain_t)( int argc, char **argv );
#else
#error
#endif

#ifdef WIN32
// hinting the nvidia driver to use the dedicated graphics card in an optimus configuration
// for more info, see: http://developer.download.nvidia.com/devzone/devcenter/gamegraphics/files/OptimusRenderingPolicies.pdf
extern "C" { _declspec( dllexport ) DWORD NvOptimusEnablement = 0x00000001; }

// same thing for AMD GPUs using v13.35 or newer drivers
extern "C" { __declspec( dllexport ) int AmdPowerXpressRequestHighPerformance = 1; }

#endif


#include <steam/steam_api.h>

#ifdef _WIN32
static HMODULE Launcher_LoadModule( const char *pszPath )
{
	return LoadLibraryEx( pszPath, NULL, LOAD_WITH_ALTERED_SEARCH_PATH );
}

static void Launcher_CloseModule( HMODULE hHandle )
{
	FreeLibrary( hHandle );
}

static auto Launcher_GetProcAddress( HMODULE hHandle, const char *pszName )
{
	return GetProcAddress( hHandle, pszName );
}
#else
#include <dlfcn.h>
#ifndef OSX
#include <SDL2/SDL.h>
#endif

static void* Launcher_LoadModule( const char *pszPath )
{
	return dlopen( pszPath, RTLD_NOW );
}

static void Launcher_CloseModule( void *pHandle )
{
	dlclose( pHandle );
}

static void *Launcher_GetProcAddress( void *pHandle, const char *pszName )
{
	return dlsym( pHandle, pszName );
}

#ifdef OSX
// No SDL in the macOS launcher (the engine brings its own); errors go to the terminal.
#define MessageBox( x, text, title, y) fprintf( stderr, "%s: %s\n", title, text )
#else
#define MessageBox( x, text, title, y) SDL_ShowSimpleMessageBox( SDL_MESSAGEBOX_ERROR, title, text, NULL )
#endif
#endif

static const AppId_t k_unTF2AppId = 440;
// The client is published under two apps -- the playtest and the main app --
// out of one build. Which one a copy is running as comes from Steam at launch,
// not from here: these are only for asking Steam where an app is installed.
static const AppId_t k_unSDK2013MPAppId = 5147520;
static const AppId_t k_unMainAppId = 5147380;
// The dedicated server ships as a Steam Tool of its own.
static const AppId_t k_unSDK2013DSAppId = 5150320;

#ifdef MOD_LAUNCHER
static const AppId_t k_unMyModAppid = MOD_APPID;
#else
static const AppId_t k_unMyModAppid = k_unSDK2013MPAppId;
#endif

static bool s_bInittedSteam = false;

#ifdef _WIN32
static HMODULE s_SteamModule;
#else
static void *s_SteamModule;

#define WRAP( fn, ret, ... ) \
	ret __real_##fn(__VA_ARGS__); \
	__attribute__ ((visibility ("hidden"))) ret __wrap_##fn(__VA_ARGS__)

#define CALL( fn ) __real_##fn

// Some stubs for pre-exec. Linux links with -Wl,--wrap; macOS has no --wrap.
#ifndef OSX
extern "C"
{
    WRAP(fopen, FILE *, const char *path, const char *mode)
    {
        return CALL(fopen)( path, mode );
    }

    WRAP(fopen64, FILE *, const char *path, const char *mode)
    {
        return CALL(fopen64)( path, mode );
    }
}
#endif // !OSX
#endif

static void UnloadSteam()
{
	if ( s_SteamModule )
	{
		decltype( SteamAPI_Shutdown )* pfnSAPIShutdown = ( decltype( SteamAPI_Shutdown )* )Launcher_GetProcAddress( s_SteamModule, "SteamAPI_Shutdown" );
		if ( pfnSAPIShutdown )
		{
			pfnSAPIShutdown();
			s_bInittedSteam = false;
		}

		Launcher_CloseModule( s_SteamModule );
	}

}


static bool LoadSteam( const char *pRootDir )
{
#if defined( _WIN64 )
	#define STEAM_API_DLL_PATH	"%s\\" PLATFORM_BIN_DIR "\\steam_api64.dll"
#elif defined( _WIN32 )
	#define STEAM_API_DLL_PATH	"%s\\" PLATFORM_BIN_DIR "\\steam_api.dll"
#elif defined( POSIX )
	#define STEAM_API_DLL_PATH	"%s/" PLATFORM_BIN_DIR "/libsteam_api.so"
#endif

	char szBuffer[8192];
	// Assemble the full path to our "steam_api.dll"
	_snprintf( szBuffer, sizeof( szBuffer ), STEAM_API_DLL_PATH, pRootDir );
	szBuffer[sizeof( szBuffer ) - 1] = '\0';

	s_SteamModule = Launcher_LoadModule( szBuffer );
	if ( !s_SteamModule )
	{
		MessageBox( 0, "Not able to load Steam API. Is " STEAM_API_DLL_PATH " missing?", "Launcher Error", MB_OK );
		return false;
	}

	// Steam sets the app id in the environment for depot launches. Avoid
	// modifying a signed macOS app bundle when the Wine host already supplied it.
	if ( !getenv( "SteamAppId" ) )
	{
		FILE *pFile = fopen( "steam_appid.txt", "w" );
		if ( pFile )
		{
			fprintf( pFile, "%u\n", k_unMyModAppid );
			fclose( pFile );
		}
	}

	decltype(SteamAPI_Init) *pfnSAPIInit = (decltype( SteamAPI_Init ) *) GetProcAddress( s_SteamModule, "SteamAPI_Init" );
	if ( !pfnSAPIInit )
	{
		MessageBox( 0, "SteamAPI_Init was not available!", "Launcher Error", MB_OK );
		UnloadSteam();
		return false;
	}

	if ( !pfnSAPIInit() )
	{
		MessageBox( 0, "SteamAPI_Init failed! Please make sure you have Steam running and are logged in.", "Launcher Error", MB_OK );
		UnloadSteam();
		return false;
	}

	s_bInittedSteam = true;
	return true;
}

static bool GetGameInstallDir( const char *pRootDir, char *pszBuf, int nBufSize, bool bDedicated )
{
#if defined( _WIN32 )
	// On the macOS port the host resolves Team Fortress 2's Windows engine depot
	// out of the native Steam library and translates it to a Windows path.
	// Windows-only: the Wine build of the client is the only consumer.
	// This is checked before Steam is touched at all: native Steamworks points
	// app 440 at the content install, which deliberately has no Windows engine.
	const char *pszHostTF2Dir = getenv( "TC2_TF2_DIR" );
	if ( !bDedicated && pszHostTF2Dir && pszHostTF2Dir[0] )
	{
		if ( strlen( pszHostTF2Dir ) >= static_cast<size_t>( nBufSize ) )
		{
			MessageBox( 0, "TC2_TF2_DIR is too long.", "Launcher Error", MB_OK );
			return false;
		}

		strcpy( pszBuf, pszHostTF2Dir );

		// A path that does not hold the engine depot would otherwise
		// surface much later as a failure to load launcher.dll.
		char szLauncher[MAX_PATH];
		_snprintf( szLauncher, sizeof( szLauncher ), "%s\\" PLATFORM_BIN_DIR "\\launcher.dll", pszBuf );
		szLauncher[sizeof( szLauncher ) - 1] = '\0';

		if ( GetFileAttributesA( szLauncher ) == INVALID_FILE_ATTRIBUTES )
		{
			char szError[1024];
			_snprintf( szError, sizeof( szError ),
				"TC2_TF2_DIR does not point at the Team Fortress 2 engine depot:\n\n%s", pszBuf );
			szError[sizeof( szError ) - 1] = '\0';
			MessageBox( 0, szError, "Launcher Error", MB_OK );
			return false;
		}

		return true;
	}
#endif // _WIN32

	if ( !LoadSteam( pRootDir ) )
	{
		return false;
	}

	decltype( SteamAPI_GetHSteamUser )* pfnSteamAPI_GetHSteamUser = ( decltype( SteamAPI_GetHSteamUser )* )GetProcAddress( s_SteamModule, "SteamAPI_GetHSteamUser" );
	if ( !pfnSteamAPI_GetHSteamUser )
	{
		MessageBox( 0, "SteamAPI_GetHSteamUser not present!", "Launcher Error", MB_OK );
		return false;
	}

	decltype( SteamInternal_FindOrCreateUserInterface )* pfnSteamInternal_FindOrCreateUserInterface = ( decltype( SteamInternal_FindOrCreateUserInterface )* )GetProcAddress( s_SteamModule, "SteamInternal_FindOrCreateUserInterface" );
	if ( !pfnSteamInternal_FindOrCreateUserInterface )
	{
		MessageBox( 0, "SteamInternal_FindOrCreateUserInterface not present!", "Launcher Error", MB_OK );
		return false;
	}

	ISteamApps* pSteamApps = ( ISteamApps* )( pfnSteamInternal_FindOrCreateUserInterface( pfnSteamAPI_GetHSteamUser(), STEAMAPPS_INTERFACE_VERSION ) );
	if ( !pSteamApps )
	{
		MessageBox( 0, "ISteamApps not present!", "Launcher Error", MB_OK );
		return false;
	}

	uint32_t unLength = 0;
	// if we're running dedicated, first check for dedicated app ID
	if ( bDedicated )
	{
		if ( pSteamApps->BIsAppInstalled( k_unSDK2013DSAppId ) )
		{
			unLength = pSteamApps->GetAppInstallDir( k_unSDK2013DSAppId, pszBuf, nBufSize );
		}
#ifdef _WIN32
		// on Windows, also allow either of the client apps to be used for dedicated
		if ( unLength == 0 && pSteamApps->BIsAppInstalled( k_unSDK2013MPAppId ) )
		{
			unLength = pSteamApps->GetAppInstallDir( k_unSDK2013MPAppId, pszBuf, nBufSize );
		}

		if ( unLength == 0 && pSteamApps->BIsAppInstalled( k_unMainAppId ) )
		{
			unLength = pSteamApps->GetAppInstallDir( k_unMainAppId, pszBuf, nBufSize );
		}
#endif
	}

	if ( pSteamApps->BIsAppInstalled( k_unTF2AppId ) )
	{
#ifdef POSIX
		// dedicated app is required on posix servers, so we only check if TF2 is installed, but we keep relying on the dedicated install dir
		if ( !bDedicated )
#else
		// we prefer to use TF2 as the install dir if it's there, since the SDK apps might not be up to date with TF2 (not prerelease or what have you)
		if ( 1 )
#endif
		{
			unLength = pSteamApps->GetAppInstallDir( k_unTF2AppId, pszBuf, nBufSize );
		}
	}
	else
	{
		// If we didn't find TF2, then fail.
		unLength = 0;
	}

	if ( !bDedicated )
	{
		UnloadSteam();
	}

	if ( unLength == 0 )
	{
		if ( bDedicated )
		{
#ifdef _WIN32
			// On Windows, we prompt for Multiplayer since we can use it and less chance for duplication for a user that may want it in the future (since they don't already have it).
			MessageBox( 0, "Team Fortress 2 (440) and Team Frontress (5147380 or 5147520) must be installed to launch this mod.", "Launcher Error", MB_OK );
#else
			// On Posix, there's no choice but the dedicated server app.
			MessageBox( 0, "Team Fortress 2 (440) and Team Frontress Dedicated Server (5150320) must be installed to launch this mod.", "Launcher Error", MB_OK );
#endif
		}
		else
		{
			// If it's not dedicated, then we only require TF2.
			MessageBox( 0, "Team Fortress 2 (440) must be installed to launch this mod.", "Launcher Error", MB_OK );
		}
		if ( bDedicated )
		{
			UnloadSteam();
		}
		return false;
	}

	return true;
}



//-----------------------------------------------------------------------------
// Purpose: Return the directory where this .exe is running from
// Output : char
//-----------------------------------------------------------------------------
#if !defined( _X360 )

void GetBaseDir( const char *pszBuffer, char *basedir )
{
	char szBuffer[ MAX_PATH ];
	size_t j;
	char *pBuffer = NULL;

	strcpy( szBuffer, pszBuffer );

#ifdef _WIN32
	pBuffer = strrchr( szBuffer,'\\' );
#else
	pBuffer = strrchr( szBuffer,'/' );
#endif
	if ( pBuffer )
	{
		*(pBuffer+1) = '\0';
	}

	strcpy( basedir, szBuffer );

	j = strlen( basedir );
	if (j > 0)
	{
		if ( ( basedir[ j-1 ] == '\\' ) || 
			 ( basedir[ j-1 ] == '/' ) )
		{
			basedir[ j-1 ] = 0;
		}
	}
}

#ifdef WIN32

#include <process.h>

std::wstring GetExePath()
{
	std::vector<WCHAR> exePath;
	exePath.resize( MAX_PATH + 1 );

	DWORD len = ::GetModuleFileNameW( NULL, exePath.data(), MAX_PATH );
	if ( !len || len == MAX_PATH )
		return L"";
	exePath.resize( len + 1 );

	return exePath.data();
}

std::wstring GetExeName()
{
	std::wstring fullPath = GetExePath();
	auto n = fullPath.find_last_of( L'\\' );

	return ( n != std::wstring::npos )
		? fullPath.substr( n + 1 )
		: fullPath;
}

std::wstring GetDefaultGameName()
{
	std::wstring sName = GetExeName();
	auto n = sName.find_last_of( L'.' );

	return ( n != std::wstring::npos )
		? sName.substr( 0, n )
		: sName;
}

static void RelaunchAs64Bit( std::vector<std::wstring> &args )
{
#ifndef _WIN64
	if ( args.size() == 0 )
	{
		// oh no
		return;
	}

	auto iter = args[0].find( L".exe" );
	if ( iter == std::string::npos )
		return;
	args[0].replace( iter, wcslen( L".exe" ), L"_win64.exe" );

	std::wstring sExecutable = args[0];

	std::vector<wchar_t*> pArgs;
	for ( std::wstring& arg : args )
	{
		arg = L"\"" + arg + L"\"";
		pArgs.push_back( (wchar_t *)arg.c_str() );
	}

	pArgs.push_back( NULL );

	_wexecvp( sExecutable.c_str(), &pArgs[0]);
	exit( 1 );
#endif
}

static BOOL FileExists( wchar_t *pwcsPath )
{
	DWORD dwAttrib = GetFileAttributesW( pwcsPath );

	return ( dwAttrib != INVALID_FILE_ATTRIBUTES && !( dwAttrib & FILE_ATTRIBUTE_DIRECTORY ) );
}

bool Is64BitWindows()
{
#if defined(_WIN64)
	return true;
#elif defined(_WIN32)
	BOOL f64 = FALSE;
	return IsWow64Process( GetCurrentProcess(), &f64 ) && f64;
#else
	return false;
#endif
}

static void HandleRelaunching()
{
#ifndef _WIN64
	// Can't relaunch if on 32-bit only windows.
	if ( !Is64BitWindows() )
		return;

	int nArgs = 0;
	LPWSTR* pBaseArgs = CommandLineToArgvW( GetCommandLineW(), &nArgs );

	std::vector<std::wstring> pArgs;
	for ( int i = 0; i < nArgs; i++ )
	{
		pArgs.push_back( pBaseArgs[i] );
	}

	std::wstring sDefaultGameName = GetDefaultGameName();

	const wchar_t *pGameArg = NULL;
	bool bNextIsGameArg = false;
	for ( std::wstring &arg : pArgs )
	{
		if ( bNextIsGameArg )
		{
			pGameArg = arg.c_str();
			bNextIsGameArg = false;
		}
		else if ( arg == L"-game" )
		{
			bNextIsGameArg = true;
		}
		else if ( arg == L"-force32bit" )
		{
			return;
		}
		else if ( arg == L"-force64bit" )
		{
			RelaunchAs64Bit( pArgs );
			return;
		}
	}

	if ( !pGameArg )
	{
		pGameArg = sDefaultGameName.c_str();
	}

	std::wstring sServerDLL = pGameArg;
	if ( sServerDLL.empty() )
	{
		return;
	}

	if ( sServerDLL[sServerDLL.size() - 1] != L'\\' )
	{
		sServerDLL += L"\\";
	}

	sServerDLL += L"bin\\x64\\server.dll";

	if ( FileExists( (wchar_t *)sServerDLL.c_str() ) )
	{
		RelaunchAs64Bit( pArgs );
	}
#endif
}

// Team Fortress 2's engine links against a few open-source libraries that
// Valve ships without a code signature (SDL2, FreeType and company). Easy
// Anti-Cheat reports an unsigned module it has no record of, and our integrity
// catalog only covers files in our own folder. So the same libraries ship in
// our bin\x64, where the catalog covers them, and are loaded first: Windows
// hands a module that links against "SDL2.dll" by name the SDL2.dll already
// in the process, wherever it came from, so TF2's copies are never loaded.
// tools/eac/tf2_unsigned_modules.py lists them; refresh ours when TF2's change.
static void PreloadOwnLibraries( const char *pRootDir, bool bVulkan )
{
	// Dependencies first; LOAD_WITH_ALTERED_SEARCH_PATH finds the rest of a
	// library's dependencies next to it.
	static const char *s_pszLibraries[] =
	{
		"SDL2.dll", "libxml2-2.dll", "libharfbuzz-0.dll", "libfreetype-6.dll", "libfontconfig-1.dll",
	};
	char szPath[ MAX_PATH ];
	for ( const char *pszLibrary : s_pszLibraries )
	{
		_snprintf( szPath, sizeof( szPath ), "%s\\" PLATFORM_BIN_DIR "\\%s", pRootDir, pszLibrary );
		szPath[ sizeof( szPath ) - 1 ] = '\0';
		if ( GetFileAttributesA( szPath ) != INVALID_FILE_ATTRIBUTES )
			LoadLibraryExA( szPath, NULL, LOAD_WITH_ALTERED_SEARCH_PATH );
	}
	// The Vulkan renderer's Direct3D 9 layer, only when it is used.
	if ( bVulkan )
	{
		_snprintf( szPath, sizeof( szPath ), "%s\\" PLATFORM_BIN_DIR "\\dxvk_d3d9.dll", pRootDir );
		szPath[ sizeof( szPath ) - 1 ] = '\0';
		if ( GetFileAttributesA( szPath ) != INVALID_FILE_ATTRIBUTES )
			LoadLibraryExA( szPath, NULL, LOAD_WITH_ALTERED_SEARCH_PATH );
	}
}

// tc2_launcher.log next to the executable: how far a launch got. A launch
// that dies before the engine's console is up (or is closed from outside)
// leaves no console.log; this says which step it was on. The game's modules
// add their own lines through TC2_LAUNCHER_LOG (see FrontressEOS).
static char s_szLauncherLog[ MAX_PATH ];

static void LauncherLog( const char *pszFormat, ... )
{
	if ( !s_szLauncherLog[0] )
		return;
	FILE *pFile = fopen( s_szLauncherLog, "a" );
	if ( !pFile )
		return;
	SYSTEMTIME now;
	GetLocalTime( &now );
	fprintf( pFile, "[%02d:%02d:%02d.%03d] ", now.wHour, now.wMinute, now.wSecond, now.wMilliseconds );
	va_list args;
	va_start( args, pszFormat );
	vfprintf( pFile, pszFormat, args );
	va_end( args );
	fputc( '\n', pFile );
	fclose( pFile );
}

static void StartLauncherLog( const char *pszRootDir )
{
	_snprintf( s_szLauncherLog, sizeof( s_szLauncherLog ), "%s\\tc2_launcher.log", pszRootDir );
	s_szLauncherLog[ sizeof( s_szLauncherLog ) - 1 ] = '\0';
	FILE *pFile = fopen( s_szLauncherLog, "w" ); // one launch per file
	if ( !pFile )
	{
		s_szLauncherLog[0] = 0;
		return;
	}
	fclose( pFile );
	SetEnvironmentVariableA( "TC2_LAUNCHER_LOG", s_szLauncherLog );
	char szCwd[ MAX_PATH ] = "";
	GetCurrentDirectoryA( sizeof( szCwd ), szCwd );
	LauncherLog( "start: %s", GetCommandLineA() );
	LauncherLog( "working directory: %s", szCwd );
}

int APIENTRY WinMain( HINSTANCE hInstance, HINSTANCE hPrevInstance, LPSTR lpCmdLine, int nCmdShow )
{
	HandleRelaunching();

	// Must add 'bin' to the path....
	char* pPath = getenv("PATH");

	char szBuffer[4096];

	// Use the .EXE name to determine the root directory
	char moduleName[MAX_PATH];
	if ( !GetModuleFileName( hInstance, moduleName, MAX_PATH ) )
	{
		MessageBox( 0, "Failed calling GetModuleFileName", "Launcher Error", MB_OK );
		return 0;
	}

	int nArgs = 0;
	LPWSTR* pBaseArgs = CommandLineToArgvW(GetCommandLineW(), &nArgs);

	std::vector<std::wstring> pArgs;
	for (int i = 0; i < nArgs; i++)
	{
		pArgs.push_back(pBaseArgs[i]);
	}

	bool bLaunchDedicated = false;
	bool bForceNoSteamClient = false;
	bool bHasPriorityArg = false;
	bool bVulkan = false;
	for (std::wstring& arg : pArgs)
	{
		if ( arg == L"-vulkan" )
			bVulkan = true;
		if (arg == L"-dedicated")
		{
			bLaunchDedicated = true;
			// we don't use our priority wrapper hack on dedicated.
			bHasPriorityArg = true;
		}
		else if (arg == L"-nosteamclient")
		{
			bForceNoSteamClient = true;
		}
		else if ( !bHasPriorityArg && ( arg == L"-high" || arg == L"-normal" || arg == L"-low" ) )
		{
			bHasPriorityArg = true;
		}
	}

	// cannot use dedicated launch options on non-dedicated
	if ( !bLaunchDedicated && bForceNoSteamClient )
	{
		return 1;
	}

	// Get the root directory the .exe is in
	char pRootDir[MAX_PATH];
	GetBaseDir( moduleName, pRootDir );
	StartLauncherLog( pRootDir );
	const char *pBinaryGameDir = pRootDir;
	char szGameInstallDir[4096];
	if ( !bForceNoSteamClient )
	{
		LauncherLog( "finding Team Fortress 2 through Steam" );
		if ( !bForceNoSteamClient && !GetGameInstallDir( pRootDir, szGameInstallDir, 4096, bLaunchDedicated ) )
		{
			LauncherLog( "no Team Fortress 2 install (or Steam failed); exiting with 1" );
			return 1;
		}

		pBinaryGameDir = szGameInstallDir;
	}
	LauncherLog( "engine: %s", pBinaryGameDir );

	SetEnvironmentVariableA( "SDK_EXEC_DIR", pBinaryGameDir );

#define LAUNCHER_DLL_PATH	"%s\\" PLATFORM_BIN_DIR "\\launcher.dll"
#define DEDICATED_DLL_PATH	"%s\\" PLATFORM_BIN_DIR "\\dedicated.dll"
#define LAUNCHER_PATH		"%s\\" PLATFORM_BIN_DIR

	_snprintf( szBuffer, sizeof( szBuffer ), "PATH=" LAUNCHER_PATH ";%s", pBinaryGameDir, pPath );
	szBuffer[sizeof( szBuffer ) - 1] = '\0';
	_putenv( szBuffer );

	// Assemble the full path to our "launcher.dll"
	if (bLaunchDedicated)
	{
		_snprintf(szBuffer, sizeof(szBuffer), DEDICATED_DLL_PATH, pBinaryGameDir);
	}
	else
	{
		_snprintf(szBuffer, sizeof(szBuffer), LAUNCHER_DLL_PATH, pBinaryGameDir);
	}
	szBuffer[sizeof( szBuffer ) - 1] = '\0';

	if ( !bLaunchDedicated )
		PreloadOwnLibraries( pRootDir, bVulkan );

	// STEAM OK ... filesystem not mounted yet
	HINSTANCE launcher = LoadLibraryEx( szBuffer, NULL, LOAD_WITH_ALTERED_SEARCH_PATH );

	if ( !launcher )
	{
		LauncherLog( "could not load %s (error %lu)", szBuffer, GetLastError() );
		char *pszError;
		FormatMessage(FORMAT_MESSAGE_ALLOCATE_BUFFER | FORMAT_MESSAGE_FROM_SYSTEM | FORMAT_MESSAGE_IGNORE_INSERTS, NULL, GetLastError(), MAKELANGID(LANG_NEUTRAL, SUBLANG_DEFAULT), (LPTSTR)&pszError, 0, NULL);

		char szBuf[1024];
		_snprintf(szBuf, sizeof( szBuf ), "Failed to load the launcher DLL:\n\n%s", pszError);
		szBuf[sizeof( szBuf ) - 1] = '\0';
		MessageBox( 0, szBuf, "Launcher Error", MB_OK );

		LocalFree(pszError);
		return 0;
	}

	if ( !bHasPriorityArg )
	{
		SetPriorityClass( GetCurrentProcess(), ABOVE_NORMAL_PRIORITY_CLASS );
	}

	LauncherMain_t main = (LauncherMain_t)GetProcAddress(launcher, bLaunchDedicated ? "DedicatedMain" : "LauncherMain");
	LauncherLog( "running the engine" );
	const int nResult = main( hInstance, hPrevInstance, lpCmdLine, nCmdShow );
	LauncherLog( "the engine returned %d", nResult );
	return nResult;
}

#elif defined (POSIX)

#if defined( LINUX )

bool GetExePath( char *pszPath, size_t size )
{
	// readlink does not terminate the string.
	const ssize_t n = readlink( "/proc/self/exe", pszPath, size - 1 );
	if ( n <= 0 )
		return false;
	pszPath[ n ] = 0;
	return true;
}

#include <dirent.h>
#include <errno.h>
#include <fcntl.h>
#include <sys/resource.h>
#include <unistd.h>
#include <set>
#include <vector>

// The shared libraries (lib*.so*) in a directory. openat + fdopendir, not
// opendir: the launcher links with --wrap for opendir, stat, access and the
// like (pathmatch), and nothing here provides the wrappers.
static std::vector< std::string > ListLibraries( const std::string &dir )
{
	std::vector< std::string > names;
	const int fd = openat( AT_FDCWD, dir.c_str(), O_RDONLY | O_DIRECTORY | O_CLOEXEC );
	if ( fd < 0 )
		return names;
	DIR *pDir = fdopendir( fd );
	if ( !pDir )
	{
		close( fd );
		return names;
	}
	while ( struct dirent *pEntry = readdir( pDir ) )
	{
		const std::string name = pEntry->d_name;
		if ( name.compare( 0, 3, "lib" ) != 0 || name.find( ".so" ) == std::string::npos )
			continue;
		// Never a second allocator in a running process.
		if ( name.find( "malloc" ) != std::string::npos )
			continue;
		names.push_back( name );
	}
	closedir( pDir );
	return names;
}

// The client runs Team Fortress 2's engine inside this process, as on Windows.
// Easy Anti-Cheat needs that: its launcher (start_protected_game) starts this
// binary and injects the anti-cheat into it, so the game has to stay in this
// process -- and in this program image: starting it again (exec) loses the
// anti-cheat too, which then reports the client as not started through its
// launcher. -gathermod hands off to hl2.sh and TF2's own tf_linux64 instead.
//
// TF2's engine libraries find each other through LD_LIBRARY_PATH, which the
// dynamic linker only reads when a program starts. Without it, every shared
// library they link against is loaded here first, by full path: a library
// already in the process satisfies anything that links against it by name.
// Ours (libsteam_api.so and the like) go first and win over TF2's.
static void PrepareInProcessClient( const char *pRootDir, const char *pEngineDir )
{
	setenv( "SDK_EXEC_DIR", pEngineDir, 1 );
	// Steam reads steam_appid.txt from the working directory, which is about
	// to be TF2's; a launch outside Steam (start_protected_game run by hand)
	// names the app here instead.
	if ( !getenv( "SteamAppId" ) )
	{
		char szAppId[ 16 ];
		snprintf( szAppId, sizeof( szAppId ), "%u", k_unMyModAppid );
		setenv( "SteamAppId", szAppId, 1 );
		setenv( "SteamGameId", szAppId, 0 );
	}
	// The engine loads its modules from <working directory>/bin/linux64
	// (Sys_LoadModule), so it runs from TF2's folder, as hl2.sh has it. The
	// game folder reaches it as an absolute -game.
	if ( chdir( pEngineDir ) != 0 )
		fprintf( stderr, "Could not change to %s: %s\n", pEngineDir, strerror( errno ) );
	setenv( "__GL_THREADED_OPTIMIZATIONS", "1", 0 );
	const std::string pathmatch = std::string( pEngineDir ) + "/pathmatch.inf";
	if ( faccessat( AT_FDCWD, pathmatch.c_str(), F_OK, 0 ) == 0 )
		setenv( "ENABLE_PATHMATCH", "1", 0 );

	struct rlimit files;
	if ( getrlimit( RLIMIT_NOFILE, &files ) == 0 && files.rlim_cur < 2048 )
	{
		files.rlim_cur = files.rlim_max < 2048 ? files.rlim_max : 2048;
		setrlimit( RLIMIT_NOFILE, &files );
	}

	const std::string engineBin = std::string( pEngineDir ) + "/" PLATFORM_BIN_DIR;
	const char *pszLibraryPath = getenv( "LD_LIBRARY_PATH" );
	const std::string libraryPath = pszLibraryPath ? pszLibraryPath : "";
	if ( ( ":" + libraryPath + ":" ).find( ":" + engineBin + ":" ) != std::string::npos )
		return; // set up by hand; the dynamic linker finds everything

	std::vector< std::string > pending;
	std::set< std::string > seen;
	const std::string dirs[] = { std::string( pRootDir ) + "/" PLATFORM_BIN_DIR, engineBin };
	for ( const std::string &dir : dirs )
	{
		for ( const std::string &name : ListLibraries( dir ) )
		{
			if ( seen.insert( name ).second )
				pending.push_back( dir + "/" + name );
		}
	}

	// A library whose own dependencies are not loaded yet fails; go round
	// until a pass loads nothing more.
	bool bProgress = true;
	while ( bProgress && !pending.empty() )
	{
		bProgress = false;
		for ( size_t i = 0; i < pending.size(); )
		{
			if ( dlopen( pending[ i ].c_str(), RTLD_LAZY | RTLD_GLOBAL ) )
			{
				pending.erase( pending.begin() + i );
				bProgress = true;
			}
			else
			{
				i++;
			}
		}
	}
	for ( const std::string &path : pending )
	{
		dlopen( path.c_str(), RTLD_LAZY | RTLD_GLOBAL );
		fprintf( stderr, "Could not preload %s: %s\n", path.c_str(), dlerror() );
	}
}

#include <fcntl.h>

static bool IsDebuggerPresent( int time )
{
	// Need to get around the __wrap_open() stuff. Just find the open symbol
	// directly and use it...
	typedef int (open_func_t)( const char *pathname, int flags, mode_t mode );
	open_func_t *open_func = (open_func_t *)dlsym( RTLD_NEXT, "open" );

	if ( open_func )
	{
		for ( int i = 0; i < time; i++ )
		{
			int tracerpid = -1;

			int fd = (*open_func)( "/proc/self/status", O_RDONLY, S_IRUSR );
			if (fd >= 0)
			{
				char buf[ 4096 ];
				static const char tracerpid_str[] = "TracerPid:";

				const int len = read( fd, buf, sizeof(buf) - 1 );
				if ( len > 0 )
				{
					buf[ len ] = 0;

					const char *str = strstr( buf, tracerpid_str );
					tracerpid = str ? atoi( str + sizeof( tracerpid_str ) ) : -1;
				}

				close( fd );
			}

			if ( tracerpid > 0 )
				return true;

			sleep( 1 );
		}
	}

	return false;
}

static void WaitForDebuggerConnect( int argc, char *argv[], int time )
{
	for ( int i = 1; i < argc; i++ )
	{
		if ( strstr( argv[i], "-wait_for_debugger" ) )
		{
			printf( "\nArg -wait_for_debugger found.\nWaiting %dsec for debugger...\n", time );
			printf( "  pid = %d\n", getpid() );

			if ( IsDebuggerPresent( time ) )
				printf("Debugger connected...\n\n");

			break;
		}
	}
}

#else

#if defined( OSX )
#include <mach-o/dyld.h>

bool GetExePath( char *pszPath, size_t size )
{
	char szRaw[ MAX_PATH ];
	uint32_t unSize = sizeof( szRaw );
	if ( _NSGetExecutablePath( szRaw, &unSize ) != 0 )
		return false;
	char szReal[ PATH_MAX ];
	if ( !realpath( szRaw, szReal ) || strlen( szReal ) >= size )
		return false;
	strcpy( pszPath, szReal );
	return true;
}
#endif

static void WaitForDebuggerConnect( int argc, char *argv[], int time )
{
}

#endif // !LINUX


static const char *GetExecutableModName( char *pszExePath )
{
	static char s_szFinalFilename[ MAX_PATH + 1 ] = "tc2";

	char szExePath[ MAX_PATH + 1 ];
	strncpy( szExePath, pszExePath, sizeof( szExePath ) );
	szExePath[ MAX_PATH ] = 0;

	if ( !*szExePath )
		return s_szFinalFilename;

	const char *pszLastSeparator = strrchr( szExePath, '/' );
	if ( !pszLastSeparator )
		return s_szFinalFilename;

	strncpy( s_szFinalFilename, pszLastSeparator + 1, sizeof( s_szFinalFilename ) );
	s_szFinalFilename[ MAX_PATH ] = 0;
	char *pszLastUnderscore = NULL;
	char* pszLastDot = NULL;
	for ( char *ch = s_szFinalFilename; *ch != 0; ch++ )
	{
		if ( *ch == '_' )
		{
			pszLastUnderscore = ch;
		}

		if ( *ch == '.' )
		{
			pszLastDot = ch;
		}
	}

	if ( pszLastUnderscore )
		*pszLastUnderscore = 0;

	if ( pszLastDot )
		*pszLastDot = 0;

	return s_szFinalFilename;
}

void var_snprintf(std::string& dst, const char* format, ...) {
    va_list args;
    va_start(args, format);
    int required_len = std::vsnprintf(nullptr, 0, format, args);
    va_end(args);
    std::vector<char> buffer(required_len + 1);
    va_start(args, format);
    std::vsnprintf(buffer.data(), buffer.size(), format, args);
    va_end(args);

	dst.assign(buffer.data());
}

#if defined( OSX )
// macOS runs on the engine that ships next to the game, in game/bin/osxarm64:
// Team Fortress 2 has no macOS engine any more, so there is no install to
// borrow one from. Team Fortress 2 itself is only needed for its content,
// which the filesystem finds on its own (see gameinfo.txt).
static int LaunchBundledEngine( const char *pszExePath, int argc, char *argv[] )
{
	char szRootDir[ MAX_PATH ];
	GetBaseDir( pszExePath, szRootDir );

	char szLauncher[ MAX_PATH ];
	snprintf( szLauncher, sizeof( szLauncher ), "%s/" PLATFORM_BIN_DIR "/launcher.dylib", szRootDir );

	void *pLauncher = Launcher_LoadModule( szLauncher );
	if ( !pLauncher )
	{
		fprintf( stderr, "Failed to load the launcher: %s\n%s\n", szLauncher, dlerror() );
		MessageBox( 0, "Failed to load bin/" PLATFORM_DIR "/launcher.dylib. Is the engine installed next to the game?", "Launcher Error", MB_OK );
		return 1;
	}

	LauncherMain_t entryFunc = (LauncherMain_t)Launcher_GetProcAddress( pLauncher, "LauncherMain" );
	if ( !entryFunc )
	{
		fprintf( stderr, "Failed to load the launcher entry proc\n" );
		return 1;
	}

	// Default to the game named after the executable (tc2_osx -> tc2).
	std::vector<char *> new_argv( argv, argv + argc );
	bool bHasGame = false;
	bool bHasSearchPath = false;
	for ( int i = 1; i < argc; i++ )
	{
		if ( !strcmp( argv[i], "-game" ) )
			bHasGame = true;
		if ( !strcmp( argv[i], "-insert_search_path" ) )
			bHasSearchPath = true;
	}

	static char szGamePath[ MAX_PATH ];
	if ( !bHasGame )
	{
		snprintf( szGamePath, sizeof( szGamePath ), "%s/%s", szRootDir, GetExecutableModName( (char *)pszExePath ) );
		new_argv.push_back( (char *)"-game" );
		new_argv.push_back( szGamePath );
	}
	// Engine-specific compiled content must take precedence over the installed
	// TF2 shader cache: their static/dynamic combo layouts can differ.
	static char szEngineContent[ MAX_PATH ];
	snprintf( szEngineContent, sizeof( szEngineContent ), "%s/" PLATFORM_BIN_DIR "/content", szRootDir );
	struct stat contentInfo;
	std::string searchPaths;
	if ( stat( szEngineContent, &contentInfo ) == 0 && S_ISDIR( contentInfo.st_mode ) )
		searchPaths = szEngineContent;
	char szDevelopmentContent[ MAX_PATH ];
	snprintf( szDevelopmentContent, sizeof( szDevelopmentContent ), "%s/../game_src/%s/pak1", szRootDir, GetExecutableModName( (char *)pszExePath ) );
	if ( stat( szDevelopmentContent, &contentInfo ) == 0 && S_ISDIR( contentInfo.st_mode ) )
	{
		if ( !searchPaths.empty() ) searchPaths += ",";
		searchPaths += szDevelopmentContent;
	}
	if ( !bHasSearchPath && !searchPaths.empty() )
	{
		new_argv.push_back( (char *)"-insert_search_path" );
		new_argv.push_back( (char *)searchPaths.c_str() );
	}
	new_argv.push_back( NULL );

	return entryFunc( (int)new_argv.size() - 1, new_argv.data() );
}
#endif

int main( int argc, char *argv[] )
{
	char moduleName[MAX_PATH];
	if ( !GetExePath( moduleName, sizeof( moduleName ) ) )
	{
		return 1;
	}

#if defined( OSX )
	return LaunchBundledEngine( moduleName, argc, argv );
#endif

	bool bLaunchDedicated = false;
	bool bGatherDedicated = false;
	bool bForceNoSteamClient = false;
	for (int i = 1; i < argc; i++)
	{
		if ( !strcmp(argv[i], "-dedicated") )
		{
			bLaunchDedicated = true;
		}
		else if ( !strcmp(argv[i], "-gatherdedi") || !strcmp(argv[i], "-gathermod") )
		{
			bGatherDedicated = true;
		}
		else if ( !strcmp(argv[i], "-nosteamclient") )
		{
			bForceNoSteamClient = true;
		}
	}

	// cannot use dedicated launch options on non-dedicated
	if ( !bLaunchDedicated && bForceNoSteamClient )
	{
		return 1;
	}

#if defined( LINUX )
	// Started by Easy Anti-Cheat's launcher, which preloads its module into
	// this process: -gathermod would move the game to another one (see
	// PrepareInProcessClient), so the client stays here whatever the launch
	// options say.
	const char *pszPreload = getenv( "LD_PRELOAD" );
	if ( !bLaunchDedicated && bGatherDedicated && pszPreload && strstr( pszPreload, "easyanticheat" ) )
	{
		fprintf( stderr, "Easy Anti-Cheat launch: ignoring -gathermod, the game runs in this process.\n" );
		bGatherDedicated = false;
	}
#endif

	char pRootDir[MAX_PATH];
	GetBaseDir( moduleName, pRootDir );

	char *pBinaryGameDir = pRootDir;

	std::string strGameInstallDir;
	char szGameInstallDir[4096];
	if ( !bForceNoSteamClient )
	{
		if ( !GetGameInstallDir( pRootDir, szGameInstallDir, 4096, bLaunchDedicated ) )
		{
			return 1;
		}

		pBinaryGameDir = szGameInstallDir;
	}
	else if ( bLaunchDedicated )
	{
		// get ../tf2ds hard coded path... this is used for gameinfo_server.txt
		char pTempDir[MAX_PATH];
		GetBaseDir( pRootDir, pTempDir );
		var_snprintf(strGameInstallDir, "%s/tf2ds", pTempDir);
		// i promise to be very very good with this string.
		pBinaryGameDir = (char*) strGameInstallDir.c_str();
	}

	// if we don't need to gather anymore, we just run
	if ( !bGatherDedicated )
	{
		LauncherMain_t entryFunc;
		if (bLaunchDedicated)
		{
			#define DEDICATED_DLL_PATH	"%s/" PLATFORM_BIN_DIR "/dedicated_srv.so"
			char szExecutable[8192];
			snprintf(szExecutable, sizeof(szExecutable), DEDICATED_DLL_PATH, pBinaryGameDir );

			void* launcher = Launcher_LoadModule(szExecutable);
			if (!launcher)
			{
				fprintf(stderr, "Failed to load the launcher: %s\n", szExecutable);
				return 0;
			}

			entryFunc = (LauncherMain_t)Launcher_GetProcAddress(launcher, "DedicatedMain");
			if (!entryFunc)
			{
				fprintf(stderr, "Failed to load the launcher entry proc\n");
				return 0;
			}
		}
		else
		{
#if defined( LINUX )
			PrepareInProcessClient( pRootDir, pBinaryGameDir );
#endif
			#define LAUNCHER_DLL_PATH	"%s/" PLATFORM_BIN_DIR "/launcher.so"
			char szExecutable[8192];
			snprintf(szExecutable, sizeof(szExecutable), LAUNCHER_DLL_PATH, pBinaryGameDir );

			void* launcher = Launcher_LoadModule(szExecutable);
			if (!launcher)
			{
				fprintf(stderr, "Failed to load the launcher: %s\n%s\n", szExecutable, dlerror());
				return 0;
			}

			entryFunc = (LauncherMain_t)Launcher_GetProcAddress(launcher, "LauncherMain");
			if (!entryFunc)
			{
				fprintf(stderr, "Failed to load the launcher entry proc\n");
				return 0;
			}

			WaitForDebuggerConnect( argc, argv, 30 );

			// The game named after this binary (tc2_linux64 -> tc2), as the
			// -gathermod path passes it to hl2.sh. A relative -game is taken
			// from this binary's folder: the engine runs from TF2's.
			bool bHasGame = false;
			static std::string strGameArg;
			for ( int i = 1; i < argc; i++ )
			{
				if ( strcmp( argv[i], "-game" ) )
					continue;
				bHasGame = true;
				if ( i + 1 < argc && argv[i + 1][0] != '/' )
				{
					strGameArg = std::string( pRootDir ) + "/" + argv[i + 1];
					argv[i + 1] = (char *)strGameArg.c_str();
				}
			}
			if ( !bHasGame )
			{
				static char szGamePath[8193];
				snprintf( szGamePath, sizeof( szGamePath ), "%s/%s", pRootDir, GetExecutableModName( moduleName ) );
				std::vector<char *> new_argv( argv, argv + argc );
				new_argv.push_back( (char *)"-game" );
				new_argv.push_back( szGamePath );
				new_argv.push_back( NULL );
				return entryFunc( (int)new_argv.size() - 1, new_argv.data() );
			}
		}

		return entryFunc( argc, argv );
	}
	
	char szExecutable[8192];
	if ( bLaunchDedicated )
	{
		snprintf( szExecutable, sizeof(szExecutable), "%s/srcds_run_64", pRootDir );
	}
	else
	{
		snprintf( szExecutable, sizeof(szExecutable), "%s/hl2.sh", pRootDir );
	}

	std::vector<char *> new_argv;

	new_argv.push_back( szExecutable );

	if ( !bLaunchDedicated )
	{
		new_argv.push_back( pBinaryGameDir );
	}

	bool bHasGame = false;
	for ( int i = 1; i < argc; i++ )
	{
		if ( !strcmp( argv[i], "-game" ) )
		{
			bHasGame = true;
		}

		if ( !strcmp( argv[i], "-gatherdedi" ) || !strcmp( argv[i], "-gathermod" ) )
		{
			continue;
		}

		new_argv.push_back(argv[i]);
	}

	char szGamePath[8193];
	if ( !bHasGame )
	{
		new_argv.push_back("-game");

		const char *pModName = GetExecutableModName( moduleName );
		snprintf( szGamePath, sizeof( szGamePath ), "%s/%s", pRootDir, pModName );

		printf( "[Source Mod Launcher] Launching default game: %s\n", pModName );

		new_argv.push_back(szGamePath);
	}

	if ( bLaunchDedicated )
	{
		new_argv.push_back("-binary");
		new_argv.push_back(moduleName);
		new_argv.push_back("-dsdir");
		new_argv.push_back(pBinaryGameDir);
	}

	new_argv.push_back(NULL);

	execvp( szExecutable, new_argv.data() );

	return 0;
}

#else
#error
#endif // WIN32 || POSIX

#endif
