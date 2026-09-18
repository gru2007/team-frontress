//========= Copyright Team Frontress, All rights reserved. ====================//
//
// Purpose: The information column on the main menu. See tf_mainmenu_info.h.
//
//=============================================================================//

#include "cbase.h"

#include "tf_mainmenu_info.h"

#include "tf_mm_backend.h"
#include "tf_campaign_map.h"

#include "fmtstr.h"
#include "filesystem.h"
#include "tf_friends_panel.h"
#include "tf_match_description.h"
#include "tf_matchmaking_shared.h"

#include <vgui/ILocalize.h>
#include <vgui/IScheme.h>
#include <vgui/ISurface.h>
#include <vgui/IVGui.h>

// memdbgon must be the last include file in a .cpp file!!!
#include "tier0/memdbgon.h"

using namespace vgui;

static const float k_flPi = 3.14159265f;

#define NEWS_FILE		"resource/ui/frontress_news.res"

struct MenuTextFallback_t
{
	const char *pszToken;
	const char *pszEnglish;
};

// The repository only supplies the Russian overlay; the full English token
// table remains in the game's pak.  These fallbacks keep a non-Russian client
// readable when it encounters one of our new, loose-resource tokens.
static const MenuTextFallback_t s_MenuTextFallbacks[] =
{
	{ "#Frontress_Menu_Campaign", "CAMPAIGN" },
	{ "#Frontress_Menu_NoCampaign", "No campaign is running." },
	{ "#Frontress_Menu_Matchmaking", "MATCHMAKING" },
	{ "#Frontress_Menu_NotQueued", "Not queued" },
	{ "#Frontress_Menu_MatchFull", "Match full - starting a server" },
	{ "#Frontress_Menu_Searching", "Searching for a match" },
	{ "#Frontress_Menu_ServerReady", "Server ready - joining" },
	{ "#Frontress_Menu_Connecting", "Connecting to the server" },
	{ "#Frontress_Menu_InMatch", "In a match" },
	{ "#Frontress_Menu_QueueHint", "Press Find a Game to join a queue." },
	{ "#Frontress_Menu_Contacting", "Contacting the coordinator..." },
	{ "#Frontress_Menu_CoordinatorUnavailable", "Coordinator unavailable." },
	{ "#Frontress_Menu_Updates", "UPDATES" },
	{ "#Frontress_Menu_NoNews", "No news." },
	{ "#Frontress_Menu_Friends", "FRIENDS" },
	{ "#Frontress_Menu_CloseMap", "CLOSE  (ESC)" },
	{ "#Frontress_Menu_ExampleLine", "Example Line" },
	{ "#Frontress_Menu_RedHQ", "RED HQ" },
	{ "#Frontress_Menu_RailYard", "Rail Yard" },
	{ "#Frontress_Menu_Foundry", "Foundry" },
	{ "#Frontress_Menu_BluHQ", "BLU HQ" },
	{ "#Frontress_Menu_SawmillDepot", "Sawmill Depot" },
	{ "#Frontress_Menu_Reservoir", "Reservoir" },
	{ "#Frontress_Menu_Quarry", "Quarry" },
	{ "#Frontress_Menu_IronJunction", "Iron Junction" },
	{ "#Frontress_Menu_NewsDate24Aug", "24 AUG" },
	{ "#Frontress_Menu_NewsDate23Aug", "23 AUG" },
	{ "#Frontress_Menu_NewsMatchmakingTitle", "Matchmaking is live" },
	{ "#Frontress_Menu_NewsMatchmakingBody", "Queue from Find a Game. The coordinator forms the match and hands you the server." },
	{ "#Frontress_Menu_NewsVGuiTitle", "The VGUI menu is back" },
	{ "#Frontress_Menu_NewsVGuiBody", "tf_main_menu_html 0 is the default again while the web menu is repaired." },
	{ "#Frontress_Menu_NewsCampaignTitle", "Campaign, work in progress" },
	{ "#Frontress_Menu_NewsCampaignBody", "The line above is a demo. Fronts move once the coordinator publishes them." },
};

//-----------------------------------------------------------------------------
void TFMenu_TextToUnicode( const char *pszText, wchar_t *pwszOut, int nSizeInBytes )
{
	if ( !pszText || !pszText[0] )
	{
		pwszOut[0] = L'\0';
		return;
	}

	// A leading # means it is a localization token, the same as anywhere else.
	if ( pszText[0] == '#' )
	{
		const wchar_t *pwszFound = g_pVGuiLocalize->Find( pszText );
		if ( pwszFound )
		{
			V_wcsncpy( pwszOut, pwszFound, nSizeInBytes );
			return;
		}

		for ( int i = 0; i < ARRAYSIZE( s_MenuTextFallbacks ); ++i )
		{
			if ( !V_stricmp( pszText, s_MenuTextFallbacks[i].pszToken ) )
			{
				g_pVGuiLocalize->ConvertANSIToUnicode( s_MenuTextFallbacks[i].pszEnglish,
				                                           pwszOut, nSizeInBytes );
				return;
			}
		}
	}

	g_pVGuiLocalize->ConvertANSIToUnicode( pszText, pwszOut, nSizeInBytes );
}

//-----------------------------------------------------------------------------
// Purpose: Build a localized menu sentence with named substitutions.  Keeping
//          these strings in the game's language file is important here: this
//          panel is painted by hand, so it cannot rely on a .res Label to do
//          localization for it.
//-----------------------------------------------------------------------------
bool TFMenu_LocalizedText( const char *pszToken, KeyValues *pVariables,
	                       wchar_t *pwszOut, int nSizeInBytes )
{
	const wchar_t *pwszFormat = g_pVGuiLocalize->Find( pszToken );
	if ( !pwszFormat )
		return false;

	g_pVGuiLocalize->ConstructString( pwszOut, nSizeInBytes, pwszFormat, pVariables );
	return true;
}

//=============================================================================
// CTFMenuCardPanel
//=============================================================================
CTFMenuCardPanel::CTFMenuCardPanel( Panel *pParent, const char *pszName, const char *pszTitle )
	: BaseClass( pParent, pszName )
	, m_strTitleToken( pszTitle )
{
	m_wszTitle[0] = L'\0';

	m_hTitleFont = INVALID_FONT;
	m_hBodyFont  = INVALID_FONT;
	m_hSmallFont = INVALID_FONT;

	// Nothing in the column is clickable yet, and a panel that eats the cursor
	// over the whole right half of the menu would be a bug, not a feature.
	SetMouseInputEnabled( false );
	SetKeyBoardInputEnabled( false );
}

//-----------------------------------------------------------------------------
void CTFMenuCardPanel::ApplySchemeSettings( IScheme *pScheme )
{
	BaseClass::ApplySchemeSettings( pScheme );

	m_hTitleFont = pScheme->GetFont( "HudFontSmallBold", true );
	m_hBodyFont  = pScheme->GetFont( "HudFontSmallestBold", true );
	m_hSmallFont = pScheme->GetFont( "FontStorePrice", true );

	m_colTitle  = pScheme->GetColor( "TanLight", Color( 235, 226, 202, 255 ) );
	m_colBody   = pScheme->GetColor( "TanLight", Color( 235, 226, 202, 255 ) );
	m_colDim    = pScheme->GetColor( "TanDark", Color( 117, 107, 94, 255 ) );
	m_colAccent = pScheme->GetColor( "TFOrange", Color( 145, 73, 59, 255 ) );
	m_colRule   = Color( m_colDim.r(), m_colDim.g(), m_colDim.b(), 140 );

	SetBorder( pScheme->GetBorder( "MainMenuBGBorder" ) );
	SetPaintBackgroundEnabled( true );
	SetPaintBackgroundType( 0 );
	SetBgColor( Color( 0, 0, 0, 90 ) );

	TFMenu_TextToUnicode( m_strTitleToken.Get(), m_wszTitle, sizeof( m_wszTitle ) );
}

//-----------------------------------------------------------------------------
int CTFMenuCardPanel::Inset()
{
	return MAX( 4, GetWide() / 32 );
}

//-----------------------------------------------------------------------------
int CTFMenuCardPanel::TitleHeight()
{
	if ( m_hTitleFont == INVALID_FONT )
		return Inset();

	return Inset() + surface()->GetFontTall( m_hTitleFont ) + Inset() / 2 + 2;
}

//-----------------------------------------------------------------------------
void CTFMenuCardPanel::GetContentBounds( int &x, int &y, int &wide, int &tall )
{
	const int nInset = Inset();

	x    = nInset;
	y    = TitleHeight() + nInset / 2;
	wide = MAX( 1, GetWide() - nInset * 2 );
	tall = MAX( 1, GetTall() - y - nInset );
}

//-----------------------------------------------------------------------------
void CTFMenuCardPanel::DrawTextAt( HFont hFont, const Color &color, int x, int y, const wchar_t *pwsz )
{
	if ( hFont == INVALID_FONT || !pwsz || !pwsz[0] )
		return;

	surface()->DrawSetTextFont( hFont );
	surface()->DrawSetTextColor( color );
	surface()->DrawSetTextPos( x, y );
	surface()->DrawPrintText( pwsz, V_wcslen( pwsz ) );
}

//-----------------------------------------------------------------------------
// Purpose: Word-wrap, because the only alternative in VGUI is a Label per line.
//			Stops after nMaxLines rather than growing the card.
//-----------------------------------------------------------------------------
int CTFMenuCardPanel::DrawWrappedText( HFont hFont, const Color &color, int x, int y, int wide,
                                       int nMaxLines, const wchar_t *pwsz )
{
	if ( hFont == INVALID_FONT || !pwsz || !pwsz[0] )
		return y;

	const int nLineTall = surface()->GetFontTall( hFont );

	wchar_t wszLine[ 256 ];
	wszLine[0] = L'\0';

	int nLineLen = 0;
	int nLines   = 0;

	for ( const wchar_t *pWord = pwsz; *pWord && nLines < nMaxLines; )
	{
		const wchar_t *pWordEnd = pWord;
		while ( *pWordEnd && *pWordEnd != L' ' )
			++pWordEnd;

		const wchar_t *pNext = pWordEnd;
		while ( *pNext == L' ' )
			++pNext;

		const int nWordLen = (int)( pWordEnd - pWord );
		if ( nWordLen <= 0 )
		{
			pWord = pNext;
			continue;
		}

		if ( nLineLen + nWordLen + 1 >= ARRAYSIZE( wszLine ) )
			break;

		// Try the word on the end of the line we are building.
		int nEnd = nLineLen;
		if ( nLineLen > 0 )
			wszLine[ nEnd++ ] = L' ';
		for ( int i = 0; i < nWordLen; ++i )
			wszLine[ nEnd + i ] = pWord[i];
		wszLine[ nEnd + nWordLen ] = L'\0';

		int nTextWide = 0, nTextTall = 0;
		surface()->GetTextSize( hFont, wszLine, nTextWide, nTextTall );

		if ( nTextWide > wide && nLineLen > 0 )
		{
			// Does not fit: flush what we had and start again with this word.
			wszLine[ nLineLen ] = L'\0';
			DrawTextAt( hFont, color, x, y + nLines * nLineTall, wszLine );
			++nLines;

			for ( int i = 0; i < nWordLen; ++i )
				wszLine[i] = pWord[i];
			wszLine[ nWordLen ] = L'\0';
			nLineLen = nWordLen;
		}
		else
		{
			nLineLen = nEnd + nWordLen;
		}

		pWord = pNext;
	}

	if ( nLineLen > 0 && nLines < nMaxLines )
	{
		wszLine[ nLineLen ] = L'\0';
		DrawTextAt( hFont, color, x, y + nLines * nLineTall, wszLine );
		++nLines;
	}

	return y + nLines * nLineTall;
}

//-----------------------------------------------------------------------------
int CTFMenuCardPanel::TextWidth( HFont hFont, const wchar_t *pwsz )
{
	if ( hFont == INVALID_FONT || !pwsz || !pwsz[0] )
		return 0;

	int nWide = 0, nTall = 0;
	surface()->GetTextSize( hFont, pwsz, nWide, nTall );
	return nWide;
}

//-----------------------------------------------------------------------------
int CTFMenuCardPanel::TextHeight( HFont hFont )
{
	return ( hFont == INVALID_FONT ) ? 0 : surface()->GetFontTall( hFont );
}

//-----------------------------------------------------------------------------
// Purpose: The card's chrome: the title and the rule under it. Subclasses call
//			this first and then draw inside GetContentBounds.
//-----------------------------------------------------------------------------
void CTFMenuCardPanel::Paint()
{
	BaseClass::Paint();

	const int nInset = Inset();

	DrawTextAt( m_hTitleFont, m_colTitle, nInset, nInset / 2, m_wszTitle );

	const int nRuleY = TitleHeight() - 2;

	surface()->DrawSetColor( m_colRule );
	surface()->DrawFilledRect( nInset, nRuleY, GetWide() - nInset, nRuleY + 1 );

	// A short bright run at the left end, so the eye finds the top of the card.
	surface()->DrawSetColor( m_colAccent );
	surface()->DrawFilledRect( nInset, nRuleY, nInset + GetWide() / 5, nRuleY + 1 );
}

//=============================================================================
// CTFCampaignMapPanel
//=============================================================================
CTFCampaignMapPanel::CTFCampaignMapPanel( Panel *pParent, const char *pszName )
	: BaseClass( pParent, pszName, "#Frontress_Menu_Campaign" )
{
	m_wszStatus[0]   = L'\0';
	m_wszStage[0]    = L'\0';
	m_wszEmpty[0]    = L'\0';
	m_nWhiteTexture  = -1;
	m_flPulse        = 0.f;

	ivgui()->AddTickSignal( GetVPanel(), 33 );
}

//-----------------------------------------------------------------------------
void CTFCampaignMapPanel::ApplySchemeSettings( IScheme *pScheme )
{
	BaseClass::ApplySchemeSettings( pScheme );

	m_colRed     = pScheme->GetColor( "RedSolid", Color( 192, 28, 0, 255 ) );
	m_colBlu     = Color( 88, 133, 162, 255 );
	m_colNeutral = pScheme->GetColor( "TanDark", Color( 117, 107, 94, 255 ) );

	m_nWhiteTexture = surface()->DrawGetTextureId( "vgui/white" );
	if ( m_nWhiteTexture == -1 )
	{
		m_nWhiteTexture = surface()->CreateNewTextureID();
		surface()->DrawSetTextureFile( m_nWhiteTexture, "vgui/white", true, false );
	}

	Reload();
}

//-----------------------------------------------------------------------------
// Purpose: Re-read the campaign and rebuild the two sentences under the map.
//			The campaign itself lives in CTFCampaignModel, which the web map
//			reads too -- this panel only draws it.
//-----------------------------------------------------------------------------
void CTFCampaignMapPanel::Reload()
{
	m_wszStatus[0] = L'\0';
	m_wszStage[0]  = L'\0';

	TFMenu_TextToUnicode( "#Frontress_Menu_NoCampaign", m_wszEmpty, sizeof( m_wszEmpty ) );

	CTFCampaignFeed *pFeed = TFCampaignFeed();
	pFeed->Reload();

	const CTFCampaignModel &model = pFeed->Model();
	const CTFCampaignModel::Front_t *pFront = model.PrimaryFront();
	if ( !pFront )
		return;

	const CTFCampaignModel::Node_t &node = model.Nodes()[ pFront->nNode ];

	const char *pszAttacker = ( pFront->eAttacker == k_eTFCampaignSide_Blu ) ? "BLU" : "RED";

	// The name is already wide, and a Cyrillic node name does not survive a trip
	// through the ANSI codepage on its way into the sentence.
	KeyValuesAD pStatusVars( "CampaignStatus" );
	pStatusVars->SetString( "team", pszAttacker );
	pStatusVars->SetWString( "node", node.wszName );
	if ( !TFMenu_LocalizedText( "#Frontress_Menu_Attacking", pStatusVars,
	                            m_wszStatus, sizeof( m_wszStatus ) ) )
	{
		// Only a client with no string for the token gets here, which is to say
		// an English one, whose node names are ASCII.
		char szNodeName[ 128 ];
		szNodeName[0] = '\0';
		V_UnicodeToUTF8( node.wszName, szNodeName, sizeof( szNodeName ) );

		CFmtStr strStatus( "%s attacking %s", pszAttacker, szNodeName );
		TFMenu_TextToUnicode( strStatus.Get(), m_wszStatus, sizeof( m_wszStatus ) );
	}

	KeyValuesAD pStageVars( "CampaignStage" );
	pStageVars->SetInt( "stage", pFront->nStage );
	pStageVars->SetInt( "stages", pFront->nStageCount );
	pStageVars->SetString( "map", pFront->strMap.Get() );
	if ( !TFMenu_LocalizedText( "#Frontress_Menu_Stage", pStageVars,
	                            m_wszStage, sizeof( m_wszStage ) ) )
	{
		CFmtStr strStage( "stage %d/%d  %s", pFront->nStage, pFront->nStageCount,
		                  pFront->strMap.Get() );
		TFMenu_TextToUnicode( strStage.Get(), m_wszStage, sizeof( m_wszStage ) );
	}
}

//-----------------------------------------------------------------------------
Color CTFCampaignMapPanel::SideColor( int eSide, int nAlpha ) const
{
	const Color &base = ( eSide == k_eTFCampaignSide_Red ) ? m_colRed
	                  : ( eSide == k_eTFCampaignSide_Blu ) ? m_colBlu
	                                                       : m_colNeutral;

	return Color( base.r(), base.g(), base.b(), nAlpha );
}

//-----------------------------------------------------------------------------
void CTFCampaignMapPanel::NodePos( float flX, float flY, int x, int y, int wide, int tall,
                                   int &outX, int &outY ) const
{
	outX = x + (int)( flX * wide );
	outY = y + (int)( flY * tall );
}

//-----------------------------------------------------------------------------
void CTFCampaignMapPanel::DrawDisc( int cx, int cy, int nRadius, const Color &color )
{
	const int nSegments = 16;
	Vertex_t verts[ nSegments ];

	for ( int i = 0; i < nSegments; ++i )
	{
		const float flAngle = ( 2.f * k_flPi * i ) / nSegments;
		verts[i].Init( Vector2D( cx + nRadius * cosf( flAngle ),
		                         cy + nRadius * sinf( flAngle ) ),
		               Vector2D( 0.5f, 0.5f ) );
	}

	surface()->DrawSetTexture( m_nWhiteTexture );
	surface()->DrawSetColor( color );
	surface()->DrawTexturedPolygon( nSegments, verts );
}

//-----------------------------------------------------------------------------
void CTFCampaignMapPanel::DrawThickLine( int x0, int y0, int x1, int y1, int nThickness,
                                         const Color &color )
{
	const float flDX = (float)( x1 - x0 );
	const float flDY = (float)( y1 - y0 );
	const float flLen = sqrtf( flDX * flDX + flDY * flDY );
	if ( flLen < 0.001f )
		return;

	const float flHalf = nThickness * 0.5f;
	const float flPX = -flDY / flLen * flHalf;
	const float flPY =  flDX / flLen * flHalf;

	Vertex_t verts[4];
	verts[0].Init( Vector2D( x0 + flPX, y0 + flPY ), Vector2D( 0, 0 ) );
	verts[1].Init( Vector2D( x1 + flPX, y1 + flPY ), Vector2D( 1, 0 ) );
	verts[2].Init( Vector2D( x1 - flPX, y1 - flPY ), Vector2D( 1, 1 ) );
	verts[3].Init( Vector2D( x0 - flPX, y0 - flPY ), Vector2D( 0, 1 ) );

	surface()->DrawSetTexture( m_nWhiteTexture );
	surface()->DrawSetColor( color );
	surface()->DrawTexturedPolygon( 4, verts );
}

//-----------------------------------------------------------------------------
void CTFCampaignMapPanel::OnTick()
{
	BaseClass::OnTick();

	if ( IsVisible() )
	{
		m_flPulse = (float)Plat_FloatTime();
		Repaint();
	}
}

//-----------------------------------------------------------------------------
void CTFCampaignMapPanel::Paint()
{
	BaseClass::Paint();

	int x, y, wide, tall;
	GetContentBounds( x, y, wide, tall );

	const CTFCampaignModel &model = TFCampaignFeed()->Model();
	const CUtlVector< CTFCampaignModel::Node_t > &nodes = model.Nodes();

	if ( nodes.Count() == 0 )
	{
		DrawTextAt( m_hBodyFont, m_colDim,
		            x + ( wide - TextWidth( m_hBodyFont, m_wszEmpty ) ) / 2,
		            y + ( tall - TextHeight( m_hBodyFont ) ) / 2,
		            m_wszEmpty );
		return;
	}

	// The status line lives along the bottom; the map gets what is left.
	const int nStatusH = ( m_wszStatus[0] != L'\0' ) ? TextHeight( m_hSmallFont ) + 4 : 0;
	const int nLabelH  = TextHeight( m_hSmallFont );
	const int nMapTall = MAX( 8, tall - nStatusH - nLabelH );

	const int nRadius = clamp( nMapTall / 5, 3, 10 );
	const int nEdge   = MAX( 2, nRadius / 2 );

	// Inset the plot so a node on the edge of the line still has room for its
	// disc and its ring.
	const int nPlotX = x + nRadius * 2;
	const int nPlotY = y + nRadius + 2;
	const int nPlotW = MAX( 1, wide - nRadius * 4 );
	const int nPlotH = MAX( 1, nMapTall - nRadius * 2 );

	// Edges first, so the discs sit on top of them.
	FOR_EACH_VEC( model.Edges(), i )
	{
		const CTFCampaignModel::Node_t &a = nodes[ model.Edges()[i].nA ];
		const CTFCampaignModel::Node_t &b = nodes[ model.Edges()[i].nB ];

		int ax, ay, bx, by;
		NodePos( a.flX, a.flY, nPlotX, nPlotY, nPlotW, nPlotH, ax, ay );
		NodePos( b.flX, b.flY, nPlotX, nPlotY, nPlotW, nPlotH, bx, by );

		// A line between two nodes of the same side is held territory. A line
		// between different sides is where the fighting is.
		const bool bContested = ( a.eOwner != b.eOwner );
		const Color color = bContested ? Color( m_colAccent.r(), m_colAccent.g(), m_colAccent.b(), 220 )
		                               : SideColor( a.eOwner, 110 );

		DrawThickLine( ax, ay, bx, by, bContested ? nEdge + 1 : nEdge, color );
	}

	// The live fronts, pulsing over their nodes.
	FOR_EACH_VEC( model.Fronts(), i )
	{
		const CTFCampaignModel::Front_t &front = model.Fronts()[i];

		int fx, fy;
		NodePos( nodes[ front.nNode ].flX, nodes[ front.nNode ].flY,
		         nPlotX, nPlotY, nPlotW, nPlotH, fx, fy );

		const float flWave = 0.5f + 0.5f * sinf( m_flPulse * 2.5f );
		const int nRing = nRadius + 3 + (int)( flWave * nRadius * 0.8f );
		const int nRingAlpha = (int)( 40.f + ( 1.f - flWave ) * 110.f );

		DrawDisc( fx, fy, nRing, SideColor( front.eAttacker, nRingAlpha ) );
	}

	// Nodes.
	FOR_EACH_VEC( nodes, i )
	{
		const CTFCampaignModel::Node_t &node = nodes[i];

		int nx, ny;
		NodePos( node.flX, node.flY, nPlotX, nPlotY, nPlotW, nPlotH, nx, ny );

		DrawDisc( nx, ny, nRadius + 1, Color( 0, 0, 0, 160 ) );
		DrawDisc( nx, ny, nRadius, SideColor( node.eOwner, 255 ) );

		// Held nodes get a quiet label, a contested one gets a bright one.
		bool bFront = false;
		FOR_EACH_VEC( model.Fronts(), iFront )
		{
			if ( model.Fronts()[ iFront ].nNode == i )
			{
				bFront = true;
				break;
			}
		}

		const int nNameW = TextWidth( m_hSmallFont, node.wszName );

		DrawTextAt( m_hSmallFont, bFront ? m_colTitle : m_colDim,
		            nx - nNameW / 2, ny + nRadius + 2, node.wszName );
	}

	if ( m_wszStatus[0] != L'\0' )
	{
		const int nLineY = y + tall - TextHeight( m_hSmallFont );

		DrawTextAt( m_hSmallFont, m_colBody, x, nLineY, m_wszStatus );
		DrawTextAt( m_hSmallFont, m_colDim,
		            x + wide - TextWidth( m_hSmallFont, m_wszStage ), nLineY, m_wszStage );
	}
}

//=============================================================================
// CTFQueueInfoPanel
//=============================================================================
CTFQueueInfoPanel::CTFQueueInfoPanel( Panel *pParent, const char *pszName )
	: BaseClass( pParent, pszName, "#Frontress_Menu_Matchmaking" )
{
	m_flBar       = 0.f;
	m_flBarTarget = 0.f;
	m_flPulse     = 0.f;
	m_bQueued     = false;

	ivgui()->AddTickSignal( GetVPanel(), 33 );
}

//-----------------------------------------------------------------------------
void CTFQueueInfoPanel::OnTick()
{
	BaseClass::OnTick();

	if ( !IsVisible() )
		return;

	m_flPulse = (float)Plat_FloatTime();

	const CTFMMBackend *pBackend = TFMMBackend();
	m_bQueued = ( pBackend->GetState() == k_eTFMMState_Searching );

	if ( m_bQueued )
	{
		const int nHave = pBackend->GetQueuePlayerCount();
		const int nNeed = pBackend->GetQueueNeededCount();
		m_flBarTarget = ( nHave + nNeed > 0 ) ? ( (float)nHave / (float)( nHave + nNeed ) ) : 0.f;
	}
	else
	{
		m_flBarTarget = 0.f;
	}

	// Ease, so a queue that jumps from 11 to 24 players does not snap.
	m_flBar += ( m_flBarTarget - m_flBar ) * 0.12f;

	Repaint();
}

//-----------------------------------------------------------------------------
void CTFQueueInfoPanel::Paint()
{
	BaseClass::Paint();

	int x, y, wide, tall;
	GetContentBounds( x, y, wide, tall );

	const CTFMMBackend *pBackend = TFMMBackend();
	const ETFMMState eState = pBackend->GetState();

	const int nHave = pBackend->GetQueuePlayerCount();
	const int nNeed = pBackend->GetQueueNeededCount();

	// Line one: what matchmaking is doing. While the coordinator says we are
	// still searching but nobody else is needed, the match is full and it is
	// off reserving a server -- which is a wait worth naming, because it is
	// the one that looks like nothing is happening.
	const char *pszState = "#Frontress_Menu_NotQueued";
	Color colState = m_colDim;
	const CTFMMBackend::Status_t &status = pBackend->GetStatus();

	// An unavailable coordinator is not merely a population statistic.  Put it
	// in the prominent state line as well as the service footer so the player
	// immediately understands why matchmaking cannot start.
	if ( status.bChecked && !status.bValid && eState == k_eTFMMState_Idle )
	{
		pszState = "#Frontress_Menu_CoordinatorUnavailable";
		colState = Color( 192, 28, 0, 255 );
	}

	switch ( eState )
	{
	case k_eTFMMState_Searching:
		if ( nNeed == 0 && nHave > 0 )
		{
			pszState = "#Frontress_Menu_MatchFull";
			colState = m_colAccent;
		}
		else
		{
			pszState = "#Frontress_Menu_Searching";
			colState = m_colTitle;
		}
		break;

	case k_eTFMMState_MatchReady:
		pszState = "#Frontress_Menu_ServerReady";
		colState = Color( 94, 150, 49, 255 );
		break;

	case k_eTFMMState_Connecting:
		pszState = "#Frontress_Menu_Connecting";
		colState = Color( 94, 150, 49, 255 );
		break;

	case k_eTFMMState_InMatch:
		pszState = "#Frontress_Menu_InMatch";
		colState = m_colTitle;
		break;

	default:
		break;
	}

	wchar_t wszLine[ 128 ];
	const int nBodyTall  = TextHeight( m_hBodyFont );
	const int nSmallTall = TextHeight( m_hSmallFont );
	const int nBottom    = y + tall;

	// Walk down the card, and stop drawing rather than overlap: the card is
	// sized by the column, not by how much there is to say.
	int nY = y;

	TFMenu_TextToUnicode( pszState, wszLine, sizeof( wszLine ) );
	DrawTextAt( m_hBodyFont, colState, x, nY, wszLine );

	// The match group, right-aligned against the state.
	if ( eState == k_eTFMMState_Searching )
	{
		const IMatchGroupDescription *pDesc = GetMatchGroupDescription( pBackend->GetQueuedMatchGroup() );
		const wchar_t *pwszGroup = pDesc ? g_pVGuiLocalize->Find( pDesc->GetNameLocToken() ) : NULL;
		if ( pwszGroup )
		{
			DrawTextAt( m_hBodyFont, m_colDim,
			            x + wide - TextWidth( m_hBodyFont, pwszGroup ), nY, pwszGroup );
		}
	}

	nY += nBodyTall + MAX( 2, nSmallTall / 2 );

	// Line two: the bar, and what it is filling up with.
	const int nBarTall = MAX( 3, nSmallTall / 2 + 2 );
	if ( nY + nBarTall <= nBottom - nSmallTall )
	{
		surface()->DrawSetColor( Color( 0, 0, 0, 140 ) );
		surface()->DrawFilledRect( x, nY, x + wide, nY + nBarTall );

		if ( m_flBar > 0.001f )
		{
			const float flWave = 0.5f + 0.5f * sinf( m_flPulse * 3.f );
			const int nAlpha = m_bQueued ? (int)( 170.f + flWave * 85.f ) : 200;

			surface()->DrawSetColor( Color( m_colAccent.r(), m_colAccent.g(), m_colAccent.b(), nAlpha ) );
			surface()->DrawFilledRect( x, nY, x + (int)( wide * clamp( m_flBar, 0.f, 1.f ) ), nY + nBarTall );
		}

		surface()->DrawSetColor( m_colRule );
		surface()->DrawOutlinedRect( x, nY, x + wide, nY + nBarTall );

		nY += nBarTall + MAX( 2, nSmallTall / 2 );
	}

	// Line three: the numbers behind the bar.
	if ( eState == k_eTFMMState_Searching )
	{
		const int nSeconds = (int)pBackend->GetQueueSeconds();

		char szTime[16];
		V_snprintf( szTime, sizeof( szTime ), "%d:%02d", nSeconds / 60, nSeconds % 60 );
		KeyValuesAD pQueueVars( "QueueDetail" );
		pQueueVars->SetString( "time", szTime );
		pQueueVars->SetInt( "waiting", nHave );
		pQueueVars->SetInt( "needed", nNeed );
		if ( !TFMenu_LocalizedText( "#Frontress_Menu_QueueDetail", pQueueVars, wszLine, sizeof( wszLine ) ) )
		{
			CFmtStr strDetail( "%s in queue   -   %d waiting, %d more needed", szTime, nHave, nNeed );
			TFMenu_TextToUnicode( strDetail.Get(), wszLine, sizeof( wszLine ) );
		}
	}
	else
	{
		TFMenu_TextToUnicode( "#Frontress_Menu_QueueHint", wszLine, sizeof( wszLine ) );
	}

	if ( nY + nSmallTall <= nBottom - nSmallTall )
	{
		DrawTextAt( m_hSmallFont, m_colDim, x, nY, wszLine );
		nY += nSmallTall;
	}

	// Line four, only when there is something to say: the coordinator's own
	// account of a queue that has stopped moving. Without it a match that has
	// formed and is waiting for a server looks identical to an empty queue.
	const char *pszNote = pBackend->GetQueueDetail();
	if ( eState == k_eTFMMState_Searching && pszNote && pszNote[0] &&
	     nY + nSmallTall <= nBottom - nSmallTall )
	{
		TFMenu_TextToUnicode( pszNote, wszLine, sizeof( wszLine ) );
		DrawTextAt( m_hSmallFont, m_colAccent, x, nY, wszLine );
	}

	// Along the bottom, always: how busy the service is.
	if ( !status.bChecked )
	{
		TFMenu_TextToUnicode( "#Frontress_Menu_Contacting", wszLine, sizeof( wszLine ) );
		DrawTextAt( m_hSmallFont, m_colDim, x, nBottom - nSmallTall, wszLine );
	}
	else if ( status.bValid )
	{
		if ( status.bServerCapacityKnown )
		{
			KeyValuesAD pPopulationVars( "Population" );
			pPopulationVars->SetInt( "online", status.nOnlinePlayers );
			pPopulationVars->SetInt( "matches", status.nLiveMatches );
			pPopulationVars->SetInt( "servers", status.nFreeServers );
			if ( !TFMenu_LocalizedText( "#Frontress_Menu_Population", pPopulationVars, wszLine, sizeof( wszLine ) ) )
			{
				CFmtStr strPop( "%d online   -   %d matches live   -   %d servers free",
				                status.nOnlinePlayers, status.nLiveMatches, status.nFreeServers );
				TFMenu_TextToUnicode( strPop.Get(), wszLine, sizeof( wszLine ) );
			}
		}
		else
		{
			KeyValuesAD pPopulationVars( "Population" );
			pPopulationVars->SetInt( "online", status.nOnlinePlayers );
			pPopulationVars->SetInt( "matches", status.nLiveMatches );
			if ( !TFMenu_LocalizedText( "#Frontress_Menu_PopulationOnDemand", pPopulationVars,
			                     wszLine, sizeof( wszLine ) ) )
			{
				CFmtStr strPop( "%d online   -   %d matches live   -   servers on demand",
				                status.nOnlinePlayers, status.nLiveMatches );
				TFMenu_TextToUnicode( strPop.Get(), wszLine, sizeof( wszLine ) );
			}
		}
		DrawTextAt( m_hSmallFont, m_colDim, x, nBottom - nSmallTall, wszLine );
	}
	else
	{
		TFMenu_TextToUnicode( "#Frontress_Menu_CoordinatorUnavailable", wszLine, sizeof( wszLine ) );
		DrawTextAt( m_hSmallFont, Color( 192, 28, 0, 255 ), x, nBottom - nSmallTall, wszLine );
	}
}

//=============================================================================
// CTFMenuNewsPanel
//=============================================================================
CTFMenuNewsPanel::CTFMenuNewsPanel( Panel *pParent, const char *pszName )
	: BaseClass( pParent, pszName, "#Frontress_Menu_Updates" )
{
	m_wszEmpty[0] = L'\0';
}

//-----------------------------------------------------------------------------
void CTFMenuNewsPanel::ApplySchemeSettings( IScheme *pScheme )
{
	BaseClass::ApplySchemeSettings( pScheme );

	Reload();
}

//-----------------------------------------------------------------------------
void CTFMenuNewsPanel::Reload()
{
	m_Items.RemoveAll();

	TFMenu_TextToUnicode( "#Frontress_Menu_NoNews", m_wszEmpty, sizeof( m_wszEmpty ) );

	KeyValuesAD pNews( "News" );
	if ( !pNews->LoadFromFile( g_pFullFileSystem, NEWS_FILE, NULL ) )
		return;

	KeyValues *pItems = pNews->FindKey( "items" );
	for ( KeyValues *pItem = pItems ? pItems->GetFirstTrueSubKey() : NULL;
	      pItem != NULL;
	      pItem = pItem->GetNextTrueSubKey() )
	{
		const int iItem = m_Items.AddToTail();
		Item_t &item = m_Items[ iItem ];

		TFMenu_TextToUnicode( pItem->GetString( "date", "" ),  item.wszDate,  sizeof( item.wszDate ) );
		TFMenu_TextToUnicode( pItem->GetString( "title", "" ), item.wszTitle, sizeof( item.wszTitle ) );
		TFMenu_TextToUnicode( pItem->GetString( "body", "" ),  item.wszBody,  sizeof( item.wszBody ) );
	}
}

//-----------------------------------------------------------------------------
void CTFMenuNewsPanel::Paint()
{
	BaseClass::Paint();

	int x, y, wide, tall;
	GetContentBounds( x, y, wide, tall );

	if ( m_Items.Count() == 0 )
	{
		DrawTextAt( m_hBodyFont, m_colDim, x, y, m_wszEmpty );
		return;
	}

	const int nTitleH = TextHeight( m_hBodyFont );
	const int nBodyH  = TextHeight( m_hSmallFont );
	const int nGap    = MAX( 2, nBodyH / 2 );

	int nY = y;

	FOR_EACH_VEC( m_Items, i )
	{
		const Item_t &item = m_Items[i];

		// Stop before drawing an entry that would run out of the card.
		if ( nY + nTitleH + nBodyH > y + tall )
			break;

		DrawTextAt( m_hBodyFont, m_colTitle, x, nY, item.wszTitle );

		const int nDateW = TextWidth( m_hSmallFont, item.wszDate );
		DrawTextAt( m_hSmallFont, m_colAccent, x + wide - nDateW, nY + ( nTitleH - nBodyH ) / 2, item.wszDate );

		nY += nTitleH;
		nY  = DrawWrappedText( m_hSmallFont, m_colDim, x, nY, wide, 2, item.wszBody ) + nGap;
	}
}

//=============================================================================
// CTFMenuFriendsPanel
//=============================================================================
CTFMenuFriendsPanel::CTFMenuFriendsPanel( Panel *pParent, const char *pszName )
	: BaseClass( pParent, pszName, "#Frontress_Menu_Friends" )
{
	m_pFriends = new CSteamFriendsListPanel( this, "SteamFriendsList" );

	// The list scrolls, so unlike the other cards this one does want the mouse.
	SetMouseInputEnabled( true );
}

//-----------------------------------------------------------------------------
void CTFMenuFriendsPanel::ApplySchemeSettings( IScheme *pScheme )
{
	BaseClass::ApplySchemeSettings( pScheme );

	// The settings the menu's .res used to carry for this list, before the
	// block was dropped from it.
	KeyValuesAD pSettings( "SteamFriendsList" );
	pSettings->SetInt( "columns_count", 2 );
	pSettings->SetInt( "inset_x", 10 );
	pSettings->SetInt( "inset_y", 5 );
	pSettings->SetInt( "row_gap", 5 );
	pSettings->SetInt( "column_gap", 10 );
	pSettings->SetInt( "restrict_width", 0 );

	KeyValues *pFriendPanel = new KeyValues( "friendpanel_kv" );
	pFriendPanel->SetInt( "wide", 110 );
	pFriendPanel->SetInt( "tall", 20 );
	pSettings->AddSubKey( pFriendPanel );

	m_pFriends->ApplySettings( pSettings );
}

//-----------------------------------------------------------------------------
void CTFMenuFriendsPanel::PerformLayout()
{
	BaseClass::PerformLayout();

	int x, y, wide, tall;
	GetContentBounds( x, y, wide, tall );

	m_pFriends->SetBounds( x, y, wide, tall );
}

//=============================================================================
// CTFMainMenuInfoPanel
//=============================================================================
CTFMainMenuInfoPanel::CTFMainMenuInfoPanel( Panel *pParent, const char *pszName )
	: BaseClass( pParent, pszName )
{
	m_pCampaignWeb    = NULL;
	m_pCampaign       = NULL;
	m_pCampaignDialog = NULL;

	if ( tf_campaign_map_html.GetBool() )
	{
		// The theater belongs to the menu, not to this column: it covers the
		// screen. The column is where the card that opens it lives.
		m_pCampaignDialog = new CTFCampaignMapDialog( pParent, "CampaignMapDialog" );
		m_pCampaignWeb    = new CTFCampaignWebCard( this, "CampaignMap", m_pCampaignDialog );
	}
	else
	{
		m_pCampaign = new CTFCampaignMapPanel( this, "CampaignMap" );
	}

	m_pQueue    = new CTFQueueInfoPanel( this, "QueueInfo" );
	m_pNews     = new CTFMenuNewsPanel( this, "News" );

	// The campaign card is the one thing in the column a player can click, and
	// a parent that refuses the mouse refuses it for its children too. The
	// other cards keep saying no for themselves.
	SetMouseInputEnabled( m_pCampaignWeb != NULL );
	SetKeyBoardInputEnabled( false );
	SetPaintBackgroundEnabled( false );
}

//-----------------------------------------------------------------------------
void CTFMainMenuInfoPanel::PerformLayout()
{
	BaseClass::PerformLayout();

	const int nWide = GetWide();
	const int nTall = GetTall();
	const int nGap  = MAX( 2, nTall / 60 );

	const int nBody      = MAX( 1, nTall - nGap * 2 );
	const int nCampaignH = (int)( nBody * 0.38f );
	const int nQueueH    = (int)( nBody * 0.30f );
	const int nNewsH     = nBody - nCampaignH - nQueueH;

	int nY = 0;
	if ( m_pCampaignWeb )
	{
		m_pCampaignWeb->SetBounds( 0, nY, nWide, nCampaignH );
	}
	else
	{
		m_pCampaign->SetBounds( 0, nY, nWide, nCampaignH );
	}
	nY += nCampaignH + nGap;
	m_pQueue->SetBounds( 0, nY, nWide, nQueueH );       nY += nQueueH + nGap;
	m_pNews->SetBounds( 0, nY, nWide, nNewsH );
}

//-----------------------------------------------------------------------------
void CTFMainMenuInfoPanel::CloseCampaignMap()
{
	if ( m_pCampaignDialog )
	{
		m_pCampaignDialog->CloseDialog();
	}
}

//-----------------------------------------------------------------------------
void CTFMainMenuInfoPanel::Reload()
{
	if ( m_pCampaignWeb )
	{
		m_pCampaignWeb->Reload();
	}
	else
	{
		m_pCampaign->Reload();
	}

	m_pNews->Reload();
	InvalidateLayout();
	Repaint();
}
