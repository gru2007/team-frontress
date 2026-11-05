# Frontress stack

```text
game client ─┐                          ┌─ webhooks → other community projects
             ├── frontress-gc ──────────┤
game server ─┘   (game coordinator)     │
  (the pool)                            │
```

- **frontress-gc** (`services/gc`) is the game coordinator. Clients and
  dedicated servers both connect to it with the game's own GC protocol: it owns
  parties, queues, matches, results, ratings and the war.
- **The pool**: every dedicated server that should host matches connects to the
  GC with a server key and reports itself empty. The GC hands it matches as
  they form, and only forms a match when a server is free for it. How many
  fronts the war has open at once follows from the pool and from how many
  people are online.

Nothing else is on the path of a match: no RCON from outside, no log parsing,
no agent beside the server, no booking service.

## Start the coordinator

1. Copy `../../services/gc/gc.example.json` to `gc.json` here and edit it:
   match groups, `server_keys` (one per community that runs servers),
   `auth.mode: "webapi"` for anything public, `war.enabled` for the campaign.
2. `docker compose up -d --build`

```dotenv
# Optional: one more server key, from the environment.
FRONTRESS_COORDINATOR_SECRET=generate-one-long-random-value
# Verifies players' Steam tickets (auth.mode "webapi").
STEAM_API_KEY=...
```

Expose `27100/tcp` to players and to game servers (put TLS in front of it in
production).

## Add a server to the pool

Any dedicated server can join: give it a key from `server_keys` and point it at
the GC (`game/tc2/cfg/frontress_server.cfg` is a complete example).

```text
tf_gc_server_url "https://gc.example.org"
tf_gc_server_key "<its key>"
tf_gc_server_region "eu"          // optional; a match group can ask for a region
tf_mm_servermode 1
tf_mm_strict 1
sv_password ""
tf_allow_server_hibernation 0
```

`$GC_URL` and `$GC_SECRET` work instead of the first two, for containers.

Set `GSLT` in the server's launch environment (the instance environment file
under systemd), then start it with `+map itemtest +ip 0.0.0.0 -port 27015`.
Use a token for the runtime `appID` in `tc2/steam.inf`. The SteamPipe tool
AppID identifies the download. For manual launches, an explicit
`+sv_setsteamaccount <token>` before `+map` takes precedence over `GSLT`.
Keep the token command out of `server.cfg` because it runs on every match map.

It connects, reports itself empty, and is handed matches as they form. A
community that runs servers for this GC gets its own key, so its servers can be
told apart and cut off on their own. `GET /v1/status` shows the pool
(`servers`, `free_servers`). A connected server is counted in `servers` immediately. It
only enters `free_servers` after reporting `EMPTY` with a connect address.
Look for `server readiness changed` in the GC log. If the pool stays empty,
check that the dedicated instance points to the same GC over HTTPS, has
`tf_mm_servermode 1`, has loaded its initial map, and advertises a public UDP
address. If it goes offline and comes back under a different SteamID, also
check its Steam game server login and process restart logs.

| Entry point | Selection and server settings |
| --- | --- |
| Casual Frontline | Group 7. The coordinator uses the group's population, map pool, backfill and region settings. The game executes `server_casual.cfg` and the map-type win conditions. |
| War | The War screen queues group 7 with a front preference. For groups selected by `war.match_groups`, the campaign picks the map, teams, capacity and conditions. Players need a faction and each party must share a faction. The server uses the group's cfg, then applies `frontress_cond_*.cfg` for the battle. |
| Ranked | Group 2. The coordinator forms a fixed roster, and the server executes `server_competitive.cfg` with the competitive map-type rules. |
| Accept or rejoin | The lobby becomes joinable when the assigned server reports the target map active. The client connects to the address advertised by the server. A reconnect can use an active roster reservation. |
| Community or direct connect | A standalone instance can use `tf_mm_servermode 0`. In the pool, `tf_mm_strict 1` limits connections to the roster. `0` allows ad-hoc joins and lists the server, and `2` allows ad-hoc joins but hides it. |
| Local training | Create Server opens a listen server using the local map and `lservercfgfile`. Only dedicated instances can join the pool. |

Maps marked as custom modes can select `server_custom.cfg`. Pooled servers
select their cfg in the game DLL. The GC retains the legacy `server_config`
key for compatibility. A lobby from the authenticated Frontress session is
sufficient to run a managed match with `tf_mm_trusted 0`.
