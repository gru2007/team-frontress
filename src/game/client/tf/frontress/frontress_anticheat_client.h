//========= Copyright Team Frontress, All rights reserved. ====================//
//
// Purpose: The client half of Easy Anti-Cheat (EOS Anti-Cheat, client-server
//			mode).
//
// The anti-cheat itself is not here: the bootstrapper (start_protected_game)
// loads it into the process before the game starts. This module logs the
// player into EOS with their Steam account, opens an anti-cheat session when
// a protected server says hello, carries EAC's messages to and from that
// server, shows the player any integrity violation EAC reports, and files
// player reports with EOS so the anti-cheat team sees them.
//
// See frontress_anticheat_shared.h for the protocol.
//
//=============================================================================//

#ifndef FRONTRESS_ANTICHEAT_CLIENT_H
#define FRONTRESS_ANTICHEAT_CLIENT_H
#ifdef _WIN32
#pragma once
#endif

#include "steam/steamclientpublic.h"
#include "utlstring.h"

class bf_read;

// The user message handler (registered with USER_MESSAGE_REGISTER).
void FrontressAC_Client_OnServerMessage( bf_read &msg );

// A player report made in game: also sent to EOS Player Reports, where
// reports are what anti-cheat detections and sanctions are reviewed against.
// nReason is a CMsgGC_ReportPlayer::EReason, unDetail its EDetail bits; the
// comment, map and server go along as the report's message and context.
void FrontressAC_Client_ReportPlayer( const CSteamID &target, int nReason, uint32 unDetail,
	const char *pszComment, const char *pszMap, const char *pszServer );
// Would a report reach EOS now (this player is logged in to EOS)?
bool FrontressAC_Client_CanReport();

// For menus and diagnostics.
bool FrontressAC_Client_BBuiltWithAntiCheat();
bool FrontressAC_Client_BSessionActive();
// Whether the anti-cheat module is in this process (for fr_eac_status).
CUtlString FrontressAC_Client_ModuleStatus();
const char *FrontressAC_Client_LastViolation();
// This player's EOS Product User ID once logged in, else "".
CUtlString FrontressAC_Client_ProductUserId();

#endif // FRONTRESS_ANTICHEAT_CLIENT_H
