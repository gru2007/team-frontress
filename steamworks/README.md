# Steamworks assets

Files here are uploaded on the Steamworks partner site. Nothing in this
directory ships with the game or is read at runtime.

## Inventory service

`inventory/itemdefs.json`, and `inventory/README.md` for what to do with it.

The app has an inventory of its own, separate from the Team Fortress backpack:
the Steam Inventory Service, on our appid, holding items we define. The JSON is
the whole of it -- Steam hosts the definitions and the client reads them back,
so an item is a publish rather than a build. Everything in it is free, and has
to stay that way; the Source SDK licence does not let this project sell
anything.

Like rich presence, item definitions are per app, so they have to be published
to both the playtest `5147520` and the main app `5147380` -- and so do the items
players own in them, which do not cross between the two.

## Rich presence

`rich_presence_english.vdf` / `rich_presence_russian.vdf`

Steam's friends list renders the `steam_display` key the client sets
(`#Frontress_RichPresence_Display`) by looking it up in **the app's own**
uploaded rich presence localization. Until these files are uploaded the friends
list shows nothing at all, however correct the client is -- which is why the in-game friends panel has always worked and the
Steam one has not: the in-game panel reads the `status` key, which the client
localizes itself out of `tf_*.txt`.

To upload:

1. Steamworks -> the app -> Community -> Rich Presence Localization
2. Upload one file per language, then publish to the default branch.
3. Restart the Steam client; it caches the token table per app.

The game is published under two apps -- the playtest `5147520` and the main app
`5147380` -- and the table is per app, so this has to be done for both. A player
on the app that was missed sees an empty friends list entry while everyone else
looks fine.

The keys the client sets live in `ClientModeTFNormal::UpdateSteamRichPresence`
(`src/game/client/tf/clientmode_tf.cpp`): `state`, `matchgrouploc`,
`steam_display`, `steam_player_group`, `steam_player_group_size`. `matchgrouploc`
is a *suffix* -- "Casual", "Competitive6v6", "BootCamp", "MannUp",
"SpecialEvent" -- so the tokens resolve it through
`#Frontress_RichPresence_MatchGroup_%matchgrouploc%` rather than printing it
raw. Adding a match group means adding a token here as well, or Steam will
render an unresolved `{#...}` for it.
