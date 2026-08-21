# Team Frontress dedicated server build

This tree uses three different Steam AppIDs on purpose:

- **5147520** — Team Frontress Playtest, the runtime game AppID.
- **5150320** — Team Frontress Dedicated Server Tool, used only for
  SteamPipe/SteamCMD distribution.
- **232250** — Valve TF2 Dedicated Server files used as an engine/content
  dependency.

The Tool can live under the publisher's main product in Steamworks. The
dedicated payload still contains `steam_appid.txt` with **5147520**, matching
Valve's dedicated-server packaging model: the Tool distributes the files, while
the server runs as the game's AppID.

## Local build

```bash
./src/buildallprojects
./game_clean/copy_server.sh
```

The server-only artifact is `game_server_dist/`. It removes `client.so` and the
render shader module, switches `tc2/gameinfo.txt` to the server search paths and
writes:

```text
steam_appid.txt = 5147520
tc2/steam.inf: appID=5147520
tc2/steam.inf: ServerAppID=5150320
```

Install/update TF2 DS and Steam Linux Runtime beside the package:

```bash
cd game_server_dist
./steamcmd_update.sh
```

Then launch:

```bash
GSLT=YOUR_TOKEN ./start_dedicated_tc2.sh   +map ctf_2fort   +maxplayers 24   +ip 0.0.0.0   -port 27015
```

Create the GSLT for runtime AppID **5147520**.

## SteamPipe / CI

The dedicated workflow is `.github/workflows/build_dedicated.yml`.

Configure repository variable:

```text
STEAM_DS_DEPOT_LINUX=<the Linux depot ID belonging to Tool 5150320>
```

Optional:

```text
STEAM_DS_BRANCH=<beta branch to set live>
```

It reuses the existing `STEAM_USERNAME` and `STEAM_CONFIG_VDF` secrets. The
uploader refuses to publish if `steam_appid.txt` is missing or is not 5147520,
so the Tool AppID cannot accidentally leak into runtime identity.
