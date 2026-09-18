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
#include "tf_shareddefs.h"
#include "gamestate/frontress_instructor.h"
#include "cdll_util.h"

#include "filesystem.h"
#include "fmtstr.h"
#include "tier0/icommandline.h"

#include <time.h>

#include <vgui/ILocalize.h>
#include <vgui/IInput.h>
#include <vgui/IScheme.h>
#include <vgui/ISurface.h>
#include <vgui/IVGui.h>

// memdbgon must be the last include file in a .cpp file!!!
#include "tier0/memdbgon.h"

using namespace vgui;

#define CAMPAIGN_FILE		"resource/ui/frontress_campaign.res"
#define DEMO_STATE_FILE		"cfg/frontress_demo_state.vdf"
#define DEMO_STATE_TEMP		"cfg/frontress_demo_state.tmp"
#define DEMO_STATE_BACKUP	"cfg/frontress_demo_state.backup.vdf"
#define DEMO_STAGE_COUNT	3
#define DEMO_MAX_BATTLES	4
#define FRONTRESS_DEMO_APPID	5260620

// The page, and the two ways it is asked to draw itself. It is one file so a
// change to the map is one edit, and so the theater and the card can never
// disagree about what the war looks like.
#define CAMPAIGN_PAGE_CARD_LIVE	"ui/campaign.html?view=card&embedded=1&demo=0"
#define CAMPAIGN_PAGE_FULL_LIVE	"ui/campaign.html?view=full&embedded=1&demo=0"
#define CAMPAIGN_PAGE_CARD_DEMO	"ui/campaign.html?view=card&embedded=1&demo=1"
#define CAMPAIGN_PAGE_FULL_DEMO	"ui/campaign.html?view=full&embedded=1&demo=1"

ConVar tf_campaign_map_html( "tf_campaign_map_html", "1", FCVAR_ARCHIVE,
                             "Draw the main menu's campaign map as the web page in resource/html/campaign.html. "
                             "0 falls back to the plain VGUI line." );

ConVar tf_campaign_deploy( "tf_campaign_deploy", "", FCVAR_NONE,
                           "The campaign node the player last asked to be deployed to. Set from the campaign map." );

bool TFCampaignDemoMode()
{
	if ( CommandLine()->CheckParm( "-frontressdemo" ) != NULL )
		return true;

	// Steam launch options should carry the flag, but the demo's own identity is
	// an independent backstop. This also makes its steam_appid.txt overlay useful
	// for a direct QA launch outside the Steam client.
	const char *pszEnvironmentAppID = getenv( "SteamAppId" );
	if ( pszEnvironmentAppID && V_atoi( pszEnvironmentAppID ) == FRONTRESS_DEMO_APPID )
		return true;
	if ( steamapicontext && steamapicontext->SteamUtils() &&
	     steamapicontext->SteamUtils()->GetAppID() == FRONTRESS_DEMO_APPID )
		return true;

	return engine && engine->GetAppID() == FRONTRESS_DEMO_APPID;
}

static const char *CampaignPage( bool bFull )
{
	if ( TFCampaignDemoMode() )
		return bFull ? CAMPAIGN_PAGE_FULL_DEMO : CAMPAIGN_PAGE_CARD_DEMO;

	return bFull ? CAMPAIGN_PAGE_FULL_LIVE : CAMPAIGN_PAGE_CARD_LIVE;
}

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

// The web map localizes saved campaign events from stable semantic keys.  The
// in-match instructor is native, so resolve its short debrief here as well.
// Universal character names keep this source independent of the compiler's
// active Windows code page.
static bool DemoUsesRussian()
{
	char szLanguage[64];
	szLanguage[0] = '\0';
	if ( engine )
		engine->GetUILanguage( szLanguage, sizeof( szLanguage ) );
	return V_stristr( szLanguage, "russian" ) != NULL;
}

static void DemoLocalizedResultText( const char *pszResultType,
	                                  const char *pszEnglishTitle,
	                                  const char *pszEnglishBody,
	                                  char *pszTitle, int nTitleBytes,
	                                  char *pszBody, int nBodyBytes )
{
	V_strncpy( pszTitle, pszEnglishTitle ? pszEnglishTitle : "", nTitleBytes );
	V_strncpy( pszBody, pszEnglishBody ? pszEnglishBody : "", nBodyBytes );
	if ( !DemoUsesRussian() )
		return;

	const wchar_t *pwszTitle = NULL;
	const wchar_t *pwszBody = NULL;
	if ( !V_stricmp( pszResultType, "CAPTURED" ) )
	{
		pwszTitle = L"\u041f\u041e\u0411\u0415\u0414\u0410 \u2014 \u0421\u0415\u041a\u0422\u041e\u0420 \u0417\u0410\u0425\u0412\u0410\u0427\u0415\u041d";
		pwszBody = L"\u041e\u043f\u0435\u0440\u0430\u0446\u0438\u044f \u0443\u0441\u043f\u0435\u0448\u043d\u0430. \u0421\u0435\u043a\u0442\u043e\u0440 \u043f\u0435\u0440\u0435\u0448\u0451\u043b \u043a \u0432\u0430\u0448\u0435\u0439 \u0441\u0442\u043e\u0440\u043e\u043d\u0435, \u0438 \u043e\u0442\u043a\u0440\u044b\u043b\u0441\u044f \u043d\u043e\u0432\u044b\u0439 \u0444\u0440\u043e\u043d\u0442.";
	}
	else if ( !V_stricmp( pszResultType, "ADVANCED" ) )
	{
		pwszTitle = L"\u041f\u041e\u0411\u0415\u0414\u0410 \u2014 \u041e\u041f\u0415\u0420\u0410\u0426\u0418\u042f \u041f\u0420\u041e\u0414\u0412\u0418\u041d\u0423\u041b\u0410\u0421\u042c";
		pwszBody = L"\u041f\u043e\u0431\u0435\u0434\u0430 \u043f\u0435\u0440\u0435\u0432\u0435\u043b\u0430 \u043e\u043f\u0435\u0440\u0430\u0446\u0438\u044e \u043d\u0430 \u0441\u043b\u0435\u0434\u0443\u044e\u0449\u0438\u0439 \u0442\u0430\u043a\u0442\u0438\u0447\u0435\u0441\u043a\u0438\u0439 \u044d\u0442\u0430\u043f.";
	}
	else if ( !V_stricmp( pszResultType, "STALEMATE" ) )
	{
		pwszTitle = L"\u041d\u0418\u0427\u042c\u042f \u2014 \u041d\u0410\u0421\u0422\u0423\u041f\u041b\u0415\u041d\u0418\u0415 \u041e\u0422\u0411\u0418\u0422\u041e";
		pwszBody = L"\u0412\u0440\u0435\u043c\u044f \u0438\u0441\u0442\u0435\u043a\u043b\u043e \u0431\u0435\u0437 \u043f\u043e\u0431\u0435\u0434\u0438\u0442\u0435\u043b\u044f. \u0417\u0430\u0449\u0438\u0442\u043d\u0438\u043a\u0438 \u0443\u0434\u0435\u0440\u0436\u0430\u043b\u0438 \u0441\u0435\u043a\u0442\u043e\u0440, \u043f\u043e\u044d\u0442\u043e\u043c\u0443 \u043d\u0430\u0441\u0442\u0443\u043f\u043b\u0435\u043d\u0438\u0435 \u0441\u0447\u0438\u0442\u0430\u0435\u0442\u0441\u044f \u043e\u0442\u0431\u0438\u0442\u044b\u043c.";
	}
	else if ( !V_stricmp( pszResultType, "REPULSED" ) )
	{
		pwszTitle = L"\u041f\u041e\u0420\u0410\u0416\u0415\u041d\u0418\u0415 \u2014 \u041d\u0410\u0421\u0422\u0423\u041f\u041b\u0415\u041d\u0418\u0415 \u041e\u0422\u0411\u0418\u0422\u041e";
		pwszBody = L"\u041f\u0440\u043e\u0442\u0438\u0432\u043d\u0438\u043a \u0443\u0434\u0435\u0440\u0436\u0430\u043b \u0441\u0435\u043a\u0442\u043e\u0440. \u041e\u043f\u0435\u0440\u0430\u0446\u0438\u044f \u043f\u0440\u043e\u0434\u043e\u043b\u0436\u0430\u0435\u0442\u0441\u044f \u2014 \u0431\u043e\u0439 \u043c\u043e\u0436\u043d\u043e \u043f\u043e\u0432\u0442\u043e\u0440\u0438\u0442\u044c.";
	}
	else if ( !V_stricmp( pszResultType, "TIMED_OUT" ) )
	{
		pwszTitle = L"\u0418\u0422\u041e\u0413\u0418 \u041a\u0410\u041c\u041f\u0410\u041d\u0418\u0418";
		pwszBody = L"\u041e\u043f\u0435\u0440\u0430\u0446\u0438\u044f \u0437\u0430\u0432\u0435\u0440\u0448\u0438\u043b\u0430\u0441\u044c \u043f\u043e\u0441\u043b\u0435 \u0447\u0435\u0442\u044b\u0440\u0451\u0445 \u0431\u043e\u0451\u0432. \u041a\u0430\u0436\u0434\u044b\u0439 \u0440\u0435\u0437\u0443\u043b\u044c\u0442\u0430\u0442 \u0432\u043b\u0438\u044f\u043b \u043d\u0430 \u0444\u0440\u043e\u043d\u0442; \u043d\u0430\u0447\u043d\u0438\u0442\u0435 \u043d\u043e\u0432\u0443\u044e \u043a\u0430\u043c\u043f\u0430\u043d\u0438\u044e \u0438\u043b\u0438 \u043f\u0435\u0440\u0435\u0439\u0434\u0438\u0442\u0435 \u0432 \u043e\u0431\u0449\u0443\u044e \u0432\u043e\u0439\u043d\u0443 \u043f\u043b\u0435\u0439\u0442\u0435\u0441\u0442\u0430.";
	}

	if ( pwszTitle && pwszBody )
	{
		V_UnicodeToUTF8( pwszTitle, pszTitle, nTitleBytes );
		V_UnicodeToUTF8( pwszBody, pszBody, nBodyBytes );
	}
}

static const char *DemoTargetForSide( ETFCampaignSide eSide )
{
	return eSide == k_eTFCampaignSide_Red ? "reservoir" : "works";
}

static const char *DemoNextTargetForSide( ETFCampaignSide eSide )
{
	return eSide == k_eTFCampaignSide_Red ? "junction" : "yard";
}

static const char *DemoBackgroundTargetForSide( ETFCampaignSide eSide )
{
	return eSide == k_eTFCampaignSide_Red ? "yard" : "junction";
}

static ETFCampaignSide OpposingSide( ETFCampaignSide eSide )
{
	return eSide == k_eTFCampaignSide_Red ? k_eTFCampaignSide_Blu : k_eTFCampaignSide_Red;
}

static const char *DemoStageKindForIndex( int nStage )
{
	static const char *s_pszKinds[ DEMO_STAGE_COUNT ] = { "breakthrough", "advance", "assault" };
	return s_pszKinds[ clamp( nStage, 1, DEMO_STAGE_COUNT ) - 1 ];
}

static const char *DemoStageMapForSide( ETFCampaignSide eSide, int nStage )
{
	// Keep this list inside the stock bot mapcycle. The demo must never depend on
	// nav generation or on a human server to make the selected objective work.
	static const char *s_pszBluMaps[ DEMO_STAGE_COUNT ] = { "cp_gorge", "pl_badwater", "cp_dustbowl" };
	// Payload and Attack/Defend hard-code BLU as the attacking team. RED's
	// offensive therefore uses symmetric modes so the tactical winner and the
	// strategic attacker can never contradict each other.
	static const char *s_pszRedMaps[ DEMO_STAGE_COUNT ] = { "koth_viaduct", "cp_badlands", "cp_foundry" };
	const int nIndex = clamp( nStage, 1, DEMO_STAGE_COUNT ) - 1;
	return eSide == k_eTFCampaignSide_Red ? s_pszRedMaps[ nIndex ] : s_pszBluMaps[ nIndex ];
}

static int DemoPlayerTargetForStage( int nStage )
{
	static const int s_nTargets[ DEMO_STAGE_COUNT ] = { 8, 12, 18 };
	return s_nTargets[ clamp( nStage, 1, DEMO_STAGE_COUNT ) - 1 ];
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
void CTFCampaignModel::BuildDocument( CUtlBuffer &buf, const char *pszDeployNode,
                                     const TFCampaignDemoState_t *pDemoState ) const
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

		ETFCampaignSide eOwner = node.eOwner;
		if ( pDemoState && pDemoState->bTerritoryCaptured && pDemoState->ePlayerSide != k_eTFCampaignSide_Neutral &&
		     !V_stricmp( node.strID.Get(), DemoTargetForSide( pDemoState->ePlayerSide ) ) )
		{
			eOwner = pDemoState->ePlayerSide;
		}

		buf.PutChar( '{' );
		JSONPutString( buf, "id", node.strID.Get() );		buf.PutChar( ',' );
		JSONPutWide( buf, "name", node.wszName );			buf.PutChar( ',' );
		JSONPutString( buf, "owner", SideName( eOwner ) );	buf.PutChar( ',' );
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

	// Fronts. The local coordinator projects one directed operation from its
	// saved domain state; the online path keeps publishing the authored/live set.
	buf.PutString( "\"fronts\":[" );
	if ( pDemoState && pDemoState->ePlayerSide != k_eTFCampaignSide_Neutral && !pDemoState->bCompleted )
	{
		const char *pszTarget = DemoTargetForSide( pDemoState->ePlayerSide );
		const int nTarget = FindNode( pszTarget );
		buf.PutChar( '{' );
		JSONPutString( buf, "node", nTarget >= 0 ? m_Nodes[nTarget].strID.Get() : pszTarget ); buf.PutChar( ',' );
		JSONPutString( buf, "attacker", SideName( pDemoState->ePlayerSide ) ); buf.PutChar( ',' );
		JSONPutInt( buf, "stage", pDemoState->nStage ); buf.PutChar( ',' );
		JSONPutInt( buf, "stages", DEMO_STAGE_COUNT ); buf.PutChar( ',' );
		JSONPutFloat( buf, "progress", pDemoState->strPendingBattleID.IsEmpty() ? 0.15f : 0.55f ); buf.PutChar( ',' );
		JSONPutInt( buf, "players", pDemoState->strPendingBattleID.IsEmpty() ? 0 : pDemoState->nPendingPlayerTarget ); buf.PutChar( ',' );
		JSONPutString( buf, "kind", DemoStageKindForIndex( pDemoState->nStage ) ); buf.PutChar( ',' );
		JSONPutString( buf, "map", DemoStageMapForSide( pDemoState->ePlayerSide, pDemoState->nStage ) ); buf.PutChar( ',' );
		JSONPutString( buf, "server", "local" );
		buf.PutChar( '}' );

		// A deterministic locked front moves beside the player's operation. It
		// demonstrates that the war is larger than this one local match without
		// pretending those combatants are online users.
		buf.PutString( ",{" );
		JSONPutString( buf, "node", DemoBackgroundTargetForSide( pDemoState->ePlayerSide ) ); buf.PutChar( ',' );
		JSONPutString( buf, "attacker", SideName( OpposingSide( pDemoState->ePlayerSide ) ) ); buf.PutChar( ',' );
		JSONPutInt( buf, "stage", 1 + ( pDemoState->nBattlesPlayed % 2 ) ); buf.PutChar( ',' );
		JSONPutInt( buf, "stages", DEMO_STAGE_COUNT ); buf.PutChar( ',' );
		JSONPutFloat( buf, "progress", 0.20f + 0.12f * ( pDemoState->nBattlesPlayed % 3 ) ); buf.PutChar( ',' );
		JSONPutInt( buf, "players", 0 ); buf.PutChar( ',' );
		JSONPutString( buf, "kind", "simulated front" ); buf.PutChar( ',' );
		JSONPutString( buf, "map", "world tick" ); buf.PutChar( ',' );
		JSONPutString( buf, "server", "local simulation" ); buf.PutChar( ',' );
		JSONPutBool( buf, "locked", true );
		buf.PutChar( '}' );
	}
	else if ( pDemoState && pDemoState->bTerritoryCaptured )
	{
		const char *pszTarget = DemoNextTargetForSide( pDemoState->ePlayerSide );
		buf.PutChar( '{' );
		JSONPutString( buf, "node", pszTarget ); buf.PutChar( ',' );
		JSONPutString( buf, "attacker", SideName( pDemoState->ePlayerSide ) ); buf.PutChar( ',' );
		JSONPutInt( buf, "stage", 1 ); buf.PutChar( ',' );
		JSONPutInt( buf, "stages", DEMO_STAGE_COUNT ); buf.PutChar( ',' );
		JSONPutFloat( buf, "progress", 0.08f ); buf.PutChar( ',' );
		JSONPutInt( buf, "players", 0 ); buf.PutChar( ',' );
		JSONPutString( buf, "kind", "new front" ); buf.PutChar( ',' );
		JSONPutString( buf, "map", "Playtest" ); buf.PutChar( ',' );
		JSONPutString( buf, "server", "shared war" ); buf.PutChar( ',' );
		JSONPutBool( buf, "locked", true );
		buf.PutChar( '}' );
	}
	else if ( !pDemoState )
	{
		FOR_EACH_VEC( m_Fronts, i )
		{
			const Front_t &front = m_Fronts[i];
			if ( i > 0 ) buf.PutChar( ',' );
			buf.PutChar( '{' );
			JSONPutString( buf, "node", m_Nodes[ front.nNode ].strID.Get() ); buf.PutChar( ',' );
			JSONPutString( buf, "attacker", SideName( front.eAttacker ) ); buf.PutChar( ',' );
			JSONPutInt( buf, "stage", front.nStage ); buf.PutChar( ',' );
			JSONPutInt( buf, "stages", front.nStageCount ); buf.PutChar( ',' );
			JSONPutFloat( buf, "progress", front.flProgress ); buf.PutChar( ',' );
			JSONPutInt( buf, "players", front.nPlayers ); buf.PutChar( ',' );
			JSONPutString( buf, "kind", front.strKind.Get() ); buf.PutChar( ',' );
			JSONPutString( buf, "map", front.strMap.Get() ); buf.PutChar( ',' );
			JSONPutString( buf, "server", front.strServer.Get() );
			buf.PutChar( '}' );
		}
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

	// What the selected provider says about itself. The local coordinator is
	// explicit about being simulated; the online provider reports its last
	// checked status rather than inventing capacity while disconnected.
	const CTFMMBackend *pBackend = TFMMBackend();
	const CTFMMBackend::Status_t &status = pBackend->GetStatus();

	buf.PutString( "\"status\":{" );
	JSONPutBool( buf, "checked", pDemoState ? true : status.bChecked ); buf.PutChar( ',' );
	JSONPutBool( buf, "valid", pDemoState ? true : status.bValid ); buf.PutChar( ',' );
	JSONPutBool( buf, "serversKnown", pDemoState ? true : status.bServerCapacityKnown ); buf.PutChar( ',' );
	JSONPutString( buf, "name", pDemoState ? "Local War Coordinator" : status.strName.Get() ); buf.PutChar( ',' );
	JSONPutInt( buf, "online", pDemoState ? 1 : status.nOnlinePlayers ); buf.PutChar( ',' );
	JSONPutInt( buf, "matches", pDemoState ? ( pDemoState->strPendingBattleID.IsEmpty() ? 0 : 1 ) : status.nLiveMatches ); buf.PutChar( ',' );
	JSONPutInt( buf, "servers", pDemoState ? 1 : status.nFreeServers );
	buf.PutString( "}," );

	// ...and what it is doing about us.
	const ETFMMState eState = pDemoState ? k_eTFMMState_Idle : pBackend->GetState();
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
	JSONPutString( buf, "state", pDemoState && !pDemoState->strPendingBattleID.IsEmpty() ? "inmatch" : pszState ); buf.PutChar( ',' );
	JSONPutString( buf, "group", szGroup );					buf.PutChar( ',' );
	JSONPutInt( buf, "have", pDemoState ? 1 : pBackend->GetQueuePlayerCount() ); buf.PutChar( ',' );
	JSONPutInt( buf, "need", pDemoState ? 1 : pBackend->GetQueueNeededCount() ); buf.PutChar( ',' );
	JSONPutInt( buf, "seconds", pDemoState ? 0 : (int)pBackend->GetQueueSeconds() ); buf.PutChar( ',' );
	JSONPutString( buf, "detail", pDemoState ? "Local battle ticket" : pBackend->GetQueueDetail() );
	buf.PutString( "}" );

	if ( pDemoState )
	{
		buf.PutString( ",\"demoState\":{" );
		JSONPutInt( buf, "schemaVersion", pDemoState->nSchemaVersion ); buf.PutChar( ',' );
		JSONPutString( buf, "faction", SideName( pDemoState->ePlayerSide ) ); buf.PutChar( ',' );
		JSONPutBool( buf, "needsFaction", pDemoState->ePlayerSide == k_eTFCampaignSide_Neutral ); buf.PutChar( ',' );
		JSONPutInt( buf, "stage", pDemoState->nStage ); buf.PutChar( ',' );
		JSONPutInt( buf, "stages", DEMO_STAGE_COUNT ); buf.PutChar( ',' );
		JSONPutInt( buf, "teamSize", DemoPlayerTargetForStage( pDemoState->nStage ) / 2 ); buf.PutChar( ',' );
		JSONPutInt( buf, "battlesPlayed", pDemoState->nBattlesPlayed ); buf.PutChar( ',' );
		JSONPutInt( buf, "victories", pDemoState->nVictories ); buf.PutChar( ',' );
		JSONPutInt( buf, "defeats", pDemoState->nDefeats ); buf.PutChar( ',' );
		JSONPutBool( buf, "completed", pDemoState->bCompleted ); buf.PutChar( ',' );
		JSONPutBool( buf, "territoryCaptured", pDemoState->bTerritoryCaptured ); buf.PutChar( ',' );
		JSONPutBool( buf, "debriefUnread", pDemoState->bDebriefUnread ); buf.PutChar( ',' );
		JSONPutString( buf, "target", pDemoState->ePlayerSide == k_eTFCampaignSide_Neutral ? "" : DemoTargetForSide( pDemoState->ePlayerSide ) ); buf.PutChar( ',' );
		JSONPutString( buf, "pendingBattleId", pDemoState->strPendingBattleID.Get() ); buf.PutChar( ',' );
		JSONPutString( buf, "pendingNode", pDemoState->strPendingNode.Get() ); buf.PutChar( ',' );
		JSONPutString( buf, "pendingMap", pDemoState->strPendingMap.Get() ); buf.PutChar( ',' );
		JSONPutString( buf, "lastResult", pDemoState->strLastResultType.Get() ); buf.PutChar( ',' );
		JSONPutString( buf, "lastTitle", pDemoState->strLastResultTitle.Get() ); buf.PutChar( ',' );
		JSONPutString( buf, "lastBody", pDemoState->strLastResultBody.Get() ); buf.PutChar( ',' );
		buf.PutString( "\"events\":[" );
		FOR_EACH_VEC( pDemoState->vecEvents, i )
		{
			if ( i > 0 ) buf.PutChar( ',' );
			const TFCampaignDemoEvent_t &event = pDemoState->vecEvents[i];
			buf.PutChar( '{' );
			JSONPutInt( buf, "sequence", event.nSequence ); buf.PutChar( ',' );
			JSONPutString( buf, "type", event.strType.Get() ); buf.PutChar( ',' );
			JSONPutString( buf, "title", event.strTitle.Get() ); buf.PutChar( ',' );
			JSONPutString( buf, "body", event.strBody.Get() ); buf.PutChar( ',' );
			JSONPutString( buf, "node", event.strNode.Get() );
			buf.PutChar( '}' );
		}
		buf.PutString( "]}" );
	}

	buf.PutChar( '}' );
}

//=============================================================================
// CTFCampaignFeed
//=============================================================================
CTFCampaignFeed::CTFCampaignFeed()
	: CAutoGameSystemPerFrame( "CTFCampaignFeed" )
{
	m_flNextPublish   = 0.f;
	m_bCloseRequested = false;
	m_bOpenRequested  = false;
	m_bLoaded         = false;
	m_flApplyDemoConfigAt = -1.f;
	m_flReturnToMapAt = -1.f;
	ResetDemoState();
}

//-----------------------------------------------------------------------------
static CTFCampaignFeed s_TFCampaignFeed;

CTFCampaignFeed *TFCampaignFeed()
{
	return &s_TFCampaignFeed;
}

CON_COMMAND_F( frontress_demo_force_red_win, "Resolve the pending demo battle as a RED victory.", FCVAR_NONE )
{
	TFCampaignFeed()->DebugResolveDemoBattle( TF_TEAM_RED );
}

CON_COMMAND_F( frontress_demo_force_blu_win, "Resolve the pending demo battle as a BLU victory.", FCVAR_NONE )
{
	TFCampaignFeed()->DebugResolveDemoBattle( TF_TEAM_BLUE );
}

CON_COMMAND_F( frontress_demo_force_player_win, "Resolve the pending demo battle as a player victory.", FCVAR_NONE )
{
	const ETFCampaignSide eSide = TFCampaignFeed()->DemoState().ePlayerSide;
	TFCampaignFeed()->DebugResolveDemoBattle( eSide == k_eTFCampaignSide_Red ? TF_TEAM_RED :
	                                         eSide == k_eTFCampaignSide_Blu ? TF_TEAM_BLUE : TEAM_UNASSIGNED );
}

CON_COMMAND_F( frontress_demo_force_player_loss, "Resolve the pending demo battle as a player defeat.", FCVAR_NONE )
{
	const ETFCampaignSide eSide = TFCampaignFeed()->DemoState().ePlayerSide;
	TFCampaignFeed()->DebugResolveDemoBattle( eSide == k_eTFCampaignSide_Red ? TF_TEAM_BLUE :
	                                         eSide == k_eTFCampaignSide_Blu ? TF_TEAM_RED : TEAM_UNASSIGNED );
}

CON_COMMAND_F( frontress_demo_force_stalemate, "Resolve the pending demo battle as a full-round stalemate.", FCVAR_NONE )
{
	TFCampaignFeed()->DebugResolveDemoBattle( TEAM_UNASSIGNED );
}

CON_COMMAND_F( frontress_demo_reset, "Reset the local Frontress demo campaign.", FCVAR_NONE )
{
	TFCampaignFeed()->DebugResetDemoCampaign();
}

CON_COMMAND_F( frontress_demo_set_stage, "Set the local demo operation stage (1-3).", FCVAR_NONE )
{
	if ( args.ArgC() != 2 )
	{
		Msg( "usage: frontress_demo_set_stage <1-3>\n" );
		return;
	}
	TFCampaignFeed()->DebugSetDemoStage( V_atoi( args[1] ) );
}

CON_COMMAND_F( frontress_demo_status, "Print the local Frontress demo campaign state.", FCVAR_NONE )
{
	const TFCampaignDemoState_t &state = TFCampaignFeed()->DemoState();
	Msg( "Frontress demo: faction=%s stage=%d battles=%d wins=%d losses=%d completed=%d captured=%d pending=%s\n",
	     SideName( state.ePlayerSide ), state.nStage, state.nBattlesPlayed, state.nVictories, state.nDefeats,
	     state.bCompleted, state.bTerritoryCaptured, state.strPendingBattleID.Get() );
}

CON_COMMAND_F( frontress_demo_validate, "Validate local Frontress demo campaign invariants.", FCVAR_NONE )
{
	TFCampaignFeed()->DebugValidateDemoCampaign();
}

//-----------------------------------------------------------------------------
bool CTFCampaignFeed::Init()
{
	if ( TFCampaignDemoMode() )
	{
		LoadDemoState();
		const int nNow = (int)time( NULL );
		if ( m_DemoState.ePlayerSide != k_eTFCampaignSide_Neutral && !m_DemoState.bCompleted &&
		     m_DemoState.strPendingBattleID.IsEmpty() && m_DemoState.nLastSeenUnix > 0 &&
		     nNow - m_DemoState.nLastSeenUnix >= 300 )
		{
			AppendDemoEvent( "WHILE_AWAY", "WHILE YOU WERE AWAY",
			                 "The opposing armies contested Iron Junction. Your operation remains active.", "junction" );
		}
		m_DemoState.nLastSeenUnix = nNow;
		SaveDemoState();
		ListenForGameEvent( "teamplay_round_win" );
		m_bOpenRequested = m_DemoState.ePlayerSide == k_eTFCampaignSide_Neutral ||
		                   !m_DemoState.strPendingBattleID.IsEmpty() || m_DemoState.bDebriefUnread || m_DemoState.bCompleted;
	}
	return true;
}

//-----------------------------------------------------------------------------
void CTFCampaignFeed::Update( float frametime )
{
	(void)frametime;
	if ( !TFCampaignDemoMode() )
		return;

	Update();
	const float flNow = (float)Plat_FloatTime();
	if ( m_flApplyDemoConfigAt > 0.f && flNow >= m_flApplyDemoConfigAt )
	{
		m_flApplyDemoConfigAt = -1.f;
		const char *pszTeam = m_DemoState.ePlayerSide == k_eTFCampaignSide_Red ? "red" : "blue";
		const int nPlayerTarget = m_DemoState.nPendingPlayerTarget > 0 ?
		                          m_DemoState.nPendingPlayerTarget : DemoPlayerTargetForStage( m_DemoState.nStage );
		// The cfg starts with quota 0. Put the human on the authored side first,
		// then let fill mode balance quota-managed bots around that player.
		CFmtStr strSetup( "exec frontress_demo.cfg\njointeam %s\ntf_bot_quota %d\n", pszTeam, nPlayerTarget );
		engine->ClientCmd_Unrestricted( strSetup.Get() );
	}

	if ( m_flReturnToMapAt > 0.f && flNow >= m_flReturnToMapAt )
	{
		m_flReturnToMapAt = -1.f;
		engine->ClientCmd_Unrestricted( "disconnect\n" );
	}
}

//-----------------------------------------------------------------------------
void CTFCampaignFeed::LevelInitPostEntity()
{
	if ( TFCampaignDemoMode() && !m_DemoState.strPendingBattleID.IsEmpty() )
	{
		m_flApplyDemoConfigAt = (float)Plat_FloatTime() + 1.f;
	}
}

//-----------------------------------------------------------------------------
void CTFCampaignFeed::FireGameEvent( IGameEvent *pEvent )
{
	if ( !TFCampaignDemoMode() || !pEvent || m_DemoState.strPendingBattleID.IsEmpty() )
		return;

	if ( !V_stricmp( pEvent->GetName(), "teamplay_round_win" ) && pEvent->GetBool( "full_round", true ) )
	{
		const int nWinningTeam = pEvent->GetInt( "team" );
		if ( nWinningTeam == TF_TEAM_RED || nWinningTeam == TF_TEAM_BLUE || nWinningTeam == TEAM_UNASSIGNED )
			ResolveDemoBattle( nWinningTeam );
	}
}

//-----------------------------------------------------------------------------
void CTFCampaignFeed::ResetDemoState()
{
	m_DemoState.nSchemaVersion = 1;
	m_DemoState.ePlayerSide = k_eTFCampaignSide_Neutral;
	m_DemoState.nStage = 1;
	m_DemoState.nBattlesPlayed = 0;
	m_DemoState.nVictories = 0;
	m_DemoState.nDefeats = 0;
	m_DemoState.nEventSequence = 0;
	m_DemoState.nNextBattleSerial = 1;
	m_DemoState.nPendingPlayerTarget = 0;
	m_DemoState.nLastSeenUnix = 0;
	m_DemoState.bCompleted = false;
	m_DemoState.bTerritoryCaptured = false;
	m_DemoState.bDebriefUnread = false;
	m_DemoState.strPendingBattleID.Clear();
	m_DemoState.strPendingNode.Clear();
	m_DemoState.strPendingMap.Clear();
	m_DemoState.strLastResultType.Clear();
	m_DemoState.strLastResultTitle.Clear();
	m_DemoState.strLastResultBody.Clear();
	m_DemoState.vecEvents.RemoveAll();
}

//-----------------------------------------------------------------------------
void CTFCampaignFeed::LoadDemoState()
{
	ResetDemoState();
	KeyValuesAD pState( "FrontressDemoState" );
	bool bLoadedBackup = false;
	bool bLoaded = pState->LoadFromFile( g_pFullFileSystem, DEMO_STATE_FILE, "MOD" ) &&
	               pState->GetInt( "schemaVersion", 0 ) == 1;
	if ( !bLoaded )
	{
		// Do not let the next atomic save rotate an unreadable primary over the
		// known-good backup we are about to recover from.
		g_pFullFileSystem->RemoveFile( DEMO_STATE_FILE, "MOD" );
		pState->Clear();
		bLoaded = pState->LoadFromFile( g_pFullFileSystem, DEMO_STATE_BACKUP, "MOD" ) &&
		          pState->GetInt( "schemaVersion", 0 ) == 1;
		bLoadedBackup = bLoaded;
	}
	if ( !bLoaded )
	{
		SaveDemoState();
		return;
	}

	const char *pszFaction = pState->GetString( "faction", "NEUTRAL" );
	m_DemoState.ePlayerSide = !V_stricmp( pszFaction, "RED" ) ? k_eTFCampaignSide_Red :
	                              ( !V_stricmp( pszFaction, "BLU" ) ? k_eTFCampaignSide_Blu : k_eTFCampaignSide_Neutral );
	m_DemoState.nStage = clamp( pState->GetInt( "stage", 1 ), 1, DEMO_STAGE_COUNT );
	m_DemoState.nBattlesPlayed = MAX( 0, pState->GetInt( "battlesPlayed", 0 ) );
	m_DemoState.nVictories = MAX( 0, pState->GetInt( "victories", 0 ) );
	m_DemoState.nDefeats = MAX( 0, pState->GetInt( "defeats", 0 ) );
	m_DemoState.nEventSequence = MAX( 0, pState->GetInt( "eventSequence", 0 ) );
	m_DemoState.nNextBattleSerial = MAX( 1, pState->GetInt( "nextBattleSerial", m_DemoState.nBattlesPlayed + 1 ) );
	m_DemoState.nPendingPlayerTarget = pState->GetInt( "pendingPlayerTarget", 0 );
	m_DemoState.nLastSeenUnix = MAX( 0, pState->GetInt( "lastSeenUnix", 0 ) );
	m_DemoState.bCompleted = pState->GetBool( "completed", false );
	m_DemoState.bTerritoryCaptured = pState->GetBool( "territoryCaptured", m_DemoState.bCompleted );
	m_DemoState.bDebriefUnread = pState->GetBool( "debriefUnread", false );
	m_DemoState.strPendingBattleID = pState->GetString( "pendingBattleId", "" );
	m_DemoState.strPendingNode = pState->GetString( "pendingNode", "" );
	m_DemoState.strPendingMap = pState->GetString( "pendingMap", "" );
	m_DemoState.strLastResultType = pState->GetString( "lastResult", "" );
	m_DemoState.strLastResultTitle = pState->GetString( "lastTitle", "" );
	m_DemoState.strLastResultBody = pState->GetString( "lastBody", "" );
	if ( m_DemoState.ePlayerSide == k_eTFCampaignSide_Neutral )
	{
		m_DemoState.nStage = 1;
		m_DemoState.nBattlesPlayed = m_DemoState.nVictories = m_DemoState.nDefeats = 0;
		m_DemoState.bCompleted = m_DemoState.bTerritoryCaptured = false;
	}
	if ( m_DemoState.bTerritoryCaptured )
		m_DemoState.bCompleted = true;
	if ( m_DemoState.nVictories + m_DemoState.nDefeats != m_DemoState.nBattlesPlayed )
		m_DemoState.nBattlesPlayed = m_DemoState.nVictories + m_DemoState.nDefeats;

	const bool bValidPending = m_DemoState.strPendingBattleID.IsEmpty() ||
	                           ( m_DemoState.ePlayerSide != k_eTFCampaignSide_Neutral &&
	                             !V_stricmp( m_DemoState.strPendingNode.Get(), DemoTargetNode() ) &&
	                             !V_stricmp( m_DemoState.strPendingMap.Get(), DemoStageMap() ) );
	if ( m_DemoState.bCompleted || !bValidPending )
	{
		m_DemoState.strPendingBattleID.Clear();
		m_DemoState.strPendingNode.Clear();
		m_DemoState.strPendingMap.Clear();
		m_DemoState.nPendingPlayerTarget = 0;
	}
	else if ( !m_DemoState.strPendingBattleID.IsEmpty() )
	{
		// Team size is authored by the stage, not trusted from a stale or edited
		// snapshot. This also migrates early demo saves that stored the wrong size.
		m_DemoState.nPendingPlayerTarget = DemoPlayerTargetForStage( m_DemoState.nStage );
	}

	KeyValues *pEvents = pState->FindKey( "events" );
	for ( KeyValues *pItem = pEvents ? pEvents->GetFirstTrueSubKey() : NULL;
	      pItem && m_DemoState.vecEvents.Count() < 12; pItem = pItem->GetNextTrueSubKey() )
	{
		const int i = m_DemoState.vecEvents.AddToTail();
		TFCampaignDemoEvent_t &event = m_DemoState.vecEvents[i];
		event.nSequence = pItem->GetInt( "sequence", 0 );
		event.strType = pItem->GetString( "type", "event" );
		event.strTitle = pItem->GetString( "title", "" );
		event.strBody = pItem->GetString( "body", "" );
		event.strNode = pItem->GetString( "node", "" );
		m_DemoState.nEventSequence = MAX( m_DemoState.nEventSequence, event.nSequence );
	}

	if ( !ValidateDemoState() )
	{
		Warning( "Frontress demo: %s violated domain invariants; starting a clean local campaign.\n",
		         bLoadedBackup ? DEMO_STATE_BACKUP : DEMO_STATE_FILE );
		g_pFullFileSystem->RemoveFile( bLoadedBackup ? DEMO_STATE_BACKUP : DEMO_STATE_FILE, "MOD" );
		ResetDemoState();
		AppendDemoEvent( "SAVE_RECOVERED", "CAMPAIGN RECOVERED",
		                 "An invalid local snapshot was replaced with a clean campaign.", "" );
	}
}

//-----------------------------------------------------------------------------
void CTFCampaignFeed::SaveDemoState()
{
	if ( !TFCampaignDemoMode() )
		return;
	if ( !ValidateDemoState() )
	{
		Warning( "Frontress demo: refusing to persist an invalid campaign state.\n" );
		return;
	}

	KeyValuesAD pState( "FrontressDemoState" );
	pState->SetInt( "schemaVersion", m_DemoState.nSchemaVersion );
	pState->SetString( "campaignId", "demo_second_gravel_war" );
	pState->SetString( "faction", SideName( m_DemoState.ePlayerSide ) );
	pState->SetInt( "stage", m_DemoState.nStage );
	pState->SetInt( "battlesPlayed", m_DemoState.nBattlesPlayed );
	pState->SetInt( "victories", m_DemoState.nVictories );
	pState->SetInt( "defeats", m_DemoState.nDefeats );
	pState->SetInt( "eventSequence", m_DemoState.nEventSequence );
	pState->SetInt( "nextBattleSerial", m_DemoState.nNextBattleSerial );
	pState->SetInt( "pendingPlayerTarget", m_DemoState.nPendingPlayerTarget );
	pState->SetInt( "lastSeenUnix", m_DemoState.nLastSeenUnix );
	pState->SetBool( "completed", m_DemoState.bCompleted );
	pState->SetBool( "territoryCaptured", m_DemoState.bTerritoryCaptured );
	pState->SetBool( "debriefUnread", m_DemoState.bDebriefUnread );
	pState->SetString( "pendingBattleId", m_DemoState.strPendingBattleID.Get() );
	pState->SetString( "pendingNode", m_DemoState.strPendingNode.Get() );
	pState->SetString( "pendingMap", m_DemoState.strPendingMap.Get() );
	pState->SetString( "lastResult", m_DemoState.strLastResultType.Get() );
	pState->SetString( "lastTitle", m_DemoState.strLastResultTitle.Get() );
	pState->SetString( "lastBody", m_DemoState.strLastResultBody.Get() );

	KeyValues *pEvents = new KeyValues( "events" );
	pState->AddSubKey( pEvents );
	FOR_EACH_VEC( m_DemoState.vecEvents, i )
	{
		const TFCampaignDemoEvent_t &event = m_DemoState.vecEvents[i];
		char szEventKey[32];
		V_snprintf( szEventKey, sizeof( szEventKey ), "%d", event.nSequence );
		KeyValues *pItem = new KeyValues( szEventKey );
		pItem->SetInt( "sequence", event.nSequence );
		pItem->SetString( "type", event.strType.Get() );
		pItem->SetString( "title", event.strTitle.Get() );
		pItem->SetString( "body", event.strBody.Get() );
		pItem->SetString( "node", event.strNode.Get() );
		pEvents->AddSubKey( pItem );
	}

	g_pFullFileSystem->CreateDirHierarchy( "cfg", "MOD" );
	if ( !pState->SaveToFile( g_pFullFileSystem, DEMO_STATE_TEMP, "MOD" ) )
	{
		Warning( "Frontress demo: failed to save %s\n", DEMO_STATE_TEMP );
		return;
	}

	g_pFullFileSystem->RemoveFile( DEMO_STATE_BACKUP, "MOD" );
	if ( g_pFullFileSystem->FileExists( DEMO_STATE_FILE, "MOD" ) )
		g_pFullFileSystem->RenameFile( DEMO_STATE_FILE, DEMO_STATE_BACKUP, "MOD" );
	if ( !g_pFullFileSystem->RenameFile( DEMO_STATE_TEMP, DEMO_STATE_FILE, "MOD" ) )
		Warning( "Frontress demo: failed to install saved campaign state\n" );
}

//-----------------------------------------------------------------------------
void CTFCampaignFeed::AppendDemoEvent( const char *pszType, const char *pszTitle,
                                      const char *pszBody, const char *pszNode )
{
	while ( m_DemoState.vecEvents.Count() >= 12 )
		m_DemoState.vecEvents.Remove( 0 );
	const int i = m_DemoState.vecEvents.AddToTail();
	TFCampaignDemoEvent_t &event = m_DemoState.vecEvents[i];
	event.nSequence = ++m_DemoState.nEventSequence;
	event.strType = pszType ? pszType : "event";
	event.strTitle = pszTitle ? pszTitle : "";
	event.strBody = pszBody ? pszBody : "";
	event.strNode = pszNode ? pszNode : "";
}

//-----------------------------------------------------------------------------
const char *CTFCampaignFeed::DemoTargetNode() const
{
	return DemoTargetForSide( m_DemoState.ePlayerSide );
}

const char *CTFCampaignFeed::DemoStageMap() const
{
	return DemoStageMapForSide( m_DemoState.ePlayerSide, m_DemoState.nStage );
}

const char *CTFCampaignFeed::DemoStageKind() const
{
	return DemoStageKindForIndex( m_DemoState.nStage );
}

//-----------------------------------------------------------------------------
void CTFCampaignFeed::SelectDemoFaction( const char *pszFaction )
{
	if ( !pszFaction || m_DemoState.nBattlesPlayed > 0 || !m_DemoState.strPendingBattleID.IsEmpty() )
		return;
	ETFCampaignSide eSide = !V_stricmp( pszFaction, "RED" ) ? k_eTFCampaignSide_Red :
	                            ( !V_stricmp( pszFaction, "BLU" ) ? k_eTFCampaignSide_Blu : k_eTFCampaignSide_Neutral );
	if ( eSide == k_eTFCampaignSide_Neutral )
		return;
	m_DemoState.ePlayerSide = eSide;
	AppendDemoEvent( eSide == k_eTFCampaignSide_Red ? "FACTION_CHOSEN_RED" : "FACTION_CHOSEN_BLU",
	                 eSide == k_eTFCampaignSide_Red ? "RED MOBILIZED" : "BLU MOBILIZED",
	                 "Your side has committed to the Iron Track operation.", DemoTargetNode() );
	SaveDemoState();
	m_flNextPublish = 0.f;
}

//-----------------------------------------------------------------------------
void CTFCampaignFeed::LaunchPendingDemoBattle()
{
	const char *pszMap = m_DemoState.strPendingMap.Get();
	bool bSafeMap = pszMap && pszMap[0];
	for ( const char *p = pszMap; bSafeMap && *p; ++p )
		bSafeMap = V_isalnum( *p ) || *p == '_';
	if ( !bSafeMap )
		return;

	const int nPlayerTarget = clamp( m_DemoState.nPendingPlayerTarget, 2, 18 );
	CFmtStr strLaunch( "disconnect\nwait\nwait\nmaxplayers %d\nmap %s\n", nPlayerTarget + 1, pszMap );
	engine->ClientCmd_Unrestricted( strLaunch.Get() );
}

//-----------------------------------------------------------------------------
void CTFCampaignFeed::StartDemoBattle( const char *pszNode )
{
	if ( m_DemoState.ePlayerSide == k_eTFCampaignSide_Neutral || m_DemoState.bCompleted ||
	     !m_DemoState.strPendingBattleID.IsEmpty() || !pszNode || V_stricmp( pszNode, DemoTargetNode() ) )
		return;

	char szBattleID[64];
	V_snprintf( szBattleID, sizeof( szBattleID ), "demo_%05d", m_DemoState.nNextBattleSerial++ );
	m_DemoState.strPendingBattleID = szBattleID;
	m_DemoState.strPendingNode = DemoTargetNode();
	m_DemoState.strPendingMap = DemoStageMap();
	m_DemoState.nPendingPlayerTarget = DemoPlayerTargetForStage( m_DemoState.nStage );
	m_strDeployNode = DemoTargetNode();
	tf_campaign_deploy.SetValue( DemoTargetNode() );

	char szBody[256];
	V_snprintf( szBody, sizeof( szBody ), "%s selected: %s on %s, %dv%d with local reinforcements.",
	            DemoStageKind(), DemoTargetNode(), DemoStageMap(),
	            m_DemoState.nPendingPlayerTarget / 2, m_DemoState.nPendingPlayerTarget / 2 );
	AppendDemoEvent( "BATTLE_DEPLOYED", "DEPLOYMENT FOUND", szBody, DemoTargetNode() );
	SaveDemoState();
	m_flNextPublish = 0.f;
	LaunchPendingDemoBattle();
}

void CTFCampaignFeed::RejoinDemoBattle()
{
	if ( !m_DemoState.strPendingBattleID.IsEmpty() )
		LaunchPendingDemoBattle();
}

void CTFCampaignFeed::AbandonDemoBattle()
{
	if ( m_DemoState.strPendingBattleID.IsEmpty() )
		return;
	AppendDemoEvent( "BATTLE_ABANDONED", "DEPLOYMENT CANCELLED",
	                 "The interrupted battle was abandoned. The war state did not change.", m_DemoState.strPendingNode.Get() );
	m_DemoState.strPendingBattleID.Clear();
	m_DemoState.strPendingNode.Clear();
	m_DemoState.strPendingMap.Clear();
	m_DemoState.nPendingPlayerTarget = 0;
	m_strDeployNode.Clear();
	tf_campaign_deploy.SetValue( "" );
	SaveDemoState();
	m_flNextPublish = 0.f;
}

//-----------------------------------------------------------------------------
void CTFCampaignFeed::ResolveDemoBattle( int nWinningTeam )
{
	const bool bStalemate = nWinningTeam == TEAM_UNASSIGNED;
	const bool bPlayerWon = ( m_DemoState.ePlayerSide == k_eTFCampaignSide_Red && nWinningTeam == TF_TEAM_RED ) ||
	                        ( m_DemoState.ePlayerSide == k_eTFCampaignSide_Blu && nWinningTeam == TF_TEAM_BLUE );
	const CUtlString strNode = m_DemoState.strPendingNode;
	const char *pszWinningSide = nWinningTeam == TF_TEAM_RED ? "RED" :
	                             ( nWinningTeam == TF_TEAM_BLUE ? "BLU" : "STALEMATE" );
	CFmtStrN<128> strBattleTitle( bStalemate ? "STALEMATE" : "%s VICTORY", pszWinningSide );
	CFmtStr strBattleBody( bStalemate ?
	                         "The offensive ran out of time; defenders held BattleTicket %s." :
	                         "Full-round result recorded for BattleTicket %s.", m_DemoState.strPendingBattleID.Get() );
	AppendDemoEvent( bStalemate ? "BATTLE_RESULT_STALEMATE" :
	                 ( nWinningTeam == TF_TEAM_RED ? "BATTLE_RESULT_RED" : "BATTLE_RESULT_BLU" ),
	                 strBattleTitle.Get(), strBattleBody.Get(), strNode.Get() );
	m_DemoState.nBattlesPlayed++;
	if ( bPlayerWon )
	{
		m_DemoState.nVictories++;
		if ( m_DemoState.nStage >= DEMO_STAGE_COUNT )
		{
			m_DemoState.bCompleted = true;
			m_DemoState.bTerritoryCaptured = true;
			m_DemoState.strLastResultType = "CAPTURED";
			m_DemoState.strLastResultTitle = "VICTORY - TERRITORY CAPTURED";
			m_DemoState.strLastResultBody = "The operation succeeded. Your side owns the sector and a new front has opened.";
			AppendDemoEvent( "TERRITORY_CAPTURED", "TERRITORY CAPTURED", m_DemoState.strLastResultBody.Get(), strNode.Get() );
			AppendDemoEvent( "FRONT_OPENED", "NEW FRONT OPENED",
			                 "The captured territory opened a route into the next enemy sector. Continue the shared war in the Playtest.",
			                 DemoNextTargetForSide( m_DemoState.ePlayerSide ) );
		}
		else
		{
			m_DemoState.nStage++;
			m_DemoState.strLastResultType = "ADVANCED";
			m_DemoState.strLastResultTitle = "VICTORY - OPERATION ADVANCED";
			m_DemoState.strLastResultBody = "Victory moved the operation to its next tactical stage.";
			AppendDemoEvent( "OPERATION_ADVANCED", "OPERATION ADVANCED", m_DemoState.strLastResultBody.Get(), strNode.Get() );
		}
	}
	else
	{
		m_DemoState.nDefeats++;
		m_DemoState.nStage = MAX( 1, m_DemoState.nStage - 1 );
		m_DemoState.strLastResultType = bStalemate ? "STALEMATE" : "REPULSED";
		m_DemoState.strLastResultTitle = bStalemate ? "STALEMATE - OFFENSIVE REPULSED" : "DEFEAT - OFFENSIVE REPULSED";
		m_DemoState.strLastResultBody = bStalemate ?
		                                      "Time expired without a winner. Strategically, the defenders held the sector and repulsed the offensive." :
		                                      "The enemy held the sector. The operation remains active and can be attempted again.";
		AppendDemoEvent( "OPERATION_REPULSED", "OFFENSIVE REPULSED", m_DemoState.strLastResultBody.Get(), strNode.Get() );
	}

	if ( !m_DemoState.bCompleted && m_DemoState.nBattlesPlayed >= DEMO_MAX_BATTLES )
	{
		m_DemoState.bCompleted = true;
		m_DemoState.bTerritoryCaptured = false;
		m_DemoState.strLastResultType = "TIMED_OUT";
		m_DemoState.strLastResultTitle = "WAR REPORT";
		m_DemoState.strLastResultBody = "The operation timed out after four battles. Every result still changed the front; begin a new local campaign or join the shared Playtest war.";
		AppendDemoEvent( "OPERATION_FAILED", "OPERATION ENDED", m_DemoState.strLastResultBody.Get(), strNode.Get() );
	}

	if ( !m_DemoState.bCompleted )
	{
		const bool bBackgroundGained = ( m_DemoState.nBattlesPlayed % 2 ) != 0;
		const char *pszBackgroundBody = bBackgroundGained ?
		                                    "A simulated neighboring offensive gained momentum while you were deployed." :
		                                    "Local defenders stalled a neighboring offensive and shifted the supply line.";
		AppendDemoEvent( bBackgroundGained ? "BACKGROUND_FRONT_GAINED" : "BACKGROUND_FRONT_STALLED",
		                 "WAR CONTINUES ELSEWHERE", pszBackgroundBody,
		                 DemoBackgroundTargetForSide( m_DemoState.ePlayerSide ) );
	}

	m_DemoState.strPendingBattleID.Clear();
	m_DemoState.strPendingNode.Clear();
	m_DemoState.strPendingMap.Clear();
	m_DemoState.nPendingPlayerTarget = 0;
	m_DemoState.bDebriefUnread = true;
	m_strDeployNode.Clear();
	tf_campaign_deploy.SetValue( "" );
	SaveDemoState();
	m_flNextPublish = 0.f;
	m_bOpenRequested = true;
	m_flReturnToMapAt = (float)Plat_FloatTime() + 7.f;

	char szInstructorTitle[256], szInstructorBody[1024];
	DemoLocalizedResultText( m_DemoState.strLastResultType.Get(),
	                         m_DemoState.strLastResultTitle.Get(), m_DemoState.strLastResultBody.Get(),
	                         szInstructorTitle, sizeof( szInstructorTitle ),
	                         szInstructorBody, sizeof( szInstructorBody ) );
	FrontressInstructor_Show( szInstructorTitle, szInstructorBody,
	                          100, 6.f, "demo-result" );
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

bool CTFCampaignFeed::BTakeOpenRequest()
{
	const bool bRequested = m_bOpenRequested;
	m_bOpenRequested = false;
	return bRequested;
}

void CTFCampaignFeed::DebugResolveDemoBattle( int nWinningTeam )
{
	if ( !TFCampaignDemoMode() )
	{
		Msg( "Frontress demo: command ignored outside demo mode.\n" );
		return;
	}
	if ( m_DemoState.strPendingBattleID.IsEmpty() )
	{
		Msg( "Frontress demo: deploy a battle first; there is no pending BattleTicket.\n" );
		return;
	}
	if ( nWinningTeam != TF_TEAM_RED && nWinningTeam != TF_TEAM_BLUE && nWinningTeam != TEAM_UNASSIGNED )
	{
		Msg( "Frontress demo: invalid forced result.\n" );
		return;
	}
	ResolveDemoBattle( nWinningTeam );
}

void CTFCampaignFeed::DebugResetDemoCampaign()
{
	if ( !TFCampaignDemoMode() )
		return;
	ResetDemoState();
	m_DemoState.nLastSeenUnix = (int)time( NULL );
	m_strDeployNode.Clear();
	tf_campaign_deploy.SetValue( "" );
	AppendDemoEvent( "CAMPAIGN_RESET", "SECOND GRAVEL WAR", "A new local campaign has begun.", "" );
	g_pFullFileSystem->RemoveFile( DEMO_STATE_TEMP, "MOD" );
	g_pFullFileSystem->RemoveFile( DEMO_STATE_BACKUP, "MOD" );
	g_pFullFileSystem->RemoveFile( DEMO_STATE_FILE, "MOD" );
	SaveDemoState();
	m_bOpenRequested = true;
	m_flNextPublish = 0.f;
	Msg( "Frontress demo: campaign reset.\n" );
}

void CTFCampaignFeed::DebugSetDemoStage( int nStage )
{
	if ( !TFCampaignDemoMode() || m_DemoState.ePlayerSide == k_eTFCampaignSide_Neutral ||
	     !m_DemoState.strPendingBattleID.IsEmpty() )
		return;
	if ( m_DemoState.bCompleted || m_DemoState.nBattlesPlayed >= DEMO_MAX_BATTLES )
	{
		Msg( "Frontress demo: reset the completed campaign before changing its stage.\n" );
		return;
	}
	m_DemoState.nStage = clamp( nStage, 1, DEMO_STAGE_COUNT );
	AppendDemoEvent( "DEBUG_STAGE", "OPERATION STAGE CHANGED", DemoStageKind(), DemoTargetNode() );
	SaveDemoState();
	m_flNextPublish = 0.f;
	Msg( "Frontress demo: operation stage set to %d (%s).\n", m_DemoState.nStage, DemoStageKind() );
}

bool CTFCampaignFeed::ValidateDemoState() const
{
	if ( m_DemoState.nSchemaVersion != 1 || m_DemoState.nStage < 1 || m_DemoState.nStage > DEMO_STAGE_COUNT )
		return false;
	if ( m_DemoState.nBattlesPlayed < 0 || m_DemoState.nBattlesPlayed > DEMO_MAX_BATTLES ||
	     m_DemoState.nVictories < 0 || m_DemoState.nDefeats < 0 ||
	     m_DemoState.nVictories + m_DemoState.nDefeats != m_DemoState.nBattlesPlayed )
		return false;
	if ( m_DemoState.bTerritoryCaptured && !m_DemoState.bCompleted )
		return false;
	if ( m_DemoState.bCompleted && !m_DemoState.strPendingBattleID.IsEmpty() )
		return false;
	if ( m_DemoState.ePlayerSide == k_eTFCampaignSide_Neutral )
	{
		return m_DemoState.nBattlesPlayed == 0 && !m_DemoState.bCompleted &&
		       m_DemoState.strPendingBattleID.IsEmpty();
	}
	if ( !m_DemoState.strPendingBattleID.IsEmpty() )
	{
		return !V_stricmp( m_DemoState.strPendingNode.Get(), DemoTargetNode() ) &&
		       !V_stricmp( m_DemoState.strPendingMap.Get(), DemoStageMap() ) &&
		       m_DemoState.nPendingPlayerTarget == DemoPlayerTargetForStage( m_DemoState.nStage );
	}
	return m_DemoState.nPendingPlayerTarget == 0;
}

void CTFCampaignFeed::DebugValidateDemoCampaign() const
{
	Msg( "Frontress demo: campaign state is %s.\n", ValidateDemoState() ? "VALID" : "INVALID" );
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
			if ( TFCampaignDemoMode() )
			{
				StartDemoBattle( pszArg );
				continue;
			}

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
		else if ( TFCampaignDemoMode() && !V_stricmp( szCommand, "select_faction" ) )
		{
			SelectDemoFaction( pszArg );
		}
		else if ( TFCampaignDemoMode() && !V_stricmp( szCommand, "rejoin" ) )
		{
			RejoinDemoBattle();
		}
		else if ( TFCampaignDemoMode() && !V_stricmp( szCommand, "abandon" ) )
		{
			AbandonDemoBattle();
		}
		else if ( TFCampaignDemoMode() && !V_stricmp( szCommand, "ack_debrief" ) )
		{
			m_DemoState.bDebriefUnread = false;
			SaveDemoState();
			m_flNextPublish = 0.f;
		}
		else if ( TFCampaignDemoMode() && !V_stricmp( szCommand, "reset_demo" ) )
		{
			DebugResetDemoCampaign();
		}
		else if ( TFCampaignDemoMode() && m_DemoState.bCompleted && !V_stricmp( szCommand, "open_playtest" ) )
		{
			UTIL_OpenWebPage( "https://store.steampowered.com/app/5147380/", true );
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
	m_model.BuildDocument( buf, m_strDeployNode.Get(), TFCampaignDemoMode() ? &m_DemoState : NULL );

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
	// Above dashboard notifications (15000), like other fullscreen menu dialogs.
	SetZPos( 30000 );

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
		m_pWeb = new CInteractiveWebPanel( this, "CampaignMapWeb", CampaignPage( true ), true, false );
		m_pWeb->SetViewportScaling( true );
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

	m_pWeb = new CInteractiveWebPanel( this, "CampaignCardWeb", CampaignPage( false ), true, false );
	m_pWeb->SetViewportScaling( true );
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
	if ( TFCampaignDemoMode() && TFCampaignFeed()->BTakeOpenRequest() && m_hDialog.Get() )
	{
		m_hDialog->ShowDialog();
	}

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
