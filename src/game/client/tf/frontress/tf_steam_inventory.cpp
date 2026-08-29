//========= Copyright Team Frontress, All rights reserved. ====================//
//
// Purpose: The app's own inventory. See tf_steam_inventory.h.
//
//=============================================================================//

#include "cbase.h"

#include "tf_steam_inventory.h"

#include "econ_item_schema.h"
#include "econ_item_system.h"
#include "game_item_schema.h"
#include "fmtstr.h"

// memdbgon must be the last include file in a .cpp file!!!
#include "tier0/memdbgon.h"

ConVar tf_steaminv_enable( "tf_steaminv_enable", "1", FCVAR_ARCHIVE,
                           "Read the app's own Steam Inventory Service inventory. The Team Fortress backpack is a "
                           "separate thing and is not affected by this." );
ConVar tf_steaminv_debug( "tf_steaminv_debug", "0", FCVAR_NONE,
                          "Log what the inventory service is doing." );
ConVar tf_steaminv_testitem( "tf_steaminv_testitem", "100", FCVAR_ARCHIVE,
                             "Item definition the inventory panel offers to grant. It has to be a promo item on the "
                             "partner site, or Steam refuses the grant." );
ConVar tf_steaminv_droplist( "tf_steaminv_droplist", "200", FCVAR_ARCHIVE,
                             "Playtime generator definition TriggerItemDrop spends playtime credit on. 0 disables "
                             "drops." );

static CFrontressInventory s_FrontressInventory;
CFrontressInventory *FrontressInventory() { return &s_FrontressInventory; }

#define InvDbg( ... ) do { if ( tf_steaminv_debug.GetBool() ) Msg( "[inv] " __VA_ARGS__ ); } while ( false )

// How long to wait before looking for Steam again when it is not up yet.
static const float k_flRetryInterval = 5.f;

// Floor under a full refresh. The Steam client rate limits GetAllItems itself
// and answers from its cache when asked too often, but a panel that refreshes
// on every layout would still be asking for a round trip we do not need.
static const float k_flRefreshInterval = 10.f;

// A definition property, or false when the definition does not carry it.
static bool BReadDefProperty( ISteamInventory *pInventory, SteamItemDef_t nDefID,
                              const char *pszProperty, CUtlString &strOut )
{
	char szValue[ 1024 ];
	szValue[0] = '\0';

	uint32 unSize = sizeof( szValue );
	if ( !pInventory->GetItemDefinitionProperty( nDefID, pszProperty, szValue, &unSize ) )
		return false;

	// unSize is the number of bytes written, which may or may not include the
	// terminator depending on how much fit.
	unSize = MIN( unSize, (uint32)sizeof( szValue ) - 1 );
	szValue[ unSize ] = '\0';

	if ( !szValue[0] )
		return false;

	strOut = szValue;
	return true;
}

//-----------------------------------------------------------------------------
CFrontressInventory::CFrontressInventory()
	: CAutoGameSystemPerFrame( "CFrontressInventory" )
	, m_bAsked( false )
	, m_bDefinitionsLoaded( false )
	, m_bItemsKnown( false )
	, m_flNextRetryTime( 0.f )
	, m_flNextRefreshTime( 0.f )
	, m_nGeneration( 0 )
{
}

//-----------------------------------------------------------------------------
bool CFrontressInventory::Init()
{
	return true;
}

//-----------------------------------------------------------------------------
void CFrontressInventory::Shutdown()
{
	DestroyPending();
}

//-----------------------------------------------------------------------------
ISteamInventory *CFrontressInventory::Inventory() const
{
	if ( !tf_steaminv_enable.GetBool() )
		return NULL;

	if ( !steamapicontext )
		return NULL;

	return steamapicontext->SteamInventory();
}

//-----------------------------------------------------------------------------
bool CFrontressInventory::BAvailable() const
{
	return Inventory() != NULL;
}

//-----------------------------------------------------------------------------
void CFrontressInventory::Update( float frametime )
{
	ISteamInventory *pInventory = Inventory();
	if ( !pInventory )
	{
		// Steam went away, or the player turned us off. Anything we were
		// holding belongs to a client we can no longer talk to.
		m_bAsked = false;
		m_vecPending.Purge();
		return;
	}

	if ( m_bAsked )
		return;

	const float flNow = Plat_FloatTime();
	if ( flNow < m_flNextRetryTime )
		return;

	m_flNextRetryTime = flNow + k_flRetryInterval;

	// The definitions are what makes an item more than a number, so they are
	// asked for first; the answer arrives as a definition update callback.
	if ( !pInventory->LoadItemDefinitions() )
	{
		m_strLastError = "LoadItemDefinitions failed";
		InvDbg( "LoadItemDefinitions failed, retrying in %.0fs\n", k_flRetryInterval );
		return;
	}

	m_bAsked = true;
	InvDbg( "asked for item definitions\n" );

	Refresh();
}

//-----------------------------------------------------------------------------
void CFrontressInventory::Refresh( bool bForce )
{
	ISteamInventory *pInventory = Inventory();
	if ( !pInventory )
		return;

	const float flNow = Plat_FloatTime();
	if ( !bForce && flNow < m_flNextRefreshTime )
		return;

	m_flNextRefreshTime = flNow + k_flRefreshInterval;

	SteamInventoryResult_t hResult = k_SteamInventoryResultInvalid;
	if ( !pInventory->GetAllItems( &hResult ) )
	{
		m_strLastError = "GetAllItems failed";
		InvDbg( "GetAllItems failed\n" );
		return;
	}

	Track( hResult, k_eRequest_FullUpdate, 0 );
	InvDbg( "GetAllItems -> handle %d\n", hResult );
}

//-----------------------------------------------------------------------------
void CFrontressInventory::GrantPromoItem( SteamItemDef_t nDefID )
{
	ISteamInventory *pInventory = Inventory();
	if ( !pInventory || nDefID <= 0 )
		return;

	SteamInventoryResult_t hResult = k_SteamInventoryResultInvalid;
	if ( !pInventory->AddPromoItem( &hResult, nDefID ) )
	{
		m_strLastError = "AddPromoItem failed";
		InvDbg( "AddPromoItem( %d ) failed\n", nDefID );
		return;
	}

	Track( hResult, k_eRequest_Grant, nDefID );
	InvDbg( "AddPromoItem( %d ) -> handle %d\n", nDefID, hResult );
}

//-----------------------------------------------------------------------------
void CFrontressInventory::TriggerDrop( SteamItemDef_t nGeneratorDefID )
{
	ISteamInventory *pInventory = Inventory();
	if ( !pInventory || nGeneratorDefID <= 0 )
		return;

	SteamInventoryResult_t hResult = k_SteamInventoryResultInvalid;
	if ( !pInventory->TriggerItemDrop( &hResult, nGeneratorDefID ) )
	{
		// Not an error worth showing a player: the Steam client suppresses
		// calls that come too close together, and says so this way.
		InvDbg( "TriggerItemDrop( %d ) refused\n", nGeneratorDefID );
		return;
	}

	Track( hResult, k_eRequest_Drop, nGeneratorDefID );
	InvDbg( "TriggerItemDrop( %d ) -> handle %d\n", nGeneratorDefID, hResult );
}

//-----------------------------------------------------------------------------
void CFrontressInventory::Track( SteamInventoryResult_t hResult, ERequest eRequest, SteamItemDef_t nDefID )
{
	PendingResult_t pending;
	pending.hResult  = hResult;
	pending.eRequest = eRequest;
	pending.nDefID   = nDefID;
	m_vecPending.AddToTail( pending );
}

//-----------------------------------------------------------------------------
int CFrontressInventory::FindPending( SteamInventoryResult_t hResult ) const
{
	FOR_EACH_VEC( m_vecPending, i )
	{
		if ( m_vecPending[i].hResult == hResult )
			return i;
	}

	return m_vecPending.InvalidIndex();
}

//-----------------------------------------------------------------------------
void CFrontressInventory::DestroyPending()
{
	ISteamInventory *pInventory = Inventory();
	if ( pInventory )
	{
		FOR_EACH_VEC( m_vecPending, i )
		{
			pInventory->DestroyResult( m_vecPending[i].hResult );
		}
	}

	m_vecPending.Purge();
}

//-----------------------------------------------------------------------------
void CFrontressInventory::OnResultReady( SteamInventoryResultReady_t *pParam )
{
	const int iPending = FindPending( pParam->m_handle );
	if ( iPending == m_vecPending.InvalidIndex() )
		return;                             // somebody else's handle; not ours to destroy

	const PendingResult_t pending = m_vecPending[ iPending ];
	m_vecPending.Remove( iPending );

	ISteamInventory *pInventory = Inventory();
	if ( !pInventory )
		return;

	if ( pParam->m_result != k_EResultOK && pParam->m_result != k_EResultExpired )
	{
		m_strLastError = CFmtStr( "inventory request failed (EResult %d)", pParam->m_result ).Get();
		InvDbg( "handle %d failed with EResult %d\n", pParam->m_handle, pParam->m_result );
		pInventory->DestroyResult( pParam->m_handle );
		return;
	}

	m_strLastError.Clear();

	switch ( pending.eRequest )
	{
	case k_eRequest_FullUpdate:
		ReadItems( pParam->m_handle );
		break;

	case k_eRequest_Grant:
	case k_eRequest_Drop:
		ReportGranted( pParam->m_handle, pending.nDefID );
		// What we own has changed, and the grant result only holds what was
		// just made. Ask for the whole thing again.
		Refresh( true );
		break;
	}

	pInventory->DestroyResult( pParam->m_handle );
}

//-----------------------------------------------------------------------------
void CFrontressInventory::OnFullUpdate( SteamInventoryFullUpdate_t *pParam )
{
	// A full update on a handle we did not ask for means the inventory changed
	// somewhere else -- a grant from the web, a trade, another client. Read it
	// again rather than showing a stale backpack.
	if ( FindPending( pParam->m_handle ) == m_vecPending.InvalidIndex() )
	{
		InvDbg( "inventory changed elsewhere, refreshing\n" );
		Refresh( true );
	}
}

//-----------------------------------------------------------------------------
void CFrontressInventory::OnDefinitionsUpdated( SteamInventoryDefinitionUpdate_t *pParam )
{
	ReadDefinitions();
}

//-----------------------------------------------------------------------------
void CFrontressInventory::ReadDefinitions()
{
	ISteamInventory *pInventory = Inventory();
	if ( !pInventory )
		return;

	uint32 unCount = 0;
	if ( !pInventory->GetItemDefinitionIDs( NULL, &unCount ) )
		return;

	m_vecDefs.Purge();
	m_bDefinitionsLoaded = true;
	m_nGeneration++;

	if ( unCount == 0 )
	{
		// The service is on and has nothing in it, which is what an app looks
		// like before the definitions are published.
		InvDbg( "the service has no item definitions\n" );
		return;
	}

	CUtlVector< SteamItemDef_t > vecIDs;
	vecIDs.SetCount( unCount );
	if ( !pInventory->GetItemDefinitionIDs( vecIDs.Base(), &unCount ) )
		return;

	for ( uint32 i = 0; i < unCount; i++ )
	{
		FrontressItemDef_t def;
		def.nDefID = vecIDs[i];

		BReadDefProperty( pInventory, def.nDefID, "name", def.strName );
		BReadDefProperty( pInventory, def.nDefID, "description", def.strDescription );
		BReadDefProperty( pInventory, def.nDefID, "display_type", def.strDisplayType );
		BReadDefProperty( pInventory, def.nDefID, "type", def.strType );
		BReadDefProperty( pInventory, def.nDefID, "tf_item_def", def.strGameItem );

		CUtlString strPromo;
		def.bPromo = BReadDefProperty( pInventory, def.nDefID, "promo", strPromo );

		if ( def.strName.IsEmpty() )
		{
			def.strName = CFmtStr( "Item %d", def.nDefID ).Get();
		}

		m_vecDefs.AddToTail( def );
	}

	InvDbg( "read %d item definitions\n", m_vecDefs.Count() );
}

//-----------------------------------------------------------------------------
void CFrontressInventory::ReadItems( SteamInventoryResult_t hResult )
{
	ISteamInventory *pInventory = Inventory();
	if ( !pInventory )
		return;

	uint32 unCount = 0;
	if ( !pInventory->GetResultItems( hResult, NULL, &unCount ) )
		return;

	m_vecItems.Purge();
	m_bItemsKnown = true;
	m_nGeneration++;

	if ( unCount == 0 )
	{
		InvDbg( "inventory is empty\n" );
		return;
	}

	CUtlVector< SteamItemDetails_t > vecDetails;
	vecDetails.SetCount( unCount );
	if ( !pInventory->GetResultItems( hResult, vecDetails.Base(), &unCount ) )
		return;

	for ( uint32 i = 0; i < unCount; i++ )
	{
		const SteamItemDetails_t &details = vecDetails[i];

		// Steam reports what a result did to an item as well as what the
		// player has. An item this result destroyed is not owned any more.
		if ( details.m_unFlags & ( k_ESteamItemRemoved | k_ESteamItemConsumed ) )
			continue;

		FrontressItem_t item;
		item.nInstanceID = details.m_itemId;
		item.nDefID      = details.m_iDefinition;
		item.nQuantity   = details.m_unQuantity;
		m_vecItems.AddToTail( item );
	}

	InvDbg( "read %d items\n", m_vecItems.Count() );
}

//-----------------------------------------------------------------------------
void CFrontressInventory::ReportGranted( SteamInventoryResult_t hResult, SteamItemDef_t nAsked )
{
	ISteamInventory *pInventory = Inventory();
	if ( !pInventory )
		return;

	uint32 unCount = 0;
	if ( !pInventory->GetResultItems( hResult, NULL, &unCount ) || unCount == 0 )
	{
		// A promo the player already has, or a drop with no playtime credit
		// behind it, both answer with an empty set and no error.
		InvDbg( "nothing was granted for %d\n", nAsked );
		return;
	}

	CUtlVector< SteamItemDetails_t > vecDetails;
	vecDetails.SetCount( unCount );
	if ( !pInventory->GetResultItems( hResult, vecDetails.Base(), &unCount ) )
		return;

	for ( uint32 i = 0; i < unCount; i++ )
	{
		const FrontressItemDef_t *pDef = FindDefinition( vecDetails[i].m_iDefinition );
		Msg( "Received %s x%d\n",
		     pDef ? pDef->strName.Get() : CFmtStr( "item %d", vecDetails[i].m_iDefinition ).Get(),
		     vecDetails[i].m_unQuantity );
	}
}

//-----------------------------------------------------------------------------
const FrontressItemDef_t *CFrontressInventory::FindDefinition( SteamItemDef_t nDefID ) const
{
	FOR_EACH_VEC( m_vecDefs, i )
	{
		if ( m_vecDefs[i].nDefID == nDefID )
			return &m_vecDefs[i];
	}

	return NULL;
}

//-----------------------------------------------------------------------------
int CFrontressInventory::GetQuantity( SteamItemDef_t nDefID ) const
{
	int nQuantity = 0;

	FOR_EACH_VEC( m_vecItems, i )
	{
		if ( m_vecItems[i].nDefID == nDefID )
		{
			nQuantity += m_vecItems[i].nQuantity;
		}
	}

	return nQuantity;
}

//-----------------------------------------------------------------------------
int CFrontressInventory::GetGameItemDefIndex( const FrontressItemDef_t *pDef ) const
{
	if ( !pDef || pDef->strGameItem.IsEmpty() )
		return 0;

	GameItemSchema_t *pSchema = GetItemSchema();
	if ( !pSchema )
		return 0;

	const char *pszItem = pDef->strGameItem.Get();

	// A number is an items_game definition index; anything else is the name of
	// one. Names travel better across a schema update, indexes are what the
	// wiki prints, so both are allowed.
	if ( V_isdigit( pszItem[0] ) )
	{
		const int nIndex = V_atoi( pszItem );
		return pSchema->GetItemDefinition( nIndex ) ? nIndex : 0;
	}

	const CEconItemDefinition *pItemDef = pSchema->GetItemDefinitionByName( pszItem );
	return pItemDef ? pItemDef->GetDefinitionIndex() : 0;
}

//-----------------------------------------------------------------------------
void CFrontressInventory::Spew() const
{
	if ( !BAvailable() )
	{
		Msg( "Steam inventory: unavailable (no Steam, or tf_steaminv_enable is 0)\n" );
		return;
	}

	Msg( "Steam inventory: %d definitions, %d items%s\n",
	     m_vecDefs.Count(), m_vecItems.Count(),
	     m_bItemsKnown ? "" : " (not read yet)" );

	if ( !m_strLastError.IsEmpty() )
	{
		Msg( "  last error: %s\n", m_strLastError.Get() );
	}

	FOR_EACH_VEC( m_vecDefs, i )
	{
		const FrontressItemDef_t &def = m_vecDefs[i];
		const int nOwned = GetQuantity( def.nDefID );
		const int nGameItem = GetGameItemDefIndex( &def );

		Msg( "  %6d  %-32s owned %-3d type %-18s model %d%s\n",
		     def.nDefID, def.strName.Get(), nOwned,
		     def.strType.IsEmpty() ? "item" : def.strType.Get(),
		     nGameItem,
		     def.bPromo ? "  (promo)" : "" );
	}
}

//-----------------------------------------------------------------------------
// Console surface. The panel is the way a player sees any of this; these are
// for looking at what the service actually said.
//-----------------------------------------------------------------------------
CON_COMMAND( tf_steaminv_status, "Print what the app's Steam inventory holds." )
{
	FrontressInventory()->Spew();
}

CON_COMMAND( tf_steaminv_refresh, "Ask Steam for the app's inventory again." )
{
	FrontressInventory()->Refresh( true );
}

CON_COMMAND( tf_steaminv_grant, "Grant yourself a promo item by definition id. With no argument, "
                                "the one tf_steaminv_testitem names." )
{
	const SteamItemDef_t nDefID = ( args.ArgC() > 1 ) ? V_atoi( args[1] ) : tf_steaminv_testitem.GetInt();
	FrontressInventory()->GrantPromoItem( nDefID );
}

CON_COMMAND( tf_steaminv_drop, "Spend playtime credit on a drop. With no argument, the generator "
                               "tf_steaminv_droplist names." )
{
	const SteamItemDef_t nDefID = ( args.ArgC() > 1 ) ? V_atoi( args[1] ) : tf_steaminv_droplist.GetInt();
	FrontressInventory()->TriggerDrop( nDefID );
}
