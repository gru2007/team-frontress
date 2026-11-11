//========= Copyright Team Frontress, All rights reserved. ====================//
//
// Purpose: The local connection to the Discord app. See header.
//
// Deliberately free of engine headers (and so of the precompiled header): it
// needs the platform's own, which do not mix with the engine's.
//
//=============================================================================//

#include "frontress_discord_ipc.h"

#include <string.h>
#include <vector>

#include <stdio.h>
#include <stdlib.h>

#ifdef _WIN32
#define WIN32_LEAN_AND_MEAN
#include <windows.h>
#pragma comment( lib, "advapi32.lib" )
#else
#include <errno.h>
#include <fcntl.h>
#include <limits.h>
#include <spawn.h>
#include <sys/socket.h>
#include <sys/stat.h>
#include <sys/un.h>
#include <sys/wait.h>
#include <unistd.h>
#ifdef __APPLE__
#include <mach-o/dyld.h>
#endif
extern char **environ;
#endif

// A message bigger than this is not Discord talking.
static const uint32_t k_unMaxMessageBytes = 1024 * 1024;
// Discord takes the first free of discord-ipc-0 .. discord-ipc-9.
static const int k_nPipeSlots = 10;

static void PutU32( char *p, uint32_t v )
{
	p[ 0 ] = (char)( v & 0xff );
	p[ 1 ] = (char)( ( v >> 8 ) & 0xff );
	p[ 2 ] = (char)( ( v >> 16 ) & 0xff );
	p[ 3 ] = (char)( ( v >> 24 ) & 0xff );
}

static uint32_t GetU32( const char *p )
{
	const unsigned char *u = (const unsigned char *)p;
	return (uint32_t)u[ 0 ] | ( (uint32_t)u[ 1 ] << 8 ) | ( (uint32_t)u[ 2 ] << 16 ) | ( (uint32_t)u[ 3 ] << 24 );
}

#ifdef _WIN32

CFrontressDiscordPipe::CFrontressDiscordPipe() : m_hPipe( INVALID_HANDLE_VALUE ) {}

bool CFrontressDiscordPipe::BIsOpen() const { return m_hPipe != INVALID_HANDLE_VALUE; }

int CFrontressDiscordPipe::CurrentProcessId() { return (int)GetCurrentProcessId(); }

bool CFrontressDiscordPipe::Open()
{
	Close();
	for ( int i = 0; i < k_nPipeSlots; ++i )
	{
		wchar_t wszName[ 64 ];
		_snwprintf( wszName, 64, L"\\\\.\\pipe\\discord-ipc-%d", i );
		wszName[ 63 ] = L'\0';
		HANDLE h = CreateFileW( wszName, GENERIC_READ | GENERIC_WRITE, 0, NULL, OPEN_EXISTING, 0, NULL );
		if ( h != INVALID_HANDLE_VALUE )
		{
			m_hPipe = h;
			return true;
		}
		// ERROR_PIPE_BUSY: that instance is serving someone else; try the next.
	}
	return false;
}

void CFrontressDiscordPipe::Drop()
{
	if ( m_hPipe != INVALID_HANDLE_VALUE )
		CloseHandle( (HANDLE)m_hPipe );
	m_hPipe = INVALID_HANDLE_VALUE;
}

bool CFrontressDiscordPipe::Write( uint32_t unOpcode, const std::string &payload )
{
	if ( !BIsOpen() )
		return false;
	std::string frame( 8, '\0' );
	PutU32( &frame[ 0 ], unOpcode );
	PutU32( &frame[ 4 ], (uint32_t)payload.size() );
	frame += payload;
	DWORD dwWritten = 0;
	if ( !WriteFile( (HANDLE)m_hPipe, frame.data(), (DWORD)frame.size(), &dwWritten, NULL ) || dwWritten != frame.size() )
	{
		Drop();
		return false;
	}
	return true;
}

bool CFrontressDiscordPipe::Fill()
{
	for ( ;; )
	{
		DWORD dwAvailable = 0;
		if ( !PeekNamedPipe( (HANDLE)m_hPipe, NULL, 0, NULL, &dwAvailable, NULL ) )
		{
			Drop();
			return false;
		}
		if ( dwAvailable == 0 )
			return true;
		char buf[ 4096 ];
		DWORD dwRead = 0;
		const DWORD dwWant = dwAvailable < sizeof( buf ) ? dwAvailable : (DWORD)sizeof( buf );
		if ( !ReadFile( (HANDLE)m_hPipe, buf, dwWant, &dwRead, NULL ) || dwRead == 0 )
		{
			Drop();
			return false;
		}
		m_in.append( buf, dwRead );
	}
}

#else // POSIX

CFrontressDiscordPipe::CFrontressDiscordPipe() : m_fd( -1 ) {}

bool CFrontressDiscordPipe::BIsOpen() const { return m_fd >= 0; }

int CFrontressDiscordPipe::CurrentProcessId() { return (int)getpid(); }

bool CFrontressDiscordPipe::Open()
{
	Close();

	// Where each Discord build puts its socket: the plain client, Flatpak and
	// Snap (Linux), and the per-user temp folder (macOS, TMPDIR).
	const char *pszBase = nullptr;
	static const char *s_pszEnv[] = { "XDG_RUNTIME_DIR", "TMPDIR", "TMP", "TEMP" };
	for ( const char *pszEnv : s_pszEnv )
	{
		pszBase = getenv( pszEnv );
		if ( pszBase && *pszBase )
			break;
		pszBase = nullptr;
	}
	if ( !pszBase )
		pszBase = "/tmp";
	static const char *s_pszSubdirs[] = { "", "app/com.discordapp.Discord/", "snap.discord/", "snap.discord-canary/" };

	for ( const char *pszSubdir : s_pszSubdirs )
	{
		for ( int i = 0; i < k_nPipeSlots; ++i )
		{
			sockaddr_un addr;
			memset( &addr, 0, sizeof( addr ) );
			addr.sun_family = AF_UNIX;
			const size_t nBase = strlen( pszBase );
			const char *pszSep = ( nBase && pszBase[ nBase - 1 ] == '/' ) ? "" : "/";
			const int n = snprintf( addr.sun_path, sizeof( addr.sun_path ), "%s%s%sdiscord-ipc-%d", pszBase, pszSep, pszSubdir, i );
			if ( n <= 0 || n >= (int)sizeof( addr.sun_path ) )
				continue;
			if ( access( addr.sun_path, F_OK ) != 0 )
				continue;

			const int fd = socket( AF_UNIX, SOCK_STREAM, 0 );
			if ( fd < 0 )
				return false;
			fcntl( fd, F_SETFD, FD_CLOEXEC );
#ifdef SO_NOSIGPIPE
			const int one = 1;
			setsockopt( fd, SOL_SOCKET, SO_NOSIGPIPE, &one, sizeof( one ) );
#endif
			// A local socket connects or refuses at once; go non-blocking after.
			if ( connect( fd, (const sockaddr *)&addr, sizeof( addr ) ) == 0 )
			{
				fcntl( fd, F_SETFL, fcntl( fd, F_GETFL, 0 ) | O_NONBLOCK );
				m_fd = fd;
				return true;
			}
			close( fd );
		}
	}
	return false;
}

void CFrontressDiscordPipe::Drop()
{
	if ( m_fd >= 0 )
		close( m_fd );
	m_fd = -1;
}

bool CFrontressDiscordPipe::Write( uint32_t unOpcode, const std::string &payload )
{
	if ( !BIsOpen() )
		return false;
	std::string frame( 8, '\0' );
	PutU32( &frame[ 0 ], unOpcode );
	PutU32( &frame[ 4 ], (uint32_t)payload.size() );
	frame += payload;
#ifdef MSG_NOSIGNAL
	const int nFlags = MSG_NOSIGNAL;
#else
	const int nFlags = 0;
#endif
	size_t nSent = 0;
	int nRetries = 0;
	while ( nSent < frame.size() )
	{
		const ssize_t n = send( m_fd, frame.data() + nSent, frame.size() - nSent, nFlags );
		if ( n > 0 )
		{
			nSent += (size_t)n;
			continue;
		}
		if ( n < 0 && errno == EINTR )
			continue;
		// Our messages are small and Discord reads them at once; a full
		// socket buffer that stays full means it has stopped reading.
		if ( n < 0 && ( errno == EAGAIN || errno == EWOULDBLOCK ) && ++nRetries < 100 )
		{
			usleep( 1000 );
			continue;
		}
		Drop();
		return false;
	}
	return true;
}

bool CFrontressDiscordPipe::Fill()
{
	for ( ;; )
	{
		char buf[ 4096 ];
		const ssize_t n = recv( m_fd, buf, sizeof( buf ), 0 );
		if ( n > 0 )
		{
			m_in.append( buf, (size_t)n );
			continue;
		}
		if ( n < 0 && errno == EINTR )
			continue;
		if ( n < 0 && ( errno == EAGAIN || errno == EWOULDBLOCK ) )
			return true;
		// 0: Discord closed it. What it sent first stays readable.
		Drop();
		return false;
	}
}

#endif

CFrontressDiscordPipe::~CFrontressDiscordPipe()
{
	Close();
}

void CFrontressDiscordPipe::Close()
{
	Drop();
	m_in.clear();
}

bool CFrontressDiscordPipe::Read( uint32_t &unOpcode, std::string &payload )
{
	// Messages that arrived before the connection was lost are still handed
	// out (Discord explains why it closes in its last one).
	if ( BIsOpen() )
		Fill();
	if ( m_in.size() < 8 )
		return false;
	const uint32_t unLength = GetU32( m_in.data() + 4 );
	if ( unLength > k_unMaxMessageBytes )
	{
		Close();
		return false;
	}
	if ( m_in.size() < 8 + (size_t)unLength )
		return false;
	unOpcode = GetU32( m_in.data() );
	payload.assign( m_in, 8, unLength );
	m_in.erase( 0, 8 + (size_t)unLength );
	return true;
}

//-----------------------------------------------------------------------------
// Launch registration
//-----------------------------------------------------------------------------

#ifdef _WIN32

static bool FileExistsW( const std::wstring &path )
{
	const DWORD dw = GetFileAttributesW( path.c_str() );
	return dw != INVALID_FILE_ATTRIBUTES && !( dw & FILE_ATTRIBUTE_DIRECTORY );
}

static std::wstring Widen( const std::string &s )
{
	if ( s.empty() )
		return std::wstring();
	const int n = MultiByteToWideChar( CP_UTF8, 0, s.c_str(), (int)s.size(), NULL, 0 );
	std::wstring w( n > 0 ? n : 0, L'\0' );
	if ( n > 0 )
		MultiByteToWideChar( CP_UTF8, 0, s.c_str(), (int)s.size(), &w[ 0 ], n );
	return w;
}

static std::string Narrow( const std::wstring &w )
{
	if ( w.empty() )
		return std::string();
	const int n = WideCharToMultiByte( CP_UTF8, 0, w.c_str(), (int)w.size(), NULL, 0, NULL, NULL );
	std::string s( n > 0 ? n : 0, '\0' );
	if ( n > 0 )
		WideCharToMultiByte( CP_UTF8, 0, w.c_str(), (int)w.size(), &s[ 0 ], n, NULL, NULL );
	return s;
}

static std::wstring ReadRegString( HKEY hRoot, const wchar_t *pszKey, const wchar_t *pszValue )
{
	wchar_t buf[ MAX_PATH * 2 ];
	DWORD cb = sizeof( buf );
	if ( RegGetValueW( hRoot, pszKey, pszValue, RRF_RT_REG_SZ, NULL, buf, &cb ) != ERROR_SUCCESS )
		return std::wstring();
	return buf;
}

static bool WriteRegString( const std::wstring &key, const wchar_t *pszValue, const std::wstring &data )
{
	return RegSetKeyValueW( HKEY_CURRENT_USER, key.c_str(), pszValue, REG_SZ, data.c_str(),
		(DWORD)( ( data.size() + 1 ) * sizeof( wchar_t ) ) ) == ERROR_SUCCESS;
}

bool FrontressDiscord_RegisterLaunch( const char *pszApplicationId, unsigned int unSteamAppId, std::string &what )
{
	wchar_t wszExe[ MAX_PATH ];
	const DWORD n = GetModuleFileNameW( NULL, wszExe, MAX_PATH );
	if ( n == 0 || n >= MAX_PATH )
		return false;
	const std::wstring exe = wszExe;
	const std::wstring dir = exe.substr( 0, exe.find_last_of( L"\\/" ) );

	std::wstring command;
	if ( unSteamAppId )
	{
		// Steam writes where it lives; slashes are how it writes them.
		std::wstring steam = ReadRegString( HKEY_CURRENT_USER, L"Software\\Valve\\Steam", L"SteamExe" );
		for ( wchar_t &c : steam )
			if ( c == L'/' )
				c = L'\\';
		if ( !steam.empty() && FileExistsW( steam ) )
		{
			wchar_t wszUrl[ 64 ];
			_snwprintf( wszUrl, 64, L"steam://rungameid/%u", unSteamAppId );
			wszUrl[ 63 ] = L'\0';
			command = L"\"" + steam + L"\" " + wszUrl;
		}
	}
	if ( command.empty() )
	{
		const std::wstring bootstrapper = dir + L"\\start_protected_game.exe";
		command = L"\"" + ( FileExistsW( bootstrapper ) ? bootstrapper : exe ) + L"\"";
	}
	what = Narrow( command );

	const std::wstring id = Widen( pszApplicationId );
	const std::wstring key = L"Software\\Classes\\discord-" + id;
	if ( ReadRegString( HKEY_CURRENT_USER, ( key + L"\\shell\\open\\command" ).c_str(), NULL ) == command )
		return true;

	bool bOk = WriteRegString( key, NULL, L"URL:Run game " + id + L" protocol" );
	bOk = WriteRegString( key, L"URL Protocol", L"" ) && bOk;
	bOk = WriteRegString( key + L"\\DefaultIcon", NULL, exe ) && bOk;
	bOk = WriteRegString( key + L"\\shell\\open\\command", NULL, command ) && bOk;
	return bOk;
}

#else // POSIX

static bool FileExists( const std::string &path )
{
	struct stat st;
	return stat( path.c_str(), &st ) == 0;
}

static std::string ExecutablePath()
{
	char buf[ PATH_MAX ];
#ifdef __APPLE__
	uint32_t size = sizeof( buf );
	if ( _NSGetExecutablePath( buf, &size ) != 0 )
		return std::string();
	char real[ PATH_MAX ];
	return realpath( buf, real ) ? std::string( real ) : std::string( buf );
#else
	const ssize_t n = readlink( "/proc/self/exe", buf, sizeof( buf ) - 1 );
	if ( n <= 0 )
		return std::string();
	buf[ n ] = '\0';
	return buf;
#endif
}

static bool ReadFile( const std::string &path, std::string &out )
{
	FILE *f = fopen( path.c_str(), "rb" );
	if ( !f )
		return false;
	char buf[ 4096 ];
	size_t n;
	out.clear();
	while ( ( n = fread( buf, 1, sizeof( buf ), f ) ) > 0 )
		out.append( buf, n );
	fclose( f );
	return true;
}

static bool WriteFileIfChanged( const std::string &path, const std::string &content, bool &bChanged )
{
	std::string old;
	bChanged = !ReadFile( path, old ) || old != content;
	if ( !bChanged )
		return true;
	FILE *f = fopen( path.c_str(), "wb" );
	if ( !f )
		return false;
	const bool bOk = fwrite( content.data(), 1, content.size(), f ) == content.size();
	return fclose( f ) == 0 && bOk;
}

static void MakeDirs( const std::string &path )
{
	for ( size_t i = 1; i <= path.size(); ++i )
	{
		if ( i == path.size() || path[ i ] == '/' )
			mkdir( path.substr( 0, i ).c_str(), 0755 );
	}
}

#ifdef __APPLE__

static std::string JsonString( const std::string &s )
{
	std::string out = "\"";
	for ( unsigned char c : s )
	{
		if ( c == '"' || c == '\\' )
		{
			out += '\\';
			out += (char)c;
		}
		else if ( c < 0x20 )
		{
			char buf[ 8 ];
			snprintf( buf, sizeof( buf ), "\\u%04x", c );
			out += buf;
		}
		else
			out += (char)c;
	}
	return out + "\"";
}

bool FrontressDiscord_RegisterLaunch( const char *pszApplicationId, unsigned int unSteamAppId, std::string &what )
{
	const char *pszHome = getenv( "HOME" );
	if ( !pszHome || !*pszHome )
		return false;

	// Discord reads ~/Library/Application Support/discord/games/<id>.json:
	// a URL to open or a command to run.
	std::string content;
	if ( unSteamAppId )
	{
		char szUrl[ 64 ];
		snprintf( szUrl, sizeof( szUrl ), "steam://rungameid/%u", unSteamAppId );
		what = szUrl;
		content = "{\"url\":" + JsonString( szUrl ) + "}";
	}
	else
	{
		const std::string exe = ExecutablePath();
		if ( exe.empty() )
			return false;
		const std::string dir = exe.substr( 0, exe.rfind( '/' ) );
		const std::string bootstrapper = dir + "/start_protected_game.app/Contents/MacOS/start_protected_game";
		what = FileExists( bootstrapper ) ? bootstrapper : exe;
		content = "{\"command\":" + JsonString( what ) + "}";
	}

	const std::string games = std::string( pszHome ) + "/Library/Application Support/discord/games";
	MakeDirs( games );
	bool bChanged = false;
	return WriteFileIfChanged( games + "/" + pszApplicationId + ".json", content, bChanged );
}

#else // Linux

static std::string ShellQuote( const std::string &s )
{
	// Desktop Entry Exec quoting: double quotes, with " ` $ \ escaped.
	std::string out = "\"";
	for ( char c : s )
	{
		if ( c == '"' || c == '`' || c == '$' || c == '\\' )
			out += '\\';
		out += c;
	}
	return out + "\"";
}

bool FrontressDiscord_RegisterLaunch( const char *pszApplicationId, unsigned int unSteamAppId, std::string &what )
{
	// A URL handler for discord-<id>://, which Discord opens with xdg-open.
	std::string dataHome;
	if ( const char *pszData = getenv( "XDG_DATA_HOME" ) )
		dataHome = pszData;
	if ( dataHome.empty() )
	{
		const char *pszHome = getenv( "HOME" );
		if ( !pszHome || !*pszHome )
			return false;
		dataHome = std::string( pszHome ) + "/.local/share";
	}

	std::string exec;
	if ( unSteamAppId )
	{
		char szUrl[ 64 ];
		snprintf( szUrl, sizeof( szUrl ), "steam://rungameid/%u", unSteamAppId );
		what = szUrl;
		exec = std::string( "xdg-open " ) + szUrl;
	}
	else
	{
		const std::string exe = ExecutablePath();
		if ( exe.empty() )
			return false;
		const std::string dir = exe.substr( 0, exe.rfind( '/' ) );
		const std::string bootstrapper = dir + "/start_protected_game";
		what = FileExists( bootstrapper ) ? bootstrapper : exe;
		exec = ShellQuote( what );
	}

	const std::string scheme = std::string( "x-scheme-handler/discord-" ) + pszApplicationId;
	const std::string desktopName = std::string( "discord-" ) + pszApplicationId + ".desktop";
	const std::string content =
		"[Desktop Entry]\n"
		"Name=Team Frontress\n"
		"Exec=" + exec + " %u\n"
		"Type=Application\n"
		"NoDisplay=true\n"
		"Categories=Discord;Games;\n"
		"MimeType=" + scheme + ";\n";

	const std::string apps = dataHome + "/applications";
	MakeDirs( apps );
	bool bChanged = false;
	if ( !WriteFileIfChanged( apps + "/" + desktopName, content, bChanged ) )
		return false;
	if ( !bChanged )
		return true;

	// xdg-mime makes it the handler. It runs without the game's library
	// overrides, which are for the game's own modules only.
	std::vector< std::string > env;
	for ( char **pp = environ; pp && *pp; ++pp )
	{
		if ( strncmp( *pp, "LD_LIBRARY_PATH=", 16 ) != 0 && strncmp( *pp, "LD_PRELOAD=", 11 ) != 0 )
			env.push_back( *pp );
	}
	std::vector< char * > envp;
	for ( std::string &e : env )
		envp.push_back( &e[ 0 ] );
	envp.push_back( nullptr );
	std::string arg0 = "xdg-mime", arg1 = "default", arg2 = desktopName, arg3 = scheme;
	char *argv[] = { &arg0[ 0 ], &arg1[ 0 ], &arg2[ 0 ], &arg3[ 0 ], nullptr };
	pid_t pid = 0;
	if ( posix_spawnp( &pid, "xdg-mime", nullptr, nullptr, argv, envp.data() ) != 0 )
		return true; // no xdg-utils: the .desktop file alone is often enough
	int status = 0;
	while ( waitpid( pid, &status, 0 ) < 0 && errno == EINTR )
		;
	return true;
}

#endif // Linux
#endif // POSIX
