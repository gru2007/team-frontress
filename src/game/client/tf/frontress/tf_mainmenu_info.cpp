//========= Copyright Team Frontress, All rights reserved. ====================//
//
// Purpose: The information column on the main menu. See tf_mainmenu_info.h.
//
//=============================================================================//

#include "cbase.h"

#include "tf_mainmenu_info.h"

#include "tf_mm_backend.h"

#include "fmtstr.h"
#include "filesystem.h"
#include "econ_controls.h"
#include "tf_match_description.h"
#include "tf_matchmaking_dashboard.h"
#include "tf_matchmaking_shared.h"

#include <vgui/ILocalize.h>
#include <vgui/IScheme.h>
#include <vgui/ISurface.h>
#include <vgui/IVGui.h>

// memdbgon must be the last include file in a .cpp file!!!
#include "tier0/memdbgon.h"

using namespace vgui;

static const float k_flPi = 3.14159265f;

#define CAMPAIGN_FILE	"resource/ui/frontress_campaign.res"
#define NEWS_FILE		"resource/ui/frontress_news.res"

//-----------------------------------------------------------------------------
static void TextToUnicode( const char *pszText, wchar_t *pwszOut, int nSizeInBytes )
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
	}

	g_pVGuiLocalize->ConvertANSIToUnicode( pszText, pwszOut, nSizeInBytes );
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

	TextToUnicode( m_strTitleToken.Get(), m_wszTitle, sizeof( m_wszTitle ) );
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
	: BaseClass( pParent, pszName, "CAMPAIGN" )
{
	m_nFrontNode     = -1;
	m_eFrontAttacker = k_eSide_Neutral;
	m_nStage         = 0;
	m_nStageCount    = 0;
	m_wszStatus[0]   = L'\0';
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
CTFCampaignMapPanel::ESide CTFCampaignMapPanel::SideFromString( const char *pszSide )
{
	if ( !V_stricmp( pszSide, "RED" ) )
		return k_eSide_Red;
	if ( !V_stricmp( pszSide, "BLU" ) || !V_stricmp( pszSide, "BLUE" ) )
		return k_eSide_Blu;

	return k_eSide_Neutral;
}

//-----------------------------------------------------------------------------
int CTFCampaignMapPanel::FindNode( const char *pszID ) const
{
	FOR_EACH_VEC( m_Nodes, i )
	{
		if ( !V_stricmp( m_Nodes[i].strID.Get(), pszID ) )
			return i;
	}

	return -1;
}

//-----------------------------------------------------------------------------
// Purpose: Read the campaign. Everything drawn below comes from this file --
//			the panel has no idea what a front is called.
//-----------------------------------------------------------------------------
void CTFCampaignMapPanel::Reload()
{
	m_Nodes.RemoveAll();
	m_Edges.RemoveAll();
	m_nFrontNode  = -1;
	m_nStage      = 0;
	m_nStageCount = 0;
	m_wszStatus[0] = L'\0';
	m_wszStage[0]  = L'\0';

	TextToUnicode( "No campaign is running.", m_wszEmpty, sizeof( m_wszEmpty ) );

	KeyValuesAD pCampaign( "Campaign" );
	if ( !pCampaign->LoadFromFile( g_pFullFileSystem, CAMPAIGN_FILE, NULL ) )
		return;

	KeyValues *pNodes = pCampaign->FindKey( "nodes" );
	for ( KeyValues *pNode = pNodes ? pNodes->GetFirstTrueSubKey() : NULL;
	      pNode != NULL;
	      pNode = pNode->GetNextTrueSubKey() )
	{
		const int iNode = m_Nodes.AddToTail();
		Node_t &node = m_Nodes[ iNode ];

		node.strID  = pNode->GetName();
		node.eOwner = SideFromString( pNode->GetString( "owner", "" ) );
		node.flX    = clamp( pNode->GetFloat( "x", 0.5f ), 0.f, 1.f );
		node.flY    = clamp( pNode->GetFloat( "y", 0.5f ), 0.f, 1.f );
		TextToUnicode( pNode->GetString( "name", node.strID.Get() ), node.wszName, sizeof( node.wszName ) );
	}

	KeyValues *pEdges = pCampaign->FindKey( "edges" );
	for ( KeyValues *pEdge = pEdges ? pEdges->GetFirstTrueSubKey() : NULL;
	      pEdge != NULL;
	      pEdge = pEdge->GetNextTrueSubKey() )
	{
		const int nA = FindNode( pEdge->GetString( "a", "" ) );
		const int nB = FindNode( pEdge->GetString( "b", "" ) );
		if ( nA < 0 || nB < 0 )
			continue;

		const int iEdge = m_Edges.AddToTail();
		m_Edges[ iEdge ].nA = nA;
		m_Edges[ iEdge ].nB = nB;
	}

	KeyValues *pFront = pCampaign->FindKey( "front" );
	if ( pFront )
	{
		m_nFrontNode     = FindNode( pFront->GetString( "node", "" ) );
		m_eFrontAttacker = SideFromString( pFront->GetString( "attacker", "" ) );
		m_nStage         = pFront->GetInt( "stage", 0 );
		m_nStageCount    = pFront->GetInt( "stages", 0 );

		if ( m_nFrontNode >= 0 )
		{
			char szNodeName[ 64 ];
			g_pVGuiLocalize->ConvertUnicodeToANSI( m_Nodes[ m_nFrontNode ].wszName, szNodeName, sizeof( szNodeName ) );

			CFmtStr strStatus( "%s attacking %s",
			                   m_eFrontAttacker == k_eSide_Blu ? "BLU" : "RED",
			                   szNodeName );
			TextToUnicode( strStatus.Get(), m_wszStatus, sizeof( m_wszStatus ) );

			CFmtStr strStage( "stage %d/%d  %s", m_nStage, m_nStageCount,
			                  pFront->GetString( "map", "" ) );
			TextToUnicode( strStage.Get(), m_wszStage, sizeof( m_wszStage ) );
		}
	}
}

//-----------------------------------------------------------------------------
Color CTFCampaignMapPanel::SideColor( ESide eSide, int nAlpha ) const
{
	const Color &base = ( eSide == k_eSide_Red ) ? m_colRed
	                  : ( eSide == k_eSide_Blu ) ? m_colBlu
	                                             : m_colNeutral;

	return Color( base.r(), base.g(), base.b(), nAlpha );
}

//-----------------------------------------------------------------------------
void CTFCampaignMapPanel::NodePos( const Node_t &node, int x, int y, int wide, int tall,
                                   int &outX, int &outY ) const
{
	outX = x + (int)( node.flX * wide );
	outY = y + (int)( node.flY * tall );
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

	if ( m_Nodes.Count() == 0 )
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
	FOR_EACH_VEC( m_Edges, i )
	{
		const Node_t &a = m_Nodes[ m_Edges[i].nA ];
		const Node_t &b = m_Nodes[ m_Edges[i].nB ];

		int ax, ay, bx, by;
		NodePos( a, nPlotX, nPlotY, nPlotW, nPlotH, ax, ay );
		NodePos( b, nPlotX, nPlotY, nPlotW, nPlotH, bx, by );

		// A line between two nodes of the same side is held territory. A line
		// between different sides is where the fighting is.
		const bool bContested = ( a.eOwner != b.eOwner );
		const Color color = bContested ? Color( m_colAccent.r(), m_colAccent.g(), m_colAccent.b(), 220 )
		                               : SideColor( a.eOwner, 110 );

		DrawThickLine( ax, ay, bx, by, bContested ? nEdge + 1 : nEdge, color );
	}

	// The live front, pulsing over its node.
	if ( m_nFrontNode >= 0 )
	{
		int fx, fy;
		NodePos( m_Nodes[ m_nFrontNode ], nPlotX, nPlotY, nPlotW, nPlotH, fx, fy );

		const float flWave = 0.5f + 0.5f * sinf( m_flPulse * 2.5f );
		const int nRing = nRadius + 3 + (int)( flWave * nRadius * 0.8f );
		const int nRingAlpha = (int)( 40.f + ( 1.f - flWave ) * 110.f );

		DrawDisc( fx, fy, nRing, SideColor( m_eFrontAttacker, nRingAlpha ) );
	}

	// Nodes.
	FOR_EACH_VEC( m_Nodes, i )
	{
		const Node_t &node = m_Nodes[i];

		int nx, ny;
		NodePos( node, nPlotX, nPlotY, nPlotW, nPlotH, nx, ny );

		DrawDisc( nx, ny, nRadius + 1, Color( 0, 0, 0, 160 ) );
		DrawDisc( nx, ny, nRadius, SideColor( node.eOwner, 255 ) );

		// Held nodes get a quiet label, the contested one gets a bright one.
		const bool bFront = ( i == m_nFrontNode );
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
	: BaseClass( pParent, pszName, "MATCHMAKING" )
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
	const char *pszState = "Not queued";
	Color colState = m_colDim;

	switch ( eState )
	{
	case k_eTFMMState_Searching:
		if ( nNeed == 0 && nHave > 0 )
		{
			pszState = "Match full - starting a server";
			colState = m_colAccent;
		}
		else
		{
			pszState = "Searching for a match";
			colState = m_colTitle;
		}
		break;

	case k_eTFMMState_MatchReady:
		pszState = "Server ready - joining";
		colState = Color( 94, 150, 49, 255 );
		break;

	case k_eTFMMState_Connecting:
		pszState = "Connecting to the server";
		colState = Color( 94, 150, 49, 255 );
		break;

	case k_eTFMMState_InMatch:
		pszState = "In a match";
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

	TextToUnicode( pszState, wszLine, sizeof( wszLine ) );
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

		CFmtStr strDetail( "%d:%02d in queue   -   %d waiting, %d more needed",
		                   nSeconds / 60, nSeconds % 60, nHave, nNeed );
		TextToUnicode( strDetail.Get(), wszLine, sizeof( wszLine ) );
	}
	else
	{
		TextToUnicode( "Press Find a Game to join a queue.", wszLine, sizeof( wszLine ) );
	}

	if ( nY + nSmallTall <= nBottom - nSmallTall )
	{
		DrawTextAt( m_hSmallFont, m_colDim, x, nY, wszLine );
	}

	// Along the bottom, always: how busy the service is.
	const CTFMMBackend::Status_t &status = pBackend->GetStatus();
	if ( !status.bChecked )
	{
		TextToUnicode( "Contacting the coordinator...", wszLine, sizeof( wszLine ) );
		DrawTextAt( m_hSmallFont, m_colDim, x, nBottom - nSmallTall, wszLine );
	}
	else if ( status.bValid )
	{
		CFmtStr strPop( "%d online   -   %d matches live   -   %d servers free",
		                status.nOnlinePlayers, status.nLiveMatches, status.nFreeServers );
		TextToUnicode( strPop.Get(), wszLine, sizeof( wszLine ) );
		DrawTextAt( m_hSmallFont, m_colDim, x, nBottom - nSmallTall, wszLine );
	}
	else
	{
		TextToUnicode( "Coordinator unreachable.", wszLine, sizeof( wszLine ) );
		DrawTextAt( m_hSmallFont, Color( 192, 28, 0, 255 ), x, nBottom - nSmallTall, wszLine );
	}
}

//=============================================================================
// CTFMenuNewsPanel
//=============================================================================
CTFMenuNewsPanel::CTFMenuNewsPanel( Panel *pParent, const char *pszName )
	: BaseClass( pParent, pszName, "NEWS" )
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

	TextToUnicode( "No news.", m_wszEmpty, sizeof( m_wszEmpty ) );

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

		TextToUnicode( pItem->GetString( "date", "" ),  item.wszDate,  sizeof( item.wszDate ) );
		TextToUnicode( pItem->GetString( "title", "" ), item.wszTitle, sizeof( item.wszTitle ) );
		TextToUnicode( pItem->GetString( "body", "" ),  item.wszBody,  sizeof( item.wszBody ) );
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
// CTFMenuActionsPanel
//=============================================================================
CTFMenuActionsPanel::CTFMenuActionsPanel( Panel *pParent, const char *pszName )
	: BaseClass( pParent, pszName )
{
	m_bShowingCancel = false;
	m_bStateKnown    = false;
	m_hBigFont       = INVALID_FONT;

	m_pPlayButton = new CExButton( this, "PlayButton", "FIND A GAME", this, "toggle_queue" );

	SetPaintBackgroundEnabled( false );
	SetKeyBoardInputEnabled( false );

	ivgui()->AddTickSignal( GetVPanel(), 200 );
}

//-----------------------------------------------------------------------------
void CTFMenuActionsPanel::ApplySchemeSettings( IScheme *pScheme )
{
	BaseClass::ApplySchemeSettings( pScheme );

	m_colText       = pScheme->GetColor( "TanLight", Color( 235, 226, 202, 255 ) );
	m_colGo         = pScheme->GetColor( "SaleGreen", Color( 76, 107, 34, 255 ) );
	m_colGoArmed    = pScheme->GetColor( "CreditsGreen", Color( 94, 150, 49, 255 ) );
	m_colStop       = Color( 120, 24, 12, 255 );
	m_colStopArmed  = pScheme->GetColor( "RedSolid", Color( 192, 28, 0, 255 ) );

	m_hBigFont = pScheme->GetFont( "HudFontSmallBold", true );

	// Restyle on the next tick, once vgui::Button has finished applying its own
	// scheme colours over the top of anything set here.
	m_bStateKnown = false;
}

//-----------------------------------------------------------------------------
void CTFMenuActionsPanel::ApplyButtonStyle()
{
	m_pPlayButton->SetFont( m_hBigFont );
	m_pPlayButton->SetContentAlignment( Label::a_center );
	m_pPlayButton->SetPaintBackgroundEnabled( true );
	m_pPlayButton->SetPaintBackgroundType( 0 );
	m_pPlayButton->SetPaintBorderEnabled( false );
	m_pPlayButton->SetMouseInputEnabled( true );

	m_pPlayButton->SetText( m_bShowingCancel ? "CANCEL SEARCH" : "FIND A GAME" );
	m_pPlayButton->SetDefaultColor( m_colText, m_bShowingCancel ? m_colStop : m_colGo );
	m_pPlayButton->SetArmedColor( m_colText, m_bShowingCancel ? m_colStopArmed : m_colGoArmed );
	m_pPlayButton->SetDepressedColor( m_colText, m_bShowingCancel ? m_colStop : m_colGo );
}

//-----------------------------------------------------------------------------
void CTFMenuActionsPanel::PerformLayout()
{
	BaseClass::PerformLayout();

	m_pPlayButton->SetBounds( 0, 0, GetWide(), GetTall() );
}

//-----------------------------------------------------------------------------
// Purpose: Follow the queue, so one button both starts and stops a search.
//-----------------------------------------------------------------------------
void CTFMenuActionsPanel::OnTick()
{
	BaseClass::OnTick();

	if ( !IsVisible() )
		return;

	const bool bQueued = ( TFMMBackend()->GetState() == k_eTFMMState_Searching );
	if ( bQueued == m_bShowingCancel && m_bStateKnown )
		return;

	m_bShowingCancel = bQueued;
	m_bStateKnown    = true;

	ApplyButtonStyle();
}

//-----------------------------------------------------------------------------
void CTFMenuActionsPanel::OnCommand( const char *pszCommand )
{
	if ( !V_stricmp( pszCommand, "toggle_queue" ) )
	{
		if ( TFMMBackend()->GetState() == k_eTFMMState_Searching )
		{
			TFMMBackend()->CancelQueue();
			return;
		}

		// The dashboard owns the play flow -- it is the thing that decides
		// between opening the playlist and falling back to quickplay.
		CTFMatchmakingDashboard *pDashboard = GetMMDashboard();
		if ( pDashboard )
		{
			pDashboard->OnCommand( "find_game" );
		}
		return;
	}

	BaseClass::OnCommand( pszCommand );
}

//=============================================================================
// CTFMainMenuInfoPanel
//=============================================================================
CTFMainMenuInfoPanel::CTFMainMenuInfoPanel( Panel *pParent, const char *pszName )
	: BaseClass( pParent, pszName )
{
	m_pActions  = new CTFMenuActionsPanel( this, "Actions" );
	m_pCampaign = new CTFCampaignMapPanel( this, "CampaignMap" );
	m_pQueue    = new CTFQueueInfoPanel( this, "QueueInfo" );
	m_pNews     = new CTFMenuNewsPanel( this, "News" );

	// The buttons need the cursor, and VGUI stops hit testing at the first
	// parent that does not take mouse input -- so the column has to take it
	// even though nothing in it but the buttons uses it.
	SetMouseInputEnabled( true );
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

	const int nBody      = MAX( 1, nTall - nGap * 3 );
	const int nActionsH  = (int)( nBody * 0.09f );
	const int nCampaignH = (int)( nBody * 0.36f );
	const int nQueueH    = (int)( nBody * 0.30f );
	const int nNewsH     = nBody - nActionsH - nCampaignH - nQueueH;

	int nY = 0;
	m_pActions->SetBounds( 0, nY, nWide, nActionsH );   nY += nActionsH + nGap;
	m_pCampaign->SetBounds( 0, nY, nWide, nCampaignH ); nY += nCampaignH + nGap;
	m_pQueue->SetBounds( 0, nY, nWide, nQueueH );       nY += nQueueH + nGap;
	m_pNews->SetBounds( 0, nY, nWide, nNewsH );
}

//-----------------------------------------------------------------------------
void CTFMainMenuInfoPanel::Reload()
{
	m_pCampaign->Reload();
	m_pNews->Reload();
	InvalidateLayout();
	Repaint();
}
