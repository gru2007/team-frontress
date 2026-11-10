//========= Team Frontress ====================================================//
//
// Frontress UI: menus written as RML/RCSS documents (HTML/CSS-like, rendered
// by RmlUi), fed from one state tree and answered by commands.
//
//   resource/fui/
//     schema.json      every state value the documents may read, with defaults
//     fonts.json       font files to load
//     lang/*.json      strings, looked up as #token or with  | t
//     shell.rml        always shown: top bar, party, play button, overlays
//     screens/*.rml    one per screen; ui.screen picks which is shown
//     style/*.rcss
//
// The host is the game (src/game/client/tf/frontress/fui_host.cpp) or the
// previewer (tools/fui-preview). It supplies files, time, a draw backend, and
// owns everything the commands do. Documents never reach into the game.
//
// Documents talk back with these data-event callbacks:
//   nav('locker')                  show a screen (sets ui.screen)
//   set('ui.class', 'soldier')     write a state value
//   toggle('ui.party_open')
//   cmd('queue', 'casual', ...)    ask the host to do something
//
//=============================================================================
#pragma once

#include "fui_node.h"
#include "fui_render.h"

#include <cstdint>
#include <memory>
#include <string>
#include <vector>

namespace Rml {
class Context;
class ElementDocument;
}

namespace fui {

class Host {
public:
	virtual ~Host() {}

	virtual double Time() = 0;
	// level: 0 error, 1 warning, 2 info, 3 debug
	virtual void Log( int level, const char *pszMessage ) = 0;
	virtual bool ReadFile( const std::string &path, std::string &out ) = 0;
	// Modification time, or 0 if unknown. Used for hot reload.
	virtual int64_t FileTime( const std::string &path ) = 0;
	virtual void SetCursor( const std::string &name ) {}
	// A UI sound: "hover", "click", or whatever an element's sfx attribute
	// names ("play", "equip"...).
	virtual void PlaySound( const std::string &name ) {}
	virtual std::string Language() = 0;		// "english", "russian", ...

	virtual void OnCommand( const std::string &name, const std::vector< std::string > &args ) = 0;
	// The screen changed (after ui.screen is written).
	virtual void OnNavigate( const std::string &screen ) {}
};

class Runtime {
public:
	Runtime();
	~Runtime();

	bool Init( Host *pHost, DrawBackend *pBackend, int width, int height );
	void Shutdown();
	bool IsInitialized() const { return m_pContext != nullptr; }

	void SetViewport( int width, int height );
	void Update();
	void Render();

	Rml::Context *Context() { return m_pContext; }

	// The state tree. Change it with Set (it marks the right part dirty).
	const Node &State() const { return m_State; }
	Node *Get( const std::string &path );
	// Returns true if the value changed.
	bool Set( const std::string &path, Node &&value );
	bool Set( const std::string &path, const char *pszValue ) { return Set( path, Node( pszValue ) ); }
	// Merge an object into the state, key by key (used for mock data).
	void Merge( const Node &values );

	void Navigate( const std::string &screen );
	const std::string &Screen() const { return m_Screen; }

	// The HUD layer (hud.rml) instead of the shell and screens: what shows
	// over the game while playing.
	void SetHud( bool bHud );
	bool IsHud() const { return m_bHud; }
	// Which HUD: a document under resource/fui/, "huds/frontress/hud.rml" by
	// default. Players pick theirs (see resource/fui/huds/README.md).
	void SetHudDocument( const std::string &path );
	const std::string &HudDocument() const { return m_HudPath; }
	// An attribute of the HUD document's <body>, e.g. "data-replaces": the
	// game's own HUD parts this HUD draws itself. Empty when none is loaded.
	std::string HudAttribute( const char *pszName ) const;
	// Give the keyboard to the HUD element with this id (the chat line).
	// False while the HUD is not up or has no such element yet.
	bool FocusHud( const std::string &id );

	// Re-read every document, stylesheet and string table.
	void Reload();
	// Checked by Update() twice a second when enabled.
	void SetHotReload( bool bEnable ) { m_bHotReload = bEnable; }
	void ShowDebugger( bool bShow );

	std::string Translate( const std::string &token ) const;
	bool Lookup( const std::string &token, std::string &out ) const;

	// Called by the Rml interfaces; not for hosts.
	struct Impl;

private:
	void LoadDocuments();
	void CloseDocuments();
	void LoadStrings();
	void LoadFonts();
	void ApplyScreen();
	void BindModel();
	void CheckFiles();
	void HandleEvent( const std::string &name, const std::vector< std::string > &args );

	Host *m_pHost = nullptr;
	std::unique_ptr< Impl > m_pImpl;
	Rml::Context *m_pContext = nullptr;
	Node m_State;
	std::vector< std::string > m_TopLevel;
	std::string m_Screen;
	Rml::ElementDocument *m_pShell = nullptr;
	Rml::ElementDocument *m_pHud = nullptr;
	std::string m_HudPath = "huds/frontress/hud.rml";
	bool m_bHud = false;
	std::vector< std::pair< std::string, Rml::ElementDocument * > > m_Screens;
	bool m_bHotReload = false;
	double m_flNextFileCheck = 0.0;
	int m_nWidth = 0, m_nHeight = 0;
};

} // namespace fui
