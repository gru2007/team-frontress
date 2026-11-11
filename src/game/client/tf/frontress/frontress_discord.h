//========= Copyright Team Frontress, All rights reserved. ====================//
//
// Purpose: Discord Rich Presence: what the player is doing, shown on their
// Discord profile, and joins from it.
//
// What friends see: the main menu, a match search, a match (mode, map, class),
// a community server (map, players), a demo. "Ask to Join" and "Invite to
// play" in Discord lead to the same places Steam's Join Game does: the
// player's party (tf_party_request_join_user) or their community server
// (connect). A match itself is never joined directly.
//
// No Discord library is loaded: the game talks to the Discord app over its
// local pipe (frontress_discord_ipc.h), so nothing new runs inside the
// process Easy Anti-Cheat protects. When Discord has to start the game for a
// join, it goes through Steam or the anti-cheat bootstrapper, never straight
// to the game executable (see FrontressDiscord_RegisterLaunch).
//
// Off with fr_discord 0 or -nodiscord; joins off with fr_discord_invites 0.
// fr_discord_status shows what it is doing. docs/discord.md has the setup of
// the Discord application.
//
//=============================================================================//

#ifndef FRONTRESS_DISCORD_H
#define FRONTRESS_DISCORD_H
#ifdef _WIN32
#pragma once
#endif

#include "steam/steamclientpublic.h"

// For the party client (tf_partyclient.cpp). Parties are for Steam friends;
// these are the two ways in for someone a player knows only on Discord.
//
// Joining side: a party whose member this player was just let in by through
// Discord (they accepted "Ask to Join", or sent an invite that this player
// accepted). Asking to join them is allowed although they are no friend.
bool FrontressDiscord_BApprovedJoinTarget( const CSteamID &steamID );
// Host side: this player offers joins to their party on Discord right now, so
// a join request from someone who is not a Steam friend is shown to them to
// accept or decline, instead of being turned down unseen. Never accepted on
// its own.
bool FrontressDiscord_BTakesJoinRequestsFromStrangers();

#endif // FRONTRESS_DISCORD_H
