/* PE half of the Steamworks bridge.
 *
 * This is the DLL the Windows build of the game loads as steam_api64.dll.  It
 * owns everything that must stay on the Windows side of the process: the
 * proxy objects the game holds, their Microsoft-ABI vtables, the callback
 * registry, and the SDK's context-init dance.  Everything that must reach the
 * real macOS Steam client goes through a single __wine_unix_call.
 *
 * Callbacks deserve a note.  The obvious implementation registers the game's
 * CCallbackBase objects with the native library, but those objects have
 * Microsoft-ABI vtables and the native library would call them System V.  So
 * the bridge does not register them at all: it drives the SDK's manual
 * dispatch, which delivers callbacks as plain data, and runs the registry
 * itself on this side.  No vtable ever changes hands.
 */

#include "bridge_pe.h"

#include <stdarg.h>
#include <stdio.h>
#include <stdlib.h>

/* ------------------------------------------------------------------ unixlib */

steam_bridge_unixlib_handle_t steam_bridge_unix_handle = 0;

typedef NTSTATUS (WINAPI *wine_unix_call_t)( steam_bridge_unixlib_handle_t handle,
                                             unsigned int code, void *args );
typedef NTSTATUS (WINAPI *nt_query_virtual_memory_t)( HANDLE process, const void *addr,
                                                      int info_class, void *buffer,
                                                      SIZE_T length, SIZE_T *result );

static wine_unix_call_t p_wine_unix_call;
static HMODULE steam_bridge_self;
static bool steam_bridge_debug;

/* Wine's MEMORY_INFORMATION_CLASS value that maps a builtin PE module to the
 * Unix library sitting next to it. */
#define STEAM_BRIDGE_MEMORY_WINE_UNIX_FUNCS 1000

void steam_bridge_log( const char *format, ... )
{
    if (!steam_bridge_debug) return;

    va_list args;
    fprintf( stderr, "steam_bridge: " );
    va_start( args, format );
    vfprintf( stderr, format, args );
    va_end( args );
    fprintf( stderr, "\n" );
    fflush( stderr );
}

STEAM_BRIDGE_STATUS steam_bridge_call( unsigned int code, void *args )
{
    if (!p_wine_unix_call || !steam_bridge_unix_handle)
        return STEAM_BRIDGE_STATUS_UNAVAILABLE;
    return (STEAM_BRIDGE_STATUS)p_wine_unix_call( steam_bridge_unix_handle, code, args );
}

extern "C" uint64 __cdecl steam_bridge_unbridged( void )
{
    steam_bridge_log( "a method the bridge does not forward was called" );
    return 0;
}

static bool steam_bridge_load_unixlib( void )
{
    HMODULE ntdll = GetModuleHandleA( "ntdll.dll" );
    if (!ntdll) return false;

    nt_query_virtual_memory_t query =
        (nt_query_virtual_memory_t)GetProcAddress( ntdll, "NtQueryVirtualMemory" );

    /* Two spellings, because Wine has both.  Current Wine exports
     * __wine_unix_call_dispatcher as a *variable* holding the function
     * pointer -- what wine/unixlib.h calls through -- while some builds also
     * export __wine_unix_call as a syscall thunk. Prefer the variable, since
     * that is the one that has been there consistently. */
    wine_unix_call_t *dispatcher =
        (wine_unix_call_t *)GetProcAddress( ntdll, "__wine_unix_call_dispatcher" );
    if (dispatcher)
        p_wine_unix_call = *dispatcher;
    else
        p_wine_unix_call = (wine_unix_call_t)GetProcAddress( ntdll, "__wine_unix_call" );

    if (!query || !p_wine_unix_call)
    {
        /* Not running under Wine, or a Wine too old to have unixlibs. */
        steam_bridge_log( "ntdll has no unixlib entry points (query %p, call %p)",
                          (void *)query, (void *)p_wine_unix_call );
        return false;
    }

    NTSTATUS status = query( GetCurrentProcess(), steam_bridge_self,
                             STEAM_BRIDGE_MEMORY_WINE_UNIX_FUNCS,
                             &steam_bridge_unix_handle,
                             sizeof(steam_bridge_unix_handle), NULL );
    if (status)
    {
        /* Almost always means Wine did not treat this DLL as a builtin, or
         * steam_api64.so is not in the runtime's x86_64-unix directory. */
        steam_bridge_log( "NtQueryVirtualMemory(MemoryWineUnixFuncs) on %p failed: %08x",
                          (void *)steam_bridge_self, (unsigned int)status );
        steam_bridge_unix_handle = 0;
        return false;
    }

    steam_bridge_log( "unix half attached, handle %llx",
                      (unsigned long long)steam_bridge_unix_handle );
    return true;
}

/* --------------------------------------------------------------- proxy table */

static CRITICAL_SECTION steam_bridge_lock;

/* An array of pointers, not of proxies.  The game holds the proxy itself --
 * that address is its interface pointer for the life of the process -- so the
 * proxies cannot live inside a block that realloc is allowed to move. Only the
 * index moves. */
static struct steam_bridge_proxy **steam_bridge_proxies;
static unsigned int steam_bridge_proxy_count;
static unsigned int steam_bridge_proxy_capacity;

void *steam_bridge_native( void *proxy )
{
    if (!proxy) return NULL;
    return ((struct steam_bridge_proxy *)proxy)->native;
}

void *steam_bridge_wrap( enum steam_bridge_iface_id iface, void *native )
{
    if (!native) return NULL;

    EnterCriticalSection( &steam_bridge_lock );

    /* The game compares interface pointers for identity, so one native object
     * has to keep mapping to one proxy for the life of the process. */
    for (unsigned int i = 0; i < steam_bridge_proxy_count; i++)
    {
        struct steam_bridge_proxy *existing = steam_bridge_proxies[i];
        if (existing->native == native && existing->iface == (int)iface)
        {
            LeaveCriticalSection( &steam_bridge_lock );
            return existing;
        }
    }

    if (steam_bridge_proxy_count == steam_bridge_proxy_capacity)
    {
        unsigned int capacity = steam_bridge_proxy_capacity ? steam_bridge_proxy_capacity * 2 : 64;
        void *grown = realloc( steam_bridge_proxies, capacity * sizeof(*steam_bridge_proxies) );
        if (!grown)
        {
            LeaveCriticalSection( &steam_bridge_lock );
            return NULL;
        }
        steam_bridge_proxies = (struct steam_bridge_proxy **)grown;
        steam_bridge_proxy_capacity = capacity;
    }

    struct steam_bridge_proxy *proxy =
        (struct steam_bridge_proxy *)calloc( 1, sizeof(*proxy) );
    if (!proxy)
    {
        LeaveCriticalSection( &steam_bridge_lock );
        return NULL;
    }
    steam_bridge_proxies[steam_bridge_proxy_count++] = proxy;

    proxy->vtable = steam_bridge_ifaces[iface].vtable;
    proxy->native = native;
    proxy->iface = (int)iface;

    LeaveCriticalSection( &steam_bridge_lock );

    steam_bridge_log( "wrapped %s %p as %p", steam_bridge_ifaces[iface].classname, native, proxy );
    return proxy;
}

/* Interface version strings carry a trailing version number that moves
 * independently of the class.  An exact match is the normal case; the prefix
 * match keeps an older caller working, with the caveat that its vtable is the
 * one this SDK describes, not the one it was compiled against. */
static int steam_bridge_iface_from_version( const char *version )
{
    if (!version) return -1;

    for (int i = 0; i < steam_bridge_iface_count; i++)
    {
        if (steam_bridge_ifaces[i].vtable && !strcmp( steam_bridge_ifaces[i].version, version ))
            return i;
    }

    size_t length = strlen( version );
    while (length && version[length - 1] >= '0' && version[length - 1] <= '9')
        length--;
    if (!length) return -1;

    for (int i = 0; i < steam_bridge_iface_count; i++)
    {
        const char *known = steam_bridge_ifaces[i].version;
        if (!steam_bridge_ifaces[i].vtable || !known[0]) continue;

        size_t known_length = strlen( known );
        while (known_length && known[known_length - 1] >= '0' && known[known_length - 1] <= '9')
            known_length--;

        if (known_length == length && !strncmp( known, version, length ))
        {
            steam_bridge_log( "version %s bridged with the %s layout (%s)",
                              version, steam_bridge_ifaces[i].classname, known );
            return i;
        }
    }
    return -1;
}

/* --------------------------------------------------------- callback registry */

/* CCallbackMgr is a friend of CCallbackBase, which is how the SDK intends the
 * flag and id fields to be reached from outside. */
class CCallbackMgr
{
public:
    static uint8 &Flags( CCallbackBase *callback ) { return callback->m_nCallbackFlags; }
    static int Id( CCallbackBase *callback ) { return callback->m_iCallback; }
    enum
    {
        Registered = CCallbackBase::k_ECallbackFlagsRegistered,
        GameServer = CCallbackBase::k_ECallbackFlagsGameServer,
    };
};

struct steam_bridge_callresult
{
    SteamAPICall_t call;
    CCallbackBase *callback;
};

static CCallbackBase **steam_bridge_callbacks;
static unsigned int steam_bridge_callback_count;
static unsigned int steam_bridge_callback_capacity;

static struct steam_bridge_callresult *steam_bridge_callresults;
static unsigned int steam_bridge_callresult_count;
static unsigned int steam_bridge_callresult_capacity;

STEAM_BRIDGE_EXPORT void __cdecl SteamAPI_RegisterCallback( CCallbackBase *callback, int id )
{
    if (!callback) return;

    EnterCriticalSection( &steam_bridge_lock );
    if (steam_bridge_callback_count == steam_bridge_callback_capacity)
    {
        unsigned int capacity = steam_bridge_callback_capacity ? steam_bridge_callback_capacity * 2 : 64;
        void *grown = realloc( steam_bridge_callbacks, capacity * sizeof(*steam_bridge_callbacks) );
        if (!grown)
        {
            LeaveCriticalSection( &steam_bridge_lock );
            return;
        }
        steam_bridge_callbacks = (CCallbackBase **)grown;
        steam_bridge_callback_capacity = capacity;
    }
    steam_bridge_callbacks[steam_bridge_callback_count++] = callback;
    CCallbackMgr::Flags( callback ) |= CCallbackMgr::Registered;
    LeaveCriticalSection( &steam_bridge_lock );

    (void)id;
}

STEAM_BRIDGE_EXPORT void __cdecl SteamAPI_UnregisterCallback( CCallbackBase *callback )
{
    if (!callback) return;

    EnterCriticalSection( &steam_bridge_lock );
    for (unsigned int i = 0; i < steam_bridge_callback_count; i++)
    {
        if (steam_bridge_callbacks[i] != callback) continue;
        steam_bridge_callbacks[i] = steam_bridge_callbacks[--steam_bridge_callback_count];
        break;
    }
    CCallbackMgr::Flags( callback ) &= ~CCallbackMgr::Registered;
    LeaveCriticalSection( &steam_bridge_lock );
}

STEAM_BRIDGE_EXPORT void __cdecl SteamAPI_RegisterCallResult( CCallbackBase *callback,
                                                              SteamAPICall_t call )
{
    if (!callback || !call) return;

    EnterCriticalSection( &steam_bridge_lock );
    if (steam_bridge_callresult_count == steam_bridge_callresult_capacity)
    {
        unsigned int capacity = steam_bridge_callresult_capacity ? steam_bridge_callresult_capacity * 2 : 32;
        void *grown = realloc( steam_bridge_callresults, capacity * sizeof(*steam_bridge_callresults) );
        if (!grown)
        {
            LeaveCriticalSection( &steam_bridge_lock );
            return;
        }
        steam_bridge_callresults = (struct steam_bridge_callresult *)grown;
        steam_bridge_callresult_capacity = capacity;
    }
    steam_bridge_callresults[steam_bridge_callresult_count].call = call;
    steam_bridge_callresults[steam_bridge_callresult_count].callback = callback;
    steam_bridge_callresult_count++;
    LeaveCriticalSection( &steam_bridge_lock );
}

STEAM_BRIDGE_EXPORT void __cdecl SteamAPI_UnregisterCallResult( CCallbackBase *callback,
                                                                SteamAPICall_t call )
{
    EnterCriticalSection( &steam_bridge_lock );
    for (unsigned int i = 0; i < steam_bridge_callresult_count; i++)
    {
        if (steam_bridge_callresults[i].callback != callback ||
            steam_bridge_callresults[i].call != call)
            continue;
        steam_bridge_callresults[i] = steam_bridge_callresults[--steam_bridge_callresult_count];
        break;
    }
    LeaveCriticalSection( &steam_bridge_lock );
}

/* --------------------------------------------------------------- lifecycle */

static bool steam_bridge_inited;
static bool steam_bridge_gameserver_inited;
static uintptr_t steam_bridge_context_counter = 1;

static bool steam_bridge_attached;

/* Attaching is what dlopens Valve's library on the Unix side.  Every entry
 * point that can be the first one the game calls goes through here: Source
 * reaches for an interface before SteamAPI_Init in more than one place. */
static bool steam_bridge_attach( void )
{
    struct steam_bridge_attach_params params;
    char dylib[MAX_PATH * 2];
    char app_id[32];

    if (steam_bridge_attached) return true;
    if (!steam_bridge_unix_handle) return false;

    memset( &params, 0, sizeof(params) );

    if (!GetEnvironmentVariableA( "TC2_NATIVE_STEAM_API", dylib, sizeof(dylib) ))
        dylib[0] = 0;
    if (!GetEnvironmentVariableA( "SteamAppId", app_id, sizeof(app_id) ))
        app_id[0] = 0;

    params.dylib_path = dylib[0] ? dylib : NULL;
    params.app_id = app_id[0] ? app_id : NULL;

    if (steam_bridge_call( steam_bridge_call_attach, &params ))
    {
        steam_bridge_log( "attach failed: %s", params.error );
        return false;
    }

    steam_bridge_attached = true;
    return true;
}

STEAM_BRIDGE_EXPORT bool __cdecl SteamAPI_Init( void )
{
    if (steam_bridge_inited) return true;
    if (!steam_bridge_attach())
    {
        steam_bridge_log( "no native Steamworks backend; SteamAPI_Init fails" );
        return false;
    }

    struct steam_bridge_bool_params params;
    memset( &params, 0, sizeof(params) );
    if (steam_bridge_call( steam_bridge_call_init, &params ) || !params._ret)
        return false;

    steam_bridge_inited = true;
    steam_bridge_context_counter++;
    return true;
}

STEAM_BRIDGE_EXPORT bool __cdecl SteamAPI_InitSafe( void )
{
    return SteamAPI_Init();
}

STEAM_BRIDGE_EXPORT bool __cdecl SteamAPI_InitAnonymousUser( void )
{
    return SteamAPI_Init();
}

STEAM_BRIDGE_EXPORT void __cdecl SteamAPI_Shutdown( void )
{
    if (!steam_bridge_inited) return;

    steam_bridge_call( steam_bridge_call_shutdown, NULL );
    steam_bridge_inited = false;
    steam_bridge_context_counter++;
}

STEAM_BRIDGE_EXPORT bool __cdecl SteamAPI_RestartAppIfNecessary( uint32 unOwnAppID )
{
    /* Under Wine the Windows build is launched by the macOS host, not by the
     * Steam client, so a restart request would send the player in a circle. */
    steam_bridge_log( "SteamAPI_RestartAppIfNecessary(%u) declined", unOwnAppID );
    return false;
}

STEAM_BRIDGE_EXPORT bool __cdecl SteamAPI_IsSteamRunning( void )
{
    if (!steam_bridge_attach()) return false;

    struct steam_bridge_bool_params params;
    memset( &params, 0, sizeof(params) );
    if (steam_bridge_call( steam_bridge_call_is_steam_running, &params )) return false;
    return params._ret;
}

STEAM_BRIDGE_EXPORT void __cdecl SteamAPI_ReleaseCurrentThreadMemory( void ) {}
STEAM_BRIDGE_EXPORT void __cdecl SteamAPI_SetMiniDumpComment( const char *pchMsg ) { (void)pchMsg; }
STEAM_BRIDGE_EXPORT void __cdecl SteamAPI_WriteMiniDump( uint32 a, void *b, uint32 c )
{
    (void)a; (void)b; (void)c;
}
STEAM_BRIDGE_EXPORT void __cdecl SteamAPI_SetBreakpadAppID( uint32 unAppID ) { (void)unAppID; }
STEAM_BRIDGE_EXPORT void __cdecl SteamAPI_UseBreakpadCrashHandler( char const *pchVersion,
                                                                   char const *pchDate,
                                                                   char const *pchTime,
                                                                   bool bFullMemoryDumps,
                                                                   void *pvContext,
                                                                   PFNPreMinidumpCallback m_pfnPreMinidumpCallback )
{
    (void)pchVersion; (void)pchDate; (void)pchTime;
    (void)bFullMemoryDumps; (void)pvContext; (void)m_pfnPreMinidumpCallback;
}
STEAM_BRIDGE_EXPORT void __cdecl SteamAPI_SetTryCatchCallbacks( bool bTryCatchCallbacks )
{
    (void)bTryCatchCallbacks;
}

STEAM_BRIDGE_EXPORT HSteamUser __cdecl SteamAPI_GetHSteamUser( void )
{
    struct steam_bridge_handle_params params;
    memset( &params, 0, sizeof(params) );
    steam_bridge_call( steam_bridge_call_get_hsteamuser, &params );
    return params._ret;
}

STEAM_BRIDGE_EXPORT HSteamPipe __cdecl SteamAPI_GetHSteamPipe( void )
{
    struct steam_bridge_handle_params params;
    memset( &params, 0, sizeof(params) );
    steam_bridge_call( steam_bridge_call_get_hsteampipe, &params );
    return params._ret;
}

STEAM_BRIDGE_EXPORT const char * __cdecl SteamAPI_GetSteamInstallPath( void )
{
    struct steam_bridge_install_path_params params;
    memset( &params, 0, sizeof(params) );
    steam_bridge_call( steam_bridge_call_get_steam_install_path, &params );
    if (!params._ret) return NULL;

    /* Native Steam answers with a POSIX path.  The caller is a Windows process
     * that will try to open it, so it gets one it can open. */
    static thread_local char path[MAX_PATH * 4];
    size_t length = strlen( params._ret );
    if (length + 1 > sizeof(path)) return params._ret;

    memcpy( path, params._ret, length + 1 );
    steam_bridge_path_out( path, (unsigned int)sizeof(path) );
    return path;
}

/* ------------------------------------------------------------ interfaces */

STEAM_BRIDGE_EXPORT void * __cdecl SteamInternal_CreateInterface( const char *version )
{
    struct steam_bridge_interface_params params;
    memset( &params, 0, sizeof(params) );
    params.version = version;

    if (!steam_bridge_attach()) return NULL;

    if (steam_bridge_call( steam_bridge_call_create_interface, &params ) || !params._ret)
        return NULL;

    int iface = steam_bridge_iface_from_version( version );
    if (iface < 0)
    {
        steam_bridge_log( "no bridged vtable for interface %s", version ? version : "(null)" );
        return NULL;
    }
    return steam_bridge_wrap( (enum steam_bridge_iface_id)iface, params._ret );
}

STEAM_BRIDGE_EXPORT void * __cdecl SteamInternal_FindOrCreateUserInterface( HSteamUser user,
                                                                            const char *version )
{
    struct steam_bridge_interface_params params;
    memset( &params, 0, sizeof(params) );
    params.user = user;
    params.version = version;

    if (!steam_bridge_attach()) return NULL;

    if (steam_bridge_call( steam_bridge_call_find_user_interface, &params ) || !params._ret)
        return NULL;

    int iface = steam_bridge_iface_from_version( version );
    if (iface < 0)
    {
        steam_bridge_log( "no bridged vtable for user interface %s", version ? version : "(null)" );
        return NULL;
    }
    return steam_bridge_wrap( (enum steam_bridge_iface_id)iface, params._ret );
}

STEAM_BRIDGE_EXPORT void * __cdecl SteamInternal_FindOrCreateGameServerInterface( HSteamUser user,
                                                                                   const char *version )
{
    struct steam_bridge_interface_params params;
    memset( &params, 0, sizeof(params) );
    params.user = user;
    params.version = version;

    if (!steam_bridge_attach()) return NULL;

    if (steam_bridge_call( steam_bridge_call_find_gameserver_interface, &params ) || !params._ret)
        return NULL;

    int iface = steam_bridge_iface_from_version( version );
    if (iface < 0)
    {
        steam_bridge_log( "no bridged vtable for gameserver interface %s",
                          version ? version : "(null)" );
        return NULL;
    }
    return steam_bridge_wrap( (enum steam_bridge_iface_id)iface, params._ret );
}

void *steam_bridge_find_user_interface( const char *version )
{
    return SteamInternal_FindOrCreateUserInterface( SteamAPI_GetHSteamUser(), version );
}

void *steam_bridge_find_gameserver_interface( const char *version )
{
    return SteamInternal_FindOrCreateGameServerInterface( SteamGameServer_GetHSteamUser(), version );
}

STEAM_BRIDGE_EXPORT void * __cdecl SteamAPI_ISteamClient_GetISteamGenericInterface(
    ISteamClient *self, HSteamUser hSteamUser, HSteamPipe hSteamPipe, const char *pchVersion )
{
    (void)self; (void)hSteamUser; (void)hSteamPipe;
    steam_bridge_log( "GetISteamGenericInterface(%s) is not bridged",
                      pchVersion ? pchVersion : "(null)" );
    return NULL;
}

/* The SDK's accessor macros route through here so a cached interface pointer
 * is refreshed whenever the API is re-initialised.  Layout is fixed by the
 * header: { void (*pFn)(void *); uintptr_t counter; void *ptr; }. */
struct steam_bridge_context
{
    void (S_CALLTYPE *init)( void *context );
    uintptr_t counter;
    void *ptr;
};

STEAM_BRIDGE_EXPORT void * __cdecl SteamInternal_ContextInit( void *data )
{
    struct steam_bridge_context *context = (struct steam_bridge_context *)data;

    if (context->counter != steam_bridge_context_counter)
    {
        context->init( &context->ptr );
        context->counter = steam_bridge_context_counter;
    }
    return &context->ptr;
}

/* Legacy undecorated exports.  Valve's steam_api64.dll still carries these and
 * the import library still references them, so a drop-in replacement has to
 * have them too. */

STEAM_BRIDGE_EXPORT HSteamUser __cdecl GetHSteamUser( void )
{
    return SteamAPI_GetHSteamUser();
}

STEAM_BRIDGE_EXPORT HSteamPipe __cdecl GetHSteamPipe( void )
{
    return SteamAPI_GetHSteamPipe();
}

STEAM_BRIDGE_EXPORT ISteamClient * __cdecl SteamClient( void )
{
    return (ISteamClient *)SteamInternal_CreateInterface( STEAMCLIENT_INTERFACE_VERSION );
}

/* A data export, not a function.  Older code read it directly instead of
 * calling an accessor; it is filled in when the game server side comes up.
 * The definition is separated from the extern "C" declaration so this does not
 * read as an initialised extern. */
extern "C" { __declspec(dllexport) ISteamClient *g_pSteamClientGameServer; }

/* ------------------------------------------------------- manual dispatch */

STEAM_BRIDGE_EXPORT void __cdecl SteamAPI_ManualDispatch_Init( void ) {}

STEAM_BRIDGE_EXPORT void __cdecl SteamAPI_ManualDispatch_RunFrame( HSteamPipe pipe )
{
    struct steam_bridge_pipe_params params;
    params.pipe = pipe;
    steam_bridge_call( steam_bridge_call_dispatch_run_frame, &params );
}

/* A callback arrives packed the way macOS packs it.  The game reads it packed
 * the way Windows does, so it is repacked into a buffer this side owns, which
 * lives until the matching FreeLastCallback. */
static thread_local void *steam_bridge_callback_buffer;
static thread_local size_t steam_bridge_callback_buffer_size;

static void *steam_bridge_callback_scratch( size_t size )
{
    if (steam_bridge_callback_buffer_size < size)
    {
        void *grown = realloc( steam_bridge_callback_buffer, size );
        if (!grown) return NULL;
        steam_bridge_callback_buffer = grown;
        steam_bridge_callback_buffer_size = size;
    }
    return steam_bridge_callback_buffer;
}

STEAM_BRIDGE_EXPORT bool __cdecl SteamAPI_ManualDispatch_GetNextCallback( HSteamPipe pipe,
                                                                          CallbackMsg_t *msg )
{
    struct steam_bridge_dispatch_next_params params;
    memset( &params, 0, sizeof(params) );
    params.pipe = pipe;

    if (steam_bridge_call( steam_bridge_call_dispatch_next, &params ) || !params._ret)
        return false;

    msg->m_hSteamUser = params.user;
    msg->m_iCallback = params.callback;
    msg->m_pubParam = params.param;
    msg->m_cubParam = params.param_size;

    const struct steam_bridge_callback_info *info = steam_bridge_callback_info( params.callback );
    if (info && params.param && params.param_size >= info->native_size)
    {
        void *repacked = steam_bridge_callback_scratch( (size_t)info->windows_size );
        if (repacked)
        {
            memset( repacked, 0, (size_t)info->windows_size );
            info->u2w( params.param, repacked );
            msg->m_pubParam = (uint8 *)repacked;
            msg->m_cubParam = info->windows_size;
        }
    }
    else if (!info)
    {
        steam_bridge_log( "callback %d has no packing description; passed through raw",
                          params.callback );
    }

    return true;
}

STEAM_BRIDGE_EXPORT void __cdecl SteamAPI_ManualDispatch_FreeLastCallback( HSteamPipe pipe )
{
    struct steam_bridge_pipe_params params;
    params.pipe = pipe;
    steam_bridge_call( steam_bridge_call_dispatch_free, &params );
}

STEAM_BRIDGE_EXPORT bool __cdecl SteamAPI_ManualDispatch_GetAPICallResult(
    HSteamPipe pipe, SteamAPICall_t call, void *callback, int callback_size,
    int callback_expected, bool *failed )
{
    struct steam_bridge_dispatch_result_params params;
    memset( &params, 0, sizeof(params) );
    params.pipe = pipe;
    params.call = call;
    params.callback = callback;
    params.callback_size = callback_size;
    params.callback_expected = callback_expected;

    /* Same repacking problem as a callback, in the other direction: the caller
     * hands us a Windows-layout buffer and Steam fills a native-layout one. */
    const struct steam_bridge_callback_info *info = steam_bridge_callback_info( callback_expected );
    void *native = NULL;

    if (info && callback && callback_size >= info->windows_size)
    {
        native = malloc( (size_t)info->native_size );
        if (native)
        {
            memset( native, 0, (size_t)info->native_size );
            params.callback = native;
            params.callback_size = info->native_size;
        }
    }

    if (steam_bridge_call( steam_bridge_call_dispatch_get_result, &params ))
    {
        free( native );
        return false;
    }

    if (native)
    {
        if (params._ret) info->u2w( native, callback );
        free( native );
    }

    if (failed) *failed = params.failed;
    return params._ret;
}

static void steam_bridge_dispatch_callback( const CallbackMsg_t *msg, bool gameserver )
{
    EnterCriticalSection( &steam_bridge_lock );

    /* Copied out of the registry before dispatch: a callback is allowed to
     * register or unregister others while it runs. */
    unsigned int count = steam_bridge_callback_count;
    CCallbackBase **snapshot = (CCallbackBase **)malloc( count * sizeof(*snapshot) );
    if (snapshot) memcpy( snapshot, steam_bridge_callbacks, count * sizeof(*snapshot) );

    LeaveCriticalSection( &steam_bridge_lock );
    if (!snapshot) return;

    for (unsigned int i = 0; i < count; i++)
    {
        CCallbackBase *callback = snapshot[i];
        if (CCallbackMgr::Id( callback ) != msg->m_iCallback) continue;
        if (!!(CCallbackMgr::Flags( callback ) & CCallbackMgr::GameServer) != gameserver) continue;
        callback->Run( msg->m_pubParam );
    }
    free( snapshot );
}

static void steam_bridge_dispatch_callresult( SteamAPICall_t call, void *param, bool failed )
{
    EnterCriticalSection( &steam_bridge_lock );

    CCallbackBase *callback = NULL;
    for (unsigned int i = 0; i < steam_bridge_callresult_count; i++)
    {
        if (steam_bridge_callresults[i].call != call) continue;
        callback = steam_bridge_callresults[i].callback;
        steam_bridge_callresults[i] = steam_bridge_callresults[--steam_bridge_callresult_count];
        break;
    }

    LeaveCriticalSection( &steam_bridge_lock );

    if (callback) callback->Run( param, failed, call );
}

static void steam_bridge_run_callbacks( HSteamPipe pipe, bool gameserver )
{
    CallbackMsg_t msg;

    if (!pipe) return;

    SteamAPI_ManualDispatch_RunFrame( pipe );

    while (SteamAPI_ManualDispatch_GetNextCallback( pipe, &msg ))
    {
        if (msg.m_iCallback == SteamAPICallCompleted_t::k_iCallback)
        {
            SteamAPICallCompleted_t *completed = (SteamAPICallCompleted_t *)msg.m_pubParam;
            const struct steam_bridge_callback_info *info =
                steam_bridge_callback_info( completed->m_iCallback );

            /* m_cubParam is the size Steam reports, which is the native one.
             * The buffer handed to the game has to be the Windows size. */
            int size = info ? info->windows_size : (int)completed->m_cubParam;
            void *result = malloc( (size_t)size );
            bool failed = false;

            if (result && SteamAPI_ManualDispatch_GetAPICallResult( pipe, completed->m_hAsyncCall,
                                                                    result, size,
                                                                    completed->m_iCallback, &failed ))
            {
                steam_bridge_dispatch_callresult( completed->m_hAsyncCall, result, failed );
            }
            free( result );
        }
        else
        {
            steam_bridge_dispatch_callback( &msg, gameserver );
        }

        SteamAPI_ManualDispatch_FreeLastCallback( pipe );
    }
}

STEAM_BRIDGE_EXPORT void __cdecl SteamAPI_RunCallbacks( void )
{
    if (!steam_bridge_inited) return;
    steam_bridge_run_callbacks( SteamAPI_GetHSteamPipe(), false );
}

STEAM_BRIDGE_EXPORT void __cdecl SteamGameServer_RunCallbacks( void )
{
    if (!steam_bridge_gameserver_inited) return;
    steam_bridge_run_callbacks( SteamGameServer_GetHSteamPipe(), true );
}

/* ---------------------------------------------------------- game server */

STEAM_BRIDGE_EXPORT bool __cdecl SteamInternal_GameServer_Init( uint32 unIP,
                                                                uint16 usLegacySteamPort,
                                                                uint16 usGamePort,
                                                                uint16 usQueryPort,
                                                                EServerMode eServerMode,
                                                                const char *pchVersionString )
{
    if (!steam_bridge_attach()) return false;

    struct steam_bridge_init_params params;
    memset( &params, 0, sizeof(params) );
    params.ip = unIP;
    params.legacy_steam_port = usLegacySteamPort;
    params.game_port = usGamePort;
    params.query_port = usQueryPort;
    params.server_mode = (int)eServerMode;
    params.version_string = pchVersionString;

    if (steam_bridge_call( steam_bridge_call_gameserver_init, &params ) || !params._ret)
        return false;

    steam_bridge_gameserver_inited = true;
    steam_bridge_context_counter++;
    g_pSteamClientGameServer = (ISteamClient *)SteamInternal_CreateInterface(
        STEAMCLIENT_INTERFACE_VERSION );
    return true;
}

STEAM_BRIDGE_EXPORT bool __cdecl SteamGameServer_InitSafe( uint32 unIP, uint16 usSteamPort,
                                                           uint16 usGamePort, uint16 usQueryPort,
                                                           EServerMode eServerMode,
                                                           const char *pchVersionString )
{
    return SteamInternal_GameServer_Init( unIP, usSteamPort, usGamePort, usQueryPort,
                                          eServerMode, pchVersionString );
}

STEAM_BRIDGE_EXPORT void __cdecl SteamGameServer_Shutdown( void )
{
    if (!steam_bridge_gameserver_inited) return;

    steam_bridge_call( steam_bridge_call_gameserver_shutdown, NULL );
    steam_bridge_gameserver_inited = false;
    g_pSteamClientGameServer = NULL;
    steam_bridge_context_counter++;
}

STEAM_BRIDGE_EXPORT HSteamUser __cdecl SteamGameServer_GetHSteamUser( void )
{
    struct steam_bridge_handle_params params;
    memset( &params, 0, sizeof(params) );
    steam_bridge_call( steam_bridge_call_gameserver_get_hsteamuser, &params );
    return params._ret;
}

STEAM_BRIDGE_EXPORT HSteamPipe __cdecl SteamGameServer_GetHSteamPipe( void )
{
    struct steam_bridge_handle_params params;
    memset( &params, 0, sizeof(params) );
    steam_bridge_call( steam_bridge_call_gameserver_get_hsteampipe, &params );
    return params._ret;
}

STEAM_BRIDGE_EXPORT bool __cdecl SteamGameServer_BSecure( void )
{
    ISteamGameServer *server = (ISteamGameServer *)steam_bridge_find_gameserver_interface(
        STEAMGAMESERVER_INTERFACE_VERSION );
    return server ? server->BSecure() : false;
}

STEAM_BRIDGE_EXPORT uint64 __cdecl SteamGameServer_GetSteamID( void )
{
    ISteamGameServer *server = (ISteamGameServer *)steam_bridge_find_gameserver_interface(
        STEAMGAMESERVER_INTERFACE_VERSION );
    return server ? server->GetSteamID().ConvertToUint64() : 0;
}

STEAM_BRIDGE_EXPORT int __cdecl SteamGameServer_GetIPCCallCount( void )
{
    ISteamClient *client = (ISteamClient *)SteamInternal_CreateInterface(
        STEAMCLIENT_INTERFACE_VERSION );
    return client ? (int)client->GetIPCCallCount() : 0;
}

/* ------------------------------------------------------------- entry point */

extern "C" BOOL WINAPI DllMain( HINSTANCE instance, DWORD reason, void *reserved )
{
    (void)reserved;

    switch (reason)
    {
    case DLL_PROCESS_ATTACH:
        DisableThreadLibraryCalls( instance );
        steam_bridge_self = instance;
        steam_bridge_debug = GetEnvironmentVariableA( "TC2_STEAM_BRIDGE_DEBUG", NULL, 0 ) != 0;
        InitializeCriticalSection( &steam_bridge_lock );
        if (!steam_bridge_load_unixlib())
        {
            /* Deliberately not fatal: the DLL still loads and every entry
             * point fails cleanly, which produces a readable "Steam is not
             * running" error instead of a load-time crash. */
            steam_bridge_log( "the Unix half of the bridge is not available" );
        }
        break;

    case DLL_PROCESS_DETACH:
        if (steam_bridge_inited) SteamAPI_Shutdown();
        if (steam_bridge_gameserver_inited) SteamGameServer_Shutdown();
        steam_bridge_call( steam_bridge_call_detach, NULL );
        steam_bridge_attached = false;
        break;
    }
    return TRUE;
}
