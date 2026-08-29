//========= Copyright Team Frontress, All rights reserved. ====================//
//
// Purpose: The app's own inventory, on screen. See tf_inventory_panel.h.
//
//=============================================================================//

#include "cbase.h"

#include "tf_inventory_panel.h"
#include "tf_steam_inventory.h"

#include <vgui/ILocalize.h>
#include <vgui/IScheme.h>
#include <vgui/ISurface.h>
#include <vgui/IVGui.h>
#include <vgui_controls/Label.h>

#include "econ_controls.h"
#include "econ_item_view.h"
#include "fmtstr.h"
#include "ienginevgui.h"
#include "item_model_panel.h"

// memdbgon must be the last include file in a .cpp file!!!
#include "tier0/memdbgon.h"

using namespace vgui;

extern ConVar tf_steaminv_testitem;
extern ConVar tf_steaminv_droplist;

//-----------------------------------------------------------------------------
// The English strings, for a client whose language file does not carry these
// tokens yet -- which is every client until the pak is rebuilt. Same shape as
// the menu column's table next door.
//-----------------------------------------------------------------------------
struct InvTextFallback_t
{
	const char *pszToken;
	const char *pszEnglish;
};

static const InvTextFallback_t s_InvTextFallbacks[] =
{
	{ "#Frontress_Inv_Title",       "PLAYTEST INVENTORY" },
	{ "#Frontress_Inv_Unavailable", "Steam is not answering, so there is no inventory to show." },
	{ "#Frontress_Inv_Loading",     "Reading your inventory..." },
	{ "#Frontress_Inv_Empty",       "You own nothing from this app yet." },
	{ "#Frontress_Inv_NoModel",     "This item has no model of its own." },
	{ "#Frontress_Inv_NoSelection", "Pick an item on the left." },
	{ "#Frontress_Inv_Claim",       "Claim test item" },
	{ "#Frontress_Inv_Refresh",     "Refresh" },
	{ "#Frontress_Inv_Close",       "Close" },
	{ "#Frontress_Inv_Free",        "Everything here is free. Items live on your Steam account, not in the game." },
};

//-----------------------------------------------------------------------------
static void InvText( const char *pszText, wchar_t *pwszOut, int nSizeInBytes )
{
	if ( !pszText || !pszText[0] )
	{
		pwszOut[0] = L'\0';
		return;
	}

	if ( pszText[0] == '#' )
	{
		const wchar_t *pwszFound = g_pVGuiLocalize->Find( pszText );
		if ( pwszFound )
		{
			V_wcsncpy( pwszOut, pwszFound, nSizeInBytes );
			return;
		}

		for ( int i = 0; i < ARRAYSIZE( s_InvTextFallbacks ); ++i )
		{
			if ( !V_stricmp( pszText, s_InvTextFallbacks[i].pszToken ) )
			{
				g_pVGuiLocalize->ConvertANSIToUnicode( s_InvTextFallbacks[i].pszEnglish, pwszOut, nSizeInBytes );
				return;
			}
		}
	}

	g_pVGuiLocalize->ConvertANSIToUnicode( pszText, pwszOut, nSizeInBytes );
}

//-----------------------------------------------------------------------------
static void SetLabelText( Label *pLabel, const char *pszText )
{
	if ( !pLabel )
		return;

	wchar_t wszText[ 512 ];
	InvText( pszText, wszText, sizeof( wszText ) );
	pLabel->SetText( wszText );
}

static DHANDLE< CFrontressInventoryPanel > g_hInventoryPanel;

//=============================================================================
CFrontressInventoryPanel::CFrontressInventoryPanel( Panel *pParent )
	: BaseClass( pParent, "FrontressInventory" )
	, m_nSelectedDef( 0 )
	, m_nSeenGeneration( 0 )
{
	SetScheme( scheme()->LoadSchemeFromFileEx( enginevgui->GetPanel( PANEL_CLIENTDLL ),
	                                           "resource/ClientScheme.res", "ClientScheme" ) );

	SetSizeable( false );
	SetMoveable( true );
	SetCloseButtonVisible( true );
	SetDeleteSelfOnClose( false );

	wchar_t wszTitle[ 128 ];
	InvText( "#Frontress_Inv_Title", wszTitle, sizeof( wszTitle ) );
	SetTitle( wszTitle, true );

	m_pPreview         = new CItemModelPanel( this, "Preview" );
	m_pItemName        = new Label( this, "ItemName", "" );
	m_pItemType        = new Label( this, "ItemType", "" );
	m_pItemDescription = new Label( this, "ItemDescription", "" );
	m_pStatus          = new Label( this, "Status", "" );
	m_pHint            = new Label( this, "Hint", "" );

	m_pClaimButton   = new CExButton( this, "ClaimButton",   "", this, "claim" );
	m_pRefreshButton = new CExButton( this, "RefreshButton", "", this, "refresh" );
	m_pCloseButton   = new CExButton( this, "CloseButton",   "", this, "close" );

	m_pItemDescription->SetWrap( true );
	m_pStatus->SetWrap( true );
	m_pHint->SetWrap( true );

	// The preview draws the model, not the backpack icon, and nothing else:
	// the name and the description on the right are the ones the inventory
	// service gave us, which are not the game item's.
	m_pPreview->SetActAsButton( false, false );
}

//-----------------------------------------------------------------------------
void CFrontressInventoryPanel::ApplySchemeSettings( IScheme *pScheme )
{
	BaseClass::ApplySchemeSettings( pScheme );

	SetBgColor( pScheme->GetColor( "TFDarkBrown", Color( 51, 47, 42, 255 ) ) );

	const HFont hBody  = pScheme->GetFont( "HudFontSmall", true );
	const HFont hSmall = pScheme->GetFont( "HudFontSmallest", true );

	m_pItemName->SetFont( pScheme->GetFont( "HudFontMediumSmall", true ) );
	m_pItemName->SetFgColor( pScheme->GetColor( "TanLight", Color( 235, 226, 202, 255 ) ) );
	m_pItemType->SetFont( hSmall );
	m_pItemType->SetFgColor( pScheme->GetColor( "TanDark", Color( 160, 152, 137, 255 ) ) );
	m_pItemDescription->SetFont( hSmall );
	m_pItemDescription->SetFgColor( pScheme->GetColor( "TanDark", Color( 160, 152, 137, 255 ) ) );
	m_pStatus->SetFont( hBody );
	m_pStatus->SetFgColor( pScheme->GetColor( "TanLight", Color( 235, 226, 202, 255 ) ) );
	m_pHint->SetFont( hSmall );
	m_pHint->SetFgColor( pScheme->GetColor( "TanDark", Color( 160, 152, 137, 255 ) ) );

	SetLabelText( m_pClaimButton,   "#Frontress_Inv_Claim" );
	SetLabelText( m_pRefreshButton, "#Frontress_Inv_Refresh" );
	SetLabelText( m_pCloseButton,   "#Frontress_Inv_Close" );
	SetLabelText( m_pHint,          "#Frontress_Inv_Free" );

	// The model panel loads its own layout out of the game's resources, so it
	// has to be made to do that before our settings can win over it.
	m_pPreview->InvalidateLayout( true, true );

	// The panel draws the item and nothing else. Anything the model itself
	// wants has to go in the "itemmodelpanel" block, which is the one the
	// embedded model panel inside is given.
	KeyValuesAD pPreviewKV( "Preview" );
	pPreviewKV->SetInt( "model_only", 1 );
	pPreviewKV->SetInt( "model_center_x", 1 );
	pPreviewKV->SetInt( "model_center_y", 1 );

	KeyValues *pModelKV = new KeyValues( "itemmodelpanel" );
	pModelKV->SetInt( "force_use_model", 1 );        // the model, not the backpack icon
	pModelKV->SetFloat( "model_rotate_yaw_speed", 20.f );
	pPreviewKV->AddSubKey( pModelKV );

	m_pPreview->ApplySettings( pPreviewKV );

	Rebuild();
}

//-----------------------------------------------------------------------------
void CFrontressInventoryPanel::PerformLayout()
{
	BaseClass::PerformLayout();

	const int nWide = GetWide();
	const int nTall = GetTall();
	const int nMargin = MAX( 8, nWide / 40 );
	const int nTitle  = MAX( 24, nTall / 12 );      // the frame's own title bar

	const int nListWide = ( nWide - nMargin * 3 ) / 3;
	const int nRightX   = nMargin * 2 + nListWide;
	const int nRightW   = nWide - nRightX - nMargin;

	const int nButtonTall = MAX( 20, nTall / 14 );
	const int nRowTall    = MAX( 16, nTall / 18 );
	const int nGap        = MAX( 2, nRowTall / 6 );

	const int nBottom = nTall - nMargin - nButtonTall;

	// Left: the status line, then a row per owned item.
	int nY = nTitle + nMargin;
	m_pStatus->SetBounds( nMargin, nY, nListWide, nRowTall * 2 );
	nY += nRowTall * 2 + nGap;

	FOR_EACH_VEC( m_vecRows, i )
	{
		m_vecRows[i]->SetBounds( nMargin, nY, nListWide, nRowTall );
		nY += nRowTall + nGap;
	}

	// Right: the model, then what the service says about the item.
	const int nPreviewTall = ( nBottom - nTitle - nMargin ) / 2;
	m_pPreview->SetBounds( nRightX, nTitle + nMargin, nRightW, nPreviewTall );

	nY = nTitle + nMargin + nPreviewTall + nGap;
	m_pItemName->SetBounds( nRightX, nY, nRightW, nRowTall );
	nY += nRowTall;
	m_pItemType->SetBounds( nRightX, nY, nRightW, nRowTall );
	nY += nRowTall + nGap;
	m_pItemDescription->SetBounds( nRightX, nY, nRightW, nRowTall * 3 );

	// Bottom: what the player may do, which is claim, refresh and leave.
	const int nButtonWide = ( nWide - nMargin * 4 ) / 3;
	m_pClaimButton->SetBounds( nMargin, nBottom, nButtonWide, nButtonTall );
	m_pRefreshButton->SetBounds( nMargin * 2 + nButtonWide, nBottom, nButtonWide, nButtonTall );
	m_pCloseButton->SetBounds( nMargin * 3 + nButtonWide * 2, nBottom, nButtonWide, nButtonTall );

	m_pHint->SetBounds( nMargin, nBottom - nRowTall - nGap, nWide - nMargin * 2, nRowTall );
}

//-----------------------------------------------------------------------------
void CFrontressInventoryPanel::OnThink()
{
	BaseClass::OnThink();

	// The service answers on its own schedule; redraw when it actually said
	// something new rather than on every frame.
	const uint32 nGeneration = FrontressInventory()->GetGeneration();
	if ( nGeneration != m_nSeenGeneration )
	{
		m_nSeenGeneration = nGeneration;
		Rebuild();
	}
}

//-----------------------------------------------------------------------------
void CFrontressInventoryPanel::Rebuild()
{
	CFrontressInventory *pInventory = FrontressInventory();

	// One row per definition the player owns something of. Stacks of the same
	// definition are one row: the quantity is on it.
	m_vecRowDefs.Purge();

	const CUtlVector< FrontressItem_t > &vecItems = pInventory->GetItems();
	FOR_EACH_VEC( vecItems, i )
	{
		if ( m_vecRowDefs.Find( vecItems[i].nDefID ) == m_vecRowDefs.InvalidIndex()
		     && m_vecRowDefs.Count() < k_nMaxRows )
		{
			m_vecRowDefs.AddToTail( vecItems[i].nDefID );
		}
	}

	// Rows are made once and reused: the list is short and rebuilt whenever
	// anything changes, and deleting panels under the mouse is how a click
	// ends up in a panel that is no longer there.
	while ( m_vecRows.Count() < m_vecRowDefs.Count() )
	{
		CExButton *pRow = new CExButton( this, CFmtStr( "Row%d", m_vecRows.Count() ), "", this, "" );
		pRow->SetContentAlignment( Label::a_west );
		m_vecRows.AddToTail( pRow );
	}

	FOR_EACH_VEC( m_vecRows, i )
	{
		CExButton *pRow = m_vecRows[i];
		const bool bUsed = ( i < m_vecRowDefs.Count() );

		pRow->SetVisible( bUsed );
		if ( !bUsed )
			continue;

		const SteamItemDef_t nDefID = m_vecRowDefs[i];
		const FrontressItemDef_t *pDef = pInventory->FindDefinition( nDefID );
		const int nQuantity = pInventory->GetQuantity( nDefID );

		wchar_t wszRow[ 128 ];
		wchar_t wszName[ 96 ];
		InvText( pDef ? pDef->strName.Get() : CFmtStr( "Item %d", nDefID ).Get(), wszName, sizeof( wszName ) );

		if ( nQuantity > 1 )
		{
			V_snwprintf( wszRow, ARRAYSIZE( wszRow ), L"%ls  x%d", wszName, nQuantity );
		}
		else
		{
			V_wcsncpy( wszRow, wszName, sizeof( wszRow ) );
		}

		pRow->SetText( wszRow );
		pRow->SetCommand( CFmtStr( "select %d", nDefID ) );
	}

	// A selection that is no longer owned is not a selection.
	if ( m_vecRowDefs.Find( m_nSelectedDef ) == m_vecRowDefs.InvalidIndex() )
	{
		m_nSelectedDef = m_vecRowDefs.Count() ? m_vecRowDefs[0] : 0;
	}

	UpdateStatus();
	UpdateSelection();
	InvalidateLayout();
}

//-----------------------------------------------------------------------------
void CFrontressInventoryPanel::UpdateStatus()
{
	CFrontressInventory *pInventory = FrontressInventory();

	const char *pszStatus = "#Frontress_Inv_Empty";
	if ( !pInventory->BAvailable() )
	{
		pszStatus = "#Frontress_Inv_Unavailable";
	}
	else if ( !pInventory->BItemsKnown() )
	{
		pszStatus = "#Frontress_Inv_Loading";
	}
	else if ( m_vecRowDefs.Count() > 0 )
	{
		pszStatus = "";
	}

	// An error the service reported is worth more than any of the above.
	if ( pInventory->GetLastError() && pInventory->GetLastError()[0] )
	{
		pszStatus = pInventory->GetLastError();
	}

	SetLabelText( m_pStatus, pszStatus );

	// Claiming is only offered while there is something claimable: a promo
	// definition the player does not already own.
	const SteamItemDef_t nTestItem = tf_steaminv_testitem.GetInt();
	const bool bCanClaim = pInventory->BAvailable()
	                    && nTestItem > 0
	                    && pInventory->GetQuantity( nTestItem ) == 0;
	m_pClaimButton->SetEnabled( bCanClaim );
}

//-----------------------------------------------------------------------------
void CFrontressInventoryPanel::UpdateSelection()
{
	CFrontressInventory *pInventory = FrontressInventory();
	const FrontressItemDef_t *pDef = m_nSelectedDef ? pInventory->FindDefinition( m_nSelectedDef ) : NULL;

	if ( !pDef )
	{
		m_pPreview->SetItem( NULL );
		SetLabelText( m_pItemName, "" );
		SetLabelText( m_pItemType, "#Frontress_Inv_NoSelection" );
		SetLabelText( m_pItemDescription, "" );
		return;
	}

	SetLabelText( m_pItemName, pDef->strName.Get() );
	SetLabelText( m_pItemType, pDef->strDisplayType.Get() );
	SetLabelText( m_pItemDescription, pDef->strDescription.Get() );

	// What the game draws for it, if the definition named something the item
	// schema knows. An item that names nothing is not an error: a token or a
	// currency has no model to show.
	const int nGameItemDef = pInventory->GetGameItemDefIndex( pDef );
	if ( nGameItemDef <= 0 )
	{
		// The panel is drawing the model and nothing else, so the note has to
		// go somewhere the player is actually looking.
		m_pPreview->SetItem( NULL );
		SetLabelText( m_pItemType, "#Frontress_Inv_NoModel" );
		return;
	}

	CEconItemView item;
	item.SetItemDefIndex( nGameItemDef );
	item.SetItemQuality( AE_UNIQUE );
	item.SetItemLevel( 0 );
	item.SetInitialized( true );
	item.SetItemOriginOverride( kEconItemOrigin_Invalid );

	m_pPreview->SetItem( &item );
}

//-----------------------------------------------------------------------------
void CFrontressInventoryPanel::OnCommand( const char *pszCommand )
{
	if ( !V_strnicmp( pszCommand, "select ", 7 ) )
	{
		m_nSelectedDef = V_atoi( pszCommand + 7 );
		UpdateSelection();
		return;
	}

	if ( !V_stricmp( pszCommand, "claim" ) )
	{
		FrontressInventory()->GrantPromoItem( tf_steaminv_testitem.GetInt() );
		return;
	}

	if ( !V_stricmp( pszCommand, "refresh" ) )
	{
		FrontressInventory()->Refresh( true );
		return;
	}

	if ( !V_stricmp( pszCommand, "close" ) )
	{
		Close();
		return;
	}

	BaseClass::OnCommand( pszCommand );
}

//-----------------------------------------------------------------------------
void CFrontressInventoryPanel::ShowPanel()
{
	if ( !g_hInventoryPanel.Get() )
	{
		CFrontressInventoryPanel *pPanel = new CFrontressInventoryPanel( NULL );
		pPanel->SetParent( enginevgui->GetPanel( PANEL_GAMEUIDLL ) );
		g_hInventoryPanel = SETUP_PANEL( pPanel );
	}

	CFrontressInventoryPanel *pPanel = g_hInventoryPanel.Get();

	// Half the screen, and never so small that the model has nowhere to go.
	const int nWide = MAX( 480, (int)( ScreenWidth() * 0.6f ) );
	const int nTall = MAX( 320, (int)( ScreenHeight() * 0.6f ) );
	pPanel->SetSize( nWide, nTall );
	pPanel->MoveToCenterOfScreen();

	pPanel->Activate();
	pPanel->MoveToFront();
	pPanel->RequestFocus();

	// A player looking at their inventory is the moment the API documentation
	// asks for: ask for what is new, and spend whatever playtime credit Steam
	// has been holding for us. Both are free, and both are no-ops when there
	// is nothing to hand over.
	FrontressInventory()->Refresh( true );
	FrontressInventory()->TriggerDrop( tf_steaminv_droplist.GetInt() );

	pPanel->Rebuild();
}

//-----------------------------------------------------------------------------
CON_COMMAND( tf_steaminv_open, "Open the app's own inventory." )
{
	// From in-game the panel would open behind the world, since it lives in
	// the game UI like the rest of the menu.
	engine->ExecuteClientCmd( "gameui_activate" );
	CFrontressInventoryPanel::ShowPanel();
}
