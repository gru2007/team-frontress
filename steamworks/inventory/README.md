# The app's own inventory

This is the Steam Inventory Service on **our** appid. It is not the Team
Fortress backpack: that one belongs to Valve's GC, describes items out of
`items_game.txt`, and nothing here touches it. A player has both, side by side.

`itemdefs.json` is the whole content of ours. Steam hosts it, the client reads
it back with `LoadItemDefinitions`, and the game draws an item using the model
named in the item's own `tf_item_def` property. Adding an item is therefore a
JSON edit and a publish -- no build, no patch.

The client side lives in
[`src/game/client/tf/frontress/tf_steam_inventory.cpp`](../../src/game/client/tf/frontress/tf_steam_inventory.cpp)
and the panel in `tf_inventory_panel.cpp` next to it.

## Everything here is free, and has to stay free

The project is built on the Source SDK, whose licence does not allow selling
anything. So:

* no item carries `price` or `price_category`;
* the Item Store is left off, and `ISteamInventory::StartPurchase` is never
  called -- the client has no code path that could;
* items are only ever handed over by `AddPromoItem` (a promo the player is
  eligible for) or `TriggerItemDrop` (playtime credit Steam already accrued).
  Both are free by construction.

Keep it that way: the moment an itemdef grows a price, the Steam side becomes a
storefront and the licence question becomes real.

## What to do on the partner site

Once per app -- and there are two, the playtest `5147520` and the main app
`5147380`. Item definitions are per app, so an item published to one does not
exist in the other, and neither do the items players own in it.

1. **Steamworks -> the app -> Inventory Service**
   (`https://partner.steamgames.com/apps/inventoryservice/<appid>`).
2. Paste the contents of `itemdefs.json` into the item definition editor, or
   upload the file. Change the `appid` line first when uploading to the main
   app.
3. **Save**, then **publish** the definitions. A saved-but-unpublished
   definition is invisible to the client; this is the step that is easy to miss
   and that makes `tf_steaminv_status` print "the service has no item
   definitions".
4. Tick **Enable Inventory Service** on the same page.
5. Set the playtime drop rate for itemdef `200` if drops are wanted. Without a
   rate, `TriggerItemDrop` is a no-op forever, which is a correct and quiet
   answer, not an error.
6. Restart the Steam client. It caches the definitions per app.

### The keys on that page

* **Secret key (HMAC-SHA1), "ItemCart"** -- signs item-cart and purchase URLs
  for the paid Item Store. We sell nothing, so nothing in this repository uses
  it. Leave it alone, keep it in a password manager, and never put it in the
  game, in a config file, or in a commit: it is a publisher secret that lets
  whoever holds it construct carts against the app.
* **Resource server key** -- only needed if Steam has to fetch item definitions
  or assets from a server we run. We paste the definitions into Steamworks and
  host the icons at a public URL, so there is nothing to install. If a resource
  server is ever set up, that key stays on it, server side.
* **Publisher Web API key** -- what `IInventoryService/AddItem` and friends need
  if the coordinator ever grants items server side (see "Later" below). Not used
  today, and it must never reach the client: a client that can call `AddItem` is
  a client that can grant itself anything.

## What the game does with it

The one custom property is `tf_item_def`. It is either an `items_game`
definition index or a definition name, and it says which model the game draws
for the item:

```json
"tf_item_def": "513"          // The Original, by index
"tf_item_def": "Name Tag"     // or by definition name
```

An item without it is fine -- a token or a currency has no model -- and the
panel says so instead of drawing nothing.

The test item is itemdef `100`: a promo item, granted once, drawn with a
launcher the game already ships. Nothing about it is hardcoded in the client
except the two ids, which are convars.

| Convar | Default | What it is |
| --- | --- | --- |
| `tf_steaminv_enable` | `1` | Read our inventory at all |
| `tf_steaminv_debug` | `0` | Log what the service is doing |
| `tf_steaminv_testitem` | `100` | Definition the panel's claim button grants |
| `tf_steaminv_droplist` | `200` | Playtime generator drops are spent on |

| Command | What it does |
| --- | --- |
| `tf_steaminv_open` | The inventory panel |
| `tf_steaminv_status` | What the service said, definition by definition |
| `tf_steaminv_refresh` | Ask for the inventory again |
| `tf_steaminv_grant [defid]` | Grant a promo item |
| `tf_steaminv_drop [defid]` | Spend playtime credit on a drop |

## Checking that it works

Run the game through Steam as the app the definitions were published to -- the
inventory the client sees is the inventory of the app it is running as, which
for a playtest build is `5147520`.

1. `tf_steaminv_status` lists the definitions. If it prints none, the
   definitions are not published or the service is not enabled.
2. `tf_steaminv_open` -> **Claim test item** -> the item appears in the list and
   its launcher turns on the right.
3. The same item shows up on the Steam side, under the app, in the player's
   Steam inventory -- which is the point of using the service rather than a
   table in the coordinator.

An account in the app's publisher group can also use
`ISteamInventory::GenerateItems` to conjure any definition; that path is not
wired to a command here on purpose, since it fails for everybody else and the
failure looks like a bug in the panel.

## Later

Two things are deliberately not done yet:

* **Granting from the coordinator.** Rewarding a campaign result means the
  coordinator calling `IInventoryService/AddItem` with the publisher key.
  That is the only way a grant can be trusted; a client that grants its own
  rewards is a client that grants itself everything.
* **Equipping.** The panel draws the model an item names. Wearing it in a match
  is the loadout path, which is the GC's econ inventory, and joining the two is
  a separate piece of work.
