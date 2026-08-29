//========= Copyright Team Frontress, All rights reserved. ====================//
//
// Purpose: What the app's own inventory looks like. See tf_steam_inventory.h
//          for where the items come from.
//
//          The layout is built in code rather than in a .res, like the rest of
//          the Frontress menu: the panel has to work in an installed game
//          whose resource pak we do not build.
//
//=============================================================================//

#ifndef TF_INVENTORY_PANEL_H
#define TF_INVENTORY_PANEL_H
#ifdef _WIN32
#pragma once
#endif

#include "vgui_controls/Frame.h"
#include "steam/isteaminventory.h"
#include "utlvector.h"

namespace vgui { class Label; }
class CExButton;
class CItemModelPanel;

//-----------------------------------------------------------------------------
// A list of what the player owns on the left, the item the game would draw for
// it on the right.
//-----------------------------------------------------------------------------
class CFrontressInventoryPanel : public vgui::Frame
{
	DECLARE_CLASS_SIMPLE( CFrontressInventoryPanel, vgui::Frame );
public:
	CFrontressInventoryPanel( vgui::Panel *pParent );

	virtual void ApplySchemeSettings( vgui::IScheme *pScheme ) OVERRIDE;
	virtual void PerformLayout() OVERRIDE;
	virtual void OnCommand( const char *pszCommand ) OVERRIDE;
	virtual void OnThink() OVERRIDE;

	// Bring the panel up, creating it if this is the first time. Also asks the
	// service for anything new: a player looking at their inventory is exactly
	// when a drop is worth spending.
	static void ShowPanel();

private:
	void Rebuild();          // rows, from what the player owns
	void UpdateSelection();  // the right-hand side, from the selected row
	void UpdateStatus();

	// The most items the list shows. It does not scroll, and an app with more
	// definitions than this wants a real backpack rather than a longer list.
	static const int k_nMaxRows = 12;

	CUtlVector< CExButton * >     m_vecRows;
	CUtlVector< SteamItemDef_t >  m_vecRowDefs;

	CItemModelPanel *m_pPreview;
	vgui::Label     *m_pItemName;
	vgui::Label     *m_pItemType;
	vgui::Label     *m_pItemDescription;
	vgui::Label     *m_pStatus;
	vgui::Label     *m_pHint;
	CExButton       *m_pClaimButton;
	CExButton       *m_pRefreshButton;
	CExButton       *m_pCloseButton;

	SteamItemDef_t m_nSelectedDef;
	uint32         m_nSeenGeneration;
};

#endif // TF_INVENTORY_PANEL_H
