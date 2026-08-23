//========= Copyright Team Frontress, All rights reserved. ====================//
//
// Purpose: The information column on the main menu -- the campaign map, what
//          the queue is doing, and the news.
//
//          None of it is content. The map and the news are read out of
//          KeyValues files under resource/ui/, and the queue readout comes
//          from CTFMMBackend. A demo campaign and a demo news file ship with
//          the game so the panels have something to draw before the
//          coordinator is answering.
//
//=============================================================================//

#ifndef TF_MAINMENU_INFO_H
#define TF_MAINMENU_INFO_H
#ifdef _WIN32
#pragma once
#endif

#include "vgui_controls/EditablePanel.h"
#include "utlstring.h"
#include "utlvector.h"

namespace vgui { class Label; }

//-----------------------------------------------------------------------------
// One card in the column: a titled box. The cards paint their own chrome so
// the column can be assembled without a layout file, which matters because the
// menu's .res lives in a pak we do not build.
//-----------------------------------------------------------------------------
class CTFMenuCardPanel : public vgui::EditablePanel
{
	DECLARE_CLASS_SIMPLE( CTFMenuCardPanel, vgui::EditablePanel );
public:
	CTFMenuCardPanel( vgui::Panel *pParent, const char *pszName, const char *pszTitle );

	virtual void ApplySchemeSettings( vgui::IScheme *pScheme ) OVERRIDE;
	virtual void Paint() OVERRIDE;

	// Where a subclass may draw: below the title bar, inside the margins.
	void GetContentBounds( int &x, int &y, int &wide, int &tall );

protected:
	int Inset();
	int TitleHeight();

	void DrawTextAt( vgui::HFont hFont, const Color &color, int x, int y, const wchar_t *pwsz );
	// Draws at most nMaxLines of wrapped text and returns the y below it.
	int  DrawWrappedText( vgui::HFont hFont, const Color &color, int x, int y, int wide,
	                      int nMaxLines, const wchar_t *pwsz );
	int  TextWidth( vgui::HFont hFont, const wchar_t *pwsz );
	int  TextHeight( vgui::HFont hFont );

	vgui::HFont m_hTitleFont;
	vgui::HFont m_hBodyFont;
	vgui::HFont m_hSmallFont;

	Color m_colTitle;
	Color m_colBody;
	Color m_colDim;
	Color m_colAccent;
	Color m_colRule;

private:
	CUtlString m_strTitleToken;
	wchar_t    m_wszTitle[ 64 ];
};

//-----------------------------------------------------------------------------
// The campaign line: nodes owned by a side, edges between them, and the front
// currently being fought over. Read from resource/ui/frontress_campaign.res.
//-----------------------------------------------------------------------------
class CTFCampaignMapPanel : public CTFMenuCardPanel
{
	DECLARE_CLASS_SIMPLE( CTFCampaignMapPanel, CTFMenuCardPanel );
public:
	CTFCampaignMapPanel( vgui::Panel *pParent, const char *pszName );

	virtual void ApplySchemeSettings( vgui::IScheme *pScheme ) OVERRIDE;
	virtual void Paint() OVERRIDE;
	virtual void OnTick() OVERRIDE;

	// Re-read the campaign file. Cheap, and the only way to see an edit.
	void Reload();

private:
	enum ESide
	{
		k_eSide_Neutral = 0,
		k_eSide_Red,
		k_eSide_Blu,
	};

	struct Node_t
	{
		CUtlString strID;
		wchar_t    wszName[ 32 ];
		ESide      eOwner;
		float      flX;			// 0..1 across the map area
		float      flY;			// 0..1 down the map area
	};

	struct Edge_t
	{
		int nA;
		int nB;
	};

	int   FindNode( const char *pszID ) const;
	void  NodePos( const Node_t &node, int x, int y, int wide, int tall, int &outX, int &outY ) const;
	void  DrawDisc( int cx, int cy, int nRadius, const Color &color );
	void  DrawThickLine( int x0, int y0, int x1, int y1, int nThickness, const Color &color );
	Color SideColor( ESide eSide, int nAlpha ) const;
	static ESide SideFromString( const char *pszSide );

	CUtlVector< Node_t > m_Nodes;
	CUtlVector< Edge_t > m_Edges;

	int     m_nFrontNode;		// index into m_Nodes, -1 when nothing is live
	ESide   m_eFrontAttacker;
	int     m_nStage;
	int     m_nStageCount;
	wchar_t m_wszStatus[ 96 ];
	wchar_t m_wszStage[ 96 ];
	wchar_t m_wszEmpty[ 96 ];

	Color m_colRed;
	Color m_colBlu;
	Color m_colNeutral;

	int   m_nWhiteTexture;
	float m_flPulse;
};

//-----------------------------------------------------------------------------
// What matchmaking is doing right now, and how busy the service is.
//-----------------------------------------------------------------------------
class CTFQueueInfoPanel : public CTFMenuCardPanel
{
	DECLARE_CLASS_SIMPLE( CTFQueueInfoPanel, CTFMenuCardPanel );
public:
	CTFQueueInfoPanel( vgui::Panel *pParent, const char *pszName );

	virtual void Paint() OVERRIDE;
	virtual void OnTick() OVERRIDE;

private:
	float m_flBar;			// eased towards the real fraction
	float m_flBarTarget;
	float m_flPulse;
	bool  m_bQueued;
};

//-----------------------------------------------------------------------------
// News, read from resource/ui/frontress_news.res.
//-----------------------------------------------------------------------------
class CTFMenuNewsPanel : public CTFMenuCardPanel
{
	DECLARE_CLASS_SIMPLE( CTFMenuNewsPanel, CTFMenuCardPanel );
public:
	CTFMenuNewsPanel( vgui::Panel *pParent, const char *pszName );

	virtual void ApplySchemeSettings( vgui::IScheme *pScheme ) OVERRIDE;
	virtual void Paint() OVERRIDE;

	void Reload();

private:
	struct Item_t
	{
		wchar_t wszDate[ 24 ];
		wchar_t wszTitle[ 64 ];
		wchar_t wszBody[ 192 ];
	};

	CUtlVector< Item_t > m_Items;
	wchar_t m_wszEmpty[ 96 ];
};

//-----------------------------------------------------------------------------
// The column itself. Owns the cards and stacks them.
//-----------------------------------------------------------------------------
class CTFMainMenuInfoPanel : public vgui::EditablePanel
{
	DECLARE_CLASS_SIMPLE( CTFMainMenuInfoPanel, vgui::EditablePanel );
public:
	CTFMainMenuInfoPanel( vgui::Panel *pParent, const char *pszName );

	virtual void PerformLayout() OVERRIDE;

	void Reload();

private:
	CTFCampaignMapPanel *m_pCampaign;
	CTFQueueInfoPanel   *m_pQueue;
	CTFMenuNewsPanel    *m_pNews;
};

#endif // TF_MAINMENU_INFO_H
