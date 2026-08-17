//========= GREYLINE FRONTRESS ============================================//
//
// Purpose: shared definitions for Greyline battle hosting.
//
// Transport is not our problem. Since the February 2025 Steam Networking
// change, a plain listen server started with "map" is reachable from outside
// without port forwarding, so Greyline only has to orchestrate: pick a host,
// put a Steam lobby around the battle, and point everyone at the server the
// engine already stood up.
//
//=========================================================================//

#ifndef GREYLINE_SHARED_H
#define GREYLINE_SHARED_H
#ifdef _WIN32
#pragma once
#endif

#include "convar.h"

//-----------------------------------------------------------------------------
// Lobby metadata. These are the keys clients filter on in RequestLobbyList, so
// they are part of the wire contract between builds: adding a key is safe,
// changing the meaning of one is not.
//-----------------------------------------------------------------------------
#define GREYLINE_KEY_GAME		"greyline"		// always "1", so a filter can find only our lobbies
#define GREYLINE_KEY_PROTOCOL	"protocol"
#define GREYLINE_KEY_BUILD		"build"
#define GREYLINE_KEY_FRONT		"front"
#define GREYLINE_KEY_BATTLE		"battle"
#define GREYLINE_KEY_MAP		"map"
#define GREYLINE_KEY_STATE		"state"

#define GREYLINE_STATE_FORMING	"forming"	// lobby exists, the host has not loaded the map yet
#define GREYLINE_STATE_PLAYING	"playing"	// game server is published, joinable

// Bumped whenever the lobby contract changes in a way older clients cannot read.
#define GREYLINE_PROTOCOL_VERSION	"1"

//-----------------------------------------------------------------------------
// The hosting contract.
//
// A Greyline host is matched with strangers, so the two defaults TF2 and
// mastercomfig ship for private local servers are both wrong for us:
//
//   sv_friends_only 1   would reject every player the coordinator picked who
//                       does not happen to be a Steam friend of the host.
//                       The engine refuses them with "STEAM UserID ... is not
//                       friends with the host!" before anything else is tried.
//
//   sv_allow_server_adverisement_to_master_server 0 stays 0 on purpose: the
//                       battle is entered through its lobby, not through the
//                       public server browser.
//
// So: Steam Networking on, friends-only off, browser advertising off.
//-----------------------------------------------------------------------------
struct GreylineConVarSetting_t
{
	const char *pszName;
	const char *pszValue;
	bool		bRequired;	// hosting is refused if this one cannot be applied
};

extern const GreylineConVarSetting_t g_GreylineHostContract[];
extern const int g_nGreylineHostContractCount;

//-----------------------------------------------------------------------------
// Replicated channel from the listen server to its own client.
//
// Only the lobby owner may call SetLobbyGameServer, and the owner is the host
// *player* — client-side code. But the game server's SteamID is only readable
// through IVEngineServer::GetGameServerSteamID(), which is server-side. A
// replicated convar is the cheapest honest way across that boundary; it also
// happens to tell every connected client which server they are on, which is
// useful for reporting results later.
//-----------------------------------------------------------------------------
extern ConVar greyline_server_id;

//-----------------------------------------------------------------------------
// Live battle score, replicated the same way and for the same reason: the
// coordinator link lives on the client, but the score lives on the server.
//
// This is what survives a change of host. Only the round scoreline does —
// mid-round state (cart position, point ownership, the round timer, who is
// alive) is spread across hundreds of entities and cannot be moved without
// engine-level save/restore, which a mod does not have. A migrated battle
// therefore resumes at its score and replays the interrupted round.
//-----------------------------------------------------------------------------
extern ConVar greyline_score_red;
extern ConVar greyline_score_blu;
extern ConVar greyline_rounds_played;

#endif // GREYLINE_SHARED_H
