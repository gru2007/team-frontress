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
class CExButton;
class KeyValues;

//-----------------------------------------------------------------------------
// Text for a panel that paints itself. A leading # is a localization token, and
// a token this repository ships without a translation still reads in English
// rather than as its own name. Shared with the campaign map.
//-----------------------------------------------------------------------------
void TFMenu_TextToUnicode( const char *pszText, wchar_t *pwszOut, int nSizeInBytes );

// A localized sentence with named substitutions, false when the language file
// has no such token -- the caller then builds an English one by hand.
bool TFMenu_LocalizedText( const char *pszToken, KeyValues *pVariables,
                           wchar_t *pwszOut, int nSizeInBytes );

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
// The campaign line, drawn with VGUI's own surface: nodes owned by a side, the
// edges between them, and a ring on the front that is live.
//
// This is the fallback. The campaign card is normally the web map (see
// tf_campaign_map.h), which can draw territory, a front line and a capture
// backdrop; this one is what tf_campaign_map_html 0 gets, and what is left if
// the web view cannot start. Both read the same campaign, through
// CTFCampaignModel.
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
	// Takes the side as an int rather than the model's enum so this header does
	// not have to know about the model at all -- the map's own header includes
	// this one, for the card chrome.
	void  NodePos( float flX, float flY, int x, int y, int wide, int tall, int &outX, int &outY ) const;
	void  DrawDisc( int cx, int cy, int nRadius, const Color &color );
	void  DrawThickLine( int x0, int y0, int x1, int y1, int nThickness, const Color &color );
	Color SideColor( int eSide, int nAlpha ) const;

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
// The friends block, which is a card around Team Fortress' own Steam friends
// list. It is built here rather than in the menu's .res because that .res
// lives in a pak we do not build, and the block was dropped from it.
//-----------------------------------------------------------------------------
class CTFMenuFriendsPanel : public CTFMenuCardPanel
{
	DECLARE_CLASS_SIMPLE( CTFMenuFriendsPanel, CTFMenuCardPanel );
public:
	CTFMenuFriendsPanel( vgui::Panel *pParent, const char *pszName );

	virtual void ApplySchemeSettings( vgui::IScheme *pScheme ) OVERRIDE;
	virtual void PerformLayout() OVERRIDE;

private:
	class CSteamFriendsListPanel *m_pFriends;
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

	// Shut the full-screen campaign map, if it is open. The menu calls this
	// when the column itself is taken down -- in a game the menu is the pause
	// screen, which is no place for a war map.
	void CloseCampaignMap();

private:
	// Exactly one of these is built, by tf_campaign_map_html: the web map, or
	// the line VGUI draws itself.
	class CTFCampaignWebCard *m_pCampaignWeb;
	CTFCampaignMapPanel      *m_pCampaign;

	// The theater the web card's button opens. Lives on the menu rather than in
	// the column so it can cover the screen; NULL with the web map off.
	class CTFCampaignMapDialog *m_pCampaignDialog;

	CTFQueueInfoPanel   *m_pQueue;
	CTFMenuNewsPanel    *m_pNews;
};

#endif // TF_MAINMENU_INFO_H
