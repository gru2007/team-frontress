//========= GREYLINE FRONTRESS ============================================//
//
// Purpose: shared definitions for Greyline battle hosting.
//
//=========================================================================//

#include "cbase.h"
#include "greyline_shared.h"

// memdbgon must be the last include file in a .cpp file!!!
#include "tier0/memdbgon.h"

ConVar greyline_server_id( "greyline_server_id", "0",
	FCVAR_REPLICATED | FCVAR_HIDDEN,
	"SteamID of the game server hosting this battle. Set by the server, read by the host's own client to publish the lobby's game server." );

ConVar greyline_score_red( "greyline_score_red", "0",
	FCVAR_REPLICATED | FCVAR_HIDDEN,
	"RED's round score in the running battle. Written by the server, read by the host's client for the coordinator heartbeat." );

ConVar greyline_score_blu( "greyline_score_blu", "0",
	FCVAR_REPLICATED | FCVAR_HIDDEN,
	"BLU's round score in the running battle." );

ConVar greyline_rounds_played( "greyline_rounds_played", "0",
	FCVAR_REPLICATED | FCVAR_HIDDEN,
	"Rounds completed in the running battle." );

//-----------------------------------------------------------------------------
// See the comment on the contract in greyline_shared.h. Every entry here is an
// engine convar, reached through ICvar::FindVar rather than ConVarRef, because
// they do not exist in the game DLLs.
//-----------------------------------------------------------------------------
const GreylineConVarSetting_t g_GreylineHostContract[] =
{
	// Without this the battle is only reachable by whatever the host's router
	// happens to allow. This is the whole reason Greyline can use player hosts.
	{ "sv_use_steam_networking",						"1", true },

	// Matched strangers are not friends. Leaving this at the TF2/mastercomfig
	// default of 1 rejects them at the door.
	{ "sv_friends_only",								"0", true },

	// A battle is entered through its lobby. Nothing about it belongs in the
	// public server browser.
	{ "sv_allow_server_adverisement_to_master_server",	"0", false },

	// Admission is decided by the coordinator, not by a shared secret.
	{ "sv_password",									"",  false },

	// A player-run host on a LAN-mode server would skip Steam auth for joiners.
	{ "sv_lan",											"0", true },
};

const int g_nGreylineHostContractCount = ARRAYSIZE( g_GreylineHostContract );
