//========= Team Frontress ====================================================//
//
// Frontress UI's game host. See fui_host.h.
//
//=============================================================================
#include "cbase.h"

// RmlUi and the runtime first: they bring their own STL use, and Source's
// headers define min/max.
// Source's Assert and min/max macros would rewrite RmlUi's declarations.
#pragma push_macro( "Assert" )
#undef Assert
#include "tier0/valve_minmax_off.h"
#include "fui.h"
#include <RmlUi/Core/Context.h>
#include <RmlUi/Core/Element.h>
#include <RmlUi/Core/Input.h>
#include "tier0/valve_minmax_on.h"
#pragma pop_macro( "Assert" )

#include "fui_host.h"
#include "fui_tf.h"
#include "fui_stage.h"
#include "fui_hud.h"
#include "hud.h"

#include "bitmap/imageformat.h"
#include "filesystem.h"
#include "ienginevgui.h"
#include "materialsystem/imaterial.h"
#include "materialsystem/imaterialsystem.h"
#include "materialsystem/imesh.h"
#include "materialsystem/itexture.h"
#include "pixelwriter.h"
#include "steam/steam_api.h"
#include "vgui/IInput.h"
#include "vgui/IPanel.h"
#include "vgui/ISurface.h"
#include "vgui/IVGui.h"
#include "vgui_controls/Panel.h"
#include "iclientmode.h"
#include "c_tf_player.h"
#include "baseviewport.h"
#include "vtf/vtf.h"

// memdbgon must be the last include file in a .cpp file!!!
#include "tier0/memdbgon.h"

static void FuiMenuChanged( IConVar *pVar, const char *pszOld, float flOld );
ConVar fui_menu( "fui_menu", "1", FCVAR_ARCHIVE, "Use the Frontress main menu (resource/fui). 0 shows Team Fortress' own.", FuiMenuChanged );
ConVar fui_hot_reload( "fui_hot_reload", "0", FCVAR_ARCHIVE, "Reload resource/fui when its files change on disk." );
ConVar fui_hud( "fui_hud", "1", FCVAR_ARCHIVE, "Show the Frontress HUD layer (quests, notifications) in matches." );
ConVar fui_pause_menu( "fui_pause_menu", "1", FCVAR_ARCHIVE, "Use the Frontress pause menu in matches." );
ConVar fui_class_select( "fui_class_select", "1", FCVAR_ARCHIVE, "Use the Frontress class select in matches." );
ConVar fui_team_select( "fui_team_select", "1", FCVAR_ARCHIVE, "Use the Frontress team select in matches." );

static CFuiStage *s_pStage = NULL;
static bool s_bClassSelect = false;
static int s_iClassAtSelect = 0;	// the local class when class select opened
static bool s_bTeamSelect = false;
static int s_iTeamAtSelect = 0;		// the local team when team select opened
static bool s_bTeamPicked = false;	// a team was asked for since it opened

//-----------------------------------------------------------------------------
// Drawing on the material system. Every texture is a procedural ITexture
// with an UnlitGeneric material around it; triangles go through a dynamic
// mesh in the screen-space matrices VGUI paints with.
//-----------------------------------------------------------------------------
class CFuiTextureBits final : public ITextureRegenerator
{
public:
	CFuiTextureBits( const uint8 *pRGBA, int w, int h ) : m_nWide( w ), m_nTall( h ) { Update( pRGBA ); }

	void Update( const uint8 *pRGBA ) { m_Pixels.CopyArray( pRGBA, m_nWide * m_nTall * 4 ); }

	virtual void RegenerateTextureBits( ITexture *pTexture, IVTFTexture *pVTFTexture, Rect_t *pRect ) OVERRIDE
	{
		if ( pVTFTexture->Width() != m_nWide || pVTFTexture->Height() != m_nTall )
			return;
		ImageLoader::ConvertImageFormat( m_Pixels.Base(), IMAGE_FORMAT_RGBA8888, pVTFTexture->ImageData( 0, 0, 0 ), pVTFTexture->Format(), m_nWide,
			m_nTall );
	}
	virtual void Release() OVERRIDE {}

private:
	CUtlVector< uint8 > m_Pixels;
	int m_nWide, m_nTall;
};

class CFuiMaterialBackend : public fui::DrawBackend
{
public:
	struct Texture_t
	{
		ITexture *pTexture;			// NULL for a game texture we only reference by name
		IMaterial *pMaterial;
		CFuiTextureBits *pBits;
		int w, h;
		bool bRepeat;
		float uv[ 4 ];				// the part of it to draw: s0 t0 s1 t1
	};

	CFuiMaterialBackend() : m_nNextID( 1 ), m_nNextName( 0 ), m_nOffsetX( 0 ), m_nOffsetY( 0 ), m_White( 0 ) {}

	void Init()
	{
		const uint8 white[ 4 ] = { 255, 255, 255, 255 };
		m_White = CreateTexture( white, 1, 1, false );
	}

	void Shutdown()
	{
		FOR_EACH_MAP_FAST( m_Textures, i )
			Free( m_Textures[ i ] );
		m_Textures.RemoveAll();
		FOR_EACH_VEC( m_FreeTextures, i )
			Free( m_FreeTextures[ i ] );
		m_FreeTextures.RemoveAll();
		m_White = 0;
	}

	void SetOffset( int x, int y )
	{
		m_nOffsetX = x;
		m_nOffsetY = y;
	}

	virtual uintptr_t CreateTexture( const uint8_t *pRGBA, int w, int h, bool bRepeat ) OVERRIDE
	{
		if ( w <= 0 || h <= 0 )
			return 0;

		char szTexture[ 64 ], szMaterial[ 64 ];
		// Source interns texture and material names permanently. Reuse freed
		// procedural textures when decorators are rebuilt during animation.
		FOR_EACH_VEC( m_FreeTextures, i )
		{
			const Texture_t &slot = m_FreeTextures[ i ];
			if ( slot.w != w || slot.h != h || slot.bRepeat != bRepeat )
				continue;
			Texture_t texture = slot;
			m_FreeTextures.FastRemove( i );
			texture.pBits->Update( pRGBA );
			texture.pTexture->Download();
			const uintptr_t id = m_nNextID++;
			m_Textures.Insert( id, texture );
			return id;
		}
		const int n = m_nNextName++;
		V_snprintf( szTexture, sizeof( szTexture ), "_fui/tex%d", n );
		V_snprintf( szMaterial, sizeof( szMaterial ), "_fui/mat%d", n );

		int nFlags = TEXTUREFLAGS_NOMIP | TEXTUREFLAGS_NOLOD | TEXTUREFLAGS_PROCEDURAL | TEXTUREFLAGS_SINGLECOPY;
		if ( !bRepeat )
			nFlags |= TEXTUREFLAGS_CLAMPS | TEXTUREFLAGS_CLAMPT;
		ITexture *pTexture = materials->CreateProceduralTexture( szTexture, TEXTURE_GROUP_VGUI, w, h, IMAGE_FORMAT_RGBA8888, nFlags );
		if ( !pTexture || pTexture->IsError() )
			return 0;

		CFuiTextureBits *pBits = new CFuiTextureBits( pRGBA, w, h );
		pTexture->SetTextureRegenerator( pBits );
		pTexture->Download();

		Texture_t texture = { pTexture, MakeMaterial( szMaterial, szTexture ), pBits, w, h, bRepeat, { 0, 0, 1, 1 } };
		const uintptr_t id = m_nNextID++;
		m_Textures.Insert( id, texture );
		return id;
	}

	virtual uintptr_t LoadNamedTexture( const std::string &source, int &w, int &h ) OVERRIDE
	{
		if ( !V_strncmp( source.c_str(), "tex:", 4 ) )
		{
			// A texture the game already has: class portraits, backpack icons,
			// map thumbnails. "_large" icons fall back to the small ones.
			CUtlString strName( source.c_str() + 4 );
			ITexture *pTexture = materials->FindTexture( strName, TEXTURE_GROUP_VGUI, false );
			if ( ( !pTexture || pTexture->IsError() ) && strName.Length() > 6 && !V_strcmp( strName.Get() + strName.Length() - 6, "_large" ) )
			{
				strName = strName.Slice( 0, strName.Length() - 6 );
				pTexture = materials->FindTexture( strName, TEXTURE_GROUP_VGUI, false );
			}
			if ( !pTexture || pTexture->IsError() )
				return 0;

			char szMaterial[ MAX_PATH + 16 ];
			V_snprintf( szMaterial, sizeof( szMaterial ), "_fui/named/%s", strName.Get() );
			w = pTexture->GetActualWidth();
			h = pTexture->GetActualHeight();
			Texture_t texture = { NULL, MakeMaterial( szMaterial, strName ), NULL, 0, 0, false, { 0, 0, 1, 1 } };
			const uintptr_t id = m_nNextID++;
			m_Textures.Insert( id, texture );
			return id;
		}
		if ( !V_strncmp( source.c_str(), "hudicon:", 8 ) )
		{
			// A HUD icon from scripts/mod_textures.txt (kill icons: d_<weapon>):
			// a rectangle of a sheet.
			CHudTexture *pIcon = gHUD.GetIcon( source.c_str() + 8 );
			if ( !pIcon || pIcon->bRenderUsingFont )
				return 0;
			if ( !pIcon->bPrecached )
				pIcon->Precache();
			const char *pszFile = pIcon->szTextureFile;
			while ( !V_strncmp( pszFile, "../", 3 ) )
				pszFile += 3;
			CUtlString strName( pszFile );
			ITexture *pTexture = materials->FindTexture( strName, TEXTURE_GROUP_VGUI, false );
			if ( !pTexture || pTexture->IsError() )
			{
				strName = CFmtStr( "vgui/%s", pszFile ).Get();
				pTexture = materials->FindTexture( strName, TEXTURE_GROUP_VGUI, false );
			}
			if ( !pTexture || pTexture->IsError() )
				return 0;
			const float tw = (float)pTexture->GetMappingWidth(), th = (float)pTexture->GetMappingHeight();
			char szMaterial[ MAX_PATH + 16 ];
			V_snprintf( szMaterial, sizeof( szMaterial ), "_fui/named/%s", strName.Get() );
			w = pIcon->rc.right - pIcon->rc.left;
			h = pIcon->rc.bottom - pIcon->rc.top;
			Texture_t texture = { NULL, MakeMaterial( szMaterial, strName ), NULL, 0, 0, false,
				{ pIcon->rc.left / tw, pIcon->rc.top / th, pIcon->rc.right / tw, pIcon->rc.bottom / th } };
			const uintptr_t id = m_nNextID++;
			m_Textures.Insert( id, texture );
			return id;
		}
		if ( !V_strncmp( source.c_str(), "avatar:", 7 ) )
		{
			ISteamFriends *pFriends = steamapicontext ? steamapicontext->SteamFriends() : NULL;
			ISteamUtils *pUtils = steamapicontext ? steamapicontext->SteamUtils() : NULL;
			const uint64 ulSteamID = V_atoui64( source.c_str() + 7 );
			if ( !pFriends || !pUtils || !ulSteamID )
				return 0;
			const int nImage = pFriends->GetLargeFriendAvatar( CSteamID( ulSteamID ) );
			uint32 uWide = 0, uTall = 0;
			if ( nImage <= 0 || !pUtils->GetImageSize( nImage, &uWide, &uTall ) || !uWide || !uTall )
				return 0;
			CUtlVector< uint8 > rgba;
			rgba.SetCount( uWide * uTall * 4 );
			if ( !pUtils->GetImageRGBA( nImage, rgba.Base(), rgba.Count() ) )
				return 0;
			w = uWide;
			h = uTall;
			return CreateTexture( rgba.Base(), w, h, false );
		}
		return 0;
	}

	virtual void ReleaseTexture( uintptr_t texture ) OVERRIDE
	{
		const auto i = m_Textures.Find( texture );
		if ( i == m_Textures.InvalidIndex() )
			return;
		Texture_t &slot = m_Textures[ i ];
		if ( slot.pBits )
			m_FreeTextures.AddToTail( slot );
		else
			Free( slot );
		m_Textures.RemoveAt( i );
	}

	virtual void DrawTriangles( const fui::DrawVertex *pVerts, int nVerts, const int *pIndices, int nIndices, uintptr_t texture ) OVERRIDE
	{
		const auto i = m_Textures.Find( texture ? texture : m_White );
		if ( i == m_Textures.InvalidIndex() || !m_Textures[ i ].pMaterial || nVerts <= 0 || nIndices <= 0 )
			return;

		CMatRenderContextPtr pRenderContext( materials );
		int nMaxVerts, nMaxIndices;
		pRenderContext->GetMaxToRender( pRenderContext->GetDynamicMesh( true, NULL, NULL, m_Textures[ i ].pMaterial ), false, &nMaxVerts, &nMaxIndices );
		if ( nVerts > nMaxVerts || nIndices > nMaxIndices )
			return;	// a UI mesh this big is a bug upstream; skip rather than crash

		const float *uv = m_Textures[ i ].uv;
		IMesh *pMesh = pRenderContext->GetDynamicMesh( true, NULL, NULL, m_Textures[ i ].pMaterial );
		CMeshBuilder meshBuilder;
		meshBuilder.Begin( pMesh, MATERIAL_TRIANGLES, nVerts, nIndices );
		for ( int v = 0; v < nVerts; ++v )
		{
			const fui::DrawVertex &vert = pVerts[ v ];
			meshBuilder.Position3f( vert.x + m_nOffsetX, vert.y + m_nOffsetY, 0.0f );
			meshBuilder.Color4ub( vert.r, vert.g, vert.b, vert.a );
			meshBuilder.TexCoord2f( 0, uv[ 0 ] + vert.u * ( uv[ 2 ] - uv[ 0 ] ), uv[ 1 ] + vert.v * ( uv[ 3 ] - uv[ 1 ] ) );
			meshBuilder.AdvanceVertex();
		}
		for ( int n = 0; n < nIndices; ++n )
			meshBuilder.FastIndex( (unsigned short)pIndices[ n ] );
		meshBuilder.End();
		pMesh->Draw();
	}

	virtual void SetScissor( bool bEnable, int x, int y, int w, int h ) OVERRIDE
	{
		CMatRenderContextPtr pRenderContext( materials );
		pRenderContext->SetScissorRect( x + m_nOffsetX, y + m_nOffsetY, x + m_nOffsetX + w, y + m_nOffsetY + h, bEnable );
	}

private:
	static IMaterial *MakeMaterial( const char *pszName, const char *pszTexture )
	{
		KeyValues *pVMT = new KeyValues( "UnlitGeneric" );
		pVMT->SetString( "$basetexture", pszTexture );
		pVMT->SetInt( "$vertexcolor", 1 );
		pVMT->SetInt( "$vertexalpha", 1 );
		pVMT->SetInt( "$translucent", 1 );
		pVMT->SetInt( "$ignorez", 1 );
		pVMT->SetInt( "$nocull", 1 );
		pVMT->SetInt( "$nofog", 1 );
		IMaterial *pMaterial = materials->FindProceduralMaterial( pszName, TEXTURE_GROUP_VGUI, pVMT );
		if ( pMaterial )
			pMaterial->IncrementReferenceCount();
		return pMaterial;
	}

	static void Free( Texture_t &texture )
	{
		if ( texture.pMaterial )
		{
			texture.pMaterial->DecrementReferenceCount();
			texture.pMaterial->DeleteIfUnreferenced();
		}
		if ( texture.pTexture )
		{
			texture.pTexture->SetTextureRegenerator( NULL );
			texture.pTexture->DecrementReferenceCount();
			texture.pTexture->DeleteIfUnreferenced();
		}
		delete texture.pBits;
	}

	CUtlVector< Texture_t > m_FreeTextures;
	CUtlMap< uintptr_t, Texture_t, int > m_Textures{ DefLessFunc( uintptr_t ) };
	uintptr_t m_nNextID;
	int m_nNextName;
	int m_nOffsetX, m_nOffsetY;
	uintptr_t m_White;
};

//-----------------------------------------------------------------------------
// The host: files from the game's search paths, Source's clock and log.
//-----------------------------------------------------------------------------
class CFuiGameHost : public fui::Host
{
public:
	vgui::Panel *m_pPanel = NULL;

	virtual double Time() OVERRIDE { return Plat_FloatTime(); }

	virtual void Log( int level, const char *pszMessage ) OVERRIDE
	{
		switch ( level )
		{
		case 0: Warning( "[fui] %s\n", pszMessage ); break;
		case 1: Warning( "[fui] %s\n", pszMessage ); break;
		case 2: DevMsg( "[fui] %s\n", pszMessage ); break;
		default: DevMsg( 2, "[fui] %s\n", pszMessage ); break;
		}
	}

	virtual bool ReadFile( const std::string &path, std::string &out ) OVERRIDE
	{
		CUtlBuffer buf;
		if ( !g_pFullFileSystem->ReadFile( path.c_str(), "GAME", buf ) )
			return false;
		out.assign( (const char *)buf.Base(), buf.TellPut() );
		return true;
	}

	virtual int64_t FileTime( const std::string &path ) OVERRIDE
	{
		return (int64_t)g_pFullFileSystem->GetFileTime( path.c_str(), "GAME" );
	}

	virtual void SetCursor( const std::string &name ) OVERRIDE
	{
		if ( !m_pPanel )
			return;
		if ( name == "pointer" )
			m_pPanel->SetCursor( vgui::dc_hand );
		else if ( name == "text" )
			m_pPanel->SetCursor( vgui::dc_ibeam );
		else
			m_pPanel->SetCursor( vgui::dc_arrow );
	}

	virtual void PlaySound( const std::string &name ) OVERRIDE
	{
		const char *pszWave = NULL;
		if ( name == "hover" )
			pszWave = "ui/buttonrollover.wav";
		else if ( name == "click" )
			pszWave = "ui/buttonclick.wav";
		else if ( name == "play" )
			pszWave = "ui/mm_xp_chime.wav";
		else if ( name == "equip" )
			pszWave = "ui/item_hat_pickup.wav";
		else if ( name == "tab" )
			pszWave = "ui/panel_open.wav";
		else if ( name == "class" )
			pszWave = "ui/item_default_pickup.wav";
		if ( pszWave )
			vgui::surface()->PlaySound( pszWave );
	}

	virtual std::string Language() OVERRIDE
	{
		static ConVarRef cl_language( "cl_language" );
		const char *pszLanguage = cl_language.IsValid() ? cl_language.GetString() : "";
		if ( ( !pszLanguage || !pszLanguage[ 0 ] ) && steamapicontext && steamapicontext->SteamApps() )
			pszLanguage = steamapicontext->SteamApps()->GetCurrentGameLanguage();
		return ( pszLanguage && pszLanguage[ 0 ] ) ? pszLanguage : "english";
	}

	virtual void OnCommand( const std::string &name, const std::vector< std::string > &args ) OVERRIDE
	{
		FuiTF().OnCommand( name, args );
	}

	virtual void OnNavigate( const std::string &screen ) OVERRIDE
	{
		FuiTF().OnNavigate( screen );
	}
};

//-----------------------------------------------------------------------------
// The panel: full screen, above the main menu, hands VGUI input to RmlUi.
//-----------------------------------------------------------------------------
static Rml::Input::KeyIdentifier FuiKey( vgui::KeyCode code )
{
	switch ( code )
	{
	case KEY_BACKSPACE: return Rml::Input::KI_BACK;
	case KEY_DELETE: return Rml::Input::KI_DELETE;
	case KEY_ENTER: return Rml::Input::KI_RETURN;
	case KEY_PAD_ENTER: return Rml::Input::KI_NUMPADENTER;
	case KEY_LEFT: return Rml::Input::KI_LEFT;
	case KEY_RIGHT: return Rml::Input::KI_RIGHT;
	case KEY_UP: return Rml::Input::KI_UP;
	case KEY_DOWN: return Rml::Input::KI_DOWN;
	case KEY_HOME: return Rml::Input::KI_HOME;
	case KEY_END: return Rml::Input::KI_END;
	case KEY_TAB: return Rml::Input::KI_TAB;
	case KEY_ESCAPE: return Rml::Input::KI_ESCAPE;
	case KEY_A: return Rml::Input::KI_A;
	case KEY_C: return Rml::Input::KI_C;
	case KEY_V: return Rml::Input::KI_V;
	case KEY_X: return Rml::Input::KI_X;
	default: return Rml::Input::KI_UNKNOWN;
	}
}

static int FuiModifiers()
{
	int mods = 0;
	if ( vgui::input()->IsKeyDown( KEY_LCONTROL ) || vgui::input()->IsKeyDown( KEY_RCONTROL ) )
		mods |= Rml::Input::KM_CTRL;
	if ( vgui::input()->IsKeyDown( KEY_LSHIFT ) || vgui::input()->IsKeyDown( KEY_RSHIFT ) )
		mods |= Rml::Input::KM_SHIFT;
	if ( vgui::input()->IsKeyDown( KEY_LALT ) || vgui::input()->IsKeyDown( KEY_RALT ) )
		mods |= Rml::Input::KM_ALT;
	return mods;
}

static int FuiButton( vgui::MouseCode code )
{
	return code == MOUSE_RIGHT ? 1 : code == MOUSE_MIDDLE ? 2 : 0;
}

static bool FuiYielding( vgui::VPANEL self );

class CFuiPanel : public vgui::Panel
{
	DECLARE_CLASS_SIMPLE( CFuiPanel, vgui::Panel );

public:
	CFuiPanel( vgui::VPANEL parent, fui::Runtime *pRuntime, CFuiMaterialBackend *pBackend )
		: BaseClass( NULL, "FrontressUI" ), m_pRuntime( pRuntime ), m_pBackend( pBackend )
	{
		SetParent( parent );
		SetPaintBackgroundEnabled( false );
		SetPaintBorderEnabled( false );
		SetMouseInputEnabled( true );
		SetKeyBoardInputEnabled( false );
		SetVisible( false );
		MakePopup( false, false );
	}

	virtual void OnThink() OVERRIDE
	{
		int w, h;
		vgui::surface()->GetScreenSize( w, h );
		if ( w != GetWide() || h != GetTall() )
		{
			SetBounds( 0, 0, w, h );
			m_pRuntime->SetViewport( w, h );
		}
		// Above the matchmaking dashboard and anything else that pops up on
		// the menu; the 3D characters directly underneath.
		const bool bYield = FuiYielding( GetVPanel() );
		if ( !bYield )
		{
			FuiStageRaise( s_pStage );
			MoveToFront();
		}

		// Keys only while a text field has focus, so the console key and the
		// game's own bindings keep working everywhere else.
		Rml::Element *pFocus = m_pRuntime->Context() ? m_pRuntime->Context()->GetFocusElement() : NULL;
		const bool bTyping = !bYield && IsMouseInputEnabled() && ( ( pFocus && pFocus->GetTagName() == "input" ) || s_bClassSelect || s_bTeamSelect );
		if ( bTyping != IsKeyBoardInputEnabled() )
		{
			SetKeyBoardInputEnabled( bTyping );
			if ( bTyping )
				RequestFocus();
		}
	}

	virtual void Paint() OVERRIDE
	{
		int x = 0, y = 0;
		vgui::ipanel()->GetAbsPos( GetVPanel(), x, y );
		m_pBackend->SetOffset( x, y );
		m_pRuntime->Render();
		CMatRenderContextPtr pRenderContext( materials );
		pRenderContext->SetScissorRect( 0, 0, 0, 0, false );
	}

	virtual void OnCursorMoved( int x, int y ) OVERRIDE { Ctx()->ProcessMouseMove( x, y, FuiModifiers() ); }
	virtual void OnCursorExited() OVERRIDE { Ctx()->ProcessMouseLeave(); }
	virtual void OnMousePressed( vgui::MouseCode code ) OVERRIDE { Ctx()->ProcessMouseButtonDown( FuiButton( code ), FuiModifiers() ); }
	virtual void OnMouseDoublePressed( vgui::MouseCode code ) OVERRIDE { Ctx()->ProcessMouseButtonDown( FuiButton( code ), FuiModifiers() ); }
	virtual void OnMouseReleased( vgui::MouseCode code ) OVERRIDE { Ctx()->ProcessMouseButtonUp( FuiButton( code ), FuiModifiers() ); }
	virtual void OnMouseWheeled( int delta ) OVERRIDE { Ctx()->ProcessMouseWheel( Rml::Vector2f( 0.f, (float)-delta ), FuiModifiers() ); }

	virtual void OnKeyCodePressed( vgui::KeyCode code ) OVERRIDE
	{
		// Typing a chat line in the HUD: Esc puts it away.
		if ( code == KEY_ESCAPE && FuiHudCloseChat() )
			return;
		// Team select: 1 BLU, 2 RED, 3 auto, 4 spectate, as the stock menu
		// lays them out; Esc closes once you are on a team.
		if ( s_bTeamSelect )
		{
			static const char *s_Teams[] = { "blu", "red", "auto", "spectate" };
			if ( code >= KEY_1 && code <= KEY_4 )
			{
				FuiTF().OnCommand( "pick_team", { s_Teams[ code - KEY_1 ] } );
				return;
			}
			if ( code == KEY_ESCAPE || code == KEY_0 )
			{
				FuiTF().OnCommand( "close_teams", {} );
				return;
			}
		}
		// Class select: the number row picks, as in the stock menu.
		if ( s_bClassSelect )
		{
			static const char *s_Keys[] = { "scout", "soldier", "pyro", "demoman", "heavy", "engineer", "medic", "sniper", "spy" };
			if ( code >= KEY_1 && code <= KEY_9 )
			{
				FuiTF().OnCommand( "pick_class", { s_Keys[ code - KEY_1 ] } );
				return;
			}
			if ( code == KEY_ESCAPE || code == KEY_0 )
			{
				FuiTF().OnCommand( "close_classes", {} );
				return;
			}
		}
		const Rml::Input::KeyIdentifier key = FuiKey( code );
		if ( key == Rml::Input::KI_UNKNOWN || Ctx()->ProcessKeyDown( key, FuiModifiers() ) )
			BaseClass::OnKeyCodePressed( code );
	}

	virtual void OnKeyCodeReleased( vgui::KeyCode code ) OVERRIDE
	{
		Ctx()->ProcessKeyUp( FuiKey( code ), FuiModifiers() );
	}

	virtual void OnKeyTyped( wchar_t unichar ) OVERRIDE
	{
		if ( unichar >= 32 )
			Ctx()->ProcessTextInput( (Rml::Character)unichar );
	}

private:
	Rml::Context *Ctx() { return m_pRuntime->Context(); }

	fui::Runtime *m_pRuntime;
	CFuiMaterialBackend *m_pBackend;
};

//-----------------------------------------------------------------------------
// fui_after <seconds> "<command>": run a command later, by the clock. Scripts
// for screenshots use it instead of wait, which counts frames (and which some
// engines turn off).
//-----------------------------------------------------------------------------
struct FuiScheduled_t
{
	double flAt;
	CUtlString strCommand;
};
static CUtlVector< FuiScheduled_t > s_Scheduled;

CON_COMMAND( fui_after, "fui_after <seconds> \"<command>\": run a console command after a delay." )
{
	if ( args.ArgC() < 3 )
	{
		Msg( "usage: fui_after <seconds> \"<command>\"\n" );
		return;
	}
	FuiScheduled_t item;
	item.flAt = Plat_FloatTime() + V_atof( args[ 1 ] );
	item.strCommand = args.ArgS() + V_strlen( args[ 1 ] );
	item.strCommand.TrimLeft();
	if ( item.strCommand.Length() >= 2 && item.strCommand[ 0 ] == '"' && item.strCommand[ item.strCommand.Length() - 1 ] == '"' )
		item.strCommand = item.strCommand.Slice( 1, item.strCommand.Length() - 1 );
	s_Scheduled.AddToTail( item );
}

// fui_dev_hide <panel>: close a viewport panel from a script.
CON_COMMAND( fui_dev_hide, "fui_dev_hide <panel>: close a viewport panel, e.g. team." )
{
	if ( args.ArgC() >= 2 && gViewPortInterface )
		gViewPortInterface->ShowPanel( args[ 1 ], false );
}

//-----------------------------------------------------------------------------
// Classic windows (options, backpack, achievements...) are popups of their
// own. Our panel raises itself every frame, which would bury them: after the
// UI opens one, the popups that appear are "theirs", and while any of them
// is up the UI stays where it is.
//-----------------------------------------------------------------------------
static CUtlVector< vgui::HPanel > s_PopupsBefore, s_Foreign;
static double s_flWatchUntil = 0.0;

static bool FuiInteractivePopup( vgui::VPANEL popup )
{
	return popup && vgui::ipanel()->IsFullyVisible( popup ) &&
		( vgui::ipanel()->IsMouseInputEnabled( popup ) || vgui::ipanel()->IsKeyBoardInputEnabled( popup ) );
}

void FrontressUIYield()
{
	s_PopupsBefore.RemoveAll();
	for ( int i = 0; i < vgui::surface()->GetPopupCount(); ++i )
	{
		const vgui::VPANEL popup = vgui::surface()->GetPopup( i );
		if ( FuiInteractivePopup( popup ) )
			s_PopupsBefore.AddToTail( vgui::ivgui()->PanelToHandle( popup ) );
	}
	s_flWatchUntil = Plat_FloatTime() + 1.0;
}

static bool FuiYielding( vgui::VPANEL self )
{
	const vgui::VPANEL modal = vgui::input()->GetAppModalSurface();
	bool bYield = modal && modal != self;
	for ( int i = 0; i < vgui::surface()->GetPopupCount(); ++i )
	{
		const vgui::VPANEL popup = vgui::surface()->GetPopup( i );
		if ( popup == self || !FuiInteractivePopup( popup ) )
			continue;
		// The console may also be opened by its key binding.
		if ( !V_stricmp( vgui::ipanel()->GetName( popup ), "GameConsole" ) )
			bYield = true;
		if ( Plat_FloatTime() < s_flWatchUntil )
		{
			const vgui::HPanel h = vgui::ivgui()->PanelToHandle( popup );
			if ( !s_PopupsBefore.HasElement( h ) && !s_Foreign.HasElement( h ) )
				s_Foreign.AddToTail( h );
		}
	}
	for ( int i = s_Foreign.Count() - 1; i >= 0; --i )
	{
		const vgui::VPANEL popup = vgui::ivgui()->HandleToPanel( s_Foreign[ i ] );
		if ( !FuiInteractivePopup( popup ) )
			s_Foreign.Remove( i );
		else
			bYield = true;
	}
	return bYield;
}

static void RunScheduled()
{
	const double flNow = Plat_FloatTime();
	for ( int i = 0; i < s_Scheduled.Count(); )
	{
		if ( s_Scheduled[ i ].flAt > flNow )
		{
			++i;
			continue;
		}
		engine->ClientCmd_Unrestricted( CFmtStr1024( "%s\n", s_Scheduled[ i ].strCommand.Get() ) );
		s_Scheduled.Remove( i );
	}
}

//-----------------------------------------------------------------------------
// The system: starts the UI once VGUI is up, shows it at the main menu.
//-----------------------------------------------------------------------------
class CFuiSystem : public CAutoGameSystemPerFrame
{
public:
	CFuiSystem() : CAutoGameSystemPerFrame( "CFuiSystem" ), m_pPanel( NULL ), m_bFailed( false ), m_eMode( k_eHidden ) {}

	enum Mode_t
	{
		k_eHidden,
		k_eMenu,	// main menu
		k_ePause,	// in a match, game menu open
		k_eClass,	// in a match, choosing a class
		k_eTeam,	// in a match, choosing a team
		k_eHud,		// in a match, playing
	};

	virtual void Update( float frametime ) OVERRIDE
	{
		if ( !fui_menu.GetBool() )
		{
			if ( m_pPanel )
				m_pPanel->SetVisible( false );
			return;
		}
		if ( !m_runtime.IsInitialized() && !m_bFailed )
			Start();
		if ( !m_runtime.IsInitialized() )
			return;

		// Class and team select belong to a match; leaving one closes them.
		if ( s_bClassSelect && ( !engine->IsInGame() || engine->IsLevelMainMenuBackground() ) )
			s_bClassSelect = false;
		if ( s_bTeamSelect && ( !engine->IsInGame() || engine->IsLevelMainMenuBackground() ) )
			s_bTeamSelect = false;
		// On RED or BLU now (jointeam, a bind, the server), or anywhere new
		// after picking: the choice is made. Being moved to the spectators by
		// the server alone (arena's queue does that) is not a choice.
		if ( s_bTeamSelect )
		{
			C_TFPlayer *pLocal = C_TFPlayer::GetLocalTFPlayer();
			const int iTeam = pLocal ? pLocal->GetTeamNumber() : TEAM_UNASSIGNED;
			if ( pLocal && iTeam != s_iTeamAtSelect && ( iTeam >= FIRST_GAME_TEAM || s_bTeamPicked ) )
				s_bTeamSelect = false;
		}
		// A class picked some other way (joinclass, a bind): the choice is made.
		if ( s_bClassSelect )
		{
			C_TFPlayer *pLocal = C_TFPlayer::GetLocalTFPlayer();
			if ( pLocal && pLocal->IsAlive() && pLocal->GetPlayerClass() && pLocal->GetPlayerClass()->GetClassIndex() != s_iClassAtSelect )
				s_bClassSelect = false;
		}
		const Mode_t eMode = CurrentMode();
		if ( eMode != m_eMode )
			EnterMode( eMode );

		// In a match GameUI's root is hidden whenever its menu is closed, and
		// a popup under a hidden root is not drawn: the HUD and class select
		// live under the client's root then (like the 3D stage).
		const vgui::VPANEL root = enginevgui->GetPanel( enginevgui->IsGameUIVisible() ? PANEL_GAMEUIDLL : PANEL_CLIENTDLL );
		if ( root && m_pPanel->GetVParent() != root )
			m_pPanel->SetParent( root );

		static ConVarRef fui_stage_debug( "fui_stage_debug" );
		const bool bShow = eMode != k_eHidden && !( fui_stage_debug.IsValid() && fui_stage_debug.GetInt() == 2 );
		if ( m_pPanel->IsVisible() != bShow )
			m_pPanel->SetVisible( bShow );
		// In a match the HUD takes the mouse only to type a chat line or to pick
		// a player on its scoreboard.
		const bool bMouse = bShow && ( eMode == k_eMenu || eMode == k_ePause || eMode == k_eClass || eMode == k_eTeam ||
			( eMode == k_eHud && ( FuiHudWantsMouse() || FuiHudWantsKeyboard() ) ) );
		const bool bInput = bMouse && !FuiYielding( m_pPanel->GetVPanel() );
		if ( m_pPanel->IsMouseInputEnabled() != bInput )
			m_pPanel->SetMouseInputEnabled( bInput );

		// The background level is a real map: its HUD (round timer and all)
		// has no business on the main menu.
		if ( g_pClientMode && g_pClientMode->GetViewport() )
		{
			static ConVarRef cl_drawhud( "cl_drawhud" );
			const bool bHud = !engine->IsLevelMainMenuBackground() && ( !cl_drawhud.IsValid() || cl_drawhud.GetBool() );
			if ( g_pClientMode->GetViewport()->IsVisible() != bHud )
				g_pClientMode->GetViewport()->SetVisible( bHud );
		}

		AutoShot( eMode );
		m_runtime.SetHudDocument( FuiHudDocument() );
		FuiHudUpdate( m_runtime, eMode == k_eHud );
		FuiTF().Update( m_runtime, bMouse );
		m_runtime.SetHotReload( fui_hot_reload.GetBool() );
		if ( bShow )
			m_runtime.Update();
		FuiStageUpdate( s_pStage, m_runtime, bMouse );
	}

	// -fui_autoshot home,war,locker: once the menu is up, show each screen
	// for a few seconds and take a screenshot of it (screenshots/fui_<name>.jpg),
	// then quit if -fui_autoshot_quit. For documentation and checking work.
	void AutoShot( Mode_t eMode )
	{
		RunScheduled();

		// -fui_autoexec <cfg>: run a script once the first match is up (HUD
		// screenshots in a real fight, see tools/fui-preview/README.md).
		static bool s_bExecDone = false;
		if ( !s_bExecDone && eMode == k_eHud && C_BasePlayer::GetLocalPlayer() )
		{
			s_bExecDone = true;
			if ( const char *pszCfg = CommandLine()->ParmValue( "-fui_autoexec", (const char *)NULL ) )
				engine->ClientCmd_Unrestricted( CFmtStr( "exec %s\n", pszCfg ) );
		}
		static CUtlStringList s_Screens;
		static int s_nNext = -1;
		static float s_flAt = 0.f;
		if ( s_nNext == -1 )
		{
			const char *pszList = CommandLine()->ParmValue( "-fui_autoshot", (const char *)NULL );
			if ( !pszList )
			{
				s_nNext = -2;
				return;
			}
			V_SplitString( pszList, ",", s_Screens );
			s_nNext = 0;
			s_flAt = 0.f;
		}
		if ( s_nNext < 0 || eMode != k_eMenu )
			return;
		const float flNow = (float)Plat_FloatTime();
		if ( s_flAt == 0.f )
		{
			s_flAt = flNow + 12.f;	// let the world, avatars and the GC arrive
			return;
		}
		if ( flNow < s_flAt )
			return;
		const int nScreen = s_nNext / 2;
		if ( nScreen >= s_Screens.Count() )
		{
			s_nNext = -2;
			if ( CommandLine()->FindParm( "-fui_autoshot_quit" ) )
				engine->ClientCmd_Unrestricted( "quit\n" );
			return;
		}
		if ( s_nNext % 2 == 0 )
		{
			m_runtime.Navigate( s_Screens[ nScreen ] );
			s_flAt = flNow + 4.f;	// entrances and the characters' animation
		}
		else
		{
			engine->ClientCmd_Unrestricted( CFmtStr( "jpeg fui_%s 92\n", s_Screens[ nScreen ] ) );
			s_flAt = flNow + 1.f;
		}
		++s_nNext;
	}

	Mode_t CurrentMode() const
	{
		if ( engine->IsDrawingLoadingImage() )
			return k_eHidden;
		const bool bInMatch = engine->IsInGame() && !engine->IsLevelMainMenuBackground();
		if ( !bInMatch )
		{
			// At the main menu, with or without the background level (which
			// counts as connected to a local server).
			const bool bMenu = enginevgui->IsGameUIVisible() && ( !engine->IsConnected() || engine->IsLevelMainMenuBackground() );
			return bMenu ? k_eMenu : k_eHidden;
		}
		if ( s_bTeamSelect && fui_team_select.GetBool() )
			return k_eTeam;
		if ( s_bClassSelect && fui_class_select.GetBool() )
			return k_eClass;
		if ( enginevgui->IsGameUIVisible() )
			return fui_pause_menu.GetBool() ? k_ePause : k_eHidden;
		return fui_hud.GetBool() ? k_eHud : k_eHidden;
	}

	void EnterMode( Mode_t eMode )
	{
		const Mode_t eOld = m_eMode;
		m_eMode = eMode;
		m_runtime.SetHud( eMode == k_eHud );
		switch ( eMode )
		{
		case k_eMenu:
			// Back at the main menu from a match: its results if it was played
			// to the end, home otherwise.
			if ( FuiTF().TakeResults() )
				m_runtime.Navigate( "results" );
			else if ( eOld != k_eHidden || m_runtime.Screen() == "pause" || m_runtime.Screen() == "classes" || m_runtime.Screen() == "teams" )
				m_runtime.Navigate( "home" );
			break;
		case k_ePause: m_runtime.Navigate( "pause" ); break;
		case k_eClass: m_runtime.Navigate( "classes" ); break;
		case k_eTeam: m_runtime.Navigate( "teams" ); break;
		default: break;
		}
	}

	virtual void Shutdown() OVERRIDE
	{
		if ( m_pPanel )
		{
			delete m_pPanel;
			m_pPanel = NULL;
		}
		s_Foreign.RemoveAll();
		s_PopupsBefore.RemoveAll();
		s_flWatchUntil = 0.;
		FuiStageDestroy( s_pStage );
		s_pStage = NULL;
		FuiTF().Detach();
		m_runtime.Shutdown();
		m_backend.Shutdown();
	}

	bool BOwnsMenu() const { return !m_bFailed && enginevgui->IsGameUIVisible(); }
	bool BReplacesMenu() const { return !m_bFailed; }
	fui::Runtime *Runtime() { return m_runtime.IsInitialized() ? &m_runtime : NULL; }

	void Restart()
	{
		Shutdown();
		m_bFailed = false;
	}

private:
	void Start()
	{
		int w, h;
		vgui::surface()->GetScreenSize( w, h );
		if ( w <= 0 || h <= 0 )
			return;

		m_backend.Init();
		m_host.m_pPanel = NULL;
		if ( !m_runtime.Init( &m_host, &m_backend, w, h ) )
		{
			Warning( "[fui] The Frontress menu did not start; using Team Fortress' own. See the messages above.\n" );
			m_bFailed = true;
			m_backend.Shutdown();
			return;
		}
		FuiTF().Attach( m_runtime );
		// -fui_screen locker: open on a screen, for working on it.
		if ( const char *pszScreen = CommandLine()->ParmValue( "-fui_screen", (const char *)NULL ) )
			m_runtime.Navigate( pszScreen );
		s_pStage = FuiStageCreate( enginevgui->GetPanel( PANEL_CLIENTDLL ) );
		m_pPanel = new CFuiPanel( enginevgui->GetPanel( PANEL_GAMEUIDLL ), &m_runtime, &m_backend );
		m_pPanel->SetBounds( 0, 0, w, h );
		m_host.m_pPanel = m_pPanel;
	}

	fui::Runtime m_runtime;
	CFuiMaterialBackend m_backend;
	CFuiGameHost m_host;
	CFuiPanel *m_pPanel;
	bool m_bFailed;
	Mode_t m_eMode;
};

static CFuiSystem s_FuiSystem;

fui::Runtime *FrontressUI()
{
	return s_FuiSystem.Runtime();
}

bool FrontressUIOwnsMenu()
{
	return fui_menu.GetBool() && s_FuiSystem.BOwnsMenu();
}

bool FrontressUIReplacesMenu()
{
	return fui_menu.GetBool() && s_FuiSystem.BReplacesMenu();
}

static void FuiMenuChanged( IConVar *pVar, const char *pszOld, float flOld )
{
	if ( !fui_menu.GetBool() )
		s_FuiSystem.Restart();
}

bool FrontressUISetClassSelect( bool bOpen )
{
	if ( bOpen && ( !fui_menu.GetBool() || !fui_class_select.GetBool() || !FrontressUI() ) )
		return false;
	if ( bOpen && !s_bClassSelect )
	{
		C_TFPlayer *pLocal = C_TFPlayer::GetLocalTFPlayer();
		s_iClassAtSelect = pLocal && pLocal->GetPlayerClass() ? pLocal->GetPlayerClass()->GetClassIndex() : 0;
	}
	s_bClassSelect = bOpen;
	return true;
}

bool FrontressUISetTeamSelect( bool bOpen )
{
	if ( bOpen && ( !fui_menu.GetBool() || !fui_team_select.GetBool() || !FrontressUI() ) )
		return false;
	if ( bOpen && !s_bTeamSelect )
	{
		C_TFPlayer *pLocal = C_TFPlayer::GetLocalTFPlayer();
		s_iTeamAtSelect = pLocal ? pLocal->GetTeamNumber() : TEAM_UNASSIGNED;
		s_bTeamPicked = false;
	}
	s_bTeamSelect = bOpen;
	return true;
}

void FrontressUITeamPicked()
{
	s_bTeamPicked = true;
}

CON_COMMAND( fui_reload, "Reload the Frontress menu's documents, styles and strings." )
{
	if ( fui::Runtime *pRuntime = FrontressUI() )
		pRuntime->Reload();
}

CON_COMMAND( fui_restart, "Restart the Frontress menu from scratch (fonts included)." )
{
	s_FuiSystem.Restart();
}

CON_COMMAND( fui_screen, "Show a Frontress menu screen: home, play, war, locker, career, results." )
{
	if ( args.ArgC() > 1 )
	{
		if ( fui::Runtime *pRuntime = FrontressUI() )
			pRuntime->Navigate( args[ 1 ] );
	}
}

CON_COMMAND( fui_cmd, "Run a Frontress UI command as if a document sent it: fui_cmd <name> [args...]. For testing and scripts." )
{
	if ( args.ArgC() < 2 || !FrontressUI() )
		return;
	std::vector< std::string > rest;
	for ( int i = 2; i < args.ArgC(); ++i )
		rest.push_back( args[ i ] );
	FuiTF().OnCommand( args[ 1 ], rest );
}

CON_COMMAND( fui_set, "Set a Frontress menu state value: fui_set <path> <json value>." )
{
	fui::Runtime *pRuntime = FrontressUI();
	if ( !pRuntime || args.ArgC() < 3 )
		return;
	fui::Node value;
	if ( !fui::Node::Parse( args.ArgS() + V_strlen( args[ 1 ] ) + 1, value ) )
		value.SetString( args[ 2 ] );
	pRuntime->Set( args[ 1 ], std::move( value ) );
}
