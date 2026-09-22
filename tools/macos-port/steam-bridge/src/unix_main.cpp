/* Unix half of the Steamworks bridge: a Mach-O library loaded into the Wine
 * process next to the PE half.
 *
 * It links nothing at build time.  The real libsteam_api.dylib is dlopen'd at
 * attach, which keeps the bridge itself free of the Steamworks redistributable
 * and lets the app bundle ship Valve's binary under Valve's own terms.
 *
 * Nothing here copies a buffer.  Both halves share one address space, so a
 * pointer the game passed in is already valid; the whole job is to re-issue the
 * call under the other ABI.
 */

#include "bridge_unix.h"

#include <dlfcn.h>
#include <stdarg.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

/* -------------------------------------------------------------- native SDK */

static void *steam_native_handle;
static bool steam_native_debug;

static bool (*p_SteamAPI_Init)( void );
static bool (*p_SteamAPI_InitSafe)( void );
static void (*p_SteamAPI_Shutdown)( void );
static bool (*p_SteamAPI_IsSteamRunning)( void );
static HSteamUser (*p_SteamAPI_GetHSteamUser)( void );
static HSteamPipe (*p_SteamAPI_GetHSteamPipe)( void );
static const char *(*p_SteamAPI_GetSteamInstallPath)( void );
static void *(*p_SteamInternal_CreateInterface)( const char * );
static void *(*p_SteamInternal_FindOrCreateUserInterface)( HSteamUser, const char * );
static void *(*p_SteamInternal_FindOrCreateGameServerInterface)( HSteamUser, const char * );
static void (*p_SteamAPI_ManualDispatch_Init)( void );
static void (*p_SteamAPI_ManualDispatch_RunFrame)( HSteamPipe );
static bool (*p_SteamAPI_ManualDispatch_GetNextCallback)( HSteamPipe, CallbackMsg_t * );
static void (*p_SteamAPI_ManualDispatch_FreeLastCallback)( HSteamPipe );
static bool (*p_SteamAPI_ManualDispatch_GetAPICallResult)( HSteamPipe, SteamAPICall_t, void *,
                                                           int, int, bool * );
static bool (*p_SteamInternal_GameServer_Init)( uint32, uint16, uint16, uint16, EServerMode,
                                                const char * );
static void (*p_SteamGameServer_Shutdown)( void );
static HSteamUser (*p_SteamGameServer_GetHSteamUser)( void );
static HSteamPipe (*p_SteamGameServer_GetHSteamPipe)( void );

void steam_bridge_unix_log( const char *format, ... )
{
    if (!steam_native_debug) return;

    va_list args;
    fprintf( stderr, "steam_bridge/unix: " );
    va_start( args, format );
    vfprintf( stderr, format, args );
    va_end( args );
    fprintf( stderr, "\n" );
    fflush( stderr );
}

#define STEAM_BRIDGE_RESOLVE( name )                                              \
    do {                                                                          \
        *(void **)&p_##name = dlsym( steam_native_handle, #name );                 \
    } while (0)

#define STEAM_BRIDGE_REQUIRE( name, params )                                      \
    do {                                                                          \
        if (!p_##name)                                                            \
        {                                                                         \
            snprintf( (params)->error, sizeof((params)->error),                    \
                      "libsteam_api.dylib does not export %s", #name );            \
            dlclose( steam_native_handle );                                        \
            steam_native_handle = NULL;                                            \
            return STEAM_BRIDGE_STATUS_UNAVAILABLE;                                \
        }                                                                         \
    } while (0)

static NTSTATUS unix_attach( void *args )
{
    struct steam_bridge_attach_params *params = (struct steam_bridge_attach_params *)args;

    steam_native_debug = getenv( "TC2_STEAM_BRIDGE_DEBUG" ) != NULL;

    if (steam_native_handle) return STATUS_SUCCESS;

    const char *path = params->dylib_path;
    if (!path || !path[0]) path = "libsteam_api.dylib";

    if (params->app_id && params->app_id[0])
        setenv( "SteamAppId", params->app_id, 1 );

    steam_native_handle = dlopen( path, RTLD_NOW | RTLD_LOCAL );
    if (!steam_native_handle)
    {
        snprintf( params->error, sizeof(params->error), "dlopen(%s): %s", path, dlerror() );
        return STEAM_BRIDGE_STATUS_UNAVAILABLE;
    }

    STEAM_BRIDGE_RESOLVE( SteamAPI_Init );
    STEAM_BRIDGE_RESOLVE( SteamAPI_InitSafe );
    STEAM_BRIDGE_RESOLVE( SteamAPI_Shutdown );
    STEAM_BRIDGE_RESOLVE( SteamAPI_IsSteamRunning );
    STEAM_BRIDGE_RESOLVE( SteamAPI_GetHSteamUser );
    STEAM_BRIDGE_RESOLVE( SteamAPI_GetHSteamPipe );
    STEAM_BRIDGE_RESOLVE( SteamAPI_GetSteamInstallPath );
    STEAM_BRIDGE_RESOLVE( SteamInternal_CreateInterface );
    STEAM_BRIDGE_RESOLVE( SteamInternal_FindOrCreateUserInterface );
    STEAM_BRIDGE_RESOLVE( SteamInternal_FindOrCreateGameServerInterface );
    STEAM_BRIDGE_RESOLVE( SteamAPI_ManualDispatch_Init );
    STEAM_BRIDGE_RESOLVE( SteamAPI_ManualDispatch_RunFrame );
    STEAM_BRIDGE_RESOLVE( SteamAPI_ManualDispatch_GetNextCallback );
    STEAM_BRIDGE_RESOLVE( SteamAPI_ManualDispatch_FreeLastCallback );
    STEAM_BRIDGE_RESOLVE( SteamAPI_ManualDispatch_GetAPICallResult );
    STEAM_BRIDGE_RESOLVE( SteamInternal_GameServer_Init );
    STEAM_BRIDGE_RESOLVE( SteamGameServer_Shutdown );
    STEAM_BRIDGE_RESOLVE( SteamGameServer_GetHSteamUser );
    STEAM_BRIDGE_RESOLVE( SteamGameServer_GetHSteamPipe );

    /* Manual dispatch is what makes the callback design work; without it the
     * bridge would have to hand PE vtables to the native library. */
    STEAM_BRIDGE_REQUIRE( SteamAPI_ManualDispatch_Init, params );
    STEAM_BRIDGE_REQUIRE( SteamAPI_ManualDispatch_GetNextCallback, params );
    STEAM_BRIDGE_REQUIRE( SteamInternal_FindOrCreateUserInterface, params );

    steam_bridge_unix_log( "attached to %s", path );
    return STATUS_SUCCESS;
}

static NTSTATUS unix_detach( void *args )
{
    (void)args;
    if (steam_native_handle)
    {
        dlclose( steam_native_handle );
        steam_native_handle = NULL;
    }
    return STATUS_SUCCESS;
}

static NTSTATUS unix_init( void *args )
{
    struct steam_bridge_bool_params *params = (struct steam_bridge_bool_params *)args;

    if (p_SteamAPI_Init) params->_ret = p_SteamAPI_Init();
    else if (p_SteamAPI_InitSafe) params->_ret = p_SteamAPI_InitSafe();
    else params->_ret = false;

    if (params->_ret) p_SteamAPI_ManualDispatch_Init();
    else steam_bridge_unix_log( "native SteamAPI_Init failed" );

    return STATUS_SUCCESS;
}

static NTSTATUS unix_shutdown( void *args )
{
    (void)args;
    if (p_SteamAPI_Shutdown) p_SteamAPI_Shutdown();
    return STATUS_SUCCESS;
}

static NTSTATUS unix_restart_app_if_necessary( void *args )
{
    struct steam_bridge_bool_params *params = (struct steam_bridge_bool_params *)args;
    params->_ret = false;
    return STATUS_SUCCESS;
}

static NTSTATUS unix_is_steam_running( void *args )
{
    struct steam_bridge_bool_params *params = (struct steam_bridge_bool_params *)args;
    params->_ret = p_SteamAPI_IsSteamRunning ? p_SteamAPI_IsSteamRunning() : false;
    return STATUS_SUCCESS;
}

static NTSTATUS unix_get_hsteamuser( void *args )
{
    struct steam_bridge_handle_params *params = (struct steam_bridge_handle_params *)args;
    params->_ret = p_SteamAPI_GetHSteamUser ? p_SteamAPI_GetHSteamUser() : 0;
    return STATUS_SUCCESS;
}

static NTSTATUS unix_get_hsteampipe( void *args )
{
    struct steam_bridge_handle_params *params = (struct steam_bridge_handle_params *)args;
    params->_ret = p_SteamAPI_GetHSteamPipe ? p_SteamAPI_GetHSteamPipe() : 0;
    return STATUS_SUCCESS;
}

static NTSTATUS unix_create_interface( void *args )
{
    struct steam_bridge_interface_params *params = (struct steam_bridge_interface_params *)args;
    params->_ret = p_SteamInternal_CreateInterface
        ? p_SteamInternal_CreateInterface( params->version ) : NULL;
    return STATUS_SUCCESS;
}

static NTSTATUS unix_find_user_interface( void *args )
{
    struct steam_bridge_interface_params *params = (struct steam_bridge_interface_params *)args;
    params->_ret = p_SteamInternal_FindOrCreateUserInterface
        ? p_SteamInternal_FindOrCreateUserInterface( params->user, params->version ) : NULL;
    return STATUS_SUCCESS;
}

static NTSTATUS unix_find_gameserver_interface( void *args )
{
    struct steam_bridge_interface_params *params = (struct steam_bridge_interface_params *)args;
    params->_ret = p_SteamInternal_FindOrCreateGameServerInterface
        ? p_SteamInternal_FindOrCreateGameServerInterface( params->user, params->version ) : NULL;
    return STATUS_SUCCESS;
}

static NTSTATUS unix_release_user_interface( void *args )
{
    (void)args;
    return STATUS_SUCCESS;
}

static NTSTATUS unix_dispatch_run_frame( void *args )
{
    struct steam_bridge_pipe_params *params = (struct steam_bridge_pipe_params *)args;
    if (p_SteamAPI_ManualDispatch_RunFrame) p_SteamAPI_ManualDispatch_RunFrame( params->pipe );
    return STATUS_SUCCESS;
}

static NTSTATUS unix_dispatch_next( void *args )
{
    struct steam_bridge_dispatch_next_params *params =
        (struct steam_bridge_dispatch_next_params *)args;

    CallbackMsg_t msg = {};

    /* m_pubParam points into the native library's own buffer.  That is fine to
     * hand straight back: the PE side reads it before FreeLastCallback, and it
     * is the same address space.  Its contents are still native-packed, which
     * is what the PE side converts. */
    params->_ret = p_SteamAPI_ManualDispatch_GetNextCallback
        ? p_SteamAPI_ManualDispatch_GetNextCallback( params->pipe, &msg ) : false;

    if (params->_ret)
    {
        params->user = msg.m_hSteamUser;
        params->callback = msg.m_iCallback;
        params->param = msg.m_pubParam;
        params->param_size = msg.m_cubParam;
    }
    return STATUS_SUCCESS;
}

static NTSTATUS unix_dispatch_free( void *args )
{
    struct steam_bridge_pipe_params *params = (struct steam_bridge_pipe_params *)args;
    if (p_SteamAPI_ManualDispatch_FreeLastCallback)
        p_SteamAPI_ManualDispatch_FreeLastCallback( params->pipe );
    return STATUS_SUCCESS;
}

static NTSTATUS unix_dispatch_get_result( void *args )
{
    struct steam_bridge_dispatch_result_params *params =
        (struct steam_bridge_dispatch_result_params *)args;

    params->_ret = p_SteamAPI_ManualDispatch_GetAPICallResult
        ? p_SteamAPI_ManualDispatch_GetAPICallResult( params->pipe, params->call, params->callback,
                                                      params->callback_size,
                                                      params->callback_expected, &params->failed )
        : false;
    return STATUS_SUCCESS;
}

static NTSTATUS unix_gameserver_init( void *args )
{
    struct steam_bridge_init_params *params = (struct steam_bridge_init_params *)args;

    params->_ret = p_SteamInternal_GameServer_Init
        ? p_SteamInternal_GameServer_Init( params->ip, params->legacy_steam_port,
                                           params->game_port, params->query_port,
                                           (EServerMode)params->server_mode,
                                           params->version_string )
        : false;
    if (params->_ret && p_SteamAPI_ManualDispatch_Init) p_SteamAPI_ManualDispatch_Init();
    return STATUS_SUCCESS;
}

static NTSTATUS unix_gameserver_shutdown( void *args )
{
    (void)args;
    if (p_SteamGameServer_Shutdown) p_SteamGameServer_Shutdown();
    return STATUS_SUCCESS;
}

static NTSTATUS unix_gameserver_get_hsteamuser( void *args )
{
    struct steam_bridge_handle_params *params = (struct steam_bridge_handle_params *)args;
    params->_ret = p_SteamGameServer_GetHSteamUser ? p_SteamGameServer_GetHSteamUser() : 0;
    return STATUS_SUCCESS;
}

static NTSTATUS unix_gameserver_get_hsteampipe( void *args )
{
    struct steam_bridge_handle_params *params = (struct steam_bridge_handle_params *)args;
    params->_ret = p_SteamGameServer_GetHSteamPipe ? p_SteamGameServer_GetHSteamPipe() : 0;
    return STATUS_SUCCESS;
}

static NTSTATUS unix_set_warning_hook( void *args )
{
    (void)args;
    return STATUS_SUCCESS;
}

static NTSTATUS unix_get_steam_install_path( void *args )
{
    struct steam_bridge_install_path_params *params =
        (struct steam_bridge_install_path_params *)args;
    params->_ret = p_SteamAPI_GetSteamInstallPath ? p_SteamAPI_GetSteamInstallPath() : NULL;
    return STATUS_SUCCESS;
}

/* ---------------------------------------------- callbacks owned by the game */

#define STEAM_BRIDGE_FNPTR_WRAPPER( type, param )                                       \
    static void (STEAM_BRIDGE_MS_ABI *steam_pe_##type)( param );                        \
    static void steam_bridge_thunk_##type( param arg )                                  \
    {                                                                                   \
        if (steam_pe_##type) steam_pe_##type( arg );                                    \
    }                                                                                   \
    type steam_bridge_wrap_##type( type fn )                                            \
    {                                                                                   \
        steam_pe_##type = (void (STEAM_BRIDGE_MS_ABI *)( param ))fn;                     \
        return fn ? steam_bridge_thunk_##type : NULL;                                   \
    }

STEAM_BRIDGE_FNPTR_WRAPPER( FnSteamNetConnectionStatusChanged,
                            SteamNetConnectionStatusChangedCallback_t * )
STEAM_BRIDGE_FNPTR_WRAPPER( FnSteamNetAuthenticationStatusChanged, SteamNetAuthenticationStatus_t * )
STEAM_BRIDGE_FNPTR_WRAPPER( FnSteamRelayNetworkStatusChanged, SteamRelayNetworkStatus_t * )
STEAM_BRIDGE_FNPTR_WRAPPER( FnSteamNetworkingMessagesSessionRequest,
                            SteamNetworkingMessagesSessionRequest_t * )
STEAM_BRIDGE_FNPTR_WRAPPER( FnSteamNetworkingMessagesSessionFailed,
                            SteamNetworkingMessagesSessionFailed_t * )
STEAM_BRIDGE_FNPTR_WRAPPER( FnSteamNetworkingFakeIPResult, SteamNetworkingFakeIPResult_t * )

static void (STEAM_BRIDGE_MS_ABI *steam_pe_warning_hook)( int, const char * );

static void steam_bridge_thunk_warning_hook( int severity, const char *message )
{
    if (steam_pe_warning_hook) steam_pe_warning_hook( severity, message );
}

SteamAPIWarningMessageHook_t
steam_bridge_wrap_SteamAPIWarningMessageHook_t( SteamAPIWarningMessageHook_t hook )
{
    steam_pe_warning_hook = (void (STEAM_BRIDGE_MS_ABI *)( int, const char * ))hook;
    return hook ? steam_bridge_thunk_warning_hook : NULL;
}

/* ------------------------------------------- server-browser response objects */

/* Reading slot N out of the PE object's vtable and calling it with ms_abi is
 * the mirror image of what the PE half does for interfaces coming the other
 * way.  Only these four interfaces travel in this direction. */
#define STEAM_BRIDGE_PE_VCALL( object, slot, signature )                                \
    ((signature)(((void ***)(object))[0][(slot)]))

class steam_bridge_server_list_response : public ISteamMatchmakingServerListResponse
{
public:
    void *pe;

    explicit steam_bridge_server_list_response( void *object ) : pe( object ) {}

    void ServerResponded( HServerListRequest request, int server ) override
    {
        typedef void (STEAM_BRIDGE_MS_ABI *fn_t)( void *, HServerListRequest, int );
        STEAM_BRIDGE_PE_VCALL( pe, 0, fn_t )( pe, request, server );
    }

    void ServerFailedToRespond( HServerListRequest request, int server ) override
    {
        typedef void (STEAM_BRIDGE_MS_ABI *fn_t)( void *, HServerListRequest, int );
        STEAM_BRIDGE_PE_VCALL( pe, 1, fn_t )( pe, request, server );
    }

    void RefreshComplete( HServerListRequest request, EMatchMakingServerResponse response ) override
    {
        typedef void (STEAM_BRIDGE_MS_ABI *fn_t)( void *, HServerListRequest,
                                                  EMatchMakingServerResponse );
        STEAM_BRIDGE_PE_VCALL( pe, 2, fn_t )( pe, request, response );
    }
};

class steam_bridge_ping_response : public ISteamMatchmakingPingResponse
{
public:
    void *pe;

    explicit steam_bridge_ping_response( void *object ) : pe( object ) {}

    void ServerResponded( gameserveritem_t &server ) override
    {
        typedef void (STEAM_BRIDGE_MS_ABI *fn_t)( void *, gameserveritem_t * );
        STEAM_BRIDGE_PE_VCALL( pe, 0, fn_t )( pe, &server );
    }

    void ServerFailedToRespond() override
    {
        typedef void (STEAM_BRIDGE_MS_ABI *fn_t)( void * );
        STEAM_BRIDGE_PE_VCALL( pe, 1, fn_t )( pe );
    }
};

class steam_bridge_players_response : public ISteamMatchmakingPlayersResponse
{
public:
    void *pe;

    explicit steam_bridge_players_response( void *object ) : pe( object ) {}

    void AddPlayerToList( const char *name, int score, float time_played ) override
    {
        typedef void (STEAM_BRIDGE_MS_ABI *fn_t)( void *, const char *, int, float );
        STEAM_BRIDGE_PE_VCALL( pe, 0, fn_t )( pe, name, score, time_played );
    }

    void PlayersFailedToRespond() override
    {
        typedef void (STEAM_BRIDGE_MS_ABI *fn_t)( void * );
        STEAM_BRIDGE_PE_VCALL( pe, 1, fn_t )( pe );
    }

    void PlayersRefreshComplete() override
    {
        typedef void (STEAM_BRIDGE_MS_ABI *fn_t)( void * );
        STEAM_BRIDGE_PE_VCALL( pe, 2, fn_t )( pe );
    }
};

class steam_bridge_rules_response : public ISteamMatchmakingRulesResponse
{
public:
    void *pe;

    explicit steam_bridge_rules_response( void *object ) : pe( object ) {}

    void RulesResponded( const char *rule, const char *value ) override
    {
        typedef void (STEAM_BRIDGE_MS_ABI *fn_t)( void *, const char *, const char * );
        STEAM_BRIDGE_PE_VCALL( pe, 0, fn_t )( pe, rule, value );
    }

    void RulesFailedToRespond() override
    {
        typedef void (STEAM_BRIDGE_MS_ABI *fn_t)( void * );
        STEAM_BRIDGE_PE_VCALL( pe, 1, fn_t )( pe );
    }

    void RulesRefreshComplete() override
    {
        typedef void (STEAM_BRIDGE_MS_ABI *fn_t)( void * );
        STEAM_BRIDGE_PE_VCALL( pe, 2, fn_t )( pe );
    }
};

/* One wrapper per PE object, kept for the life of the process.  These are
 * created once per server-browser query and the game holds them across it, so
 * a small append-only table is the right shape. */
struct steam_bridge_response_entry
{
    void *pe;
    void *wrapper;
    int kind;
};

static struct steam_bridge_response_entry *steam_bridge_responses;
static unsigned int steam_bridge_response_used;
static unsigned int steam_bridge_response_capacity;

static void *steam_bridge_response_lookup( void *pe, int kind )
{
    for (unsigned int i = 0; i < steam_bridge_response_used; i++)
    {
        if (steam_bridge_responses[i].pe == pe && steam_bridge_responses[i].kind == kind)
            return steam_bridge_responses[i].wrapper;
    }
    return NULL;
}

static void *steam_bridge_response_store( void *pe, int kind, void *wrapper )
{
    if (steam_bridge_response_used == steam_bridge_response_capacity)
    {
        unsigned int capacity = steam_bridge_response_capacity ? steam_bridge_response_capacity * 2 : 16;
        void *grown = realloc( steam_bridge_responses, capacity * sizeof(*steam_bridge_responses) );
        if (!grown) return wrapper;
        steam_bridge_responses = (struct steam_bridge_response_entry *)grown;
        steam_bridge_response_capacity = capacity;
    }
    steam_bridge_responses[steam_bridge_response_used].pe = pe;
    steam_bridge_responses[steam_bridge_response_used].kind = kind;
    steam_bridge_responses[steam_bridge_response_used].wrapper = wrapper;
    steam_bridge_response_used++;
    return wrapper;
}

#define STEAM_BRIDGE_RESPONSE_WRAPPER( iface, implementation, kind )                    \
    iface *steam_bridge_wrap_##iface( iface *pe )                                       \
    {                                                                                   \
        if (!pe) return NULL;                                                           \
        void *cached = steam_bridge_response_lookup( pe, kind );                        \
        if (cached) return (iface *)cached;                                             \
        return (iface *)steam_bridge_response_store( pe, kind,                          \
                                                     new implementation( pe ) );        \
    }

STEAM_BRIDGE_RESPONSE_WRAPPER( ISteamMatchmakingServerListResponse,
                               steam_bridge_server_list_response, 0 )
STEAM_BRIDGE_RESPONSE_WRAPPER( ISteamMatchmakingPingResponse, steam_bridge_ping_response, 1 )
STEAM_BRIDGE_RESPONSE_WRAPPER( ISteamMatchmakingPlayersResponse, steam_bridge_players_response, 2 )
STEAM_BRIDGE_RESPONSE_WRAPPER( ISteamMatchmakingRulesResponse, steam_bridge_rules_response, 3 )

static NTSTATUS unix_release_response( void *args )
{
    struct steam_bridge_release_response_params *params =
        (struct steam_bridge_release_response_params *)args;

    for (unsigned int i = 0; i < steam_bridge_response_used; i++)
    {
        if (steam_bridge_responses[i].pe != params->pe_object) continue;
        steam_bridge_responses[i] = steam_bridge_responses[--steam_bridge_response_used];
        i--;
    }
    return STATUS_SUCCESS;
}

/* ------------------------------------------------------------- dispatch table */

#include "unix_thunks.cpp"

extern "C" __attribute__((visibility("default")))
const unixlib_entry_t __wine_unix_call_funcs[] =
{
    unix_attach,
    unix_detach,
    unix_init,
    unix_shutdown,
    unix_restart_app_if_necessary,
    unix_is_steam_running,
    unix_get_hsteamuser,
    unix_get_hsteampipe,
    unix_create_interface,
    unix_find_user_interface,
    unix_find_gameserver_interface,
    unix_release_user_interface,
    unix_dispatch_run_frame,
    unix_dispatch_next,
    unix_dispatch_free,
    unix_dispatch_get_result,
    unix_gameserver_init,
    unix_gameserver_shutdown,
    unix_gameserver_get_hsteamuser,
    unix_gameserver_get_hsteampipe,
    unix_set_warning_hook,
    unix_get_steam_install_path,
    unix_release_response,
    STEAM_BRIDGE_GENERATED_CALLS
};

/* The one thing that can silently rot: the enum and the table are written by
 * two different files, so make the compiler check they still line up. */
static_assert( sizeof(__wine_unix_call_funcs) / sizeof(__wine_unix_call_funcs[0])
                   == steam_bridge_call_count,
               "the unix dispatch table and steam_bridge_call are out of sync" );
