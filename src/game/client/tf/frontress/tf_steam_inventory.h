//========= Copyright Team Frontress, All rights reserved. ====================//
//
// Purpose: The app's own inventory, next to the Team Fortress one.
//
// Team Fortress' backpack is the econ inventory: items the GC owns, described
// by items_game.txt, and none of it is ours to hand out. This is the other
// inventory -- the Steam Inventory Service on our own appid, which Steam hosts
// for us and which we may write to.
//
// Nothing about an item lives in the client. The item definitions are uploaded
// on the partner site (see steamworks/inventory/), Steam hands them to
// LoadItemDefinitions, and an item says which model the game should draw for it
// in its own "tf_item_def" property -- an items_game definition index, or a
// definition name. So a new item is a JSON edit and a publish, not a build.
//
// Everything here is free. The Source SDK licence does not let this project
// sell anything, so no item carries a price, StartPurchase is never called, and
// items are only ever granted by AddPromoItem (a promo the player is eligible
// for) or TriggerItemDrop (playtime credit Steam has already accrued).
//
//=============================================================================//

#ifndef TF_STEAM_INVENTORY_H
#define TF_STEAM_INVENTORY_H
#ifdef _WIN32
#pragma once
#endif

#include "igamesystem.h"
#include "steam/steam_api.h"
#include "utlstring.h"
#include "utlvector.h"

//-----------------------------------------------------------------------------
// One item definition, as the service describes it. This is a cache of what
// Steam already holds: it is thrown away and rebuilt whenever the definitions
// are updated, so nothing may keep a pointer to it across a frame.
//-----------------------------------------------------------------------------
struct FrontressItemDef_t
{
	FrontressItemDef_t() : nDefID( 0 ), bPromo( false ) {}

	SteamItemDef_t nDefID;
	CUtlString     strName;         // already localized by Steam
	CUtlString     strDescription;
	CUtlString     strDisplayType;
	CUtlString     strType;         // "item", "bundle", "playtimegenerator", ...
	bool           bPromo;          // has a "promo" rule, so it can be granted for free

	// What the game should draw for this item, from the definition's own
	// "tf_item_def" property: either an items_game definition index or a
	// definition name. Empty when the item has no model of its own.
	CUtlString     strGameItem;
};

//-----------------------------------------------------------------------------
// One item instance the player owns.
//-----------------------------------------------------------------------------
struct FrontressItem_t
{
	FrontressItem_t() : nInstanceID( k_SteamItemInstanceIDInvalid ), nDefID( 0 ), nQuantity( 0 ) {}

	SteamItemInstanceID_t nInstanceID;
	SteamItemDef_t        nDefID;
	int                   nQuantity;
};

//-----------------------------------------------------------------------------
// The inventory itself.
//
// Every call into Steam is asynchronous and answers with a result handle, and
// a handle that is not destroyed is a leak inside the Steam client, so every
// handle we ask for is remembered here until its callback lands.
//-----------------------------------------------------------------------------
class CFrontressInventory : public CAutoGameSystemPerFrame
{
public:
	CFrontressInventory();

	// CAutoGameSystemPerFrame
	virtual bool Init() OVERRIDE;
	virtual void Shutdown() OVERRIDE;
	virtual void Update( float frametime ) OVERRIDE;

	// Is the service answering us at all? False without Steam, and false
	// while the app has no inventory service enabled on the partner site.
	bool BAvailable() const;
	bool BDefinitionsLoaded() const { return m_bDefinitionsLoaded; }
	// Have we ever been told what the player owns? An empty inventory and an
	// inventory we have not read yet are different things to show a player.
	bool BItemsKnown() const { return m_bItemsKnown; }

	// Ask Steam for the whole inventory again. Rate limited by the Steam
	// client, and by us: calling this on every frame is not an error. bForce
	// is for the times a player asked in so many words, where waiting out our
	// own floor would look like a dead button.
	void Refresh( bool bForce = false );

	// Grant a promo item the player is eligible for, once. Free by
	// construction: AddPromoItem cannot charge for anything.
	void GrantPromoItem( SteamItemDef_t nDefID );

	// Spend whatever playtime credit Steam has accrued for us on the given
	// playtimegenerator definition. Returns nothing: an empty drop is the
	// normal answer and not a failure.
	void TriggerDrop( SteamItemDef_t nGeneratorDefID );

	const CUtlVector< FrontressItem_t > &GetItems() const { return m_vecItems; }
	const CUtlVector< FrontressItemDef_t > &GetDefinitions() const { return m_vecDefs; }
	const FrontressItemDef_t *FindDefinition( SteamItemDef_t nDefID ) const;

	// How many of a definition the player owns, counting stacks.
	int GetQuantity( SteamItemDef_t nDefID ) const;

	// The items_game definition index this item is drawn as, or 0 if it has
	// none and 0 as well if the schema does not know it. Resolved on every
	// call on purpose: the item schema is loaded long after the first
	// definitions can arrive.
	int GetGameItemDefIndex( const FrontressItemDef_t *pDef ) const;

	// Bumped whenever the items or the definitions actually change, so a panel
	// can redraw when there is something new rather than every frame.
	uint32 GetGeneration() const { return m_nGeneration; }

	// What went wrong last, for the UI to show. Empty when nothing has.
	const char *GetLastError() const { return m_strLastError.Get(); }

	void Spew() const;

private:
	// What we asked for, so the answer can be read the right way.
	enum ERequest
	{
		k_eRequest_FullUpdate,
		k_eRequest_Grant,
		k_eRequest_Drop,
	};

	struct PendingResult_t
	{
		SteamInventoryResult_t hResult;
		ERequest               eRequest;
		SteamItemDef_t         nDefID;   // what we asked to be granted, for the log
	};

	ISteamInventory *Inventory() const;

	void Track( SteamInventoryResult_t hResult, ERequest eRequest, SteamItemDef_t nDefID );
	int  FindPending( SteamInventoryResult_t hResult ) const;
	void DestroyPending();

	void ReadDefinitions();
	void ReadItems( SteamInventoryResult_t hResult );
	// The items a grant or a drop actually produced, announced in the console.
	void ReportGranted( SteamInventoryResult_t hResult, SteamItemDef_t nAsked );

	STEAM_CALLBACK( CFrontressInventory, OnResultReady, SteamInventoryResultReady_t );
	STEAM_CALLBACK( CFrontressInventory, OnFullUpdate, SteamInventoryFullUpdate_t );
	STEAM_CALLBACK( CFrontressInventory, OnDefinitionsUpdated, SteamInventoryDefinitionUpdate_t );

	bool  m_bAsked;               // LoadItemDefinitions has gone out
	bool  m_bDefinitionsLoaded;
	bool  m_bItemsKnown;
	float m_flNextRetryTime;      // while Steam is not up yet
	float m_flNextRefreshTime;    // floor under Refresh()

	uint32     m_nGeneration;
	CUtlString m_strLastError;

	CUtlVector< FrontressItemDef_t > m_vecDefs;
	CUtlVector< FrontressItem_t >    m_vecItems;
	CUtlVector< PendingResult_t >    m_vecPending;
};

CFrontressInventory *FrontressInventory();

#endif // TF_STEAM_INVENTORY_H
