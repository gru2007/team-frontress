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
//     <body data-replaces="health ammo medic objective killfeed">
//
// and those stock panels stay hidden while it is up (FrontressHudReplaces,
// asked by CHud::Think). Everything else -- target ID, building status,
// the scoreboard, chat -- is the game's as always.
//
// The state it reads is "hud" in schema.json, filled here every frame.
//
//=============================================================================
#ifndef FUI_HUD_H
#define FUI_HUD_H
#ifdef _WIN32
#pragma once
#endif

namespace fui
{
	class Runtime;
}

// The HUD document to use now, relative to resource/fui/.
const char *FuiHudDocument();

// Each frame in a match while the HUD layer is up.
void FuiHudUpdate( fui::Runtime &runtime, bool bShown );

// Whether the stock HUD element of this name is drawn by our HUD instead.
bool FrontressHudReplaces( const char *pszElement );

#endif // FUI_HUD_H
