# Team Frontress macOS depot

This directory builds a self-contained `Team Frontress.app` around the Windows
x64 client, and it is the content of Steam depot **5147523**.

There is no macOS build of the engine. The Windows client runs under an
embedded Wine runtime, D3D9 is translated to Metal by D9MT, and Steamworks
calls are forwarded to the *native* macOS Steam client the player already runs.
Team Fortress 2's Windows depot supplies the engine binaries and the assets, as
it does on every other platform.

## Runtime layout

```text
Team Frontress.app/Contents/
├── Info.plist
├── MacOS/team-frontress             the launcher
└── Resources/
    ├── game/                        the packaged Windows client + D9MT's d3d9.dll
    ├── install-tf2                  the app-440 prompt
    ├── licenses/
    ├── steam/libsteam_api.dylib     Valve's macOS Steamworks redistributable
    └── wine/                        the LGPL Wine runtime, with:
        ├── lib/wine/x86_64-windows/ d9mtmetal.dll, winemetal.dll, steam_api64.dll
        └── lib/wine/x86_64-unix/    d9mtmetal.so,  winemetal.so,  steam_api64.so
```

The mutable Wine prefix lives in
`~/Library/Application Support/Team Frontress/prefix` and is never part of the
depot. The launcher writes `~/Library/Logs/Team Frontress/launcher.log`, keeping
the previous run alongside it as `launcher.log.previous`.

## Where the assets come from

The launcher reads native Steam's `libraryfolders.vdf`, finds
`appmanifest_440.acf` in each library, and accepts the first install that
actually has `bin/x64/launcher.dll` — the Windows depot, not a macOS one. It
translates that path with `winepath -w` and exports it as `TC2_TF2_DIR`.

`GetGameInstallDir` in `src/launcher_main/main.cpp` uses `TC2_TF2_DIR` in
preference to asking Steamworks, and does so *before* loading Steam at all.
That ordering is deliberate twice over: native Steamworks would answer with a
POSIX path the Windows launcher cannot open, and the assets stay reachable even
when the Steam bridge is not working.

If no Windows install of app 440 is present, `install-tf2` puts the two console
commands on the clipboard and opens the Steam Console. Steam exposes no
documented way to submit a console command to a client that is already running,
and no global `steam_dev.cfg` is installed, so no other macOS game is switched
to Windows depots behind the player's back.

## The Steam bridge

`steam-bridge/` is the piece that lets a Windows process inside Wine talk to
the macOS Steam client. It has its own [README](steam-bridge/README.md); the
short version is that it is a Wine builtin `steam_api64.dll` paired with a
Mach-O unixlib, generated from the SDK description already in this repository,
and it is built from source by CI on every run.

## Building

Three binaries cannot live in this repository: the Wine runtime, D9MT, and
Valve's Steamworks redistributable. `fetch-runtimes.sh` collects them and lays
them out the way `build-depot.sh` expects. Wine and D9MT have working defaults;
the Steamworks library is under the Steamworks SDK Access Agreement, so it has
to be supplied.

| input | default |
| --- | --- |
| Wine | [`wine-devel-11.16-osx64`](https://github.com/Gcenx/macOS_Wine_builds/releases/tag/11.16) — vanilla rather than staging, since D9MT's `winemetal` is a third-party builtin and staging's patches are a variable this port does not need |
| Wine's `COPYING.LIB` | `gitlab.winehq.org/wine/wine` at `wine-11.16` — the builds do not ship it |
| D9MT | [`d9mt-x64.zip` from `gru2007/d9mt-builded` v0.1](https://github.com/gru2007/d9mt-builded/releases/tag/v0.1) |
| Steamworks | none — set `STEAMWORKS_SDK_ZIP`, `STEAMWORKS_REDIST_DYLIB` or `STEAMWORKS_REDIST_URL` |

Every input takes a local path as readily as a URL, so an archive already on
disk is reused rather than downloaded again. The SDK zip and the bare dylib are
both accepted.

```bash
STEAMWORKS_SDK_ZIP=~/steamworks_sdk_165.zip \
  ./tools/macos-port/fetch-runtimes.sh macos-runtimes

TC2_WINDOWS_DIR=/path/to/game_dist \
WINE_RUNTIME_DIR=macos-runtimes/wine \
WINE_LICENSE_FILE=macos-runtimes/wine/COPYING.LIB \
D9MT_DIST_DIR=macos-runtimes/d9mt \
STEAMWORKS_REDIST_DYLIB=macos-runtimes/steam/libsteam_api.dylib \
  ./tools/macos-port/build-depot.sh
```

The bridge is built as part of this unless `STEAM_BRIDGE_DIR` points at one
that already exists. `CODESIGN_IDENTITY` selects a signing identity; without it
the bundle is signed ad-hoc, which is enough to run locally.

### On the SDK version

The Steamworks headers in `src/public/steam` are older than SDK 1.65, which is
fine and is how Steam is meant to be used: the game asks for the interface
versions it was compiled against, and the Steam client keeps serving them.
Five of the six callback structs whose definitions moved between the two are
append-only, and the sixth carves a field out of reserved space in a struct only
`ISteamNetworkingSockets` uses. The bridge is generated from the headers in this
repository, so it stays consistent with the game rather than with whichever
`libsteam_api.dylib` is shipped alongside it.

### Signing

Innermost-first, and on purpose. Wine and D9MT both write pages
they then execute, which the hardened runtime refuses without
`com.apple.security.cs.allow-jit` and friends
(see [Frontress.entitlements](Frontress.entitlements)). Those entitlements have
to be on the Mach-O binaries that do it, so every executable and dylib under
`Resources/wine` and `Resources/steam` is signed individually before the bundle
is. `codesign --deep` would be wrong here: it signs nested code with no
entitlements at all, and the bundle's own executable is a shell script, which
carries no signature of its own.

## CI

Two jobs in `.github/workflows/build.yml`:

- **`steam-bridge`** builds the bridge and runs its two self-checks. It needs
  nothing but `mingw-w64` and the SDK headers in this repository, so it runs on
  every build.
- **`macos`** waits for the Windows client and assembles the app bundle. It
  runs `fetch-runtimes.sh`, so Wine and D9MT need no configuration;
  `vars.MACOS_WINE_URL` and `vars.MACOS_D9MT_URL` only exist to pin something
  else. The one thing it must be given is **`secrets.STEAMWORKS_REDIST_URL`**,
  pointing at the SDK zip or the bare `libsteam_api.dylib`. Without that secret
  the job says so and finishes green — a macOS depot that cannot be built is not
  a reason to hold up a Windows and Linux release.

The bundle travels between jobs as a tar, not as loose files: the Wine tree is
full of symlinks and executable bits, and an `.app` that lost either does not
launch.

`publish-steam` pushes depot `5147523` alongside the Windows and Linux depots
in a single BuildID when the macOS lane produced a bundle, and leaves that depot
at its previous content when it did not.

## Licensing

Wine is LGPL and is redistributed under `licenses/Wine-LGPL-2.1.txt`. D9MT is
packaged under the zlib terms in `licenses/D9MT-zlib.txt`; put the same notice
in the D9MT source repository and confirm that every D9MT copyright holder
agrees before publishing binaries.

`libsteam_api.dylib` and the native Steam client are proprietary Valve
components, redistributable only under the Steamworks SDK Access Agreement. The
bridge itself contains no part of the SDK — it loads that library through
`dlopen` at runtime — and is MIT.
