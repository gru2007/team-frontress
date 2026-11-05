//========= Team Frontress ====================================================//
//
// 3D characters in the Frontress menu. See fui_stage.h.
//
//=============================================================================
#include "cbase.h"

#pragma push_macro( "Assert" )
#undef Assert
#include "tier0/valve_minmax_off.h"
#include "fui.h"
#include <RmlUi/Core/Context.h>
#include <RmlUi/Core/Element.h>
#include <RmlUi/Core/ElementDocument.h>
#include "tier0/valve_minmax_on.h"
#pragma pop_macro( "Assert" )

#include "fui_stage.h"
#include "fui_tf.h"

#include "datacache/imdlcache.h"
#include "ienginevgui.h"
#include "tf_playerclass_shared.h"
#include "tf_item_inventory.h"
#include "tf_playermodelpanel.h"
#include "vgui/IPanel.h"
#include "vgui/ISurface.h"
#include "vgui/IVGui.h"
#include "vgui_controls/Panel.h"

// memdbgon must be the last include file in a .cpp file!!!
#include "tier0/memdbgon.h"

extern const char *g_pszLegacyClassSelectVCDWeapons[ TF_LAST_NORMAL_CLASS ];
extern int g_iLegacyClassSelectWeaponSlots[ TF_LAST_NORMAL_CLASS ];

ConVar fui_menu_3d( "fui_menu_3d", "1", FCVAR_ARCHIVE, "Show live 3D characters in the main menu." );
ConVar fui_stage_frame( "fui_stage_frame", "108", FCVAR_NONE, "How many world units tall a character's box shows (smaller: bigger characters)." );
ConVar fui_stage_distance( "fui_stage_distance", "122", FCVAR_NONE, "How far the menu's camera stands from a character." );
ConVar fui_stage_debug( "fui_stage_debug", "0", FCVAR_NONE, "Print where the menu's 3D characters are, once a second." );
ConVar fui_stage_height( "fui_stage_height", "-49", FCVAR_NONE, "Camera height relative to a character's origin." );

uint32 FuiLook_t::Hash() const
{
	uint32 h = 2166136261u;
	auto mix = [ &h ]( uint32 v ) { h = ( h ^ v ) * 16777619u; };
	mix( iClass );
	mix( iTeam );
	mix( bLocalLoadout );
	FOR_EACH_VEC( vecItems, i )
	{
		const FuiLookItem_t &it = vecItems[ i ];
		uint32 wear;
		V_memcpy( &wear, &it.flWear, sizeof( wear ) );
		for ( uint32 v : { (uint32)it.iDef, (uint32)it.iQuality, it.iPaintKit, wear, (uint32)it.ulSeed, (uint32)( it.ulSeed >> 32 ),
				it.iPaint, it.iPaint2, it.iUnusual, it.iSheen, (uint32)it.bAustralium, (uint32)it.bFestive, (uint32)it.iStyle } )
			mix( v );
	}
	return h;
}

//-----------------------------------------------------------------------------
// Item looks. Attributes are stored as 32 bits: an integer's bits for the
// ones stored as integers (a war paint's index, a seed), a float's for the
// rest (wear, paint colour, effect).
//-----------------------------------------------------------------------------
static const CEconItemAttributeDefinition *AttrDef( const char *pszName )
{
	return GetItemSchema() ? GetItemSchema()->GetAttributeDefinitionByName( pszName ) : NULL;
}

static bool ReadAttr( const CEconItemView *pItem, const char *pszName, double &out )
{
	const CEconItemAttributeDefinition *pDef = AttrDef( pszName );
	uint32 bits = 0;
	if ( !pDef || !FindAttribute_UnsafeBitwiseCast< attrib_value_t >( pItem, pDef, &bits ) )
		return false;
	if ( pDef->IsStoredAsInteger() )
		out = bits;
	else
	{
		float fl;
		V_memcpy( &fl, &bits, sizeof( fl ) );
		out = fl;
	}
	return true;
}

static void WriteAttr( CEconItemView &item, const char *pszName, double value )
{
	const CEconItemAttributeDefinition *pDef = AttrDef( pszName );
	if ( !pDef )
		return;
	float fl = (float)value;
	if ( pDef->IsStoredAsInteger() )
	{
		const uint32 bits = (uint32)value;
		V_memcpy( &fl, &bits, sizeof( fl ) );
	}
	item.GetAttributeList()->SetRuntimeAttributeValue( pDef, fl );
}

void FuiLookItemRead( const CEconItemView *pItem, FuiLookItem_t &out )
{
	out = FuiLookItem_t();
	if ( !pItem || !pItem->IsValid() )
		return;
	out.iDef = pItem->GetItemDefIndex();
	out.iQuality = (uint8)pItem->GetQuality();
	double v = 0;
	if ( ReadAttr( pItem, "paintkit_proto_def_index", v ) )
		out.iPaintKit = (uint32)v;
	float flWear = 0.f;
	if ( out.iPaintKit && GetPaintKitWear( pItem, flWear ) )
		out.flWear = flWear;
	double lo = 0, hi = 0;
	if ( ReadAttr( pItem, "custom_paintkit_seed_lo", lo ) | ReadAttr( pItem, "custom_paintkit_seed_hi", hi ) )
		out.ulSeed = ( (uint64)(uint32)hi << 32 ) | (uint32)lo;
	if ( ReadAttr( pItem, "set item tint RGB", v ) )
		out.iPaint = (uint32)v;
	if ( ReadAttr( pItem, "set item tint RGB 2", v ) )
		out.iPaint2 = (uint32)v;
	if ( ReadAttr( pItem, "attach particle effect", v ) )
		out.iUnusual = (uint32)v;
	if ( ReadAttr( pItem, "killstreak idleeffect", v ) )
		out.iSheen = (uint32)v;
	out.bAustralium = ReadAttr( pItem, "is australium item", v ) && v != 0;
	out.bFestive = ReadAttr( pItem, "is_festivized", v ) && v != 0;
	out.iStyle = (uint8)pItem->GetItemStyle();
}

void FuiLookItemApply( const FuiLookItem_t &in, CEconItemView &item )
{
	if ( in.iPaintKit )
	{
		WriteAttr( item, "paintkit_proto_def_index", in.iPaintKit );
		WriteAttr( item, "set_item_texture_wear", in.flWear > 0.f ? in.flWear : 0.2f );
		if ( in.ulSeed )
		{
			WriteAttr( item, "custom_paintkit_seed_lo", (uint32)in.ulSeed );
			WriteAttr( item, "custom_paintkit_seed_hi", (uint32)( in.ulSeed >> 32 ) );
		}
	}
	if ( in.iPaint )
		WriteAttr( item, "set item tint RGB", in.iPaint );
	if ( in.iPaint2 )
		WriteAttr( item, "set item tint RGB 2", in.iPaint2 );
	if ( in.iUnusual )
		WriteAttr( item, "attach particle effect", in.iUnusual );
	if ( in.iSheen )
		WriteAttr( item, "killstreak idleeffect", in.iSheen );
	if ( in.bAustralium )
		WriteAttr( item, "is australium item", 1 );
	if ( in.bFestive )
		WriteAttr( item, "is_festivized", 1 );
	if ( in.iStyle )
		WriteAttr( item, "item style override", in.iStyle );
}

class CFuiStage : public vgui::Panel
{
	DECLARE_CLASS_SIMPLE( CFuiStage, vgui::Panel );

public:
	explicit CFuiStage( vgui::VPANEL parent ) : BaseClass( NULL, "FrontressStage" )
	{
		SetParent( parent );
		SetPaintBackgroundEnabled( false );
		SetPaintBorderEnabled( false );
		SetMouseInputEnabled( false );
		SetKeyBoardInputEnabled( false );
		// Not a popup, so the menu (a popup) is always drawn over it. Its
		// parent follows whichever root is on screen: GameUI's at the main
		// menu and in the pause menu (the engine hides the client's then),
		// the client's while playing (class select).
		SetZPos( 1000 );
		SetVisible( false );

	}

	~CFuiStage()
	{
		if ( m_nBackdropTexture >= 0 )
			vgui::surface()->DestroyTextureID( m_nBackdropTexture );
	}

	struct Slot_t
	{
		CTFPlayerModelPanel *pPanel;
		std::string key;
		uint32 nLook;
		uint32 nStock;		// the local inventory's state, for "me"
		float flFOV;
		bool bUsed;
	};

	void Sync( fui::Runtime &runtime, bool bShown )
	{
		const vgui::VPANEL root = enginevgui->GetPanel( enginevgui->IsGameUIVisible() ? PANEL_GAMEUIDLL : PANEL_CLIENTDLL );
		if ( root && GetVParent() != root )
			SetParent( root );

		int w, h;
		vgui::surface()->GetScreenSize( w, h );
		if ( GetWide() != w || GetTall() != h )
			SetBounds( 0, 0, w, h );

		FOR_EACH_VEC( m_Slots, i )
			m_Slots[ i ].bUsed = false;

		Rml::Context *pContext = runtime.Context();
		if ( bShown && pContext && ( fui_menu_3d.GetBool() || engine->IsInGame() ) )
		{
			for ( int d = 0; d < pContext->GetNumDocuments(); ++d )
			{
				Rml::ElementDocument *pDoc = pContext->GetDocument( d );
				if ( !pDoc || !pDoc->IsVisible() )
					continue;
				Rml::ElementList elements;
				pDoc->QuerySelectorAll( elements, "[fui-model]" );
				for ( Rml::Element *pElement : elements )
					Place( pElement );
			}
		}

		static float s_flNextDebug = 0.f;
		if ( fui_stage_debug.GetBool() && Plat_FloatTime() > s_flNextDebug )
		{
			s_flNextDebug = (float)Plat_FloatTime() + 1.f;
			Msg( "[fui stage] shown %d, %d slots, stage %s, fully visible %d, paints %d, parent %s, z %d, size %dx%d\n", bShown, m_Slots.Count(),
				IsVisible() ? "visible" : "hidden", vgui::ipanel()->IsFullyVisible( GetVPanel() ), m_nPaints,
				GetVParent() ? vgui::ipanel()->GetName( GetVParent() ) : "-", GetZPos(), GetWide(), GetTall() );
			FOR_EACH_VEC( m_Slots, i )
			{
				int x, y, w, h;
				m_Slots[ i ].pPanel->GetBounds( x, y, w, h );
				Msg( "  %s: %s %d,%d %dx%d\n", m_Slots[ i ].key.c_str(), m_Slots[ i ].pPanel->IsVisible() ? "visible" : "hidden", x, y, w, h );
			}
		}

		bool bAny = false;
		FOR_EACH_VEC( m_Slots, i )
		{
			Slot_t &slot = m_Slots[ i ];
			if ( !slot.bUsed && slot.pPanel->IsVisible() )
				slot.pPanel->SetVisible( false );
			bAny |= slot.bUsed;
		}
		// Behind the live 3D models, draw scenery without starting a server.
		m_bBackdrop = bShown && !engine->IsInGame();
		if ( IsVisible() != ( bAny || m_bBackdrop ) )
			SetVisible( bAny || m_bBackdrop );
	}

	// fui_stage_debug 3: outline the characters' boxes.
	int m_nPaints = 0;
	virtual void Paint() OVERRIDE
	{
		++m_nPaints;
		if ( m_bBackdrop )
			PaintBackdrop();
		if ( fui_stage_debug.GetInt() != 3 )
			return;
		vgui::surface()->DrawSetColor( 255, 0, 255, 255 );
		FOR_EACH_VEC( m_Slots, i )
		{
			if ( !m_Slots[ i ].pPanel->IsVisible() )
				continue;
			int x, y, w, h;
			m_Slots[ i ].pPanel->GetBounds( x, y, w, h );
			vgui::surface()->DrawOutlinedRect( x, y, x + w, y + h );
		}
	}

	// A TF2 loading-screen view of a map, slowly panned and zoomed (Ken
	// Burns), under the characters.
	void PaintBackdrop()
	{
		if ( m_nBackdropTexture < 0 )
		{
			static const char *s_Backdrops[] = { "console/background_upward_widescreen", "console/background_2fort_widescreen", "console/background_gravelpit_widescreen" };
			m_nBackdropTexture = vgui::surface()->CreateNewTextureID();
			vgui::surface()->DrawSetTextureFile( m_nBackdropTexture, s_Backdrops[ RandomInt( 0, ARRAYSIZE( s_Backdrops ) - 1 ) ], true, false );
		}
		const float t = (float)Plat_FloatTime();
		const float flZoom = 0.90f + 0.06f * sinf( t * 0.035f );	// share of the image shown
		const float flPanX = ( 1.f - flZoom ) * ( 0.5f + 0.5f * sinf( t * 0.021f ) );
		const float flPanY = ( 1.f - flZoom ) * ( 0.5f + 0.5f * sinf( t * 0.017f + 1.f ) );
		vgui::surface()->DrawSetColor( 255, 255, 255, 255 );
		vgui::surface()->DrawSetTexture( m_nBackdropTexture );
		vgui::surface()->DrawTexturedSubRect( 0, 0, GetWide(), GetTall(), flPanX, flPanY, flPanX + flZoom, flPanY + flZoom );
	}

	bool m_bBackdrop = false;
	int m_nBackdropTexture = -1;

private:
	Slot_t &SlotFor( const std::string &key )
	{
		FOR_EACH_VEC( m_Slots, i )
		{
			if ( m_Slots[ i ].key == key && !m_Slots[ i ].bUsed )
				return m_Slots[ i ];
		}
		// Reuse a panel nobody wants this frame before making another.
		FOR_EACH_VEC( m_Slots, i )
		{
			if ( !m_Slots[ i ].bUsed && !m_Slots[ i ].pPanel->IsVisible() )
			{
				m_Slots[ i ].key = key;
				m_Slots[ i ].nLook = 0;
				return m_Slots[ i ];
			}
		}
		Slot_t slot;
		slot.pPanel = new CTFPlayerModelPanel( this, "FrontressCharacter" );
		KeyValues *pSettings = new KeyValues( "settings" );
		pSettings->SetInt( "render_texture", 0 );
		pSettings->SetFloat( "fov", 30.f );
		pSettings->SetInt( "allow_rot", 0 );
		KeyValues *pModel = pSettings->FindKey( "model", true );
		pModel->SetInt( "force_pos", 1 );
		pModel->SetFloat( "angles_y", 170.f );
		pModel->SetFloat( "origin_x", fui_stage_distance.GetFloat() );
		pModel->SetFloat( "origin_z", fui_stage_height.GetFloat() );
		pModel->SetString( "modelname", "" );
		slot.pPanel->ApplySettings( pSettings );
		pSettings->deleteThis();
		slot.pPanel->SetMouseInputEnabled( false );
		slot.pPanel->SetKeyBoardInputEnabled( false );
		slot.pPanel->SetVisible( false );
		slot.key = key;
		slot.nLook = 0;
		slot.nStock = 0;
		slot.flFOV = 0.f;
		slot.bUsed = false;
		return m_Slots[ m_Slots.AddToTail( slot ) ];
	}

	void Place( Rml::Element *pElement )
	{
		if ( !pElement->IsVisible( true ) )
			return;
		const Rml::Variant *pKey = pElement->GetAttribute( "fui-model" );
		const std::string key = pKey ? pKey->Get< Rml::String >() : std::string();
		if ( key.empty() )
			return;

		FuiLook_t look;
		if ( !FuiTF().GetLook( key, look ) || look.iClass <= TF_CLASS_UNDEFINED || look.iClass >= TF_LAST_NORMAL_CLASS )
		{
			if ( fui_stage_debug.GetBool() )
				Msg( "[fui stage] no look for '%s'\n", key.c_str() );
			return;
		}

		const Rml::Vector2f pos = pElement->GetAbsoluteOffset( Rml::BoxArea::Border );
		const Rml::Vector2f size = pElement->GetBox().GetSize( Rml::BoxArea::Border );
		if ( size.x < 8.f || size.y < 8.f )
			return;

		Slot_t &slot = SlotFor( key );
		slot.bUsed = true;
		slot.pPanel->SetBounds( (int)pos.x, (int)pos.y, (int)size.x, (int)size.y );

		// The camera's field of view is horizontal: pick it from the box's
		// shape so a character is the same height in a wide box as in a
		// narrow one -- the box's height always shows fui_stage_frame units.
		const float flHalfV = 0.5f * fui_stage_frame.GetFloat() / fui_stage_distance.GetFloat();
		const float flFOV = RAD2DEG( 2.f * atanf( flHalfV * size.x / size.y ) );
		if ( fabsf( flFOV - slot.flFOV ) > 0.05f )
		{
			slot.pPanel->SetCameraFOV( flFOV );
			slot.flFOV = flFOV;
		}

		// Nearer characters over further ones: the z-index the document gave
		// the element or its nearest positioned ancestor.
		int z = 0;
		for ( Rml::Element *p = pElement; p && z == 0; p = p->GetParentNode() )
			z = (int)p->GetZIndex();
		if ( slot.pPanel->GetZPos() != z )
			slot.pPanel->SetZPos( z );

		// The local loadout can change under the same look (equipping in the
		// locker), so it counts as part of the look.
		const uint32 nStock = look.bLocalLoadout ? LocalLoadoutHash( look.iClass ) : 0;
		const uint32 nLook = look.Hash();
		if ( nLook != slot.nLook || nStock != slot.nStock )
		{
			const Rml::Variant *pAnim = pElement->GetAttribute( "fui-model-anim" );
			const bool bAnimate = !pAnim || pAnim->Get< Rml::String >() != "none";
			Dress( slot.pPanel, look, bAnimate && nLook != slot.nLook );
			slot.nLook = nLook;
			slot.nStock = nStock;
		}
		if ( !slot.pPanel->IsVisible() )
			slot.pPanel->SetVisible( true );
	}

	static uint32 LocalLoadoutHash( int iClass )
	{
		uint32 h = 2166136261u;
		CTFInventoryManager *pManager = TFInventoryManager();
		for ( int i = 0; pManager && i < CLASS_LOADOUT_POSITION_COUNT; ++i )
		{
			CEconItemView *pItem = pManager->GetItemInLoadoutForClass( iClass, i );
			const uint64 id = ( pItem && pItem->IsValid() ) ? pItem->GetItemID() : 0;
			h = ( h ^ (uint32)id ^ (uint32)( id >> 32 ) ^ ( pItem ? pItem->GetItemDefIndex() : 0 ) ) * 16777619u;
		}
		return h;
	}

	// Mirrors the stock menu's character (tf_hud_mainmenuoverride.cpp).
	static void Dress( CTFPlayerModelPanel *pPanel, const FuiLook_t &look, bool bEntrance )
	{
		const int iClass = look.iClass;
		pPanel->ClearCarriedItems();
		pPanel->SetTeam( look.iTeam );

		if ( look.bLocalLoadout )
		{
			CTFInventoryManager *pManager = TFInventoryManager();
			for ( int i = 0; pManager && i < CLASS_LOADOUT_POSITION_COUNT; ++i )
			{
				CEconItemView *pItem = pManager->GetItemInLoadoutForClass( iClass, i );
				if ( pItem && pItem->IsValid() )
					pPanel->AddCarriedItem( pItem );
			}
		}
		else
		{
			FOR_EACH_VEC( look.vecItems, i )
			{
				const FuiLookItem_t &it = look.vecItems[ i ];
				CEconItemView item;
				item.Init( it.iDef, it.iQuality ? it.iQuality : AE_UNIQUE, AE_USE_SCRIPT_VALUE, 0 );
				FuiLookItemApply( it, item );
				if ( item.IsValid() && item.GetStaticData() && item.GetStaticData()->CanBeUsedByClass( iClass ) )
					pPanel->AddCarriedItem( &item );
			}
		}

		pPanel->SetToPlayerClass( iClass, true );
		pPanel->SetTeam( look.iTeam );
		const int iSlot = g_iLegacyClassSelectWeaponSlots[ iClass ];
		if ( bEntrance )
			pPanel->PlayVCD( "class_select", g_pszLegacyClassSelectVCDWeapons[ iClass ], false );
		pPanel->HoldItemInSlot( iSlot );
	}

	CUtlVector< Slot_t > m_Slots;
};

CFuiStage *FuiStageCreate( uintp parentVPanel )
{
	return new CFuiStage( (vgui::VPANEL)parentVPanel );
}

void FuiStageDestroy( CFuiStage *pStage )
{
	if ( pStage )
		pStage->MarkForDeletion();
}

void FuiStageUpdate( CFuiStage *pStage, fui::Runtime &runtime, bool bShown )
{
	if ( pStage )
		pStage->Sync( runtime, bShown );
}

void FuiStageRaise( CFuiStage *pStage )
{
	if ( pStage && pStage->IsVisible() )
		pStage->MoveToFront();
}
