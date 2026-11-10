//========= Team Frontress ====================================================//
//
// In-match windows of the Frontress menu: calling a vote and muting players
// (the Esc menu's), and the class menus the HUD draws (an engineer's build and
// destroy menus, a spy's disguise menu).
//
// Calling a vote: the server still says what can be voted on (VoteSetup, as
// for the stock dialog); the window is "vote" in shell.rml. The class menus
// keep their stock logic and keys (tf_hud_menu_*.cpp) and stop painting while
// the HUD on screen names "buildmenu" in data-replaces; the HUD reads them.
//
//=============================================================================
#ifndef FUI_INGAME_H
#define FUI_INGAME_H
#ifdef _WIN32
#pragma once
#endif

#include <string>
#include <vector>

namespace fui
{
	class Runtime;
	class Node;
}

// The Esc menu's in-match commands: open_vote, call_vote, open_mute,
// mute_toggle, mute_all_comms. True if it was one of these.
bool FuiIngameCommand( fui::Runtime &ui, const std::string &name, const std::vector< std::string > &args );

// The class menu up now, for the HUD: hud.menu.
void FuiWriteClassMenu( fui::Node &out );

#endif // FUI_INGAME_H
