//========= Team Frontress ====================================================//
//
// The in-match HUD, drawn by Frontress UI (resource/fui/huds/<style>/hud.rml).
//
// HUDs are swappable. fui_hud_style picks a folder under resource/fui/huds/;
// "auto" takes "frontress" unless a classic VGUI custom HUD is installed in
// custom/, in which case "minimal" keeps it on screen and only adds quests and
// notifications. Players write their own the same way (see huds/README.md).
//
// A HUD document says which parts of the game's own HUD it draws itself:
//
//     <body data-replaces="health ammo medic class objective killfeed ...">
//
// and those stock panels stay hidden while it is up (FrontressHudReplaces,
// asked by CHud::Think and by the viewport panels that are not HUD elements:
// the scoreboard, the spectator bars). Parts a HUD leaves out are the game's
// as always.
//
// The state it reads is "hud" in schema.json, filled here every frame
// (fui_hud.cpp: the player, the class, the objective; fui_hud_overlay.cpp:
// the scoreboard, round and match ends, chat, votes, the spectator view,
// who killed you, notices).
//
//=============================================================================
#ifndef FUI_HUD_H
#define FUI_HUD_H
#ifdef _WIN32
#pragma once
#endif

#include "inputsystem/ButtonCode.h"

#include <string>
#include <vector>

namespace fui
{
	class Runtime;
	class Node;
}
class CHudElement;

// The HUD document to use now, relative to resource/fui/.
const char *FuiHudDocument();

// Each frame in a match while the HUD layer is up.
void FuiHudUpdate( fui::Runtime &runtime, bool bShown );

// Whether this stock HUD element is drawn by our HUD instead (CHud::Think).
bool FrontressHudReplaces( CHudElement *pElement );
// Whether the HUD on screen draws this part itself: a data-replaces word
// ("scoreboard", "spectator", "chat"...). False while it is not up.
bool FrontressHudReplaces( const char *pszPart );

//-----------------------------------------------------------------------------
// fui_hud_overlay.cpp: what the stock panels that are not HUD elements tell
// the HUD, and what it needs from the host.
//-----------------------------------------------------------------------------
// The scoreboard was asked for (+showscores) or put away. Returns false when
// the HUD does not draw one, and the stock scoreboard should show.
bool FuiHudShowScores( bool bShow );
bool FuiHudScoresShown();
// A click while it is up turns the cursor on, to pick a player (as TF2's
// tf_scoreboard_mouse_mode 2). True: the click was taken.
bool FuiHudScoresClick( int down, ButtonCode_t keynum );
// The spectator bars were asked for or put away (PANEL_SPECGUI). False when
// the HUD does not draw them.
bool FuiHudShowSpectator( bool bShow );
// Chat: messagemode (MM_SAY, MM_SAY_TEAM, MM_SAY_PARTY). False: the HUD has
// no chat, use the stock one.
bool FuiHudStartChat( int iMode );
// A chat line as CBaseHudChat::ChatPrintf has it, colour codes and all.
void FuiHudChatLine( int iPlayer, const char *pszLine );
// The chat line is being typed / the scoreboard has the cursor: the host
// gives the HUD the keyboard and mouse.
bool FuiHudWantsKeyboard();
bool FuiHudWantsMouse();
// Esc while typing: put the chat away. True if it was open.
bool FuiHudCloseChat();
// A HUD command (cmd(...) in the document); true if it was one of ours.
bool FuiHudCommand( fui::Runtime &runtime, const std::string &name, const std::vector< std::string > &args );

// Called by FuiHudUpdate.
void FuiHudOverlayUpdate( fui::Runtime &runtime, fui::Node &hud );
void FuiHudOverlayReset();

#endif // FUI_HUD_H
