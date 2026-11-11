//========= Copyright Team Frontress, All rights reserved. ====================//
//
// Purpose: The local connection to the Discord app (its RPC pipe).
//
// The Discord client listens on a named pipe (Windows, \\.\pipe\discord-ipc-N)
// or a Unix socket (<temp dir>/discord-ipc-N). A message is an 8-byte header
// (opcode, length; both little-endian uint32) and that many bytes of JSON.
// This is all the Discord RPC library ever did; speaking it here keeps any
// Discord code out of the game process, which Easy Anti-Cheat protects.
//
// Nothing here blocks: Open() returns at once whether Discord is running or
// not, Read() returns what has arrived. No engine headers, so it builds and
// tests on its own.
//
//=============================================================================//

#ifndef FRONTRESS_DISCORD_IPC_H
#define FRONTRESS_DISCORD_IPC_H
#ifdef _WIN32
#pragma once
#endif

#include <stdint.h>
#include <string>

class CFrontressDiscordPipe
{
public:
	enum EOpcode
	{
		k_eOp_Handshake = 0,
		k_eOp_Frame = 1,
		k_eOp_Close = 2,
		k_eOp_Ping = 3,
		k_eOp_Pong = 4,
	};

	CFrontressDiscordPipe();
	~CFrontressDiscordPipe();

	// Connects to the first Discord instance that answers. False if none does.
	bool Open();
	// Disconnects and drops anything not yet read.
	void Close();
	bool BIsOpen() const;

	// False when the connection is lost.
	bool Write( uint32_t unOpcode, const std::string &payload );
	// One whole message if one has arrived, false otherwise. Once the
	// connection is lost BIsOpen() is false, but what arrived before can still
	// be read.
	bool Read( uint32_t &unOpcode, std::string &payload );

	static int CurrentProcessId();

private:
	bool Fill();
	void Drop();

#ifdef _WIN32
	void *m_hPipe;
#else
	int m_fd;
#endif
	std::string m_in;
};

// Tell Discord how to start the game when a friend joins while it is not
// running (Discord reads this per application: a URL handler on Windows and
// Linux, a file on macOS). With a Steam app ID, Steam starts it (its launch
// options run the anti-cheat bootstrapper); otherwise the Easy Anti-Cheat
// bootstrapper next to the executable, or the executable when there is none
// (a development build). Writes only when something changed;
// what gets a short description of what was registered, for fr_discord_status.
bool FrontressDiscord_RegisterLaunch( const char *pszApplicationId, unsigned int unSteamAppId, std::string &what );

#endif // FRONTRESS_DISCORD_IPC_H
