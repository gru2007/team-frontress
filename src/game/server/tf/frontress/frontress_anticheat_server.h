//========= Copyright Team Frontress, All rights reserved. ====================//
//
// Purpose: The server half of Easy Anti-Cheat (EOS Anti-Cheat, client-server
//			mode). It replaces VAC as what keeps cheaters off our servers.
//
// What it does on a protected server:
//   - opens an anti-cheat session for every map;
//   - checks each player's EOS identity against their Steam account and
//     registers them with EAC, which then authenticates the anti-cheat on
//     their machine and keeps watching it for the whole match;
//   - holds players out of a team until EAC has authenticated them;
//   - removes whoever EAC says must go (no anti-cheat, a violation, a ban),
//     and tells the game coordinator, which keeps sanctioned players out of
//     matchmaking;
//   - feeds EAC gameplay data -- movement, aim, shots, hits -- that Epic's
//     server-side detection works from, independent of the client.
//
// A dedicated server needs its own EOS credentials (the TrustedServer client
// policy) in FRONTRESS_EOS_SERVER_CLIENT_ID / FRONTRESS_EOS_SERVER_CLIENT_SECRET.
// Without them it runs unprotected, says so in the log, and reports itself
// as not secure to the GC. A listen server protects itself with the host's
// own EOS login.
//
// See frontress_anticheat_shared.h for the protocol.
//
//=============================================================================//

#ifndef FRONTRESS_ANTICHEAT_SERVER_H
#define FRONTRESS_ANTICHEAT_SERVER_H
#ifdef _WIN32
#pragma once
#endif

class CBasePlayer;
class CTFPlayer;
class CTakeDamageInfo;
class KeyValues;
struct edict_t;

// Player lifecycle, from CServerGameClients.
void FrontressAC_Server_ClientActive( CBasePlayer *pPlayer );
void FrontressAC_Server_ClientDisconnect( CBasePlayer *pPlayer );

// A KeyValues command from a client. True if it was ours (handled or
// rejected); the caller must not pass it on.
bool FrontressAC_Server_HandleKeyValues( edict_t *pEdict, KeyValues *pKeyValues );

// May this player join a team yet? False until EAC has authenticated them
// on a server that enforces anti-cheat.
bool FrontressAC_Server_CanPlay( CBasePlayer *pPlayer );

// Is this server running a protected session right now? This is what the GC
// is told in place of VAC's "secure".
bool FrontressAC_Server_IsProtected();

// Gameplay data for EAC's server-side detection, from CTFGameStats.
void FrontressAC_Server_OnRoundStart();
void FrontressAC_Server_OnRoundEnd( int iWinningTeam );
void FrontressAC_Server_OnPlayerSpawned( CTFPlayer *pPlayer );
void FrontressAC_Server_OnPlayerKilled( CBasePlayer *pPlayer );
void FrontressAC_Server_OnWeaponFired( CTFPlayer *pPlayer );
void FrontressAC_Server_OnPlayerDamaged( CBasePlayer *pVictim, const CTakeDamageInfo &info, int iDamageTaken );

#endif // FRONTRESS_ANTICHEAT_SERVER_H
