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
    ├── lib/libmacdrvshim.dylib      what D9MT expects CrossOver's Wine to have
    ├── game/                        the packaged Windows client + D9MT's d3d9.dll
    ├── install-tf2                  the app-440 prompt
    ├── licenses/
    ├── steam/libsteam_api.dylib     Valve's macOS Steamworks redistributable
    └── wine/                        the LGPL Wine runtime, with:
        ├── lib/wine/x86_64-windows/ d9mtmetal.dll, winemetal.dll, steam_api64.dll
        └── lib/wine/x86_64-unix/    d9mtmetal.so,  winemetal.so,  steam_api64.so
```

Nothing under the `.app` is ever written to at runtime. Everything mutable lives
in `~/Library/Application Support/Team Frontress`:

```text
~/Library/Application Support/Team Frontress/
├── prefix/                          the Wine prefix
└── game/                            the client the launcher actually runs
```

and the log is `~/Library/Logs/Team Frontress/launcher.log`, with the previous
run kept next to it as `launcher.log.previous`.

### Why the client does not run from inside the bundle

A Source client writes next to its own binary: `console.log`, `config.cfg`,
`steam_appid.txt`, the DXVK/D9MT shader caches, screenshots, downloads. Doing
that inside the `.app` breaks two things at once. The bundle's code signature
seals `Contents/Resources`, so the first write invalidates it; and Steam sees
depot files that no longer match the manifest, so it wants to repair the install
under the player every time. Between them that is a game that starts, writes one
file and disappears, which is exactly what the port did.

So the launcher stages `Contents/Resources/game` into
`~/Library/Application Support/Team Frontress/game` and runs it from there. The
first stage is `cp -Rc`, which is `clonefile(2)`: on APFS the copy is instant and
occupies no additional disk space, and a write to the copy never touches the
bundle's blocks. Later launches only re-sync when the bundle changed, with
`rsync -rlt`, so a depot update reaches the copy while everything the player
wrote — configs, `custom/`, demos, caches — is left alone. `TC2_GAME_DIR`
overrides where that copy lives.

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

That covers the launcher. The engine finds the same install for itself, and by a
different route: `gameinfo.txt` mounts Team Fortress 2's content as
`|appid_440|tf/tf2_misc.vpk`, which the engine resolves through
`ISteamApps::GetAppInstallDir`. Native Steam answers that with a POSIX path, so
the bridge translates it — see [the bridge's README](steam-bridge/README.md#paths).
Without that translation the engine mounts nothing at all, which is a client
that starts and disappears no matter what the launcher got right.

If no Windows install of app 440 is present, `install-tf2` puts the two console
commands on the clipboard and opens the Steam Console. Steam exposes no
documented way to submit a console command to a client that is already running,
and no global `steam_dev.cfg` is installed, so no other macOS game is switched
to Windows depots behind the player's back.

## What D9MT expects of Wine

D9MT is developed against CrossOver's Wine and uses two things CodeWeavers add
to it: an `ntdll.__wine_unix_call` export, and a `macdrv_functions` table on the
Cocoa driver's unixlib. Neither exists in an LGPL Wine, and each is fatal on its
own — the first aborts the client the moment D9MT crosses into its unixlib, the
second leaves a client that runs and never presents a frame.

`wine-compat/` supplies both without patching Wine: a rebuilt `d9mtmetal.dll`
that dispatches unix calls the way upstream does, and `libmacdrvshim.dylib`,
which answers the `macdrv_functions` lookup in terms of AppKit and is inserted
into the Wine process by the launcher. It has its own
[README](wine-compat/README.md). Like the bridge, it is built from source by CI
on every run.

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
| D9MT | [`d9mt-x64.zip` from `gru2007/d9mt-builded` v0.1](https://github.com/gru2007/d9mt-builded/releases/tag/v0.1) — an x86_64 build of [`neo773/d9mt`](https://github.com/neo773/d9mt), whose own releases are arm64 for CrossOver and will not load under an x86_64 Wine |
| Steamworks | none — set `STEAMWORKS_SDK_ZIP`, `STEAMWORKS_REDIST_DYLIB` or `STEAMWORKS_REDIST_URL` |

Every input takes a local path as readily as a URL, so an archive already on
disk is reused rather than downloaded again. The SDK zip and the bare dylib are
both accepted.

D9MT is unpacked by name rather than by layout: `d3d9.dll` (or `d3d9fe.dll`,
which is what upstream's build script produces and which is deployed under the
other name), plus a `.dll`/`.so` pair for each of `winemetal` and `d9mtmetal`,
wherever in the archive they happen to sit. What comes out is the layout
`build-depot.sh` wants:

```text
<dest>/d9mt/
├── d3d9.dll                     goes next to tc2_win64.exe
├── x86_64-windows/{winemetal,d9mtmetal}.dll    Wine builtins
└── x86_64-unix/{winemetal,d9mtmetal}.so        their unixlibs
```

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

### What the build checks

Each of these is invisible in a directory listing and each one is a client that
starts and vanishes with nothing in the log, so `build-depot.sh` refuses to
produce a bundle until they hold:

| check | why |
| --- | --- |
| `d9mtmetal.so`, `winemetal.so`, `steam_api64.so` and `libsteam_api.dylib` carry Wine's architecture | a unixlib is loaded into the Wine process itself; an arm64 one next to an x86_64 Wine does not load at all |
| `d3d9.dll`, `d9mtmetal.dll`, `winemetal.dll`, `steam_api64.dll` are x86-64 PE images | the client is x86-64 |
| the three Wine-side DLLs carry winebuild's `Wine builtin DLL` marker | without it Wine logs *"found in WINEDLLPATH but not a builtin, ignoring"*, never pairs the DLL with its `.so`, and D3D9 creation fails inside the client. Anything unmarked is stamped by `steam-bridge/mark-builtin.py` |
| the bundle contains no symlinks | see below |
| the bundle's executable is executable and starts with `#!` | it is what Steam runs |
| the Wine binary's signature carries `allow-jit` | the entitlements are the only reason the nested binaries are signed one at a time |

### Symlinks

The depot is delivered by SteamPipe, whose file format has no symlink in it: a
player ends up with whatever the depot builder made of one. A Wine tree that
arrives with `bin/wine64` or a versioned dylib missing is a client that exits
before it prints anything, and a link that came back as a copy invalidates the
bundle's signature just as surely as a new file would.

`build-depot.sh` therefore resolves every symlink in the bundle into a real file
before signing — repeatedly, since a link to a directory is replaced by a copy of
that directory, links and all — and fails if any survive. It costs some tens of
megabytes of duplicated dylibs and it makes the delivered bundle byte-identical
to the signed one.

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

The bundle travels between jobs as a tar, not as loose files: an artifact upload
of loose files does not keep executable bits, and an `.app` that lost them does
not launch. A verification step then re-checks the finished bundle on the macOS
runner — signature, executable bit, interpreter line, no symlinks — because none
of that can be checked again on the Linux runner that publishes it.

`publish-steam` pushes depot `5147523` alongside the Windows and Linux depots
in a single BuildID when the macOS lane produced a bundle, and leaves that depot
at its previous content when it did not.

## When it does not start

The launcher's whole job on a bad launch is to leave something behind, so start
with the log:

```bash
open ~/Library/Logs/Team\ Frontress/
```

It records the bundle path, the macOS version and architecture, what Wine is,
where Team Fortress 2 was found, and the client's exit status. The same output
also goes to stderr, so Steam's own console log has a copy. If the client dies
within a minute of starting, the launcher puts the last lines of that log in an
alert rather than exiting quietly.

For more than that, run it by hand with Wine's loader tracing on:

```bash
TC2_DEBUG=1 "/path/to/Team Frontress.app/Contents/MacOS/team-frontress"
```

which turns on `WINEDEBUG=+loaddll,+module,+seh` and the bridge's own logging —
enough to see which module failed to load, which is what almost every "it opens
and closes" report turns out to be.

| symptom | cause |
| --- | --- |
| `Bad CPU type in executable`, or an alert about Rosetta | an x86_64 Wine on Apple silicon without Rosetta 2: `softwareupdate --install-rosetta --agree-to-license` |
| `err:module:import_dll Library winemetal.dll not found` | the D9MT builtins did not reach the prefix; delete `~/Library/Application Support/Team Frontress/prefix` and launch again |
| `found in WINEDLLPATH but not a builtin, ignoring` | a Wine-side DLL without the builtin marker — a build-time check now stamps these |
| the client starts and exits with no log at all | the bundle executable never ran: check that `Contents/MacOS/team-frontress` survived with its executable bit |
| the game runs but Steam features do not | the bridge could not load Valve's library; `TC2_DEBUG=1` makes it say why |

Deleting `~/Library/Application Support/Team Frontress` resets the prefix and
the staged client without touching the install.

## Licensing

Wine is LGPL and is redistributed under `licenses/Wine-LGPL-2.1.txt`. D9MT is
packaged under the zlib terms in `licenses/D9MT-zlib.txt`; put the same notice
in the D9MT source repository and confirm that every D9MT copyright holder
agrees before publishing binaries.

`libsteam_api.dylib` and the native Steam client are proprietary Valve
components, redistributable only under the Steamworks SDK Access Agreement. The
bridge itself contains no part of the SDK — it loads that library through
`dlopen` at runtime — and is MIT.
