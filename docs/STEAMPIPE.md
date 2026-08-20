# Shipping on Steam (SteamPipe)

The game is published as **Team Frontress Playtest, AppID `5147520`**. This
document covers what binds the build to that AppID, how a build gets to Steam,
and what has to exist in Steamworks for either half to work.

## AppIDs

| AppID | What it is | Who needs it |
| --- | --- | --- |
| `5147520` | Team Frontress Playtest — the app we publish under | every player |
| `440` | Team Fortress 2 — engine binaries and game content | every player |
| `243750` | Source SDK Base 2013 Multiplayer | Windows dedicated servers |
| `244310` | Source SDK Base 2013 Dedicated Server | Linux dedicated servers |

We ship the mod's own DLLs, tools and paks; the engine itself is loaded out of
the player's TF2 install (`GetGameInstallDir` in `src/launcher_main/main.cpp`),
and TF2's VPKs are mounted through `|appid_440|` search paths. TF2 remains a
hard requirement — the launcher shows an error and exits without it.

Dedicated servers stay on the SDK 2013 apps. They log into Steam anonymously,
which cannot mount a playtest app, so `gameinfo_server.txt` still declares
`243750`.

## What binds the client to 5147520

Four places, all of which have to agree:

| File | Field |
| --- | --- |
| `game/tc2/gameinfo.txt` | `FileSystem/SteamAppId` — what the engine's filesystem initialises as |
| `game/tc2/steam.inf` | `appID` — what the engine reports to the server browser and to Steam |
| `src/launcher_main/launcher_main_tc2.vpc` | `MOD_APPID` — compiled into the launcher, written to `steam_appid.txt` before `SteamAPI_Init` |
| `src/public/filesystem_init.cpp` | `g_Source1Appids` — only the name shown in "you must own …" errors |

`steam_appid.txt` is written next to the executable on every launch, so a stale
file from an older build can never win over the AppID we ship with. It is
excluded from the depot for the same reason it exists: a copy of it that we did
*not* write would let the game start without Steam having checked ownership.

## Depots

`game_clean/steampipe.sh` defaults to `AppID + 1` for Windows content and
`AppID + 2` for Linux:

| Depot | Content |
| --- | --- |
| `5147521` | Windows (`game_dist` from a Windows build) |
| `5147522` | Linux (`game_dist` from a Linux build) |

If Steamworks assigned different depot IDs, override them — repository variables
`STEAM_DEPOT_WIN` / `STEAM_DEPOT_LINUX` in CI, or the same environment variables
locally. Nothing else needs to change.

Debug symbols (`*.pdb`, `*.dbg`) are not in the depots. They stay in the
`game-debug-info` build artifact, same as before.

## Uploading

`game_clean/steampipe.sh` generates the app and depot build scripts from a dist
directory, fetches `steamcmd` if it isn't on `PATH`, and runs `run_app_build`.
The generated scripts and the upload log are left in `steam_build/` for
inspection after a failed run.

Locally, after `./game_clean/copy.sh`:

```bash
STEAM_USERNAME=<builder account> STEAM_PASSWORD=<password> \
  ./game_clean/steampipe.sh game_dist
```

Both platforms in a single build (one BuildID covering both depots) — this is
what the release job does, from two unpacked release zips:

```bash
STEAM_USERNAME=… STEAM_CONFIG_VDF=… \
STEAM_WIN_DIR=$PWD/game_dist_win STEAM_LINUX_DIR=$PWD/game_dist_linux \
  ./game_clean/steampipe.sh
```

Add `STEAMPIPE_PREVIEW=1` to generate and validate the build without uploading
anything.

### Variables

| Variable | Default | Meaning |
| --- | --- | --- |
| `STEAM_APPID` | `5147520` | app to build into |
| `STEAM_DEPOT_WIN` | `STEAM_APPID + 1` | Windows content depot |
| `STEAM_DEPOT_LINUX` | `STEAM_APPID + 2` | Linux content depot |
| `STEAM_WIN_DIR` / `STEAM_LINUX_DIR` | — | content dirs; set both for a combined build |
| `STEAM_BRANCH` | empty | branch to set live; empty uploads without setting anything live |
| `STEAM_USERNAME` | — | builder account (required) |
| `STEAM_CONFIG_VDF` | — | base64 `config.vdf` for unattended login |
| `STEAM_PASSWORD` | — | password, for local runs without a `config.vdf` |
| `RELEASE_VERSION` | `VERSION` from `shared.sh` | build description |
| `STEAMPIPE_PREVIEW` | `0` | `1` = validate only, no upload |
| `STEAMCMD` | `steamcmd` on `PATH`, else downloaded | path to a specific steamcmd |
| `STEAMCMD_HOME` | `~/steamcmd` | where a downloaded steamcmd is installed |

steamcmd is installed outside the repository on purpose: it stores the login
token next to itself, and the Windows CI job caches the whole workspace.

An empty `STEAM_BRANCH` is deliberate: a `setlive` naming a branch that doesn't
exist in Steamworks fails the whole build, so auto-setlive is opt-in.

## CI

| Where | When | Sets live |
| --- | --- | --- |
| `build_linux.yml`, `build_windows.yml` | push to `gf-mod`, `campus-fortress`, or `release/*` | `STEAM_PLAYTEST_BRANCH` |
| `publish.yml` (`publish_steam`) | a non-prerelease GitHub release, or `workflow_dispatch` with a tag | `STEAM_RELEASE_BRANCH`, or the branch given to the dispatch |

The per-platform build jobs upload one depot each, so a BuildID from them covers
only the platform that produced it. The release job downloads both release zips
and uploads both depots as one build; prefer it for anything players get.

Every Steam step is skipped when `STEAM_CONFIG_VDF` is unset, so forks and
credential-less runs build normally and publish nothing.

### Secrets and variables

Repository secrets:

- `STEAM_USERNAME` — a Steamworks account with "Edit App Metadata" and
  "Publish App Changes To Steam" on the app. Use a dedicated builder account,
  not a personal one; Steam Guard on it must be email- or app-based, not
  disabled.
- `STEAM_CONFIG_VDF` — base64 of a `config.vdf` from a session that has already
  cleared Steam Guard.

Repository variables (all optional):

- `STEAM_APPID`, `STEAM_DEPOT_WIN`, `STEAM_DEPOT_LINUX` — override the defaults
  above.
- `STEAM_PLAYTEST_BRANCH` — branch the branch builds set live, e.g. `prerelease`.
- `STEAM_RELEASE_BRANCH` — branch releases set live, e.g. `default`.

### Generating `STEAM_CONFIG_VDF`

On a machine with `steamcmd`, logged in as the builder account:

```bash
steamcmd +login <builder account> +quit   # answer the Steam Guard prompt
base64 -w0 ~/Steam/config/config.vdf      # paste into the secret
```

Log in once more afterwards and confirm it no longer asks for a code — that is
the state the secret has to capture. The token expires if unused for a long
time, or when the account's password changes; regenerate it the same way.

## Steamworks configuration

The upload only covers content. These have to be set on the app itself:

- **Depots** `5147521` (Windows) and `5147522` (Linux) must exist and be
  included in the playtest package.
- **Launch options**:
  - Windows — `tc2_win64.exe`, arguments
    `-steam -particles 1 -condebug -nobreakpad -nominidumps -enablefakeip +ip 127.0.0.1`
  - Linux — `tc2.sh`, same arguments plus `-gathermod`
- **Steam Linux Runtime 3.0 (sniper), AppID `1628350`** must be a required tool
  for the Linux launch option. `tc2.sh` refuses to start without it, and on a
  Steam install it will not be there unless the app asks for it.
- **Lobbies / matchmaking**: Greyline's transport now runs over the coordinator
  link rather than Steam lobbies, but anything that does call into Steam
  matchmaking needs the app configured for it — this is the setting that made
  `CreateLobby` return `AccessDenied` back when we ran under 243750
  (`services/coordinator/docs/NETWORKING-SPIKE.md`).
- **Publisher Web API key**: needed to validate Steam auth tickets properly
  instead of trusting the SteamID a client claims (`security.auth_mode` in the
  coordinator). Owning the AppID is what makes that possible.
