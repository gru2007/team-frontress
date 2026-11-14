# fui-preview

The Frontress menu (`game/tc2/loose/resource/fui`) outside the game. It runs
the same runtime, renderer and font engine as the game (`src/frontress_ui`),
on OpenGL, with TF2's own fonts and art read from an installed Team Fortress 2,
and the state from `mock.json` instead of Steam and the coordinator.

```sh
brew install sdl3 cmake ninja
cmake -S tools/fui-preview -B build/fui-preview -G Ninja
ninja -C build/fui-preview
build/fui-preview/fui-preview                 # a window; edits reload live
build/fui-preview/fui-preview --screen locker --shot locker.png   # a screenshot
```

Screens: `--screen home|play|war|quests|clan|locker|career|support`; the
clan screen without a clan is `--set clan.has=false`, the support tab before
it is connected `--set support.offered=false`.

Keys: `1`–`6` screens, `F5` reload, `F8` RmlUi's debugger (element tree,
styles, events), `Esc` quits the modal.

The HUD: `--hud` (the default `frontress` HUD over a still of a match) or
`--hud-style minimal`; `--set hud.mode=koth` and the rest of `hud` in
`mock_base.json` stage a fight. HUDs are documented in
`game/tc2/loose/resource/fui/huds/README.md`.

Options: `--lang english`, `--size 2560x1080`, `--set ui.class=medic`,
`--set queue.state=found`, `--mock other.json`, `--tf2 <TF2 directory>`
(default: Steam's on macOS, or `TC2_TF2_DIR`).

`mock.json` is generated: `make_mock.py` takes real items (names in your
language, backpack icons) from the installed game and merges them into the
hand-written `mock_base.json`. Rerun it after changing either:

```sh
tools/fui-preview/make_mock.py > tools/fui-preview/mock.json
```

What the previewer cannot show: Steam avatars (it draws stand-ins) and
anything the game computes. Commands (`cmd(...)` in the documents) are
printed, and the few the screens need to be clicked through (queue, locker,
modes) are faked in `main.cpp`.

## Screenshots in the game

`-fui_autoshot home,war,locker` (with `-fui_autoshot_quit`) shows each menu
screen and saves `screenshots/fui_<screen>.jpg`. For the HUD in a real fight,
`-fui_autoexec <cfg>` runs a script once the first match is up; the cheat
`fui_dev_goto` puts you on the cart or the middle control point. For example
a `cfg/shots.cfg` with `sv_cheats 1`, `jointeam red`, `joinclass soldier`,
`tf_bot_add 7 blue`, `wait 900`, `fui_dev_goto`, `wait 500`, `jpeg fight 92`,
run with `./tc2_osx -windowed -fui_autoexec shots +sv_lan 1 +map cp_granary`.
Scripts time their steps with `fui_after <seconds> "<command>"` (wait counts
frames, and some engines ignore it); `fui_cmd <name> [args]` runs any UI
command and `fui_dev_cam <n>` puts a spectator camera over the middle point.
`cfg/fui_dev_steam_fight.cfg`, `fui_dev_steam_menu.cfg` and `fui_dev_steam_war.cfg` are the ones that made
`docs/steam/`.

## Reliability checks

`--verify-ui --shot /tmp/fui-check.png` runs chat wheel/follow checks, detached
and removed data-for views, commands which reload/navigate from event handlers,
and repeated screen/model transitions. It returns a nonzero status on failure.

`--verify-exit --shot /tmp/fui-exit.png` calls `exit()` with a live runtime to
exercise cleanup before RmlUi's global destructors. Combine it with `--verify-ui`
to test that exit after the structural-view stress pass and Shutdown/Init.
The suite also rejects a competing runtime before it can replace global RmlUi interfaces. Run these under
AddressSanitizer/UndefinedBehaviorSanitizer for lifetime and bounds diagnostics.
