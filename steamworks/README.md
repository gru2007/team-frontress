# Steamworks assets

Files here are uploaded on the Steamworks partner site. Nothing in this
directory ships with the game or is read at runtime.

## Rich presence

`rich_presence_english.vdf` / `rich_presence_russian.vdf`

Steam's friends list renders the `steam_display` key the client sets
(`#Frontress_RichPresence_Display`) by looking it up in **the app's own**
uploaded rich presence localization. Until these files are uploaded for
appID 5147520 the friends list shows nothing at all, however correct the
client is -- which is why the in-game friends panel has always worked and the
Steam one has not: the in-game panel reads the `status` key, which the client
localizes itself out of `tf_*.txt`.

To upload:

1. Steamworks -> the app -> Community -> Rich Presence Localization
2. Upload one file per language, then publish to the default branch.
3. Restart the Steam client; it caches the token table per app.

The keys the client sets live in `ClientModeTFNormal::UpdateSteamRichPresence`
(`src/game/client/tf/clientmode_tf.cpp`): `state`, `matchgrouploc`,
`steam_display`, `steam_player_group`, `steam_player_group_size`. `matchgrouploc`
is a *suffix* -- "Casual", "Competitive6v6", "BootCamp", "MannUp",
"SpecialEvent" -- so the tokens resolve it through
`#Frontress_RichPresence_MatchGroup_%matchgrouploc%` rather than printing it
raw. Adding a match group means adding a token here as well, or Steam will
render an unresolved `{#...}` for it.
