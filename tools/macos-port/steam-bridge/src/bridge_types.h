/* Shared between the PE half and the Unix half of the Steamworks bridge.
 *
 * Both halves compile the same Steamworks headers, so every type that crosses
 * the boundary has one definition and one layout.  That is what lets the
 * generated params structs be written by one compiler and read by the other.
 */

#ifndef STEAM_BRIDGE_TYPES_H
#define STEAM_BRIDGE_TYPES_H

#include <stdint.h>
#include <stddef.h>
#include <string.h>

#if defined(STEAM_BRIDGE_PE)
/* Makes S_API dllexport and, more importantly, opens up the methods the SDK
 * hides behind STEAM_PRIVATE_API -- they still occupy vtable slots, so the
 * bridge has to be able to name them. */
#define STEAM_API_EXPORTS
#else
#define STEAM_API_NODLL
#endif

#include "steam_api.h"
#include "steam_gameserver.h"

#include "bridge_ifaces.h"

/* The SDK packs its structs to 4 bytes on macOS and 8 on Windows, so a struct
 * that crosses the bridge has two different layouts.  Everything travelling
 * through a params struct uses the native (pack-4) layout, and the PE side
 * converts at the edge; STEAM_BRIDGE_U names that layout on both sides. */
#if defined(STEAM_BRIDGE_PE)
#include "pack_convert.h"
#define STEAM_BRIDGE_U( name ) struct u_##name
#else
#define STEAM_BRIDGE_U( name ) name
#endif

/* Wine's unixlib contract.  NTSTATUS is 32 bits on both sides; spelling it
 * `int` rather than `long` matters because macOS `long` is 64 bits. */
typedef int STEAM_BRIDGE_STATUS;
typedef uint64_t steam_bridge_unixlib_handle_t;
typedef STEAM_BRIDGE_STATUS (*steam_bridge_entry_t)( void *args );

#ifndef STEAM_BRIDGE_STATUS_SUCCESS
#define STEAM_BRIDGE_STATUS_SUCCESS ((STEAM_BRIDGE_STATUS)0)
#endif
/* STATUS_NOT_FOUND: the Unix half is present but native Steam is not usable. */
#define STEAM_BRIDGE_STATUS_UNAVAILABLE ((STEAM_BRIDGE_STATUS)0xc0000225)

/* steam_bridge_call_attach */
struct steam_bridge_attach_params
{
    const char *dylib_path;  /* absolute POSIX path to libsteam_api.dylib */
    const char *app_id;      /* decimal AppID, or NULL to leave the env alone */
    unsigned int flags;
    char error[512];         /* filled in on failure, for the launcher log */
};

/* steam_bridge_call_init / _gameserver_init */
struct steam_bridge_init_params
{
    uint32 ip;
    uint16 legacy_steam_port;
    uint16 game_port;
    uint16 query_port;
    int server_mode;
    const char *version_string;
    bool _ret;
};

/* steam_bridge_call_create_interface and the two find-or-create variants */
struct steam_bridge_interface_params
{
    HSteamUser user;
    const char *version;
    void *_ret;
};

struct steam_bridge_handle_params
{
    int32 _ret;
};

struct steam_bridge_bool_params
{
    bool _ret;
};

struct steam_bridge_uint64_params
{
    uint64 _ret;
};

struct steam_bridge_pipe_params
{
    HSteamPipe pipe;
};

/* Manual dispatch.  Callbacks are pumped as plain data rather than through
 * registered C++ objects, which keeps every vtable on the side that owns it. */
/* CallbackMsg_t is itself one of the structs the SDK packs differently per
 * platform, so it is taken apart rather than embedded here. */
struct steam_bridge_dispatch_next_params
{
    HSteamPipe pipe;
    HSteamUser user;
    int callback;
    uint8 *param;
    int param_size;
    bool _ret;
};

struct steam_bridge_dispatch_result_params
{
    HSteamPipe pipe;
    SteamAPICall_t call;
    void *callback;
    int callback_size;
    int callback_expected;
    bool failed;
    bool _ret;
};

struct steam_bridge_warning_hook_params
{
    void *hook;  /* PE SteamAPIWarningMessageHook_t, or NULL to clear */
};

struct steam_bridge_install_path_params
{
    const char *_ret;
};

struct steam_bridge_release_response_params
{
    void *pe_object;
};

#endif /* STEAM_BRIDGE_TYPES_H */
