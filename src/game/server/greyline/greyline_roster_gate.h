//========= GREYLINE FRONTRESS ============================================//
//
// Purpose: the two questions the stock TF code has to ask the war before it
// lets somebody play: may this person be here at all, and which team did the
// war put them on.
//
// It is a header of three free functions rather than an interface because the
// call sites are inside Valve's own files — tf_gamerules.cpp's ClientConnected
// and tf_player.cpp's HandleCommand_JoinTeam — and the smaller the footprint
// there is, the less there is to re-apply the next time those files move.
// Everything they answer with comes out of the roster in
// greyline_briefing.cpp, which the coordinator fills in over the console
// before the map loads.
//
// On a server that is not running a coordinator battle every one of these is
// a no-op: no roster means no opinion, and the game behaves exactly as it
// always did.
//
//=========================================================================//

#ifndef GREYLINE_ROSTER_GATE_H
#define GREYLINE_ROSTER_GATE_H
#ifdef _WIN32
#pragma once
#endif

class CBasePlayer;
class CCommand;

namespace greyline
{

// One console command a player in a battle can type: "greyline_briefing", to
// be told again what this battle is, which side the war put them on and what
// it is still waiting for. Returns true when it handled the command.
//
// It lives here, behind CTFPlayer::ClientCommand's one-line call, for the same
// reason the rest of this header does — the alternative is a second edit
// inside Valve's dispatch chain. A briefing that is printed once, at the exact
// moment a player is staring at a loading screen, is a briefing most players
// never read; there has to be a way to ask again.
bool HandleClientCommand( CBasePlayer *pPlayer, const CCommand &args );

// True when this server is running a battle the coordinator gave it a roster
// for. Everything below is inert when it is false.
bool BattleHasRoster();

// Whether this identified account is in the active coordinator roster.
bool IsRosterMember( unsigned long long ulSteamID );

// Whether this account may join the battle this server is running.
//
// ppszReason is filled in with what to tell them when the answer is no. A
// SteamID of zero — Steam has not identified the client yet — is admitted:
// refusing somebody the server cannot identify would lock out an offline or
// LAN client entirely, and the roster still decides their team once they are
// in.
bool MayJoinBattle( unsigned long long ulSteamID, const char **ppszReason );

// The in-game team the war put this player on, or TEAM_UNASSIGNED when this
// player is not on the roster or there is no battle.
int RosterTeamFor( CBasePlayer *pPlayer );

} // namespace greyline

#endif // GREYLINE_ROSTER_GATE_H
