/* Unix-side declarations for the Steamworks bridge. */

#ifndef STEAM_BRIDGE_UNIX_H
#define STEAM_BRIDGE_UNIX_H

#include "bridge_types.h"
#include "bridge_calls.h"

/* The two calling conventions in play.  Anything reached through a pointer the
 * PE side handed us is Microsoft ABI and has to be declared as such; the rest
 * of this half is ordinary System V. */
#define STEAM_BRIDGE_MS_ABI __attribute__((ms_abi))

typedef STEAM_BRIDGE_STATUS NTSTATUS;
typedef steam_bridge_entry_t unixlib_entry_t;
#define STATUS_SUCCESS STEAM_BRIDGE_STATUS_SUCCESS

/* Trampolines for callbacks the game owns.  Each takes the PE-side pointer and
 * returns something the native library can call the normal way. */
SteamAPIWarningMessageHook_t
    steam_bridge_wrap_SteamAPIWarningMessageHook_t( SteamAPIWarningMessageHook_t hook );
FnSteamNetConnectionStatusChanged
    steam_bridge_wrap_FnSteamNetConnectionStatusChanged( FnSteamNetConnectionStatusChanged fn );
FnSteamNetAuthenticationStatusChanged
    steam_bridge_wrap_FnSteamNetAuthenticationStatusChanged( FnSteamNetAuthenticationStatusChanged fn );
FnSteamRelayNetworkStatusChanged
    steam_bridge_wrap_FnSteamRelayNetworkStatusChanged( FnSteamRelayNetworkStatusChanged fn );
FnSteamNetworkingMessagesSessionRequest
    steam_bridge_wrap_FnSteamNetworkingMessagesSessionRequest( FnSteamNetworkingMessagesSessionRequest fn );
FnSteamNetworkingMessagesSessionFailed
    steam_bridge_wrap_FnSteamNetworkingMessagesSessionFailed( FnSteamNetworkingMessagesSessionFailed fn );
FnSteamNetworkingFakeIPResult
    steam_bridge_wrap_FnSteamNetworkingFakeIPResult( FnSteamNetworkingFakeIPResult fn );

/* Server-browser response objects, likewise owned by the game. */
ISteamMatchmakingServerListResponse *
    steam_bridge_wrap_ISteamMatchmakingServerListResponse( ISteamMatchmakingServerListResponse *pe );
ISteamMatchmakingPingResponse *
    steam_bridge_wrap_ISteamMatchmakingPingResponse( ISteamMatchmakingPingResponse *pe );
ISteamMatchmakingPlayersResponse *
    steam_bridge_wrap_ISteamMatchmakingPlayersResponse( ISteamMatchmakingPlayersResponse *pe );
ISteamMatchmakingRulesResponse *
    steam_bridge_wrap_ISteamMatchmakingRulesResponse( ISteamMatchmakingRulesResponse *pe );

void steam_bridge_unix_log( const char *format, ... );

#endif /* STEAM_BRIDGE_UNIX_H */
