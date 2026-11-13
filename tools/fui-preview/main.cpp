//========= Team Frontress ====================================================//
//
// fui-preview: the game's menus outside the game.
//
// Renders resource/fui with the same runtime, renderer and font engine the
// game uses, on OpenGL, with TF2's own art and fonts read from an installed
// Team Fortress 2. Edit an .rml/.rcss/.json file and the window reloads.
//
//   fui-preview [--root <repo>] [--tf2 <dir>] [--lang russian] [--mock <file>]
//               [--screen home] [--size 1920x1080] [--set path=value ...]
//               [--shot out.png [--frames N]] [--debug] [--hud] [--hud-style name]
//
// Keys: F5 reload, F8 debugger, 1-6 screens, Esc quit.
//
//=============================================================================
#define GL_SILENCE_DEPRECATION
#include <SDL3/SDL.h>
#ifdef __APPLE__
#include <OpenGL/gl.h>
#include <OpenGL/glext.h>
#else
#include <GL/gl.h>
#include <GL/glext.h>
#endif

#include "fui.h"
#include "tf2content.h"

#include <RmlUi/Core.h>

#include <sys/stat.h>
#include <cmath>
#include <cstdio>
#include <cstring>
#include <fstream>
#include <map>
#include <sstream>

#define STB_IMAGE_WRITE_IMPLEMENTATION
#include "stb_image_write.h"

namespace {

std::string g_Root;
TF2Content g_Content;

bool ReadDisk( const std::string &path, std::string &out )
{
	std::ifstream f( path, std::ios::binary );
	if ( !f )
		return false;
	std::ostringstream ss;
	ss << f.rdbuf();
	out = ss.str();
	return true;
}

//-----------------------------------------------------------------------------
class GLBackend final : public fui::DrawBackend {
public:
	struct Tex {
		GLuint gl;
	};
	std::map< uintptr_t, Tex > textures;
	uintptr_t next = 1;
	GLuint white = 0;
	int height = 0;

	void Init()
	{
		const uint8_t px[ 4 ] = { 255, 255, 255, 255 };
		white = Upload( px, 1, 1, false );
	}

	GLuint Upload( const uint8_t *rgba, int w, int h, bool repeat )
	{
		GLuint gl;
		glGenTextures( 1, &gl );
		glBindTexture( GL_TEXTURE_2D, gl );
		glTexParameteri( GL_TEXTURE_2D, GL_TEXTURE_MIN_FILTER, GL_LINEAR_MIPMAP_LINEAR );
		glTexParameteri( GL_TEXTURE_2D, GL_TEXTURE_MAG_FILTER, GL_LINEAR );
		glTexParameteri( GL_TEXTURE_2D, GL_GENERATE_MIPMAP, GL_TRUE );
		glTexParameteri( GL_TEXTURE_2D, GL_TEXTURE_WRAP_S, repeat ? GL_REPEAT : GL_CLAMP_TO_EDGE );
		glTexParameteri( GL_TEXTURE_2D, GL_TEXTURE_WRAP_T, repeat ? GL_REPEAT : GL_CLAMP_TO_EDGE );
		glTexImage2D( GL_TEXTURE_2D, 0, GL_RGBA8, w, h, 0, GL_RGBA, GL_UNSIGNED_BYTE, rgba );
		return gl;
	}

	uintptr_t CreateTexture( const uint8_t *pRGBA, int w, int h, bool bRepeat ) override
	{
		const uintptr_t id = next++;
		textures[ id ] = { Upload( pRGBA, w, h, bRepeat ) };
		return id;
	}

	// Finds an icon in scripts/mod_textures.txt: "name" { "dfile" ".." "x" .. }.
	static bool HudIcon( const std::string &name, const std::string &key, std::string &file, int &x, int &y, int &w, int &h )
	{
		static std::string s_Text;
		if ( s_Text.empty() && !g_Content.ReadFile( "scripts/mod_textures.txt", s_Text ) )
			return false;
		const size_t at = s_Text.find( "\"" + name + "\"" );
		if ( at == std::string::npos )
			return false;
		const size_t open = s_Text.find( '{', at ), close = s_Text.find( '}', at );
		if ( open == std::string::npos || close == std::string::npos )
			return false;
		const std::string block = s_Text.substr( open, close - open );
		auto value = [ & ]( const std::string &k ) -> std::string {
			const size_t p = block.find( "\"" + k + "\"" );
			if ( p == std::string::npos )
				return std::string();
			const size_t a = block.find( '"', p + k.size() + 2 );
			const size_t b = a == std::string::npos ? a : block.find( '"', a + 1 );
			return b == std::string::npos ? std::string() : block.substr( a + 1, b - a - 1 );
		};
		file = value( key );
		x = atoi( value( "x" ).c_str() );
		y = atoi( value( "y" ).c_str() );
		w = atoi( value( "width" ).c_str() );
		h = atoi( value( "height" ).c_str() );
		while ( file.compare( 0, 3, "../" ) == 0 )
			file = file.substr( 3 );
		return !file.empty() && w > 0 && h > 0;
	}

	uintptr_t LoadNamedTexture( const std::string &source, int &w, int &h ) override
	{
		std::vector< uint8_t > rgba;
		if ( source.compare( 0, 4, "tex:" ) == 0 )
		{
			if ( !g_Content.LoadVTF( source.substr( 4 ), rgba, w, h ) )
			{
				fprintf( stderr, "[fui] texture not found: %s\n", source.c_str() );
				return 0;
			}
			return CreateTexture( rgba.data(), w, h, false );
		}
		if ( source.compare( 0, 8, "hudicon:" ) == 0 )
		{
			// d_<weapon> / dneg_<weapon>: a rectangle of a sheet named in
			// scripts/mod_textures.txt, as the game's CHudTexture has it.
			std::string name = source.substr( 8 ), key = "dfile";
			if ( name.compare( 0, 5, "dneg_" ) == 0 ) { name = name.substr( 5 ); key = "dnegfile"; }
			else if ( name.compare( 0, 2, "d_" ) == 0 ) name = name.substr( 2 );
			std::string file;
			int x = 0, y = 0, iw = 0, ih = 0;
			if ( !HudIcon( name, key, file, x, y, iw, ih ) )
				return 0;
			std::vector< uint8_t > sheet;
			int sw = 0, sh = 0;
			for ( char &c : file )
				c = (char)tolower( (unsigned char)c );
			if ( !g_Content.LoadVTF( file, sheet, sw, sh ) || x + iw > sw || y + ih > sh )
				return 0;
			rgba.resize( iw * ih * 4 );
			for ( int row = 0; row < ih; ++row )
				memcpy( &rgba[ row * iw * 4 ], &sheet[ ( ( y + row ) * sw + x ) * 4 ], iw * 4 );
			w = iw;
			h = ih;
			return CreateTexture( rgba.data(), w, h, false );
		}
		if ( source.compare( 0, 7, "avatar:" ) == 0 )
		{
			// A stand-in: Steam avatars only exist in the game.
			unsigned hash = 2166136261u;
			for ( char c : source )
				hash = ( hash ^ (unsigned char)c ) * 16777619u;
			w = h = 64;
			rgba.resize( 64 * 64 * 4 );
			const uint8_t r = 80 + ( hash & 127 ), g = 70 + ( ( hash >> 8 ) & 100 ), b = 60 + ( ( hash >> 16 ) & 120 );
			for ( int y = 0; y < 64; ++y )
				for ( int x = 0; x < 64; ++x )
				{
					uint8_t *p = &rgba[ ( y * 64 + x ) * 4 ];
					const float d = std::sqrt( ( x - 32.f ) * ( x - 32.f ) + ( y - 24.f ) * ( y - 24.f ) );
					const bool head = d < 13.f;
					const bool body = y > 40 && std::fabs( x - 32.f ) < 24.f - ( 64 - y ) * 0.3f;
					const float k = ( head || body ) ? 1.35f : 1.f;
					p[ 0 ] = (uint8_t)std::min( 255.f, r * k );
					p[ 1 ] = (uint8_t)std::min( 255.f, g * k );
					p[ 2 ] = (uint8_t)std::min( 255.f, b * k );
					p[ 3 ] = 255;
				}
			return CreateTexture( rgba.data(), w, h, false );
		}
		return 0;
	}

	void ReleaseTexture( uintptr_t texture ) override
	{
		auto it = textures.find( texture );
		if ( it == textures.end() )
			return;
		glDeleteTextures( 1, &it->second.gl );
		textures.erase( it );
	}

	void DrawTriangles( const fui::DrawVertex *pVerts, int, const int *pIndices, int nIndices, uintptr_t texture ) override
	{
		GLuint gl = white;
		if ( texture )
		{
			auto it = textures.find( texture );
			if ( it == textures.end() )
				return;
			gl = it->second.gl;
		}
		glBindTexture( GL_TEXTURE_2D, gl );
		glVertexPointer( 2, GL_FLOAT, sizeof( fui::DrawVertex ), &pVerts[ 0 ].x );
		glTexCoordPointer( 2, GL_FLOAT, sizeof( fui::DrawVertex ), &pVerts[ 0 ].u );
		glColorPointer( 4, GL_UNSIGNED_BYTE, sizeof( fui::DrawVertex ), &pVerts[ 0 ].r );
		glDrawElements( GL_TRIANGLES, nIndices, GL_UNSIGNED_INT, pIndices );
	}

	void SetScissor( bool bEnable, int x, int y, int w, int h ) override
	{
		if ( !bEnable )
		{
			glDisable( GL_SCISSOR_TEST );
			return;
		}
		glEnable( GL_SCISSOR_TEST );
		glScissor( x, height - ( y + h ), std::max( w, 0 ), std::max( h, 0 ) );
	}
};

//-----------------------------------------------------------------------------
class PreviewHost final : public fui::Host {
public:
	fui::Runtime *runtime = nullptr;
	std::string language = "russian";
	double queueStart = -1.0;

	double Time() override { return SDL_GetTicks() / 1000.0; }

	void Log( int level, const char *pszMessage ) override
	{
		static const char *s_Levels[] = { "error", "warning", "info", "debug" };
		if ( level <= 2 )
			fprintf( stderr, "[fui %s] %s\n", s_Levels[ level ], pszMessage );
	}

	bool ReadFile( const std::string &path, std::string &out ) override
	{
		for ( const char *pszBase : { "/game/tc2/loose/", "/game_src/tc2/pak1/" } )
		{
			if ( ReadDisk( g_Root + pszBase + path, out ) )
				return true;
		}
		return g_Content.ReadFile( path, out );
	}

	int64_t FileTime( const std::string &path ) override
	{
		struct stat st;
		for ( const char *pszBase : { "/game/tc2/loose/", "/game_src/tc2/pak1/" } )
		{
			if ( stat( ( g_Root + pszBase + path ).c_str(), &st ) == 0 )
				return (int64_t)st.st_mtime;
		}
		return 0;
	}

	std::string Language() override { return language; }

	// Enough of the game's behaviour to click through the screens.
	void OnCommand( const std::string &name, const std::vector< std::string > &args ) override
	{
		fprintf( stderr, "[fui] cmd %s", name.c_str() );
		for ( const auto &arg : args )
			fprintf( stderr, " '%s'", arg.c_str() );
		fprintf( stderr, "\n" );

		if ( name == "queue" || name == "war_join" )
		{
			queueStart = Time();
			runtime->Set( "queue.state", "searching" );
			if ( name == "war_join" )
				runtime->Set( "queue.mode", "war" );
			else if ( !args.empty() )
				runtime->Set( "queue.mode", Node( args[ 0 ] ) );
		}
		else if ( name == "open_settings" )
			runtime->Set( "ui.modal", "settings" );
		// The social side, faked: chat lines appear, the squad toggle flips,
		// donations go through.
		else if ( name == "chat_send" || ( name == "chat_input" && args.size() > 1 && args[ 0 ] == "1" ) )
		{
			const std::string text = name == "chat_send" ? ( args.empty() ? runtime->Get( "ui.chat_draft" )->AsString() : args[ 0 ] ) : args[ 1 ];
			fui::Node *pView = runtime->Get( "social.view" );
			if ( pView && !text.empty() )
			{
				fui::Node view = pView->Clone();
				fui::Node &l = view.Append();
				l[ "id" ] = (double)view.Size() + 1000;
				l[ "ch" ] = runtime->Get( "social.tab" )->AsString();
				l[ "name" ] = runtime->Get( "player.name" )->AsString();
				l[ "tag" ] = runtime->Get( "player.tag" )->AsString();
				l[ "sup" ] = runtime->Get( "player.sup" )->AsBool();
				l[ "text" ] = text;
				l[ "sys" ] = false;
				l[ "me" ] = true;
				l[ "time" ] = "21:05";
				runtime->Set( "social.view", std::move( view ) );
				runtime->Set( "ui.chat_draft", Node( "" ) );
			}
		}
		else if ( name == "lfg_toggle" )
			runtime->Set( "social.lfg", Node( !runtime->Get( "social.lfg" )->AsBool() ) );
		else if ( name == "support_buy" )
		{
			runtime->Set( "support.result", Node( "thanks" ) );
			if ( !args.empty() && args[ 0 ] == "monthly" )
				runtime->Set( "support.me.monthly", Node( true ) );
		}
		else if ( name == "support_cancel" )
		{
			runtime->Set( "support.me.monthly", Node( false ) );
			runtime->Set( "support.result", Node( "cancelled" ) );
			runtime->Set( "ui.modal", Node( "" ) );
		}
		else if ( name == "clan_leave" )
		{
			runtime->Set( "clan.has", Node( false ) );
			runtime->Set( "ui.modal", Node( "" ) );
		}
		else if ( name == "clan_create" || name == "clan_join" )
			runtime->Set( "clan.has", Node( true ) );
		else if ( name == "clan_settings" )
			runtime->Set( "ui.modal", Node( "" ) );
		else if ( ( name == "clan_invite" || name == "clan_uninvite" ) && !args.empty() )
		{
			// The coordinator would answer with the invite listed (or gone).
			const bool bInvite = name == "clan_invite";
			if ( fui::Node *pCands = runtime->Get( "clan.cands" ) )
			{
				fui::Node cands = pCands->Clone();
				for ( size_t i = 0; i < cands.Size(); ++i )
				{
					if ( cands.At( i )->Find( "id" )->AsString() == args[ 0 ] )
						( *cands.At( i ) )[ "invited" ] = bInvite;
				}
				runtime->Set( "clan.cands", std::move( cands ) );
			}
			if ( !bInvite )
			{
				fui::Node *pInvited = runtime->Get( "clan.c.invited" );
				fui::Node kept;
				kept.SetArray();
				for ( size_t i = 0; pInvited && i < pInvited->Size(); ++i )
				{
					if ( pInvited->At( i )->Find( "id" )->AsString() != args[ 0 ] )
						kept.Append( pInvited->At( i )->Clone() );
				}
				runtime->Set( "clan.c.invited", std::move( kept ) );
			}
		}
		else if ( name == "hud_style" && !args.empty() )
			runtime->Set( "settings.hud_style", Node( args[ 0 ] ) );
		else if ( name == "unqueue" )
		{
			queueStart = -1.0;
			runtime->Set( "queue.state", "idle" );
		}
		else if ( name == "accept" )
		{
			runtime->Set( "queue.state", "joining" );
		}
		else if ( name == "select_mode" && !args.empty() )
		{
			fui::Node *pModes = runtime->Get( "modes" );
			for ( size_t i = 0; pModes && i < pModes->Size(); ++i )
			{
				const fui::Node &mode = *pModes->At( i );
				if ( mode.Find( "id" )->AsString() == args[ 0 ] && mode.Find( "enabled" )->AsBool() )
					runtime->Set( "ui.mode", Node( args[ 0 ] ) );
			}
		}
		else if ( ( name == "toggle_map" || name == "toggle_mode_maps" ) && !args.empty() || name == "pool_all" || name == "pool_none" )
		{
			// The map picker, as fui_tf.cpp does it on the party's criteria.
			const fui::Node *pPool = runtime->Get( "pool" );
			if ( !pPool )
				return;
			fui::Node pool = pPool->Clone();
			fui::Node *pCats = pool.Find( "cats" );
			int nOn = 0;
			for ( size_t c = 0; pCats && c < pCats->Size(); ++c )
			{
				fui::Node &cat = *pCats->At( c );
				fui::Node &maps = *cat.Find( "maps" );
				bool bAllOn = true;
				for ( size_t m = 0; m < maps.Size(); ++m )
					bAllOn = bAllOn && maps.At( m )->Find( "on" )->AsBool();
				int nCatOn = 0;
				for ( size_t m = 0; m < maps.Size(); ++m )
				{
					fui::Node &map = *maps.At( m );
					bool bOn = map.Find( "on" )->AsBool();
					if ( name == "toggle_map" && map.Find( "id" )->AsString() == args[ 0 ] )
						bOn = !bOn;
					else if ( name == "toggle_mode_maps" && cat.Find( "id" )->AsString() == args[ 0 ] )
						bOn = !bAllOn;
					else if ( name == "pool_all" || name == "pool_none" )
						bOn = name == "pool_all";
					map[ "on" ] = bOn;
					nCatOn += bOn ? 1 : 0;
				}
				cat[ "n_on" ] = nCatOn;
				cat[ "on" ] = nCatOn == (int)maps.Size();
				cat[ "some" ] = nCatOn > 0 && nCatOn < (int)maps.Size();
				nOn += nCatOn;
			}
			pool[ "n_on" ] = nOn;
			runtime->Set( "pool", std::move( pool ) );
		}
		else if ( name == "locker_class" && !args.empty() )
		{
			runtime->Set( "ui.class", Node( args[ 0 ] ) );
			runtime->Set( "ui.pick", Node( -1 ) );
			RefreshLocker();
		}
		else if ( name == "locker_slot" && !args.empty() )
		{
			runtime->Set( "ui.slot", Node( args[ 0 ] ) );
			runtime->Set( "ui.pick", Node( -1 ) );
			RefreshLocker();
		}
		else if ( name == "locker_filter" && !args.empty() )
		{
			runtime->Set( "locker.filter", Node( args[ 0 ] ) );
			RefreshLocker();
		}
		else if ( name == "locker_pick" && !args.empty() )
		{
			runtime->Set( "ui.pick", Node( atoi( args[ 0 ].c_str() ) ) );
			RefreshLocker();
		}
		else if ( name == "equip" && !args.empty() )
		{
			loadout[ Str( "ui.class" ) ][ Str( "ui.slot" ) ] = atoi( args[ 0 ].c_str() );
			runtime->Set( "ui.pick", Node( atoi( args[ 0 ].c_str() ) ) );
			RefreshLocker();
		}
		else if ( name == "lobby_class" && !args.empty() )
		{
			runtime->Set( "ui.class", Node( args[ 0 ] ) );
			runtime->Set( "party.members.0.class", Node( args[ 0 ] ) );
			RefreshLocker();
		}
		else if ( name == "pick_class" && !args.empty() )
		{
			if ( args[ 0 ] != "random" )
				runtime->Set( "match.class", Node( args[ 0 ] ) );
			runtime->Navigate( "pause" );
		}
		else if ( name == "close_classes" || name == "resume" )
			runtime->Navigate( runtime->Get( "ui.in_game" ) && runtime->Get( "ui.in_game" )->AsBool() ? "pause" : "home" );
		else if ( name == "changeclass" )
			runtime->Navigate( "classes" );
		else if ( name == "quit" )
		{
			SDL_Event quit;
			quit.type = SDL_EVENT_QUIT;
			SDL_PushEvent( &quit );
		}
	}

	// class -> slot -> index into locker.items
	std::map< std::string, std::map< std::string, int > > loadout;

	std::string Str( const char *pszPath )
	{
		fui::Node *pNode = runtime->Get( pszPath );
		return pNode ? pNode->AsString() : std::string();
	}

	static std::string BaseSlot( const std::string &slot )
	{
		return slot.compare( 0, 8, "cosmetic" ) == 0 ? "cosmetic" : slot;
	}

	static bool Fits( const fui::Node &item, const std::string &cls, const std::string &slot )
	{
		if ( item.Find( "slot" )->AsString() != slot )
			return false;
		const fui::Node *pClasses = item.Find( "classes" );
		for ( size_t i = 0; pClasses && i < pClasses->Size(); ++i )
		{
			if ( pClasses->At( i )->AsString() == cls )
				return true;
		}
		return false;
	}

	// What the game's provider does from the real inventory.
	void RefreshLocker()
	{
		fui::Node *pItems = runtime->Get( "locker.items" );
		if ( !pItems )
			return;
		const std::string cls = Str( "ui.class" ), slot = Str( "ui.slot" ), filter = Str( "locker.filter" );
		static const char *s_Slots[] = { "primary", "secondary", "melee", "cosmetic1", "cosmetic2", "cosmetic3", "taunt" };

		// Default loadout: the first item that fits each slot.
		std::map< std::string, int > &mine = loadout[ cls ];
		if ( mine.empty() )
		{
			int cosmetics = 0;
			for ( size_t i = 0; i < pItems->Size(); ++i )
			{
				const fui::Node &item = *pItems->At( i );
				for ( const char *pszSlot : s_Slots )
				{
					const std::string base = BaseSlot( pszSlot );
					if ( base == "cosmetic" )
					{
						if ( Fits( item, cls, "cosmetic" ) && cosmetics < 2 && !mine.count( pszSlot ) &&
							std::string( pszSlot ) == "cosmetic" + std::to_string( cosmetics + 1 ) )
						{
							mine[ pszSlot ] = (int)i;
							++cosmetics;
							break;
						}
					}
					else if ( Fits( item, cls, base ) && !mine.count( pszSlot ) )
					{
						mine[ pszSlot ] = (int)i;
						break;
					}
				}
			}
		}

		fui::Node equipped;
		equipped.SetArray();
		for ( const char *pszSlot : s_Slots )
		{
			fui::Node &entry = equipped.Append();
			entry[ "id" ] = pszSlot;
			std::string label = runtime->Translate( "fui_slot_" + BaseSlot( pszSlot ) );
			if ( BaseSlot( pszSlot ) == "cosmetic" )
				label += " " + std::string( pszSlot ).substr( 8 );
			entry[ "name" ] = label;
			auto it = mine.find( pszSlot );
			if ( it != mine.end() )
				entry[ "item" ].Assign( pItems->At( it->second )->Clone() );
			else
				entry[ "item" ].SetObject();
		}
		runtime->Set( "locker.equipped", std::move( equipped ) );

		const int pick = (int)runtime->Get( "ui.pick" )->AsNumber( -1 );
		const int worn = mine.count( slot ) ? mine[ slot ] : -1;
		fui::Node view, detail;
		view.SetArray();
		detail.SetObject();
		for ( size_t i = 0; i < pItems->Size(); ++i )
		{
			const fui::Node &item = *pItems->At( i );
			if ( !Fits( item, cls, BaseSlot( slot ) ) )
				continue;
			const std::string quality = item.Find( "quality" )->AsString();
			if ( filter == "strange" && quality != "strange" )
				continue;
			if ( filter == "rare" && ( quality == "unique" || quality == "strange" ) )
				continue;
			fui::Node &entry = view.Append( item.Clone() );
			entry[ "equipped" ] = (int)i == worn;
			if ( (int)i == pick )
				detail.Assign( entry.Clone() );
		}
		runtime->Set( "locker.count", Node( (int)view.Size() ) );
		runtime->Set( "locker.view", std::move( view ) );
		runtime->Set( "locker.detail", std::move( detail ) );
	}

	void Tick()
	{
		if ( queueStart >= 0.0 )
			runtime->Set( "queue.seconds", fui::Node( (int)( Time() - queueStart ) ) );
	}

	using Node = fui::Node;
};

Rml::Input::KeyIdentifier MapKey( SDL_Keycode key )
{
	switch ( key )
	{
	case SDLK_BACKSPACE: return Rml::Input::KI_BACK;
	case SDLK_DELETE: return Rml::Input::KI_DELETE;
	case SDLK_RETURN: return Rml::Input::KI_RETURN;
	case SDLK_LEFT: return Rml::Input::KI_LEFT;
	case SDLK_RIGHT: return Rml::Input::KI_RIGHT;
	case SDLK_UP: return Rml::Input::KI_UP;
	case SDLK_DOWN: return Rml::Input::KI_DOWN;
	case SDLK_HOME: return Rml::Input::KI_HOME;
	case SDLK_END: return Rml::Input::KI_END;
	case SDLK_TAB: return Rml::Input::KI_TAB;
	case SDLK_ESCAPE: return Rml::Input::KI_ESCAPE;
	default: return Rml::Input::KI_UNKNOWN;
	}
}

std::string DefaultTF2()
{
	const char *pszHome = getenv( "HOME" );
	return std::string( pszHome ? pszHome : "" ) + "/Library/Application Support/Steam/steamapps/common/Team Fortress 2";
}

} // namespace

int main( int argc, char **argv )
{
	std::string tf2 = getenv( "TC2_TF2_DIR" ) ? getenv( "TC2_TF2_DIR" ) : DefaultTF2();
	std::string mockFile, screen, shot;
	std::vector< std::pair< std::string, std::string > > sets;
	int width = 1920, height = 1080, shotFrames = 30;
	bool bDebug = false;
	bool bHud = false;
	std::string hudStyle = "frontress";
	// --drag x0,y0,x1,y1: a scripted mouse drag (in pixels) early on, to try
	// what dragging does in a screenshot.
	float drag[ 4 ] = { -1, -1, -1, -1 };
	PreviewHost host;

	// The repository: two levels above this executable's source by default.
	g_Root = FUI_PREVIEW_REPO;
	for ( int i = 1; i < argc; ++i )
	{
		const std::string arg = argv[ i ];
		auto next = [ & ]() { return i + 1 < argc ? std::string( argv[ ++i ] ) : std::string(); };
		if ( arg == "--root" ) g_Root = next();
		else if ( arg == "--tf2" ) tf2 = next();
		else if ( arg == "--lang" ) host.language = next();
		else if ( arg == "--mock" ) mockFile = next();
		else if ( arg == "--screen" ) screen = next();
		else if ( arg == "--shot" ) shot = next();
		else if ( arg == "--frames" ) shotFrames = atoi( next().c_str() );
		else if ( arg == "--debug" ) bDebug = true;
		else if ( arg == "--hud" ) bHud = true;
		else if ( arg == "--drag" ) sscanf( next().c_str(), "%f,%f,%f,%f", &drag[ 0 ], &drag[ 1 ], &drag[ 2 ], &drag[ 3 ] );
		else if ( arg == "--hud-style" ) { hudStyle = next(); bHud = true; }
		else if ( arg == "--size" ) sscanf( next().c_str(), "%dx%d", &width, &height );
		else if ( arg == "--set" )
		{
			const std::string kv = next();
			const size_t eq = kv.find( '=' );
			if ( eq != std::string::npos )
				sets.emplace_back( kv.substr( 0, eq ), kv.substr( eq + 1 ) );
		}
		else
		{
			fprintf( stderr, "unknown argument %s\n", arg.c_str() );
			return 2;
		}
	}
	if ( mockFile.empty() )
		mockFile = g_Root + "/tools/fui-preview/mock.json";
	if ( !g_Content.Mount( tf2 ) )
		fprintf( stderr, "[fui] Team Fortress 2 not found at %s: no TF2 art or fonts\n", tf2.c_str() );
	// Our own materials (materials/vgui/resource/icon_eac.vtf, ...), as in the game.
	g_Content.AddLooseRoot( g_Root + "/game/tc2/loose/" );

	if ( !SDL_Init( SDL_INIT_VIDEO ) )
	{
		fprintf( stderr, "SDL_Init: %s\n", SDL_GetError() );
		return 1;
	}
	SDL_GL_SetAttribute( SDL_GL_CONTEXT_MAJOR_VERSION, 2 );
	SDL_GL_SetAttribute( SDL_GL_CONTEXT_MINOR_VERSION, 1 );
	const bool bShot = !shot.empty();
	SDL_WindowFlags flags = SDL_WINDOW_OPENGL | ( bShot ? SDL_WINDOW_HIDDEN : SDL_WINDOW_RESIZABLE | SDL_WINDOW_HIGH_PIXEL_DENSITY );
	const int winW = bShot ? 320 : width * 2 / 3, winH = bShot ? 180 : height * 2 / 3;
	SDL_Window *pWindow = SDL_CreateWindow( "Team Frontress UI preview", winW, winH, flags );
	if ( !pWindow )
	{
		fprintf( stderr, "SDL_CreateWindow: %s\n", SDL_GetError() );
		return 1;
	}
	SDL_GLContext gl = SDL_GL_CreateContext( pWindow );
	SDL_GL_SetSwapInterval( 1 );

	// Screenshots render into an offscreen buffer of exactly --size.
	GLuint fbo = 0, colour = 0;
	if ( bShot )
	{
		glGenFramebuffersEXT( 1, &fbo );
		glBindFramebufferEXT( GL_FRAMEBUFFER_EXT, fbo );
		glGenRenderbuffersEXT( 1, &colour );
		glBindRenderbufferEXT( GL_RENDERBUFFER_EXT, colour );
		glRenderbufferStorageEXT( GL_RENDERBUFFER_EXT, GL_RGBA8, width, height );
		glFramebufferRenderbufferEXT( GL_FRAMEBUFFER_EXT, GL_COLOR_ATTACHMENT0_EXT, GL_RENDERBUFFER_EXT, colour );
	}
	else
		SDL_GetWindowSizeInPixels( pWindow, &width, &height );

	GLBackend backend;
	backend.Init();
	fui::Runtime runtime;
	host.runtime = &runtime;

	// Mock data first, so the documents load with it.
	{
		std::string text, error;
		fui::Node mock;
		if ( ReadDisk( mockFile, text ) && fui::Node::Parse( text, mock, &error ) )
			runtime.Merge( mock );
		else
			fprintf( stderr, "[fui] mock data %s: %s\n", mockFile.c_str(), error.empty() ? "missing" : error.c_str() );
	}
	if ( !screen.empty() )
		runtime.Set( "ui.screen", screen.c_str() );

	if ( !runtime.Init( &host, &backend, width, height ) )
		return 1;
	// Apply overrides after initial navigation, which closes modal windows.
	for ( const auto &kv : sets )
	{
		fui::Node value;
		if ( !fui::Node::Parse( kv.second, value ) )
			value.SetString( kv.second );
		runtime.Set( kv.first, std::move( value ) );
	}
	runtime.SetHotReload( !bShot );
	host.RefreshLocker();
	if ( bDebug )
		runtime.ShowDebugger( true );
	runtime.SetHudDocument( "huds/" + hudStyle + "/hud.rml" );
	if ( bHud )
		runtime.SetHud( true );

	bool bRunning = true;
	int frame = 0;
	while ( bRunning )
	{
		SDL_Event event;
		Rml::Context *pContext = runtime.Context();
		if ( drag[ 0 ] >= 0 && frame >= 30 && frame <= 52 )
		{
			const float t = ( frame - 31 ) / 20.f;
			const int x = (int)( drag[ 0 ] + ( drag[ 2 ] - drag[ 0 ] ) * std::clamp( t, 0.f, 1.f ) );
			const int y = (int)( drag[ 1 ] + ( drag[ 3 ] - drag[ 1 ] ) * std::clamp( t, 0.f, 1.f ) );
			pContext->ProcessMouseMove( x, y, 0 );
			if ( frame == 30 )
				pContext->ProcessMouseButtonDown( 0, 0 );
			if ( frame == 52 )
				pContext->ProcessMouseButtonUp( 0, 0 );
		}
		const float scale = SDL_GetWindowPixelDensity( pWindow );
		while ( SDL_PollEvent( &event ) )
		{
			switch ( event.type )
			{
			case SDL_EVENT_QUIT: bRunning = false; break;
			case SDL_EVENT_WINDOW_PIXEL_SIZE_CHANGED:
				if ( !bShot )
				{
					width = event.window.data1;
					height = event.window.data2;
					runtime.SetViewport( width, height );
				}
				break;
			case SDL_EVENT_MOUSE_MOTION: pContext->ProcessMouseMove( (int)( event.motion.x * scale ), (int)( event.motion.y * scale ), 0 ); break;
			case SDL_EVENT_MOUSE_BUTTON_DOWN: pContext->ProcessMouseButtonDown( event.button.button == SDL_BUTTON_LEFT ? 0 : event.button.button == SDL_BUTTON_RIGHT ? 1 : 2, 0 ); break;
			case SDL_EVENT_MOUSE_BUTTON_UP: pContext->ProcessMouseButtonUp( event.button.button == SDL_BUTTON_LEFT ? 0 : event.button.button == SDL_BUTTON_RIGHT ? 1 : 2, 0 ); break;
			case SDL_EVENT_MOUSE_WHEEL: pContext->ProcessMouseWheel( Rml::Vector2f( -event.wheel.x, -event.wheel.y ), 0 ); break;
			case SDL_EVENT_TEXT_INPUT: pContext->ProcessTextInput( event.text.text ); break;
			case SDL_EVENT_KEY_DOWN:
			{
				const SDL_Keycode key = event.key.key;
				if ( key == SDLK_F5 )
					runtime.Reload();
				else if ( key == SDLK_F8 )
				{
					bDebug = !bDebug;
					runtime.ShowDebugger( bDebug );
				}
				else if ( key >= SDLK_1 && key <= SDLK_6 && !( event.key.mod & SDL_KMOD_SHIFT ) && !SDL_TextInputActive( pWindow ) )
				{
					static const char *s_Screens[] = { "home", "play", "war", "quests", "locker", "career" };
					runtime.Navigate( s_Screens[ key - SDLK_1 ] );
				}
				else
					pContext->ProcessKeyDown( MapKey( key ), 0 );
				break;
			}
			case SDL_EVENT_KEY_UP: pContext->ProcessKeyUp( MapKey( event.key.key ), 0 ); break;
			default: break;
			}
		}

		host.Tick();
		runtime.Update();

		backend.height = height;
		glViewport( 0, 0, width, height );
		glClearColor( 0.08f, 0.07f, 0.06f, 1.f );
		glClear( GL_COLOR_BUFFER_BIT );
		glMatrixMode( GL_PROJECTION );
		glLoadIdentity();
		glOrtho( 0, width, height, 0, -1, 1 );
		glMatrixMode( GL_MODELVIEW );
		glLoadIdentity();
		glEnable( GL_BLEND );
		glBlendFunc( GL_SRC_ALPHA, GL_ONE_MINUS_SRC_ALPHA );
		glEnable( GL_TEXTURE_2D );
		glEnableClientState( GL_VERTEX_ARRAY );
		glEnableClientState( GL_TEXTURE_COORD_ARRAY );
		glEnableClientState( GL_COLOR_ARRAY );
		runtime.Render();
		glDisable( GL_SCISSOR_TEST );

		if ( bShot && ++frame >= shotFrames )
		{
			std::vector< uint8_t > pixels( (size_t)width * height * 4 );
			glReadPixels( 0, 0, width, height, GL_RGBA, GL_UNSIGNED_BYTE, pixels.data() );
			stbi_flip_vertically_on_write( 1 );
			for ( size_t i = 3; i < pixels.size(); i += 4 )
				pixels[ i ] = 255;
			if ( !stbi_write_png( shot.c_str(), width, height, 4, pixels.data(), width * 4 ) )
				fprintf( stderr, "could not write %s\n", shot.c_str() );
			bRunning = false;
		}
		if ( !bShot )
			SDL_GL_SwapWindow( pWindow );
		else
			SDL_Delay( 16 );	// let transitions run at real time
	}

	runtime.Shutdown();
	SDL_GL_DestroyContext( gl );
	SDL_DestroyWindow( pWindow );
	SDL_Quit();
	return 0;
}
