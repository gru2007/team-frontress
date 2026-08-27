/* Filesystem paths across the bridge.
 *
 * Every other value that crosses is a number, a buffer, or a string whose
 * meaning does not depend on which side of the process is reading it.  A path
 * is the exception: native Steam answers with a POSIX path, and the Windows
 * process it is handed to cannot open one.
 *
 * This is not a corner case.  The engine mounts Team Fortress 2's content by
 * asking Steamworks where app 440 is installed --  `|appid_440|tf/tf2_misc.vpk`
 * in gameinfo.txt is resolved through ISteamApps::GetAppInstallDir -- so
 * without this every VPK the game needs resolves to a path that does not exist
 * and the client comes up with no content at all.
 *
 * Wine already knows how to do the translation, and it is the same code
 * `winepath` runs, so that is what this asks first.  The fallback is the drive
 * every prefix has: wineboot maps Z: to /.
 */

#include "bridge_pe.h"

#include <stdio.h>
#include <string.h>

typedef WCHAR *(*wine_get_dos_file_name_t)( const char *unix_path );
typedef char *(*wine_get_unix_file_name_t)( const WCHAR *dos_path );

static wine_get_dos_file_name_t p_wine_get_dos_file_name;
static wine_get_unix_file_name_t p_wine_get_unix_file_name;
static bool steam_bridge_paths_resolved;

/* GetProcAddress rather than an import: an import Wine cannot resolve makes the
 * whole builtin fail to load, and these two have moved between kernel32 and
 * kernelbase more than once. */
static void steam_bridge_resolve_path_helpers( void )
{
    static const char *modules[] = { "kernelbase.dll", "kernel32.dll", "ntdll.dll" };

    if (steam_bridge_paths_resolved) return;
    steam_bridge_paths_resolved = true;

    for (unsigned int i = 0; i < sizeof(modules) / sizeof(modules[0]); i++)
    {
        HMODULE module = GetModuleHandleA( modules[i] );
        if (!module) module = LoadLibraryA( modules[i] );
        if (!module) continue;

        if (!p_wine_get_dos_file_name)
            p_wine_get_dos_file_name =
                (wine_get_dos_file_name_t)GetProcAddress( module, "wine_get_dos_file_name" );
        if (!p_wine_get_unix_file_name)
            p_wine_get_unix_file_name =
                (wine_get_unix_file_name_t)GetProcAddress( module, "wine_get_unix_file_name" );
    }

    steam_bridge_log( "path helpers: wine_get_dos_file_name %p, wine_get_unix_file_name %p",
                      (void *)p_wine_get_dos_file_name, (void *)p_wine_get_unix_file_name );
}

/* Z: is / in every prefix wineboot creates.  Used when the helpers above are
 * not there, and correct for anything under the user's home directory, which is
 * where a Steam library lives. */
static bool steam_bridge_unix_to_dos_fallback( const char *unix_path, char *out, unsigned int size )
{
    unsigned int length = (unsigned int)strlen( unix_path );

    if (length + 3 > size) return false;

    out[0] = 'Z';
    out[1] = ':';
    memcpy( out + 2, unix_path, length + 1 );

    for (char *p = out + 2; *p; p++)
        if (*p == '/') *p = '\\';

    return true;
}

static bool steam_bridge_unix_to_dos( const char *unix_path, char *out, unsigned int size )
{
    steam_bridge_resolve_path_helpers();

    if (p_wine_get_dos_file_name)
    {
        WCHAR *dos = p_wine_get_dos_file_name( unix_path );
        if (dos)
        {
            int written = WideCharToMultiByte( CP_ACP, 0, dos, -1, out, (int)size, NULL, NULL );
            HeapFree( GetProcessHeap(), 0, dos );
            if (written > 0) return true;
        }
    }

    return steam_bridge_unix_to_dos_fallback( unix_path, out, size );
}

unsigned int steam_bridge_path_out( char *buffer, unsigned int size )
{
    char dos[MAX_PATH * 4];

    /* Only an absolute POSIX path needs translating.  A relative one, an empty
     * buffer, or something already in Windows form is left exactly as it is. */
    if (!buffer || size < 2 || buffer[0] != '/') return 0;

    if (!steam_bridge_unix_to_dos( buffer, dos, sizeof(dos) )) return 0;

    unsigned int length = (unsigned int)strlen( dos );
    if (length + 1 > size)
    {
        /* Truncating a path is worse than handing back the one Steam gave:
         * a caller that checks whether the directory exists gets a clean no
         * either way, and the log says which case this was. */
        steam_bridge_log( "path does not fit after translation (%u > %u): %s",
                          length + 1, size, dos );
        return 0;
    }

    memcpy( buffer, dos, length + 1 );
    return length + 1;
}

const char *steam_bridge_path_in( const char *path )
{
    static thread_local char unix_path[MAX_PATH * 4];
    WCHAR wide[MAX_PATH * 4];

    /* A drive-relative or UNC path is the game's own; anything without a drive
     * letter is either already POSIX or not a path at all. */
    if (!path || !path[0]) return path;
    if (path[1] != ':') return path;

    steam_bridge_resolve_path_helpers();

    if (p_wine_get_unix_file_name &&
        MultiByteToWideChar( CP_ACP, 0, path, -1, wide, MAX_PATH * 4 ) > 0)
    {
        char *converted = p_wine_get_unix_file_name( wide );
        if (converted)
        {
            size_t length = strlen( converted );
            if (length + 1 <= sizeof(unix_path))
            {
                memcpy( unix_path, converted, length + 1 );
                HeapFree( GetProcessHeap(), 0, converted );
                return unix_path;
            }
            HeapFree( GetProcessHeap(), 0, converted );
        }
    }

    if ((path[0] == 'Z' || path[0] == 'z') && strlen( path ) + 1 <= sizeof(unix_path))
    {
        strcpy( unix_path, path + 2 );
        for (char *p = unix_path; *p; p++)
            if (*p == '\\') *p = '/';
        return unix_path;
    }

    steam_bridge_log( "cannot translate %s to a Unix path", path );
    return path;
}
