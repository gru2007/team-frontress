//========= Copyright Team Frontress, All rights reserved. ====================//
//
// Purpose: The campaign map on the main menu. See tf_campaign_map.h.
//
//=============================================================================//

#include "cbase.h"

#include "tf_campaign_map.h"

#include "tf_mm_backend.h"
#include "gamestate/gamestate.h"
#include "interactivewebpanel.h"
#include "econ_controls.h"
#include "tf_match_description.h"
#include "tf_matchmaking_shared.h"

#include "filesystem.h"
#include "fmtstr.h"

#include <vgui/ILocalize.h>
#include <vgui/IInput.h>
#include <vgui/IScheme.h>
#include <vgui/ISurface.h>
#include <vgui/IVGui.h>

// memdbgon must be the last include file in a .cpp file!!!
#include "tier0/memdbgon.h"

using namespace vgui;

#define CAMPAIGN_FILE		"resource/ui/frontress_campaign.res"

// The page, and the two ways it is asked to draw itself. It is one file so a
// change to the map is one edit, and so the theater and the card can never
// disagree about what the war looks like.
#define CAMPAIGN_PAGE_CARD	"ui/campaign.html?view=card"
#define CAMPAIGN_PAGE_FULL	"ui/campaign.html?view=full"

ConVar tf_campaign_map_html( "tf_campaign_map_html", "1", FCVAR_ARCHIVE,
                             "Draw the main menu's campaign map as the web page in resource/html/campaign.html. "
                             "0 falls back to the plain VGUI line." );

ConVar tf_campaign_deploy( "tf_campaign_deploy", "", FCVAR_NONE,
                           "The campaign node the player last asked to be deployed to. Set from the campaign map." );

//=============================================================================
// The document
//=============================================================================

//-----------------------------------------------------------------------------
static void JSONPutEscaped( CUtlBuffer &buf, const char *pszUTF8 )
{
	for ( const unsigned char *p = (const unsigned char *)pszUTF8; p && *p; ++p )
	{
		switch ( *p )
		{
		case '\"':	buf.PutString( "\\\"" );	break;
		case '\\':	buf.PutString( "\\\\" );	break;
		case '\n':	buf.PutString( "\\n" );		break;
		case '\r':	buf.PutString( "\\r" );		break;
		case '\t':	buf.PutString( "\\t" );		break;
		default:
			if ( *p < 0x20 )
			{
				// A control character in a name is not worth a broken document.
				buf.Printf( "\\u%04x", (int)*p );
			}
			else
			{
				buf.PutChar( (char)*p );
			}
			break;
		}
	}
}

//-----------------------------------------------------------------------------
// Purpose: "key":"value", with the value escaped. UTF-8 passes through as it
//			is, which is what the page wants.
//-----------------------------------------------------------------------------
static void JSONPutString( CUtlBuffer &buf, const char *pszKey, const char *pszValue )
{
	buf.PutChar( '\"' );
	JSONPutEscaped( buf, pszKey );
	buf.PutString( "\":\"" );
	JSONPutEscaped( buf, pszValue ? pszValue : "" );
	buf.PutChar( '\"' );
}

//-----------------------------------------------------------------------------
static void JSONPutWide( CUtlBuffer &buf, const char *pszKey, const wchar_t *pwszValue )
{
	char szUTF8[ 256 ];
	szUTF8[0] = '\0';
	if ( pwszValue && pwszValue[0] )
	{
		V_UnicodeToUTF8( pwszValue, szUTF8, sizeof( szUTF8 ) );
	}

	JSONPutString( buf, pszKey, szUTF8 );
}

//-----------------------------------------------------------------------------
static void JSONPutInt( CUtlBuffer &buf, const char *pszKey, int nValue )
{
	buf.PutChar( '\"' );
	JSONPutEscaped( buf, pszKey );
	buf.Printf( "\":%d", nValue );
}

//-----------------------------------------------------------------------------
static void JSONPutFloat( CUtlBuffer &buf, const char *pszKey, float flValue )
{
	buf.PutChar( '\"' );
	JSONPutEscaped( buf, pszKey );
	buf.Printf( "\":%.4f", flValue );
}

//-----------------------------------------------------------------------------
static void JSONPutBool( CUtlBuffer &buf, const char *pszKey, bool bValue )
{
	buf.PutChar( '\"' );
	JSONPutEscaped( buf, pszKey );
	buf.PutString( bValue ? "\":true" : "\":false" );
}

//-----------------------------------------------------------------------------
static const char *SideName( ETFCampaignSide eSide )
{
	switch ( eSide )
	{
	case k_eTFCampaignSide_Red:	return "RED";
	case k_eTFCampaignSide_Blu:	return "BLU";
	default:					return "NEUTRAL";
	}
}

//=============================================================================
// CTFCampaignModel
//=============================================================================
CTFCampaignModel::CTFCampaignModel()
{
	m_wszName[0] = L'\0';
	m_bDemo      = false;
}

//-----------------------------------------------------------------------------
ETFCampaignSide CTFCampaignModel::SideFromString( const char *pszSide )
{
	if ( !V_stricmp( pszSide, "RED" ) )
		return k_eTFCampaignSide_Red;
	if ( !V_stricmp( pszSide, "BLU" ) || !V_stricmp( pszSide, "BLUE" ) )
		return k_eTFCampaignSide_Blu;

	return k_eTFCampaignSide_Neutral;
}

//-----------------------------------------------------------------------------
int CTFCampaignModel::FindNode( const char *pszID ) const
{
	if ( !pszID || !pszID[0] )
		return -1;

	FOR_EACH_VEC( m_Nodes, i )
	{
		if ( !V_stricmp( m_Nodes[i].strID.Get(), pszID ) )
			return i;
	}

	return -1;
}

//-----------------------------------------------------------------------------
// Purpose: Read the campaign. Everything the map draws comes from this file --
//			neither the panel nor the page knows what a front is called.
//-----------------------------------------------------------------------------
void CTFCampaignModel::Reload()
{
	m_Nodes.RemoveAll();
	m_Edges.RemoveAll();
	m_Fronts.RemoveAll();
	m_Servers.RemoveAll();
	m_wszName[0] = L'\0';
	m_bDemo      = false;

	KeyValuesAD pCampaign( "Campaign" );
	if ( !pCampaign->LoadFromFile( g_pFullFileSystem, CAMPAIGN_FILE, NULL ) )
		return;

	TFMenu_TextToUnicode( pCampaign->GetString( "name", "" ), m_wszName, sizeof( m_wszName ) );

	// The demo campaign says so itself, and the map says so on top of it: the
	// population on a made-up front is made up too, and showing it to players
	// as though a coordinator had published it would be a lie.
	m_bDemo = pCampaign->GetBool( "demo", false );

	KeyValues *pNodes = pCampaign->FindKey( "nodes" );
	for ( KeyValues *pNode = pNodes ? pNodes->GetFirstTrueSubKey() : NULL;
	      pNode != NULL;
	      pNode = pNode->GetNextTrueSubKey() )
	{
		const int iNode = m_Nodes.AddToTail();
		Node_t &node = m_Nodes[ iNode ];

		node.strID     = pNode->GetName();
		node.eOwner    = SideFromString( pNode->GetString( "owner", "" ) );
		node.flX       = clamp( pNode->GetFloat( "x", 0.5f ), 0.f, 1.f );
		node.flY       = clamp( pNode->GetFloat( "y", 0.5f ), 0.f, 1.f );
		node.bHQ       = pNode->GetBool( "hq", false );
		node.strKind   = pNode->GetString( "kind", "" );
		node.strRegion = pNode->GetString( "region", "" );
		node.nPlayers  = pNode->GetInt( "players", 0 );
		node.nBattles  = pNode->GetInt( "battles", 0 );
		TFMenu_TextToUnicode( pNode->GetString( "name", node.strID.Get() ), node.wszName, sizeof( node.wszName ) );
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

	// A war has as many fronts as the population supports, so the file carries
	// a list. A single "front" block is the old shape and still read, because
	// an installed game may have one.
	KeyValues *pFronts = pCampaign->FindKey( "fronts" );
	for ( KeyValues *pFront = pFronts ? pFronts->GetFirstTrueSubKey() : NULL;
	      pFront != NULL;
	      pFront = pFront->GetNextTrueSubKey() )
	{
		const int nNode = FindNode( pFront->GetString( "node", "" ) );
		if ( nNode < 0 )
			continue;

		const int iFront = m_Fronts.AddToTail();
		Front_t &front = m_Fronts[ iFront ];

		front.nNode       = nNode;
		front.eAttacker   = SideFromString( pFront->GetString( "attacker", "" ) );
		front.nStage      = pFront->GetInt( "stage", 0 );
		front.nStageCount = pFront->GetInt( "stages", 0 );
		front.flProgress  = clamp( pFront->GetFloat( "progress", 0.f ), 0.f, 1.f );
		front.nPlayers    = pFront->GetInt( "players", 0 );
		front.strKind     = pFront->GetString( "kind", "" );
		front.strMap      = pFront->GetString( "map", "" );
		front.strServer   = pFront->GetString( "server", "" );
	}

	KeyValues *pFront = pCampaign->FindKey( "front" );
	if ( pFront && m_Fronts.Count() == 0 )
	{
		const int nNode = FindNode( pFront->GetString( "node", "" ) );
		if ( nNode >= 0 )
		{
			const int iFront = m_Fronts.AddToTail();
			Front_t &front = m_Fronts[ iFront ];

			front.nNode       = nNode;
			front.eAttacker   = SideFromString( pFront->GetString( "attacker", "" ) );
			front.nStage      = pFront->GetInt( "stage", 0 );
			front.nStageCount = pFront->GetInt( "stages", 0 );
			front.flProgress  = clamp( pFront->GetFloat( "progress", 0.f ), 0.f, 1.f );
			front.nPlayers    = pFront->GetInt( "players", 0 );
			front.strKind     = pFront->GetString( "kind", "" );
			front.strMap      = pFront->GetString( "map", "" );
			front.strServer   = pFront->GetString( "server", "" );
		}
	}

	KeyValues *pServers = pCampaign->FindKey( "servers" );
	for ( KeyValues *pServer = pServers ? pServers->GetFirstTrueSubKey() : NULL;
	      pServer != NULL;
	      pServer = pServer->GetNextTrueSubKey() )
	{
		const int iServer = m_Servers.AddToTail();
		Server_t &server = m_Servers[ iServer ];

		server.strID       = pServer->GetName();
		server.strName     = pServer->GetString( "name", pServer->GetName() );
		server.strRegion   = pServer->GetString( "region", "" );
		server.strMap      = pServer->GetString( "map", "" );
		server.strNode     = pServer->GetString( "node", "" );
		server.nPlayers    = pServer->GetInt( "players", 0 );
		server.nMaxPlayers = pServer->GetInt( "max", 24 );
	}
}

//-----------------------------------------------------------------------------
// Purpose: The whole war, plus what matchmaking is doing, in one document. The
//			page asks for this and nothing else.
//-----------------------------------------------------------------------------
void CTFCampaignModel::BuildDocument( CUtlBuffer &buf, const char *pszDeployNode ) const
{
	char szLanguage[ 64 ];
	szLanguage[0] = '\0';
	if ( engine )
	{
		engine->GetUILanguage( szLanguage, sizeof( szLanguage ) );
	}

	buf.PutChar( '{' );

	JSONPutInt( buf, "version", 1 );				buf.PutChar( ',' );
	JSONPutString( buf, "lang", szLanguage );		buf.PutChar( ',' );
	JSONPutBool( buf, "demo", m_bDemo );			buf.PutChar( ',' );
	JSONPutWide( buf, "name", m_wszName );			buf.PutChar( ',' );
	JSONPutString( buf, "deploy", pszDeployNode );	buf.PutChar( ',' );

	// Nodes.
	buf.PutString( "\"nodes\":[" );
	FOR_EACH_VEC( m_Nodes, i )
	{
		const Node_t &node = m_Nodes[i];

		if ( i > 0 )
			buf.PutChar( ',' );

		buf.PutChar( '{' );
		JSONPutString( buf, "id", node.strID.Get() );		buf.PutChar( ',' );
		JSONPutWide( buf, "name", node.wszName );			buf.PutChar( ',' );
		JSONPutString( buf, "owner", SideName( node.eOwner ) );	buf.PutChar( ',' );
		JSONPutFloat( buf, "x", node.flX );					buf.PutChar( ',' );
		JSONPutFloat( buf, "y", node.flY );					buf.PutChar( ',' );
		JSONPutBool( buf, "hq", node.bHQ );					buf.PutChar( ',' );
		JSONPutString( buf, "kind", node.strKind.Get() );	buf.PutChar( ',' );
		JSONPutString( buf, "region", node.strRegion.Get() );	buf.PutChar( ',' );
		JSONPutInt( buf, "players", node.nPlayers );		buf.PutChar( ',' );
		JSONPutInt( buf, "battles", node.nBattles );
		buf.PutChar( '}' );
	}
	buf.PutString( "]," );

	// Edges, by node id rather than by index: the page reads them, and an index
	// into an array it also has to trust is one more thing to get wrong.
	buf.PutString( "\"edges\":[" );
	FOR_EACH_VEC( m_Edges, i )
	{
		if ( i > 0 )
			buf.PutChar( ',' );

		buf.PutChar( '{' );
		JSONPutString( buf, "a", m_Nodes[ m_Edges[i].nA ].strID.Get() );	buf.PutChar( ',' );
		JSONPutString( buf, "b", m_Nodes[ m_Edges[i].nB ].strID.Get() );
		buf.PutChar( '}' );
	}
	buf.PutString( "]," );

	// Fronts.
	buf.PutString( "\"fronts\":[" );
	FOR_EACH_VEC( m_Fronts, i )
	{
		const Front_t &front = m_Fronts[i];

		if ( i > 0 )
			buf.PutChar( ',' );

		buf.PutChar( '{' );
		JSONPutString( buf, "node", m_Nodes[ front.nNode ].strID.Get() );	buf.PutChar( ',' );
		JSONPutString( buf, "attacker", SideName( front.eAttacker ) );		buf.PutChar( ',' );
		JSONPutInt( buf, "stage", front.nStage );			buf.PutChar( ',' );
		JSONPutInt( buf, "stages", front.nStageCount );		buf.PutChar( ',' );
		JSONPutFloat( buf, "progress", front.flProgress );	buf.PutChar( ',' );
		JSONPutInt( buf, "players", front.nPlayers );		buf.PutChar( ',' );
		JSONPutString( buf, "kind", front.strKind.Get() );	buf.PutChar( ',' );
		JSONPutString( buf, "map", front.strMap.Get() );	buf.PutChar( ',' );
		JSONPutString( buf, "server", front.strServer.Get() );
		buf.PutChar( '}' );
	}
	buf.PutString( "]," );

	// Servers.
	buf.PutString( "\"servers\":[" );
	FOR_EACH_VEC( m_Servers, i )
	{
		const Server_t &server = m_Servers[i];

		if ( i > 0 )
			buf.PutChar( ',' );

		buf.PutChar( '{' );
		JSONPutString( buf, "id", server.strID.Get() );			buf.PutChar( ',' );
		JSONPutString( buf, "name", server.strName.Get() );		buf.PutChar( ',' );
		JSONPutString( buf, "region", server.strRegion.Get() );	buf.PutChar( ',' );
		JSONPutString( buf, "map", server.strMap.Get() );		buf.PutChar( ',' );
		JSONPutString( buf, "node", server.strNode.Get() );		buf.PutChar( ',' );
		JSONPutInt( buf, "players", server.nPlayers );			buf.PutChar( ',' );
		JSONPutInt( buf, "max", server.nMaxPlayers );
		buf.PutChar( '}' );
	}
	buf.PutString( "]," );

	// What the coordinator says about itself. This part is never a demo: when
	// it has not answered, the page is told so rather than shown a zero.
	const CTFMMBackend *pBackend = TFMMBackend();
	const CTFMMBackend::Status_t &status = pBackend->GetStatus();

	buf.PutString( "\"status\":{" );
	JSONPutBool( buf, "checked", status.bChecked );		buf.PutChar( ',' );
	JSONPutBool( buf, "valid", status.bValid );			buf.PutChar( ',' );
	JSONPutBool( buf, "serversKnown", status.bServerCapacityKnown );	buf.PutChar( ',' );
	JSONPutString( buf, "name", status.strName.Get() );	buf.PutChar( ',' );
	JSONPutInt( buf, "online", status.nOnlinePlayers );	buf.PutChar( ',' );
	JSONPutInt( buf, "matches", status.nLiveMatches );	buf.PutChar( ',' );
	JSONPutInt( buf, "servers", status.nFreeServers );
	buf.PutString( "}," );

	// ...and what it is doing about us.
	const ETFMMState eState = pBackend->GetState();
	const char *pszState = "idle";
	switch ( eState )
	{
	case k_eTFMMState_Searching:	pszState = "searching";	break;
	case k_eTFMMState_MatchReady:	pszState = "ready";		break;
	case k_eTFMMState_Connecting:	pszState = "connecting";break;
	case k_eTFMMState_InMatch:		pszState = "inmatch";	break;
	default:						break;
	}

	char szGroup[ 128 ];
	szGroup[0] = '\0';
	if ( eState == k_eTFMMState_Searching )
	{
		const IMatchGroupDescription *pDesc = GetMatchGroupDescription( pBackend->GetQueuedMatchGroup() );
		const wchar_t *pwszGroup = pDesc ? g_pVGuiLocalize->Find( pDesc->GetNameLocToken() ) : NULL;
		if ( pwszGroup )
		{
			V_UnicodeToUTF8( pwszGroup, szGroup, sizeof( szGroup ) );
		}
	}

	buf.PutString( "\"queue\":{" );
	JSONPutString( buf, "state", pszState );					buf.PutChar( ',' );
	JSONPutString( buf, "group", szGroup );					buf.PutChar( ',' );
	JSONPutInt( buf, "have", pBackend->GetQueuePlayerCount() );	buf.PutChar( ',' );
	JSONPutInt( buf, "need", pBackend->GetQueueNeededCount() );	buf.PutChar( ',' );
	JSONPutInt( buf, "seconds", (int)pBackend->GetQueueSeconds() );	buf.PutChar( ',' );
	JSONPutString( buf, "detail", pBackend->GetQueueDetail() );
	buf.PutString( "}" );

	buf.PutChar( '}' );
}

//=============================================================================
// CTFCampaignFeed
//=============================================================================
CTFCampaignFeed::CTFCampaignFeed()
{
	m_flNextPublish   = 0.f;
	m_bCloseRequested = false;
	m_bLoaded         = false;
}

//-----------------------------------------------------------------------------
CTFCampaignFeed *TFCampaignFeed()
{
	static CTFCampaignFeed s_feed;
	return &s_feed;
}

//-----------------------------------------------------------------------------
void CTFCampaignFeed::Reload()
{
	m_model.Reload();
	m_bLoaded       = true;

	// The campaign can be edited under us, and a pin on a node that is no
	// longer in it is worse than no pin.
	if ( !m_strDeployNode.IsEmpty() && m_model.FindNode( m_strDeployNode.Get() ) < 0 )
	{
		m_strDeployNode.Clear();
		tf_campaign_deploy.SetValue( "" );
	}

	// Whatever we last published is now stale.
	m_flNextPublish = 0.f;
}

//-----------------------------------------------------------------------------
bool CTFCampaignFeed::BTakeCloseRequest()
{
	const bool bRequested = m_bCloseRequested;
	m_bCloseRequested = false;
	return bRequested;
}

//-----------------------------------------------------------------------------
// Purpose: What the page asked the game to do. Text rather than JSON: a verb
//			and one argument is the whole vocabulary, and a page that can only
//			say four things cannot say a fifth by accident.
//-----------------------------------------------------------------------------
void CTFCampaignFeed::ConsumeCommands()
{
	std::vector< std::string > vecCommands;
	GetGameStateManager()->TakeCampaignCommands( vecCommands );

	for ( size_t i = 0; i < vecCommands.size(); ++i )
	{
		char szCommand[ 256 ];
		V_strncpy( szCommand, vecCommands[i].c_str(), sizeof( szCommand ) );

		char *pszArg = V_strstr( szCommand, " " );
		if ( pszArg )
		{
			*pszArg = '\0';
			++pszArg;

			// A body posted by hand rather than by the page may carry a newline.
			for ( int nEnd = V_strlen( pszArg ) - 1;
			      nEnd >= 0 && V_isspace( pszArg[ nEnd ] );
			      --nEnd )
			{
				pszArg[ nEnd ] = '\0';
			}
		}

		if ( !V_stricmp( szCommand, "deploy" ) )
		{
			// A node nobody is fighting over is still somewhere to be sent, but
			// a node that is not in the campaign at all is a page talking to
			// the wrong game.
			if ( pszArg && m_model.FindNode( pszArg ) >= 0 )
			{
				m_strDeployNode = pszArg;
				tf_campaign_deploy.SetValue( pszArg );
				// Publish immediately: the page is waiting to see the pin move.
				m_flNextPublish = 0.f;
			}
		}
		else if ( !V_stricmp( szCommand, "close" ) )
		{
			m_bCloseRequested = true;
		}
		else if ( !V_stricmp( szCommand, "reload" ) )
		{
			Reload();
		}
	}
}

//-----------------------------------------------------------------------------
void CTFCampaignFeed::Update()
{
	if ( !m_bLoaded )
	{
		Reload();
	}

	ConsumeCommands();

	const float flNow = (float)Plat_FloatTime();
	if ( flNow < m_flNextPublish )
		return;

	// Once a second. The war does not move faster than that, and the document
	// is rebuilt from scratch every time.
	m_flNextPublish = flNow + 1.f;

	CUtlBuffer buf( 0, 8 * 1024, CUtlBuffer::TEXT_BUFFER );
	m_model.BuildDocument( buf, m_strDeployNode.Get() );

	GetGameStateManager()->SetCampaignJSON( std::string( (const char *)buf.Base(), buf.TellPut() ) );
}

//=============================================================================
// CTFCampaignMapDialog
//=============================================================================
CTFCampaignMapDialog::CTFCampaignMapDialog( Panel *pParent, const char *pszName )
	: BaseClass( pParent, pszName )
{
	m_pWeb         = NULL;
	m_pCloseButton = new CExButton( this, "CampaignMapClose", "#Frontress_Menu_CloseMap", this, "close" );
	m_colBackdrop  = Color( 0, 0, 0, 200 );

	SetVisible( false );
	SetMouseInputEnabled( true );
	SetKeyBoardInputEnabled( true );
	SetProportional( true );
	SetZPos( 1000 );

	ivgui()->AddTickSignal( GetVPanel(), 100 );
}

//-----------------------------------------------------------------------------
void CTFCampaignMapDialog::ApplySchemeSettings( IScheme *pScheme )
{
	BaseClass::ApplySchemeSettings( pScheme );

	SetPaintBackgroundEnabled( true );
	SetBgColor( Color( 0, 0, 0, 0 ) );

	if ( m_pCloseButton )
	{
		m_pCloseButton->SetZPos( 20 );
	}

	InvalidateLayout( true );
}

//-----------------------------------------------------------------------------
void CTFCampaignMapDialog::PerformLayout()
{
	BaseClass::PerformLayout();

	// The dialog is the whole screen so the menu behind it dims; the page is
	// inset inside that, and the button that closes it sits in the margin
	// above, where it cannot be confused for part of the map.
	if ( GetParent() )
	{
		SetBounds( 0, 0, GetParent()->GetWide(), GetParent()->GetTall() );
	}

	const HScheme hScheme = GetScheme();
	const int nButtonWide = scheme()->GetProportionalScaledValueEx( hScheme, 90 );
	const int nButtonTall = scheme()->GetProportionalScaledValueEx( hScheme, 18 );
	const int nGap        = scheme()->GetProportionalScaledValueEx( hScheme, 5 );

	const int nInsetX = GetWide() / 20;
	const int nTop    = nButtonTall + nGap * 2;
	const int nWide   = MAX( 1, GetWide() - nInsetX * 2 );
	const int nTall   = MAX( 1, GetTall() - nTop - nGap * 2 );

	if ( m_pWeb )
	{
		m_pWeb->SetBounds( nInsetX, nTop, nWide, nTall );
	}

	if ( m_pCloseButton )
	{
		m_pCloseButton->SetBounds( nInsetX + nWide - nButtonWide, nGap, nButtonWide, nButtonTall );
	}
}

//-----------------------------------------------------------------------------
void CTFCampaignMapDialog::PaintBackground()
{
	// Dim everything behind, so the theater reads as a thing you opened rather
	// than a panel that appeared.
	surface()->DrawSetColor( m_colBackdrop );
	surface()->DrawFilledRect( 0, 0, GetWide(), GetTall() );
}

//-----------------------------------------------------------------------------
void CTFCampaignMapDialog::Paint()
{
	BaseClass::Paint();
}

//-----------------------------------------------------------------------------
void CTFCampaignMapDialog::ShowDialog()
{
	// Built the first time it is opened: a player who never opens the map never
	// pays for a second web view.
	if ( !m_pWeb )
	{
		m_pWeb = new CInteractiveWebPanel( this, "CampaignMapWeb", CAMPAIGN_PAGE_FULL, true, false );
		m_pWeb->SetZPos( 10 );
	}

	SetVisible( true );
	MoveToFront();
	RequestFocus();

	TFCampaignFeed()->Update();

	if ( GetGameStateManager()->IsReady() )
	{
		m_pWeb->LoadInteractivePanel();
	}

	m_pWeb->SetVisible( true );

	InvalidateLayout( true );
}

//-----------------------------------------------------------------------------
void CTFCampaignMapDialog::CloseDialog()
{
	if ( m_pWeb )
	{
		m_pWeb->SetVisible( false );
	}

	SetVisible( false );
}

//-----------------------------------------------------------------------------
void CTFCampaignMapDialog::OnTick()
{
	BaseClass::OnTick();

	if ( !IsVisible() )
		return;

	// The theater belongs to the main menu. If the menu is gone -- the player
	// joined a game, or the panel was hidden under us -- so is the map.
	if ( GetParent() && !GetParent()->IsVisible() )
	{
		CloseDialog();
		return;
	}

	CTFCampaignFeed *pFeed = TFCampaignFeed();
	pFeed->Update();

	// The page's own close button, so the theater can be shut from inside it.
	if ( pFeed->BTakeCloseRequest() )
	{
		CloseDialog();
		return;
	}

	// The page is only loaded once the local server is answering. Until then
	// the panel is empty rather than showing a connection error.
	if ( m_pWeb && GetGameStateManager()->IsReady() )
	{
		m_pWeb->LoadInteractivePanel();
	}
}

//-----------------------------------------------------------------------------
void CTFCampaignMapDialog::OnCommand( const char *pszCommand )
{
	if ( !V_stricmp( pszCommand, "close" ) )
	{
		CloseDialog();
		return;
	}

	BaseClass::OnCommand( pszCommand );
}

//-----------------------------------------------------------------------------
void CTFCampaignMapDialog::OnKeyCodeTyped( KeyCode code )
{
	if ( code == KEY_ESCAPE )
	{
		CloseDialog();
		return;
	}

	BaseClass::OnKeyCodeTyped( code );
}

//=============================================================================
// CTFCampaignWebCard
//=============================================================================
CTFCampaignWebCard::CTFCampaignWebCard( Panel *pParent, const char *pszName, CTFCampaignMapDialog *pDialog )
	: BaseClass( pParent, pszName, "#Frontress_Menu_Campaign" )
{
	m_hDialog     = pDialog;
	m_bWebStarted = false;

	m_pWeb = new CInteractiveWebPanel( this, "CampaignCardWeb", CAMPAIGN_PAGE_CARD, true, false );
	m_pWeb->SetMouseInputEnabled( false );
	m_pWeb->SetKeyBoardInputEnabled( false );

	// The card is a picture of the war, not a control: every click on it opens
	// the theater, where there is room to do something about it. The button is
	// invisible and covers the map, so the page can draw its own affordance and
	// still never see a mouse event.
	m_pOpenButton = new CExButton( this, "CampaignCardOpen", "", this, "open_map" );
	m_pOpenButton->SetPaintBackgroundEnabled( false );
	m_pOpenButton->SetPaintBorderEnabled( false );
	m_pOpenButton->SetZPos( 10 );

	// ...which means this card, unlike the rest of the column, wants the mouse.
	SetMouseInputEnabled( true );

	ivgui()->AddTickSignal( GetVPanel(), 100 );
}

//-----------------------------------------------------------------------------
void CTFCampaignWebCard::ApplySchemeSettings( IScheme *pScheme )
{
	BaseClass::ApplySchemeSettings( pScheme );

	InvalidateLayout( true );
}

//-----------------------------------------------------------------------------
void CTFCampaignWebCard::PerformLayout()
{
	BaseClass::PerformLayout();

	int x, y, wide, tall;
	GetContentBounds( x, y, wide, tall );

	if ( m_pWeb )
	{
		m_pWeb->SetBounds( x, y, wide, tall );
	}

	if ( m_pOpenButton )
	{
		m_pOpenButton->SetBounds( x, y, wide, tall );
	}
}

//-----------------------------------------------------------------------------
void CTFCampaignWebCard::Reload()
{
	TFCampaignFeed()->Reload();
}

//-----------------------------------------------------------------------------
void CTFCampaignWebCard::OnTick()
{
	BaseClass::OnTick();

	if ( !IsVisible() )
		return;

	TFCampaignFeed()->Update();

	if ( !m_bWebStarted && m_pWeb && GetGameStateManager()->IsReady() )
	{
		// Load once the document it reads exists, so the first frame the player
		// sees is the map and not an error page.
		m_bWebStarted = true;
		m_pWeb->LoadInteractivePanel();
		m_pWeb->SetVisible( true );
	}
}

//-----------------------------------------------------------------------------
void CTFCampaignWebCard::OnCommand( const char *pszCommand )
{
	if ( !V_stricmp( pszCommand, "open_map" ) )
	{
		if ( m_hDialog.Get() )
		{
			m_hDialog->ShowDialog();
		}
		return;
	}

	BaseClass::OnCommand( pszCommand );
}
