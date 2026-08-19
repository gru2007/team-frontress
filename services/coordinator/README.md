# GREYLINE FRONTRESS — Game Coordinator

The coordinator owns the war, the queue and the server pool. It decides which
battle is worth playing next, hands that battle to a dedicated game server, and
records what the result did to the front.

This file describes what the coordinator does. For what is finished and what is
not — across the coordinator, the game code and the menu —
see [`docs/GREYLINE-STATUS.md`](../../docs/GREYLINE-STATUS.md).

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
| `POST /api/v1/client/host-register` | accept a `host_offer`: register your own machine into the server pool for exactly this battle |
| `POST /api/v1/client/confirm-result` | corroborate a P2P host's reported result |

Poll events: `queue`, `match_state`, `match_ready` (connect address, password,
side, in-game team, front, stage), `match_over` (result plus the war update),
`world` (the map moved), `contract_offer`, `host_offer` (see below), `notice`.

### When nothing dedicated is free: electing a player to host

`deploy` may carry `party_id`: everyone who sends the same (client-chosen)
value lands in the same battle, on the same side, room permitting — the one
thing a per-player server search can never express and a shared queue can.

If a formed roster has no free dedicated server, the coordinator scores the
roster with the same host-election logic the original P2P prototype used
(`internal/hostelect`: upload, CPU, memory, hosting history) and, if anyone
qualifies, sends that one player a `host_offer` instead of aborting the match.
Accepting means calling `host-register` within its `accept_deadline_s`, which
registers that client's own machine into the pool — under a token scoped to
this one battle, authenticated by the player's ordinary session, never the
pool key — and hands it the assignment exactly like a dedicated agent would
receive it. From there the protocol is identical to a dedicated server's: the
game's own listen-server code applies the hosting contract, loads the map, and
reports `ready`/`live`/`result` over the same `/api/v1/servers/*` routes.

Two differences from a dedicated server, both about trust:

- A player-hosted server does not know its own address at registration —
  its engine has not finished allocating a Steam FakeIP yet — so it patches it
  in with `POST /api/v1/servers/address` once `TryPublishAddress` succeeds.
  `servers/state ready` refuses to proceed without one, rather than sending a
  roster to an empty connect string.
- A player-hosted server's reported result is **not** recorded on its own.
  It is held until enough of the non-host roster corroborates it with
  `client/confirm-result` — `match.ResultQuorum` of them, rounded up, minimum
  one — because nothing stops an elected host from reporting a win for
  themselves the way a dedicated server's operator has no reason to.

Two things keep an elected host that cannot deliver from taking the front
with it. A host that is still loading its map is not judged by the heartbeat
rule at all — the heartbeats come from the game's own menu page, inside the
process that is busy loading the level, so silence during boot says nothing
about whether the host is alive; `timing.host_boot_deadline` bounds that phase
instead. And a host that fails — an offer it never answered, a server that
never came up — sits out `timing.host_failure_cooldown` before it can be
elected again, because election otherwise picks the same unreachable machine
on the very next tick and the front does nothing but form and abort the same
battle while everyone else on it waits.

A dedicated server is always preferred over an idle player-hosted one when
both are free (`pool.Reserve`), so a P2P host is the fallback a small,
money-constrained test population needs, not the default — see
[`internal/legacy/README.md`](internal/legacy/README.md) for why the earlier,
fully P2P prototype was retired and what this design keeps from it.

### A battle is not sealed when it forms

A formed roster is a starting roster. Every scheduling pass, before it forms
anything new, the coordinator puts queued players into battles this front
already has — running ones and ones still standing up alike — up to what the
battlefield itself holds. Somebody who deploys during the thirty seconds a map
takes to load joins that battle rather than waiting for a second one to fill up
next to it, which is what "one place to fight" has to mean in practice.

A player seated in a battle that is already up gets `match_ready` and a
`roster` command goes to its server, so it can put them on the side the war
put them on. A player seated in one that is still booting gets `match_state`
and no connect address — they are sent with everybody else when the server
reports ready, and the assignment it boots with already carries them.

Latecomers only ever fill towards balance, and only cross to the other side
under a contract they already agreed to, so a battle can never grow more
lopsided than it already was.

This is why an assignment's `max_players` is the battlefield's capacity rather
than the size of the roster that opened it: a server told to hold exactly the
four people who formed the battle has nowhere to put the fifth.

On the war map, a live battle's `players` is what the server last reported and
`roster_size` is how many the coordinator sent. They are different numbers,
and while people are still loading in the difference is the interesting part.

### A battle does not start until the people in it are there

An assignment carries `min_players` — the roster it was formed out of — and
`muster_timeout_s`. The game server holds the round in the engine's own
WAITING FOR PLAYERS period until that many humans are connected, then starts
(`greyline_min_players` / `greyline_muster_timeout`, see
`game/server/greyline/greyline_muster.cpp`). Without it a map that takes half
a minute to load begins the moment it is up, and on a small population most of
a battle is fought by whoever loaded fastest while the rest are still on a
loading screen — and the war counts that result.

The timeout is the escape: a roster member who never arrives costs a wait, not
the battle.

### Only the roster gets in, and only where the war put them

Two things decide it and neither of them is Steam's friend list: a battle turns
`sv_friends_only` off before it loads the map, because the people the war sends
to a battle are mercenaries on the same front, not each other's friends, and a
listen server left on friends-only refuses all of them with nothing but "You
are not permitted to join this server!" to go on.

The battle password is a shared secret handed to a dozen clients over HTTP, and
a shared secret leaks. The roster is the list of accounts the coordinator
actually assigned to this battle, which is the better question, so the game
server can ask that one instead: `greyline_roster_gate` refuses the connection
outright rather than kicking a stranger who has already spawned.

It is only asked when the answer means anything. An assignment carries
`verified_identities`, true only under `auth.mode=webapi`, and the gate follows
it. Under dev auth a client states whichever SteamID it likes, so the roster
can name accounts the game server will never see — and a gate on that turns the
whole battle away rather than a stranger. The same mismatch stops team
assignment working, quietly, which is the more expensive half of it.

Inside, the team is not a choice. A roster member is seated on the team the war
gave them the moment they arrive — before the team menu, not a second after it
— and `jointeam` for any other team is refused with a line saying why. The war
balanced those two sides around its own allegiances and, on a directional map,
around which team the map lets attack; a player switching out of that is not
expressing a preference, they are breaking the battle the war is counting.

That last constraint has a visible cost: the attacking side always wears BLU,
so a RED offensive is fought as the BLU team. `greyline_uniform_by_war_side`
puts the war's colours back on the player models for exactly those battles —
bodies, weapons, cosmetics, viewmodels and buildings alike, because redrawing
a player and not what they are holding reads as a bug rather than a decision —
per team and never per player, because the colour of a body is the answer to
"do I shoot this" and that answer cannot depend on a campaign. It is on: the
HUD and the map still say BLU, and reading past that once is easier than being
told all evening that you are BLU while fighting for RED. The briefing knows
which of the two the player is looking at and says the matching sentence. See
`game/shared/greyline/greyline_uniform.h`.

### Allegiance is not a choice of role

The uniform is fixed — the attacking side always wears BLU — which invites the
objection that RED therefore always defends, that people will pick a side and
spend the evening doing one job, and that TF2's classes make defending the
easier one.

Half of it is true and it is the half that does not matter. The *in-game team*
is fixed by the maps. The *war side* is not: which belligerent is on the
offensive is a property of the front, and a front exists wherever one side owns
ground next to the other's. `candidates()` generates both directions by the
same rule and scores them with terms that never name a side — distance to the
defender's headquarters, momentum, mobilization. Measured over sixty coin-flip
campaigns on the shipped theater, RED is the attacker in 50.2% of battles
(`TestNeitherSideIsStructurallyTheAttacker`, which also fails if that ever
stops being true).

Within one campaign it is lopsided — the middle half of campaigns run between
31% and 62% — but that is the war having a direction rather than a bias, and
defensive mobilization exists to turn it around when it goes too far.

What a player does need is to be able to tell which side they are on while
wearing the other one's colours, which is what the previous section is about.

### A battle is worth its size

A stage of an offensive is not cleared by winning a battle, it is cleared by
winning a stage's worth of battle. `war.BattleWeight` turns a headcount into a
fraction of a stage — linear up to `war.full_strength_players`, floored at
`war.min_battle_weight` — and `Front.Push` accumulates it, positive towards the
attacker and negative towards the defender, until a whole stage's worth moves
the front one step.

Without this a 1v1 between the two people who happened to be online moves an
offensive exactly as far as a 6v6 does, and the strategic layer becomes a
function of who pressed DEPLOY rather than of who fought. A battle at full
strength still clears a stage on its own; a thin one is worth what it was.

The weight is written into the battle event when it is recorded, not
recomputed on replay: retuning the rules must not rewrite what already
happened. An event with no weight is one from before battles were weighted and
counts as a whole stage, so old war logs replay to the state they produced.

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
| `POST /api/v1/servers/state` | `ready`, `live`, `failed` — `ready` needs a connect address on file, see below |
| `POST /api/v1/servers/address` | patch in `{connect_address}` — only a player-hosted server needs this; a dedicated one supplies it at register |
| `POST /api/v1/servers/result` | `{match_id, outcome, red_score, blu_score, players, duration_s}` |
| `POST /api/v1/servers/deregister` | leave the pool |

A server registered through `servers/register` and the pool key is trusted
infrastructure. One registered through `client/host-register` — a player's own
machine, elected because nothing dedicated was free — is not: see "When
nothing dedicated is free" above.

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
| `internal/pool` | The server registry and its command channel — dedicated and player-hosted alike |
| `internal/hostelect` | Scores a formed roster to elect a P2P host when nothing dedicated is free |
| `internal/httpapi` | Every route, for all three audiences |
| `internal/rcon` | Minimal Source RCON client, used by the agent |
| `internal/srclog` | Reads the game server's log stream |
| `internal/steam` | Auth ticket validation (`dev` and `webapi`) |
| `internal/legacy` | The retired peer-to-peer coordinator |
