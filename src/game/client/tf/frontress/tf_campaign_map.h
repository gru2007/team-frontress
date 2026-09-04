//========= Copyright Team Frontress, All rights reserved. ====================//
//
// Purpose: The campaign map on the main menu.
//
//          The map itself is a web page -- resource/html/campaign.html, served
//          by the same local server that serves the HTML menu -- because a war
//          map wants gradients, curves and a front line, and VGUI's surface
//          gives us filled rectangles and textured polygons.
//
//          What the page draws is not in the page: it reads one document from
//          GET /v1/campaign, which CTFCampaignModel builds here out of the
//          campaign file and whatever the coordinator last said. So the page is
//          a renderer, the game stays the authority, and pointing the map at a
//          live theater later is a change to this file only.
//
//          Two panels use it: a card in the menu's information column, and the
//          full-screen theater the card's button opens.
//
//=============================================================================//

#ifndef TF_CAMPAIGN_MAP_H
#define TF_CAMPAIGN_MAP_H
#ifdef _WIN32
#pragma once
#endif

#include "tf_mainmenu_info.h"

#include "vgui_controls/EditablePanel.h"
#include "utlbuffer.h"
#include "utlstring.h"
#include "utlvector.h"

class CExButton;
class CInteractiveWebPanel;

// Which campaign card the menu builds: the web map, or the VGUI line.
extern ConVar tf_campaign_map_html;

enum ETFCampaignSide
{
	k_eTFCampaignSide_Neutral = 0,
	k_eTFCampaignSide_Red,
	k_eTFCampaignSide_Blu,
};

//-----------------------------------------------------------------------------
// The war: who holds what, what is connected to what, and where the fighting
// is. Read from resource/ui/frontress_campaign.res, whose shape is the war
// layer's own shape (wire.WarStatus), so a coordinator that publishes fronts
// fills the same structure.
//-----------------------------------------------------------------------------
class CTFCampaignModel
{
public:
	CTFCampaignModel();

	struct Node_t
	{
		CUtlString strID;
		wchar_t    wszName[ 48 ];
		ETFCampaignSide eOwner;
		float      flX;			// 0..1 across the theater
		float      flY;			// 0..1 down the theater
		bool       bHQ;			// a side's home node: losing it loses the war
		CUtlString strKind;		// flavour for the page: industrial, rail, ...
		CUtlString strRegion;	// which datacentre serves it, when we know
		int        nPlayers;	// people fighting over it right now
		int        nBattles;	// matches live on it right now
	};

	struct Edge_t
	{
		int nA;
		int nB;
	};

	struct Front_t
	{
		int             nNode;		// index into the nodes, -1 when unresolved
		ETFCampaignSide eAttacker;
		int             nStage;
		int             nStageCount;
		float           flProgress;	// 0..1 through the current stage
		int             nPlayers;
		CUtlString      strKind;	// breakthrough, assault, advance, skirmish
		CUtlString      strMap;
		CUtlString      strServer;
	};

	struct Server_t
	{
		CUtlString strID;
		CUtlString strName;
		CUtlString strRegion;
		CUtlString strMap;
		CUtlString strNode;
		int        nPlayers;
		int        nMaxPlayers;
	};

	// Re-read the campaign file. Cheap, and the only way to see an edit.
	void Reload();

	bool IsEmpty() const { return m_Nodes.Count() == 0; }
	bool BDemo() const { return m_bDemo; }

	int FindNode( const char *pszID ) const;

	const CUtlVector< Node_t >   &Nodes() const   { return m_Nodes; }
	const CUtlVector< Edge_t >   &Edges() const   { return m_Edges; }
	const CUtlVector< Front_t >  &Fronts() const  { return m_Fronts; }
	const CUtlVector< Server_t > &Servers() const { return m_Servers; }

	const wchar_t *Name() const { return m_wszName; }

	// The one front the small card talks about: the first one listed.
	const Front_t *PrimaryFront() const { return m_Fronts.Count() ? &m_Fronts[0] : NULL; }

	// The whole document the campaign page reads, as JSON: the war above plus
	// the queue and the coordinator's population, so the page makes one request
	// and knows everything.
	void BuildDocument( CUtlBuffer &buf, const char *pszDeployNode ) const;

private:
	static ETFCampaignSide SideFromString( const char *pszSide );

	CUtlVector< Node_t >   m_Nodes;
	CUtlVector< Edge_t >   m_Edges;
	CUtlVector< Front_t >  m_Fronts;
	CUtlVector< Server_t > m_Servers;

	wchar_t m_wszName[ 64 ];
	bool    m_bDemo;		// the file says so: the numbers on it are made up
};

//-----------------------------------------------------------------------------
// Keeps the document the page reads up to date, and carries back what the page
// asks for. One of these; the panels drive it while they are visible.
//-----------------------------------------------------------------------------
class CTFCampaignFeed
{
public:
	CTFCampaignFeed();

	// Publish the campaign if it is time to, and pick up whatever the page has
	// asked the game to do. Safe to call every frame; does very little.
	void Update();

	void Reload();

	const CTFCampaignModel &Model() const { return m_model; }

	// Where the player has asked to be sent. Empty until they pick somewhere.
	const char *GetDeployNode() const { return m_strDeployNode.Get(); }

	// True once, when the page's close button was pressed.
	bool BTakeCloseRequest();

private:
	void ConsumeCommands();

	CTFCampaignModel m_model;
	CUtlString       m_strDeployNode;
	float            m_flNextPublish;
	bool             m_bCloseRequested;
	bool             m_bLoaded;
};

CTFCampaignFeed *TFCampaignFeed();

//-----------------------------------------------------------------------------
// The full-screen theater: the same page, drawn large, where a node can be
// picked. Opened by the card, closed by its own button or by Escape.
//-----------------------------------------------------------------------------
class CTFCampaignMapDialog : public vgui::EditablePanel
{
	DECLARE_CLASS_SIMPLE( CTFCampaignMapDialog, vgui::EditablePanel );
public:
	CTFCampaignMapDialog( vgui::Panel *pParent, const char *pszName );

	virtual void ApplySchemeSettings( vgui::IScheme *pScheme ) OVERRIDE;
	virtual void PerformLayout() OVERRIDE;
	virtual void OnTick() OVERRIDE;
	virtual void OnCommand( const char *pszCommand ) OVERRIDE;
	virtual void OnKeyCodeTyped( vgui::KeyCode code ) OVERRIDE;
	virtual void Paint() OVERRIDE;
	virtual void PaintBackground() OVERRIDE;

	void ShowDialog();
	void CloseDialog();

private:
	CInteractiveWebPanel *m_pWeb;
	CExButton            *m_pCloseButton;
	Color                 m_colBackdrop;
};

//-----------------------------------------------------------------------------
// The card in the information column. The page draws the map; the card draws
// the chrome around it and owns the button that opens the theater.
//-----------------------------------------------------------------------------
class CTFCampaignWebCard : public CTFMenuCardPanel
{
	DECLARE_CLASS_SIMPLE( CTFCampaignWebCard, CTFMenuCardPanel );
public:
	CTFCampaignWebCard( vgui::Panel *pParent, const char *pszName, CTFCampaignMapDialog *pDialog );

	virtual void ApplySchemeSettings( vgui::IScheme *pScheme ) OVERRIDE;
	virtual void PerformLayout() OVERRIDE;
	virtual void OnTick() OVERRIDE;
	virtual void OnCommand( const char *pszCommand ) OVERRIDE;

	void Reload();

private:
	CInteractiveWebPanel *m_pWeb;
	CExButton            *m_pOpenButton;
	vgui::DHANDLE< CTFCampaignMapDialog > m_hDialog;
	bool                  m_bWebStarted;
};

#endif // TF_CAMPAIGN_MAP_H
