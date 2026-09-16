# Steamworks assets

Files here are uploaded on the Steamworks partner site. Nothing in this
directory ships with the game or is read at runtime.

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

The game is published under the playtest `5147520`, main app `5147380`, and demo
`5260620`; the table is per app, so this has to be done for all three. A player
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

## Demo shared depots

The release workflow publishes the full Windows/Linux client depots to the main
app first. It then publishes one demo-owned overlay depot to AppID `5260620`.
That overlay intentionally contains only:

```text
steam_appid.txt  ->  5260620
```

`STEAM_DEMO_DEPOT` may override the assumed default depot `5260621`.

Shared-depot attachment is Steamworks metadata and cannot be created by the
SteamPipe upload script. Configure it once under demo AppID `5260620`:

1. SteamPipe -> Depots -> Add Shared Depot.
2. Add the main app's Windows and Linux client depots.
3. Add the demo overlay depot to the demo store/key packages as well.
4. Give the demo and main app the same install directory if they should share
   one on-disk content copy.
5. Add Windows/Linux launch options for the shared `tc2` launchers with
   `-frontressdemo`. The client also recognizes AppID `5260620` directly, so a
   missing argument does not silently open the live campaign.

Steam currently documents shared depots as available only when their master
app is released. If main AppID `5147380` is still unreleased, use demo-owned
full client depots instead; the one-file overlay cannot make an unavailable
shared depot mountable.
