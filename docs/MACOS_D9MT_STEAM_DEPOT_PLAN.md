# Team Frontress: macOS D9MT Steam depot plan

Status: **implementation plan / prototype track**  
Target: Apple Silicon macOS, Steam AppID **5147520**, Windows Source/TF2 runtime under Wine + D9MT.  
Goal: a macOS user installs Team Frontress from Steam, presses **Play**, completes a one-time Windows Steam/TF2 setup, and from then on launches Team Frontress directly from the native macOS Steam client.

> This document is a technical and licensing plan, not legal advice. Public redistribution must be blocked until every third-party component has an explicit redistributable license compatible with our Steam distribution obligations.

## 1. What we already have

Team Frontress is already structured correctly for a TF2 mod:

- the mod is built from the official TF2 / Source SDK 2013 code;
- AppID 5147520 is used as the mod AppID;
- `game/tc2/gameinfo.txt` has `DependsOnAppID 440`;
- TF2 assets are mounted through `|appid_440|` instead of being copied into our payload;
- the Windows launcher (`tc2_win64.exe`) initializes the real Steam API, checks `BIsAppInstalled(440)`, and obtains the TF2 install directory with `GetAppInstallDir(440)`;
- the existing CI already produces `steam-client-windows` and publishes Windows/Linux depots through SteamPipe;
- `gru2007/d9mt-builded` can already produce an experimental x86_64 D9MT `d3d9.dll` plus `d9mtmetal`/`winemetal` components.

Therefore the first macOS version does **not** need a native Source Engine port. The macOS product is a native launcher + a bundled FOSS Wine runtime executing the existing Windows x64 Team Frontress/TF2 engine, with D3D9 translated directly to Metal by D9MT.

## 2. Required legal boundary from Valve

Official Valve docs:

- Source/TF2 mod distribution: https://partner.steamgames.com/doc/sdk/uploading/distributing_source_engine
- Open-source software on Steam: https://partner.steamgames.com/doc/sdk/uploading/distributing_opensource
- Steam depots: https://partner.steamgames.com/doc/store/application/depots
- Uploading / macOS launch options: https://partner.steamgames.com/doc/sdk/uploading

Valve's Source 1 SDK license in this repository allows a free modified Valve game based on the official SDK to be distributed in source/object form, subject to the license and Steam Subscriber Agreement. We must keep `LICENSE` and `thirdpartylegalnotices.txt` in distributions containing SDK code.

For TF2 specifically Valve says that a TF2 mod **may redistribute only its own game files** and must load TF2 content from a **local TF2 installation**. Therefore:

### Never put these into our macOS depot

- TF2 VPKs/assets from AppID 440;
- TF2 `engine.dll`, `launcher.dll`, `materialsystem.dll`, `shaderapidx9.dll`, etc.;
- a pre-installed TF2 tree copied from our CI machine;
- any leaked/full Source Engine source or binaries built from leaked Source source.

### Allowed design

Our depot contains our own Team Frontress payload and compatibility runtime. On the user's Mac, the launcher installs/runs the real Windows Steam client in a Wine prefix. The user signs into Steam and installs TF2 AppID 440 from Valve. `tc2_win64.exe` then sees that local AppID 440 installation through the real Windows Steam API exactly as it does on Windows.

## 3. Third-party component license matrix

| Component | Intended source/version | License status | Ship in depot? | Decision |
|---|---|---|---|---|
| Team Frontress / TF2 SDK code | this repo / official Valve SDK | Source 1 SDK License | Yes | Must stay free; include Valve license + third-party notices. |
| TF2 engine/content AppID 440 | Valve Steam depots | Valve proprietary | **No** | User downloads it locally from Steam. |
| Wine runtime | Wine / CodeWeavers published FOSS CrossOver source | LGPL-family/FOSS | Yes, with compliance | Build ourselves; do not copy CrossOver.app/product binaries. Publish corresponding source/build recipe and license notices. |
| DXMT `winemetal` | **DXMT v0.80** | MIT | Yes | Pin v0.80; it is explicitly the final MIT release. Do not auto-upgrade to current LGPL builds without a new review. |
| DXVK D3D9 frontend | DXVK | zlib | Yes | Keep license/copyright notice. |
| SPIRV-Cross | Khronos SPIRV-Cross | MIT OR Apache-2.0 | Yes | Keep selected license/copyright notices. |
| D9MT own backend code | `neo773/d9mt` | **ambiguous** | **BLOCK public depot** | Upstream has no root LICENSE and README only says its code “follows suit”. Get an explicit license or written redistribution permission first. |
| Silo | `mikaelhug/Silo` | LGPL-2.1-or-later | Reference / selectively reuse with compliance | Best reference implementation for Steam-in-Wine on macOS. Prefer our own small launcher rather than embedding the whole app. |
| Sikarugir launcher/runtime | Sikarugir | mixed / launcher redistribution terms not clear enough for us | No | Reference only unless explicit permission/license is established. |
| CrossOver application binaries | CodeWeavers product | proprietary | **No** | We only use published FOSS source to build Wine ourselves. |
| Apple GPTK / D3DMetal | Apple license | proprietary/restricted | **No** | Not needed for D3D9/D9MT. |

### D9MT release gate

Do **not** treat our fork as permission to relicense upstream code. Before publishing D9MT in a public Steam depot, obtain one of:

1. a normal explicit upstream license (MIT/zlib/LGPL/etc.); or
2. a written redistribution grant from the copyright holder covering binary distribution with Team Frontress on Steam.

Technical CI/prototyping can proceed in parallel, but `publish-steam` must have a deliberate license gate until this is resolved.

## 4. Open-source software we should reuse as reference

### Silo — primary reference

Repo: https://github.com/mikaelhug/Silo

This is the closest existing OSS architecture to ours:

- native Swift/SwiftUI macOS launcher;
- a real Windows Steam client running inside the same Wine bottle as games;
- Steamworks IPC/ownership/auth works because Steam and the game are co-resident in the Wine prefix;
- Wine is built in CI from CodeWeavers' published FOSS CrossOver source;
- runtime dependency dylibs are bundled to make Wine self-contained;
- it has code for Wine prefix provisioning, Windows Steam setup, ACF/library discovery, graphics backend injection and launch orchestration;
- it uses ad-hoc signing for development builds.

Relevant files to study/adapt conceptually:

- `Scripts/build-wine.sh`
- `Scripts/bundle-wine-dylibs.sh`
- `Sources/SiloKit/Provisioning/*`
- `Sources/SiloKit/Steam/SteamBottle.swift`
- `Sources/SiloKit/Steam/SetupDownloads.swift`
- `Sources/SiloKit/Launch/LaunchOrchestrator.swift`

We should **not** start by forking the full Silo UI. Team Frontress needs a deterministic one-game launcher, so a much smaller native launcher is preferable. If code is copied from Silo rather than independently implemented, retain LGPL obligations for that code.

### CodeWeavers FOSS CrossOver source — Wine runtime source

Source page: https://www.codeweavers.com/crossover/source

CodeWeavers publishes the FOSS source corresponding to CrossOver. We should use a pinned source version and build Wine ourselves in CI. This gives us the macOS Wine patches we need without redistributing proprietary CrossOver product binaries.

### Sikarugir / Wineskin / Kegworks / Whisky

Useful as implementation references, but **not** the preferred shipped base:

- Sikarugir proves the app-bundle wrapper UX but has mixed component licensing;
- Whisky is GPL-oriented/GPTK-oriented and no longer a good fit for this Steam+SDK distribution boundary;
- Wineskin/Kegworks are useful packaging references but provide less direct benefit than Silo for our exact “real Windows Steam in bottle” architecture.

## 5. Target architecture

```text
Native macOS Steam
       |
       | launch option: Team Frontress.app
       v
+--------------------------------------+
| Team Frontress.app                   |
| native arm64 launcher                |
+------------------+-------------------+
                   |
                   | starts bundled x86_64 Wine (Rosetta 2)
                   v
~/Library/Application Support/Team Frontress/
  prefix/
    drive_c/Program Files (x86)/Steam/
      Steam.exe                         <- real Valve Windows Steam
      steamapps/
        appmanifest_440.acf
        common/Team Fortress 2/         <- downloaded from Valve, NOT our depot
                   |
                   | same Wine prefix / Steam IPC
                   v
<Steam install dir for Team Frontress>/payload/tc2_win64.exe
                   |
                   | loads TF2 engine from AppID 440
                   v
             TF2 Source x64
                   |
                  D3D9
                   |
             d9mt x64 d3d9.dll
                   |
          d9mtmetal + winemetal
                   |
                 Metal
```

The native macOS Steam client is only the outer launcher/distribution client. The **Windows Steam client in Wine** is the Steam API server used by `tc2_win64.exe` and the TF2 Windows runtime.

## 6. macOS Steam depot layout

Keep immutable Steam-delivered files outside the mutable Wine prefix. This also prepares us for proper signing/notarization later.

```text
<Steam install dir>/
├── Team Frontress.app/
│   └── Contents/
│       ├── Info.plist
│       ├── MacOS/
│       │   └── TeamFrontressLauncher       # arm64 Mach-O
│       └── Resources/
│           └── runtime-version.json
│
├── payload/
│   ├── tc2_win64.exe                       # from SAME CI run's Windows artifact
│   ├── bin/x64/steam_api64.dll
│   └── tc2/
│       ├── bin/x64/client.dll
│       ├── bin/x64/server.dll
│       ├── bin/x64/game_shader_generic_std.dll
│       ├── gameinfo.txt
│       ├── pak1*.vpk
│       └── ... our own files only
│
├── runtime/
│   ├── wine/
│   │   ├── bin/wine
│   │   ├── bin/wineserver
│   │   ├── lib/...
│   │   └── bundled dylibs
│   └── d9mt/
│       ├── d3d9.dll
│       ├── x86_64-windows/d9mtmetal.dll
│       ├── x86_64-unix/d9mtmetal.so
│       ├── x86_64-windows/winemetal.dll
│       └── x86_64-unix/winemetal.so
│
└── licenses/
    ├── SOURCE-1-SDK-LICENSE.txt
    ├── thirdpartylegalnotices.txt
    ├── WINE-LGPL.txt
    ├── DXMT-MIT-v0.80.txt
    ├── DXVK-ZLIB.txt
    ├── SPIRV-CROSS.txt
    ├── SDL.txt
    └── D9MT-LICENSE.txt                    # release gate until explicit
```

Mutable user data:

```text
~/Library/Application Support/Team Frontress/
├── prefix/                                 # WINEPREFIX
├── cache/
│   ├── SteamSetup.exe
│   └── d9mt-shaders/
├── logs/
└── state.json
```

Do not modify files inside the Steam depot during normal use except Steam updating the depot itself.

## 7. Native launcher scope

Create a new directory:

```text
macos/launcher/
├── Package.swift
├── Sources/TeamFrontressLauncher/
│   ├── main.swift
│   ├── AppPaths.swift
│   ├── RosettaCheck.swift
│   ├── WineRuntime.swift
│   ├── SteamProvisioner.swift
│   ├── TF2Provisioner.swift
│   ├── D9MTInstaller.swift
│   ├── GameLauncher.swift
│   └── LogManager.swift
└── Resources/
    └── Info.plist
```

The first implementation can be a small AppKit/Swift executable rather than a large SwiftUI application.

### Launcher algorithm

Every launch:

1. Resolve its own Steam install root from `Bundle.main.bundleURL`.
2. Confirm Apple Silicon/macOS supported version.
3. Verify an x86_64 process can execute through Rosetta. If Rosetta is absent, explain that macOS will need Rosetta for the Windows x64 Source runtime.
4. Create/update `~/Library/Application Support/Team Frontress/prefix`.
5. Install/update our D9MT Wine modules into the prefix/runtime overlay.
6. If Windows Steam is missing:
   - download Valve's current Windows Steam installer from an official Valve/Steam HTTPS URL at runtime;
   - launch installer through our Wine;
   - never collect/store the user's Steam credentials ourselves.
7. Start Windows Steam in that same prefix.
8. Wait for Steam readiness.
9. Check `steamapps/appmanifest_440.acf` and the TF2 install directory.
10. If TF2 is missing, show a one-time onboarding action that asks the Windows Steam client to install AppID 440. The user logs in/approves installation in the real Steam UI.
11. After TF2 is installed, launch our Steam-delivered `payload/tc2_win64.exe` in the **same prefix**.
12. Capture Wine/D9MT logs to Application Support.
13. Exit the native launcher when the game exits; optionally leave Windows Steam running or shut it down according to a setting.

### Required launch environment

Initial target:

```bash
WINEPREFIX="$HOME/Library/Application Support/Team Frontress/prefix"
WINEDLLOVERRIDES="d3d9=n;d9mtmetal=b;winemetal=b"
D9MT_SHADER_CACHE_PATH="$HOME/Library/Application Support/Team Frontress/cache/d9mt-shaders"
```

For early tests launch Team Frontress with `-insecure` until TF2/D9MT stability is established. Remove this from production once validated.

## 8. Wine runtime CI

Do **not** rebuild Wine on every Team Frontress commit. Make Wine a versioned runtime input.

Add a separate workflow:

```text
.github/workflows/macos-runtime.yml
```

Trigger:

- `workflow_dispatch`;
- optional tag such as `runtime/macos/*`.

Runner: `macos-15` (Apple Silicon where available).

Steps:

1. Pin a CodeWeavers FOSS CrossOver source version (for example the current tested CX 26.x source; never `latest`).
2. Download the official source tarball and record SHA-256.
3. Build x86_64/WoW64 Wine using the Silo build recipe as a technical reference.
4. Build/pin SDL and required runtime dylibs.
5. Bundle all dynamically required non-system dylibs so users need no Homebrew.
6. Add a `wine64` alias if needed by our launcher.
7. Archive `wine-runtime.tar.xz`.
8. Produce `wine-runtime.manifest.json` with source URL, version, hashes and build flags.
9. Publish the binary runtime as a GitHub Release asset.
10. Publish or preserve the exact corresponding source/build patches required for LGPL compliance.

The Team Frontress release workflow downloads this pinned runtime asset instead of spending 30–60 minutes rebuilding Wine every game release.

## 9. D9MT CI integration

Current source: `gru2007/d9mt-builded`.

Production changes before shipping:

1. Pin the D9MT source commit.
2. Pin **DXMT v0.80** instead of querying `releases/latest`.
3. Pin/hash every downloaded runtime artifact.
4. Include license files in the generated D9MT package.
5. Keep the x86_64 PE verification (`pei-x86-64`).
6. Add a D3D9 smoke test if feasible.
7. Add a TF2/Source smoke test later on a self-hosted Apple Silicon runner.
8. Do not create a public redistributable Steam-ready D9MT release until upstream licensing is explicit.

The Team Frontress macOS job can consume a tagged D9MT runtime release, or build the pinned D9MT commit itself. A tagged/hash-verified runtime is faster and more reproducible.

## 10. Team Frontress CI changes

Current `.github/workflows/build.yml` already builds Windows and Linux and publishes Steam artifacts. Keep those lanes unchanged.

### New job: `macos-client`

Recommended dependency:

```text
build (Windows + Linux)
        |
        +--> macos-client
```

The macOS job should:

1. Checkout Team Frontress.
2. Download the **same workflow run's** `steam-client-windows` artifact.
3. Download the pinned Wine runtime release.
4. Download the pinned D9MT x64 runtime release (when license gate permits redistribution), or build it for prototype testing.
5. Build `TeamFrontressLauncher` for `arm64-apple-macos`.
6. Assemble the depot layout from section 6.
7. Copy the existing Windows client artifact into `payload/` unchanged except for removing `STEAM_READY`.
8. Add all license/notices files.
9. For the current prototype stage, ad-hoc sign the native `.app` so macOS treats it as a valid app bundle. No notarization yet.
10. Validate:
    - launcher is Mach-O arm64;
    - bundled Wine executable is x86_64 and starts via Rosetta on the CI runner if possible;
    - `payload/tc2_win64.exe` is PE32+ x86-64;
    - D9MT `d3d9.dll` is PE32+ x86-64;
    - no known TF2 AppID 440 VPK/engine files exist in the staged depot;
    - required license files exist.
11. `touch macos_dist/STEAM_READY` only after all validations pass.
12. Upload artifact `steam-client-macos`.

### Suggested repository additions

```text
macos/
├── launcher/...
├── scripts/
│   ├── build-app.sh
│   ├── stage-depot.sh
│   ├── verify-depot.sh
│   └── adhoc-sign.sh
└── runtime/
    └── versions.env
```

Example `versions.env`:

```bash
WINE_CX_VERSION=26.3.0
WINE_RUNTIME_TAG=macos-wine-cx-26.3.0
DXMT_VERSION=v0.80
D9MT_COMMIT=<pinned SHA>
D9MT_RUNTIME_TAG=<our tested x64 tag>
```

Never use floating `main`/`latest` for files shipped to customers.

## 11. Extend SteamPipe for a macOS depot

Current `game_clean/steampipe.sh` supports `STEAM_WIN_DIR` and `STEAM_LINUX_DIR`.

Add:

```text
STEAM_DEPOT_MAC
STEAM_MAC_DIR
```

Then:

- validate `STEAM_MAC_DIR` like the existing Windows/Linux directories;
- generate `depot_${STEAM_DEPOT_MAC}.vdf` with the same recursive file mapping;
- append it to the same app build's `depots` block;
- upload Windows + Linux + macOS under **one BuildID** when all required client artifacts have `STEAM_READY`.

In `.github/workflows/build.yml` add repository variable:

```text
STEAM_DEPOT_MAC=<new depot id>
```

and include `steam-client-macos` in the downloaded publish artifacts.

For the first implementation, publish to a dedicated private branch such as:

```text
macos-d9mt
```

rather than changing the default/prerelease customer branch immediately.

## 12. Steamworks configuration

Manual setup in Steamworks App Admin:

1. **SteamPipe -> Depots**: create a new depot, e.g. `Team Frontress macOS`.
2. Set depot OS = **macOS**.
3. Add the depot to Developer Comp and whichever testing package should receive it.
4. Installation / Launch Options: add a macOS launch option:

```text
Executable: Team Frontress.app
OS: macOS
```

Valve recommends launching the `.app` bundle rather than the inner Mach-O; on Apple Silicon this also lets macOS select the best architecture in the bundle.

5. Initially expose it only to the private beta/testing package/branch.
6. Do not mark public macOS support until the runtime works and notarization is added.

## 13. First-run UX

Target user experience:

```text
Steam (macOS): Install Team Frontress
        |
        v
Press Play
        |
        v
Team Frontress setup
  [1/3] Preparing Windows runtime
  [2/3] Sign in to Steam
  [3/3] Install Team Fortress 2
        |
        v
Play Team Frontress
```

On subsequent launches:

```text
Steam -> Play -> Team Frontress.app -> start Windows Steam silently -> tc2_win64.exe
```

No CrossOver, Homebrew, command line or manual DLL copying should be visible to the player.

## 14. Prototype signing / no notarization yet

For our current private test stage:

- build 64-bit/arm64 launcher;
- ad-hoc sign the app/runtime as required to launch locally;
- distribute only on a testing branch while validating the runtime.

This is **not** the final public macOS release configuration. Valve's current macOS documentation expects modern 64-bit macOS applications to follow Apple's notarization requirements. Later we will add Developer ID signing, Hardened Runtime entitlements and notarization/stapling.

Do not enable App Sandbox; Steam's own macOS docs state Steam is not compatible with the App Sandbox entitlement.

## 15. Validation gates

A macOS build should not get `STEAM_READY` unless all of these pass:

### Legal/package

- [ ] Valve `LICENSE` present.
- [ ] Valve `thirdpartylegalnotices.txt` present.
- [ ] Wine license/source manifest present.
- [ ] DXMT v0.80 MIT notice present.
- [ ] DXVK/SPIRV-Cross notices present.
- [ ] D9MT explicit redistribution license present (**public-release blocker**).
- [ ] no TF2-owned game/engine files included.

### Binary

- [ ] `TeamFrontressLauncher` = Mach-O arm64.
- [ ] Wine runtime binaries = expected x86_64/WoW64 architecture.
- [ ] `tc2_win64.exe` = PE32+ x86-64.
- [ ] `d3d9.dll` = PE32+ x86-64.
- [ ] `d9mtmetal.dll` = PE32+ x86-64.

### Runtime

- [ ] Wine prefix initializes on a clean Mac user profile.
- [ ] Windows Steam installer launches.
- [ ] Steam UI renders and user can sign in.
- [ ] TF2 AppID 440 installs through Windows Steam.
- [ ] `tc2_win64.exe` sees AppID 440 through `ISteamApps`.
- [ ] Source loads our mod DLLs.
- [ ] D9MT initializes Metal.
- [ ] main menu renders.
- [ ] local map loads.
- [ ] online Steam auth works after `-insecure` is removed.
- [ ] controllers/audio/input work.

## 16. Implementation order

### Phase 0 — licensing gate

- Ask D9MT upstream for an explicit license/Steam redistribution permission.
- Pin DXMT to v0.80/MIT.
- Document Wine corresponding-source process.

### Phase 1 — local proof of concept

- Build self-contained Wine from CodeWeavers FOSS source using the Silo recipe as reference.
- Run Windows Steam in our prefix.
- Install TF2 440.
- Launch current `tc2_win64.exe` with our x64 D9MT.
- Fix Source-specific D9MT issues until menu + map work.

### Phase 2 — native launcher

- Implement the small Swift launcher.
- Automate prefix creation, Steam installation detection, TF2 detection and game launch.
- Add clean first-run UX and logs.

### Phase 3 — CI macOS artifact

- Add versioned Wine-runtime workflow.
- Add `macos-client` job to Team Frontress CI.
- Reuse the same-run Windows artifact as `payload/`.
- Produce `steam-client-macos` with `STEAM_READY` validation.

### Phase 4 — Steam private depot

- Add macOS depot in Steamworks.
- Extend `steampipe.sh` for `STEAM_MAC_DIR`/`STEAM_DEPOT_MAC`.
- Publish to a private `macos-d9mt` branch.
- Test on clean Apple Silicon machines.

### Phase 5 — public-quality release

- Resolve all third-party redistribution permissions.
- Remove `-insecure` after online validation.
- Add Developer ID signing/notarization.
- Publish macOS support publicly.

## 17. Immediate next coding task

The first code PR should **not** touch SteamPipe yet. It should prove the runtime locally and create reusable pieces:

1. add `macos/runtime/versions.env`;
2. add `.github/workflows/macos-runtime.yml` to build the pinned self-contained Wine runtime;
3. add `macos/scripts/stage-prototype.sh` that combines:
   - Windows Team Frontress payload,
   - Wine runtime,
   - x64 D9MT package;
4. add a minimal `Team Frontress.app` launcher that initializes the Wine prefix and starts Windows Steam / `tc2_win64.exe`;
5. output a GitHub Actions artifact, **not Steam upload yet**;
6. run it on an actual Apple Silicon Mac and fix D9MT/Steam/Source issues;
7. only then wire that exact tested artifact layout into the Steam macOS depot.

This keeps the existing Windows/Linux release pipeline stable while we make the macOS runtime deterministic.