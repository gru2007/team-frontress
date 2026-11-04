//========= Team Frontress ====================================================//
//
// Frontress UI in the game: the panel that draws resource/fui over the main
// menu, and the system that keeps its state in step with the game.
//
// The runtime itself is src/frontress_ui (shared with tools/fui-preview);
// this file only supplies what the game has: files, time, a renderer on the
// material system, input from VGUI. fui_tf.cpp fills the state.
//
//=============================================================================
#ifndef FUI_HOST_H
#define FUI_HOST_H
#ifdef _WIN32
#pragma once
#endif

namespace fui
{
	class Runtime;
}

// The running UI, or NULL when it is off (fui_menu 0) or failed to start.
fui::Runtime *FrontressUI();

// True while the Frontress menu is the one on screen.
bool FrontressUIOwnsMenu();

// Open or close the Frontress class select (in a match). Opening returns
// false when it is turned off, and the stock menu should show instead.
bool FrontressUISetClassSelect( bool bOpen );

// A classic window is about to open from the UI: let it stay on top.
void FrontressUIYield();

#endif // FUI_HOST_H
