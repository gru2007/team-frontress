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

## The demo app (5260620)

The Steam Next Fest demo is built from the `demo` branch and published to its
own app, Team Frontress Demo `5260620`, by `.github/workflows/build.yml` on
that branch -- never to the playtest or the main app.

Set up once in Steamworks:

1. Depots: Windows `5260621`, Linux `5260622` (the workflow's defaults; the
   repository variables `STEAM_DEMO_DEPOT_WIN` / `STEAM_DEMO_DEPOT_LINUX`
   override them). Add both to the demo's package.
2. Launch options: `tc2_win64.exe` (Windows) and `tc2.sh` (Linux), exactly as
   for the main app; no arguments are needed -- a demo build opens the demo.
3. The demo depends on Team Fortress 2 (440) being installed, like the game.
4. Rich presence: upload the same files as for the other apps.
5. Branch: CI sets `prerelease` live (`STEAM_DEMO_BRANCH`, `none` to only
   upload). Promote to `default` by hand when a build has been played.

To publish: push a `demo/<version>` tag on the demo branch, or run the
workflow by hand with *publish* ticked. The builder account in
`STEAM_USERNAME` needs publish rights on `5260620`.
