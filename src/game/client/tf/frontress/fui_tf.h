//========= Team Frontress ====================================================//
//
// What the Frontress menu shows, taken from the game: Steam, the party, the
// queue (GC status), the war and the player's record (GC HTTP), the
// backpack. And what its commands do. resource/fui/schema.json lists every
// value; tools/fui-preview does the same job with mock data.
//
//=============================================================================
#ifndef FUI_TF_H
#define FUI_TF_H
#ifdef _WIN32
#pragma once
#endif

#include <string>
#include <vector>

namespace fui
{
	class Runtime;
}

class CFuiTF
{
public:
	void Attach( fui::Runtime &runtime );
	void Detach();
	// Every frame. bShown: the menu is on screen (refresh often); otherwise
	// only what the queue needs.
	void Update( fui::Runtime &runtime, bool bShown );

	void OnCommand( const std::string &name, const std::vector< std::string > &args );
	void OnNavigate( const std::string &screen );

	// Who stands in a 3D slot (see fui_stage.h): "me", "class:<id>",
	// "member:<steamid>". False if nobody.
	bool GetLook( const std::string &key, struct FuiLook_t &out );
	// The coordinator's party looks message (JSON).
	void OnPartyLooks( const char *pszJSON );
	// Show the in-match class select for a team; false if the menu is off.
	bool ShowClassSelect( int iTeam );
	// Show the in-match team select (arena: play or watch); false if it is off.
	bool ShowTeamSelect( bool bArena );
	// A match ended while you played it: its results screen is waiting. True
	// once, for the menu to open it on the way back.
	bool TakeResults();
	// A notification in the corner: kind (quest, party, clan, support,
	// error), title, body, icon (a texture source or "").
	void Toast( const std::string &kind, const std::string &title, const std::string &body, const std::string &icon );

	struct Impl;

private:
	Impl *m_pImpl = nullptr;
};

CFuiTF &FuiTF();

#endif // FUI_TF_H
