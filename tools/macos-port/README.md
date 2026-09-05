# Team Frontress macOS depot

This directory builds `Team Frontress.app` around the Windows x64 client, and it
is the content of Steam depot **5147523** (the playtest) and of the main app's
macOS depot. One bundle serves both: it is signed, so it is never stamped with
an AppID after the fact, and the launcher stamps the copy it stages out of the
bundle with the AppID Steam launched it with instead.

There is no macOS build of the engine. The Windows client runs under an
embedded Wine runtime, D3D9 is translated to Metal by D9MT, and Steamworks
calls are forwarded to the *native* macOS Steam client the player already runs.
Team Fortress 2's shared assets come from depot `441` and the Windows engine
comes from depot `232251`. The app downloads a pinned official DepotDownloader
release into Application Support, then uses it to download both depots directly
from Steam CDN after Steam authorizes the player's account. No Valve content is
republished in a Team Frontress depot.

The pinned Gcenx Wine 11.16 build requires the official
[GStreamer 1.28.5 universal runtime](https://gstreamer.freedesktop.org/data/pkg/osx/1.28.5/gstreamer-1.0-1.28.5-universal.pkg)
installed for all users, as specified by that Wine release. The launcher checks
for the framework before downloading TF2 content and reports the official URL
instead of allowing Wine to fail later with a missing dylib.

## Runtime layout

```text
Team Frontress.app/Contents/
├── Info.plist
├── MacOS/team-frontress             the launcher
└── Resources/
    ├── lib/libmacdrvshim.dylib      what D9MT expects CrossOver's Wine to have
    ├── game/                        the packaged Windows client + D9MT's d3d9.dll
    ├── helpers/tf2-content-helper   native download, QR, and progress UI
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
├── game/                            the client the launcher actually runs
└── runtime/
    ├── state.json                   verified manifest pair
    ├── depot-441/                   active shared TF2 content
    ├── depot-232251/                active Windows engine
    ├── .download/                   resumable, inactive work trees
    ├── tools/DepotDownloader        SHA-256-verified upstream executable
    └── helper-home/                 private DepotDownloader/.NET state
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
wrote — configs, `custom/`, demos, caches — is left alone. The stage stamp
includes the bundled `d3d9.dll` SHA-256 and that file is copied explicitly, so
an updated D9MT cannot be hidden by rsync's size/mtime shortcut. `TC2_GAME_DIR`
overrides where that copy lives.

## Where the assets come from

The launcher invokes `tf2-content-helper`. On first use, that open-source native
helper downloads the official DepotDownloader 3.4.0 archive directly from the
SteamRE GitHub release, verifies pinned SHA-256 values for both the archive and
executable, and installs it under Application Support. It then invokes
DepotDownloader as a separate child process and requests the current public
manifests for app `440`, depot `441` and depot `232251`, with
the target explicitly set to Windows x64. Each depot has an independent work
tree, so one manifest cannot delete or overwrite files managed by the other.
DepotDownloader verifies downloaded chunks and marks its installed manifest
invalid before changing files; a killed or failed run is therefore validated
and resumed rather than trusted on the next attempt.

Downloads are written to `runtime/.download`. Only after both depot runs report
their manifest IDs, complete successfully, and contain their required markers
does the helper replace the active pair and atomically write `state.json`.
The launcher never uses a partial work tree. Current manifests are checked at
most every six hours, so ordinary repeat launches return immediately.

The engine depot path is translated with `winepath -w` and exported as
`TC2_TF2_DIR`.

`GetGameInstallDir` in `src/launcher_main/main.cpp` uses `TC2_TF2_DIR` in
preference to asking Steamworks, and does so *before* loading Steam at all.
That ordering is deliberate: the Windows launcher needs its engine before the
filesystem is mounted, while the shared assets are mounted later from the
staged content link.

That covers the launcher DLL. A standalone depot is not registered as an app
install, so Valve's `|appid_440|` resolver cannot find depot 441. The staged
game gets a `tf2_content` symlink to the depot, and its writable copy of
`gameinfo.txt` replaces `|appid_440|` with
`|gameinfo_path|../tf2_content/`. Neither the signed app bundle nor Steam's
downloaded files are modified.

### Steam authorization and credential handling

Anonymous Steam access does not expose these depots. On first preparation the
helper asks DepotDownloader for a Steam QR challenge and renders that QR in a
native Team Frontress window. The player approves it only in the official Steam
Mobile app. Team Frontress never asks for a password and does not read
`loginusers.vdf`, cookies, tokens, or any other file from the installed Steam
Client. If DepotDownloader omits its usual post-QR account-name message, the UI
asks only for that non-secret account name so it can select the approved token.

Steam still performs the entitlement and depot-key checks. No depot key is
embedded, dumped, or persisted by Team Frontress. DepotDownloader's refresh
token is moved from its private .NET storage to a device-only macOS Keychain
item after every invocation and restored only while DepotDownloader is running.
The account name saved beside `state.json` is not secret and is mode `0600`.

This design avoids bypassing Steam's access control and avoids redistributing
Valve content, but it is not a substitute for Valve or legal approval. A
release must remain free and comply with the current Source 1 SDK License,
Steam Subscriber Agreement, Steamworks agreements, and any direction Valve
gives the project. A Valve-approved shared-depot dependency remains preferable
if Valve makes one available.

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

Three binaries do not live in this repository: the Wine runtime, D9MT, and
Valve's Steamworks redistributable. `fetch-runtimes.sh` collects them and lays
them out the way `build-depot.sh` expects. Wine and D9MT have working defaults;
the Steamworks library is under the Steamworks SDK Access Agreement, so it has
to be supplied.

| input | default |
| --- | --- |
| Wine | [`wine-devel-11.16-osx64`](https://github.com/Gcenx/macOS_Wine_builds/releases/tag/11.16), pinned by SHA-256 — vanilla rather than staging, since D9MT's `winemetal` is a third-party builtin and staging's patches are a variable this port does not need |
| Wine's `COPYING.LIB` | `gitlab.winehq.org/wine/wine` at `wine-11.16` — the builds do not ship it |
| D9MT | [`d9mt-x64.zip` v0.4](https://github.com/gru2007/d9mt-builded/releases/tag/v0.4), pinned by SHA-256 — an x86_64 build of [`neo773/d9mt`](https://github.com/neo773/d9mt), whose own releases are arm64 for CrossOver and will not load under an x86_64 Wine |
| Steamworks | none — set `STEAMWORKS_SDK_ZIP`, `STEAMWORKS_REDIST_DYLIB` or `STEAMWORKS_REDIST_URL` |

Every input takes a local path as readily as a URL, so an archive already on
disk is reused rather than downloaded again. The SDK zip and the bare dylib are
both accepted. A custom Wine or D9MT URL must be paired with its matching
`MACOS_WINE_SHA256` or `MACOS_D9MT_SHA256`; unsigned replacement inputs are
rejected before extraction.

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
D9MT_LICENSE_FILE=/path/to/D9MT-LICENSE-or-permission.txt \
STEAMWORKS_REDIST_DYLIB=macos-runtimes/steam/libsteam_api.dylib \
  ./tools/macos-port/build-depot.sh
```

The bridge is built as part of this unless `STEAM_BRIDGE_DIR` points at one
that already exists. `CODESIGN_IDENTITY` selects a signing identity; without it
the bundle is signed ad-hoc, which is enough to run locally. Published builds
must use a stable signing identity so app updates retain access to the existing
DepotDownloader token in Keychain without another QR approval.

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
  runs `fetch-runtimes.sh`; Wine and D9MT are pinned by SHA-256. It requires
  **`secrets.STEAMWORKS_REDIST_URL`**, pointing at the SDK zip or bare
  `libsteam_api.dylib`, and **`secrets.D9MT_LICENSE_URL`**, pointing at genuine
  redistribution terms or written permission. Without either input the job
  reports that and finishes green, because an unbuildable macOS depot should
  not hold up Windows and Linux releases.

The bundle travels between jobs as a tar, not as loose files: an artifact upload
of loose files does not keep executable bits, and an `.app` that lost them does
not launch. A verification step then re-checks the finished bundle on the macOS
runner — signature, executable bit, interpreter line, no symlinks — because none
of that can be checked again on the Linux runner that publishes it.

`publish-steam` pushes depot `5147523` alongside the Windows and Linux depots
in a single BuildID when the macOS lane produced a bundle, and leaves that depot
at its previous content when it did not. The same tar is then pushed to the main
app's macOS depot in that app's own BuildID — unchanged, because the AppID it
runs as comes from Steam at launch rather than from anything inside the bundle.

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
| the world is black after a map loads, HUD and viewmodel fine | a framebuffer copy the engine reads back the same frame was dropped by D9MT; see below |

Deleting `~/Library/Application Support/Team Frontress` resets the prefix and
the staged client without touching the install.

### A black world with a working HUD

Source does not hand the finished frame straight to the swap chain. Several
passes copy the framebuffer into a render-target texture and then paint that
texture back over the whole screen. The glow outlines are the clearest case:
`CGlowObjectManager::RenderGlowModels` copies the finished scene into
`_rt_GlowGameSceneBackup`, clears the framebuffer to black, draws every glowing
entity in flat team colour, copies *those* out to `_rt_GlowColor`, and paints
the backup back over the framebuffer (`glow_outline_effect.cpp`); image-space
motion blur and the engine's post processing do the same through
`_rt_FullFrameFB`.

Drop the first of those copies and the backup texture is never written, so the
pass that restores it paints a full-screen black rectangle over everything drawn
so far — the world, the players, the props. What survives is whatever is drawn
after it: the glow silhouettes, the viewmodel, the HUD, the nameplates. That is
the whole symptom. The main menu draws no 3D scene and runs none of these
passes, so it is unaffected, which is what makes this look like a map-loading
problem when it is not one.

D9MT reports a dropped copy on the `err:` channel, which Wine sends to the
launcher's log:

```bash
grep 'err: *d9mt' ~/Library/Logs/Team\ Frontress/launcher.log | sort | uniq -c
```

`blitImageView:` lines there, at a couple per frame, are this failure and
nothing else. Two of them are it, and both are fixed in D9MT 0.4:

| line | what it was |
| --- | --- |
| `failed to create 2D source view` | the d3d9 front-end hands the copy's source over as a view created with transfer usage, and D9MT would only build a Metal texture from a view that was sampled, storage or an attachment. So it dropped *every* `StretchRect` that scales or converts — which is every copy in that pass, since the glow targets are `RGBA16161616F` under HDR while the back buffer is not. |
| `multisampled blit not implemented` | with antialiasing on, the source of the same copy is the multisampled scene, and Metal cannot sample a multisampled texture. D9MT now resolves it into a temporary single-sample image and copies from that, the way the present path already did. |

Turning antialiasing off is not a workaround for an older D9MT: it only silences
the second line, and the first one drops the copy on its own. Any other
`blitImageView:` line is a copy D9MT still cannot perform, and it names the
operation.

Pipeline compilation is a different symptom. D9MT builds a Metal pipeline per
shader-and-state combination the game uses; a draw whose pipeline is not ready
waits for it, so a cold map costs frame hitches rather than missing geometry.
The pipelines are recorded in `d9mt_pso_cache.bin` next to the staged client and
replayed on the next launch, so a warm run is much shorter; deleting that file
forces a cold one. `D9MT_ASYNC_SKIP=1` restores the dxvk-async behaviour of
skipping those draws instead — faster, and geometry appears when it appears —
and `D9MT_PSO_DEADLINE_MS` (default 100) then sets how long a draw may be
skipped before the frame thread compiles the pipeline itself. D9MT's own
`d3d9fe.log`, next to the staged client, carries the stall report.

## Licensing

The Source 1 SDK license and `thirdpartylegalnotices.txt` are copied into every
bundle as required by the SDK license. Wine is LGPL and its terms are copied to
`licenses/Wine-LGPL-2.1.txt`. A release owner must additionally provide the
corresponding Wine source/build materials and notices for the bundled Wine Mono,
Wine Gecko, and runtime libraries; the license text alone does not complete
those obligations. D9MT's original code is LGPL-2.1-or-later and its bundled
third-party components retain their own terms. `D9MT_LICENSE_FILE` must point to
the published D9MT license and notices; CI obtains that document from the
configured `D9MT_LICENSE_URL`.

`libsteam_api.dylib` and the native Steam client are proprietary Valve
components, redistributable only under the Steamworks SDK Access Agreement. The
bridge itself contains no part of the SDK — it loads that library through
`dlopen` at runtime — and is MIT.

Team Frontress does not distribute or link to DepotDownloader. The native
`tf2-content-helper.swift` wrapper is source in this repository and downloads
the unchanged GPL-2.0 program directly from its official upstream release. The
downloaded archive contains the GPL text, and the bundle's
`licenses/DepotDownloader.txt` identifies the exact release, source, license,
URLs, and pinned hashes used by the wrapper.
