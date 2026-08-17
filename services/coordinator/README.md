# GREYLINE FRONTRESS — Game Coordinator

The coordinator owns the war, the queue and the server pool. It decides which
battle is worth playing next, hands that battle to a dedicated game server, and
records what the result did to the front.

```
GAME CLIENT
    │  DEPLOY
    ▼
COORDINATOR ──────────── war engine ── "Foundry, stage 2 of 3: an ADVANCE"
    │                         ▲
    │ assignment              │ one battle, one event
    ▼                         │
SERVER POOL ── greyline-agent ┘  RCON + log stream → a stock srcds
```

Two rules the whole design rests on:

1. **A game server never touches war state.** It is told which map to load and
   who is on which team; it reports who won. Everything strategic happens here.
2. **One finished battle produces exactly one war event.** The event log is the
   world: replaying it rebuilds the same war, which is also the world recap, the
   audit trail and the restart story.

The peer-to-peer coordinator that came before this — where a player's own
machine was elected to host — is retired. See [`internal/legacy`](internal/legacy/README.md).

## Running it

```bash
export GREYLINE_GC_SECRET="$(head -c 32 /dev/urandom | base64)"
export GREYLINE_POOL_KEY="$(head -c 32 /dev/urandom | base64)"

make build
./bin/coordinator -config gc.example.json
```

The war starts itself: with no event log, the coordinator opens campaign 1 from
the theater's first opening and activates one front. Delete `war-events.jsonl`
to start a new war; keep it and the same war comes back after a restart.

| Flag / env | What it is |
| --- | --- |
| `-config` | Coordinator config (see `gc.example.json`, `gc.test.json`) |
| `-theater` / `GREYLINE_THEATER` | The node graph and its battlefields |
| `-war-log` / `GREYLINE_WAR_LOG` | The append-only war event log |
| `GREYLINE_POOL_KEY` | Shared secret game servers present to join the pool |
| `GREYLINE_GC_SECRET` | The coordinator's own signing key |
| `GREYLINE_LISTEN` | HTTP listen address |

## Joining a game server to the pool

Run the agent next to a dedicated server. It needs RCON and a UDP port to
receive the server's log on; it never needs an inbound connection from the
coordinator.

```bash
./bin/greyline-agent \
  -gc https://gc.example.org:27100 \
  -key "$GREYLINE_POOL_KEY" \
  -connect 203.0.113.10:27015 \
  -rcon 127.0.0.1:27015 -rcon-password "$RCON" \
  -log-listen 127.0.0.1:27500 \
  -idle-map ctf_2fort
```

What it does with a battle:

1. sets the battle password, the roster and the war briefing over RCON;
2. `changelevel` to the assigned map, and reports **ready** when it comes up;
3. reports **live** when the first player arrives;
4. reads `Round_Win`, `Team … final score` and `Game_Over` out of the log
   stream, and reports the result;
5. drops the password and returns to its idle map.

The agent reports the scoreline **in in-game teams**, exactly as the scoreboard
said it. The coordinator translates that back into war sides, because the
attacking side plays as BLU in every battle — see
[the war doc](../../docs/GREYLINE-WAR.md#the-attacking-side-always-wears-blu)
for why that is a rule rather than a per-map accident.

## HTTP API

Everything is JSON with bearer tokens. Clients and agents both long-poll, so the
coordinator never dials into anybody's network.

### Client

| | |
| --- | --- |
| `POST /api/v1/client/hello` | `{steam_id, ticket, name, side, region, client_version}` → session token |
| `POST /api/v1/client/deploy` | `{front_id?, accept_contract}` — empty `front_id` is the DEPLOY button |
| `POST /api/v1/client/cancel` | leave the queue |
| `POST /api/v1/client/side` | change allegiance between battles |
| `POST /api/v1/client/leave` | left the battle |
| `GET  /api/v1/client/poll?since=N&wait=25` | queue, assignment, result, world events |
| `GET  /api/v1/client/self` | current player, queue and battle — for a client that lost its stream |

Poll events: `queue`, `match_state`, `match_ready` (connect address, password,
side, in-game team, front, stage), `match_over` (result plus the war update),
`world` (the map moved), `contract_offer`, `notice`.

### War map — public, no session needed

| | |
| --- | --- |
| `GET /api/v1/world` | Whole snapshot: nodes, owners, fronts, live battles, population |
| `GET /api/v1/world/fronts` | Where you can deploy, with queue and battle counts |
| `GET /api/v1/world/timeline?since=N` | The war's history — this is the world recap |
| `GET /api/v1/status` | Campaign, population, pool health |

### Server pool — pool key, then a per-server token

| | |
| --- | --- |
| `POST /api/v1/servers/register` | `Authorization: Bearer <pool key>` → server id and token |
| `GET  /api/v1/servers/poll?wait=25` | long-poll for the next command (`assign`, `abort`, `idle`) |
| `POST /api/v1/servers/heartbeat` | status, current battle, map, players |
| `POST /api/v1/servers/state` | `ready`, `live`, `failed` |
| `POST /api/v1/servers/result` | `{match_id, outcome, red_score, blu_score, players, duration_s}` |
| `POST /api/v1/servers/deregister` | leave the pool |

The pool key is a production credential: anything holding it can report results
that move the war.

### Admin — its own listener, loopback by default

`GET /state`, `/world`, `/timeline`, `/servers`, `/players`, `/battles`;
`POST /servers/drain`. Set `pool.admin_key` if it is not on loopback.

## The client

There is no finished menu yet, and the MVP does not need one. The game's main
menu web panel loads a plain test page instead — the war map, the active fronts,
DEPLOY, and the jump into the battle — which lives in the game tree at
`game/tc2/loose/resource/html/greyline.html` and speaks only the public API
above. `greyline_menu_page` switches back to the stock menu.

The same file opens in a browser during development, which is why the API sends
`Access-Control-Allow-Origin: *`: the endpoints that need no token are the war
map, which is public on purpose, and everything else needs a bearer token the
page was handed explicitly.

## Testing it

[`docs/GREYLINE-MVP-TESTING.md`](../../docs/GREYLINE-MVP-TESTING.md) has four
levels, from `make race` to four people on a real server. The one to run most is
level 1 — a whole campaign in two minutes with no game involved at all:

```bash
./bin/coordinator -config gc.test.json &
./tools/greyline_sim.py --players 4 --battles 40
```

## The war

See [`docs/GREYLINE-WAR.md`](../../docs/GREYLINE-WAR.md) for the model, and
`theater.industrial.json` for the theater the MVP is fought over: seven nodes,
two headquarters, four battle profiles.

The short version:

- A **node** is a district, not a map. Foundry 17 is fought over on
  `cp_process_final`, `pl_badwater` and `cp_dustbowl` depending on the stage.
- A **front** is one side's offensive against one adjacent enemy node, fought
  over a **stage plan** — breakthrough, advance, assault. Winning clears a
  stage; losing pushes it back one; losing at the bottom breaks the offensive
  and usually opens the defender's counter-attack.
- Clearing the last stage **captures the node**, and the front physically moves
  along the graph to the next one.
- How many fronts are open at once follows the **population**: one until sixteen
  people are online, then one more per step. A small community fights in one
  place.
- Taking an enemy **headquarters** ends the campaign; after a short armistice
  the next one starts from a different opening.

## Layout

| Package | What it does |
| --- | --- |
| `internal/war` | The war: theater, staged fronts, campaigns, the event log and its reducer |
| `internal/mm` | Queue, battle formation, sides and contracts, match lifecycle |
| `internal/pool` | The dedicated server registry and its command channel |
| `internal/httpapi` | Every route, for all three audiences |
| `internal/rcon` | Minimal Source RCON client, used by the agent |
| `internal/srclog` | Reads the game server's log stream |
| `internal/steam` | Auth ticket validation (`dev` and `webapi`) |
| `internal/legacy` | The retired peer-to-peer coordinator |
