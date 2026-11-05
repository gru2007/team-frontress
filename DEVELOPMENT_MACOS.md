# Development on macOS (Apple Silicon)

Player builds come from CI on the official SDK (Windows and Linux). The macOS
build is for team development only: the game code is built from this
repository, but it runs on a private development engine that is installed
separately. Engine files in `game/bin/osxarm64` and `src/lib/*/osxarm64` are
excluded from Git and must never be committed.

Setup of the engine itself is documented in the private engine repository.
Developer builds for macOS come from `.github/workflows/macos.yml`: it builds
against the private engine without exposing it (quiet logs, encrypted
artifacts, no caches) and ships only to a password-protected Steam branch.
Once the engine is published into this checkout:

```sh
cd src
./buildallprojects_osx release     # JOBS=2 on 8 GB machines; "debug" for a debug build
cd ../game
./tc2_osx -novid -windowed -condebug
```

Sign in to Steam first. TF2 must be installed for its content; the Windows
content works. The default path is
`~/Library/Application Support/Steam/steamapps/common/Team Fortress 2`;
override it with `TC2_TF2_DIR` or `-tf2dir`.

## Search paths

In a working copy the macOS launcher adds, in front of everything else:

- `game/bin/osxarm64/content`: shader caches built for the development engine.
  TF2's own shader caches use different combo layouts and must not win.
- `game_src/tc2/pak1`: loose mod resources, so edits show up without packing a
  VPK. It is added to the `VGUI` path too, otherwise a layout could still load
  from the installed TF2.

An explicit `-insert_search_path a,b` replaces this list.

## Useful commands

- Main menu: the Frontress menu (`resource/fui`) is on by default; `fui_menu 0` shows TF2's own.
  `fui_hot_reload 1` reloads it when its files change; `fui_screen war` jumps to a screen.
- Menu work without the game: `tools/fui-preview` (see its README).
- Map test: `./tc2_osx -novid -windowed -condebug +sv_lan 1 +map ctf_2fort`,
  then `jointeam red`, `joinclass scout`, `tf_bot_add 2`, `status`.
- Debugger: `/usr/bin/lldb -- ./tc2_osx -novid -windowed`.

## Compatibility with the official SDK build

The changes made for macOS are either guarded by `OSX` / `[$OSXALL]`, or are
engine-independent fixes that apply to every build:

- The client loads TF2's base localization only when its tokens are missing,
  then reloads the mod's file so `tc2` overrides still win.
- Steam HTML: zoom is a multiplier (`1.0` = 100%), as Steam's API expects, and
  each panel ignores callbacks for other panels' browsers.
- Party publishing to the Steam lobby runs at 4 Hz instead of every frame.
- `sourcescheme.res` no longer references Replay frame textures, which TF2 no
  longer ships.

Passing the macOS build does not replace the Windows and Linux CI builds.

## Matchmaking smoke test

- Use `tf_gc_status` to check the coordinator URL and session. The production
  URL is `https://gc.team-frontress.org`; quote URLs in console commands or cfg
  files. For a local GC, use `tf_gc_url "http://127.0.0.1:27100"`.
- In the local GC, set a two-player casual group and start `gc-devserver -fill 1`
  with a test server key. Queue in the client, wait for the match card, and
  accept. The simulator can verify the lobby and the destination passed to
  `connect`, but does not run a game server. Use a real dedicated server for the
  final join and gameplay check.
- Run `version` in both the client and the dedicated server. Compare **Protocol
  version** and **Network PatchVersion**. Use a macOS engine built with
  protocol 24 and Steam session tickets for the official SDK dedicated
  servers. Servers with `tf_mm_strict 1` require a coordinator-assigned match
  before they admit a client.
- `fui_menu_3d 0` disables lobby characters. Neither setting starts a background
  map or a listen server.

## Status

On the development engine, `ctf_2fort` is playable: the world, lighting, sky,
3D skybox, particles, player models, HUD and menus render correctly. Known
gaps: no VScript, some TF2 particle operators are missing, no Opus voice codec,
and the SDK tools (Hammer, map compilers) are not built natively.
