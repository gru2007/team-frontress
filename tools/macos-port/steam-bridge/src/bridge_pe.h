/* PE-side declarations for the Steamworks bridge. */

#ifndef STEAM_BRIDGE_PE_H
#define STEAM_BRIDGE_PE_H

#include <windows.h>

#include "bridge_types.h"
#include "bridge_calls.h"
#include "pack_convert.h"

#define STEAM_BRIDGE_EXPORT extern "C" __declspec(dllexport)

extern steam_bridge_unixlib_handle_t steam_bridge_unix_handle;

STEAM_BRIDGE_STATUS steam_bridge_call( unsigned int code, void *args );
#define STEAM_BRIDGE_CALL( code, args ) steam_bridge_call( (code), (args) )

/* One row per interface, in steam_bridge_iface_id order.  vtable is the
 * Microsoft-ABI vtable the game will dispatch through. */
struct steam_bridge_iface_info
{
    const char *classname;
    const char *version;
    void *const *vtable;
    unsigned int method_count;
};

extern const struct steam_bridge_iface_info steam_bridge_ifaces[steam_bridge_iface_count];

/* A proxy is what the game actually holds.  The vtable pointer has to be the
 * first member: that is the whole ABI contract of a C++ object. */
struct steam_bridge_proxy
{
    void *const *vtable;
    void *native;
    int iface;
};

/* Path translation at the boundary.  Native Steam speaks POSIX paths and the
 * Windows process cannot open one; see path_convert.cpp.
 *
 * steam_bridge_path_out converts an out-buffer in place and returns the new
 * length including the NUL, or 0 when nothing was converted -- which is what a
 * method returning a length has to report.  steam_bridge_path_in converts the
 * other way and returns a pointer valid until the next call on this thread. */
unsigned int steam_bridge_path_out( char *buffer, unsigned int size );
const char *steam_bridge_path_in( const char *path );

void *steam_bridge_native( void *proxy );
void *steam_bridge_wrap( enum steam_bridge_iface_id iface, void *native );
void *steam_bridge_find_user_interface( const char *version );
void *steam_bridge_find_gameserver_interface( const char *version );
void steam_bridge_log( const char *format, ... );

/* Occupies the vtable slot of a method the bridge deliberately does not
 * forward.  Returning zero is the only safe answer for every return type the
 * SDK uses in those slots. */
extern "C" uint64 __cdecl steam_bridge_unbridged( void );

#endif /* STEAM_BRIDGE_PE_H */
