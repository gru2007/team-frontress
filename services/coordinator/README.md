# Team Frontress coordinator

A small Go service that runs the queue, forms matches, reserves a game server
for each one and tells the clients where to connect.

It is deliberately the *only* server-side component. The party, the invites and
the party chat are Steam lobbies, handled entirely in the game client — see
[`docs/MATCHMAKING.md`](../../docs/MATCHMAKING.md).

## Running it

```bash
cd services/coordinator
cp coordinator.example.json coordinator.json   # then edit it
go run ./cmd/coordinator -config coordinator.json
```

`go run ./cmd/coordinator -print-config` writes an annotated default to stdout.

Point the game at it with `tf_mm_coordinator "http://host:27100"`.

### Requirements

Go 1.24+. Nothing else — no database, no Redis. State is in memory, except the
war's event log, which is a file.

That is a deliberate limit: a restart loses the queue and the live matches. For
a community that fits on one server this is the right trade; if it stops being
right, the seam to add persistence is `internal/mm`.

## Configuration

Everything lives in one JSON file. The parts that matter:

### Match groups

```json
{ "match_group": 7, "name": "Casual Frontline", "mode": "frontline",
  "enabled": true, "min_players": 4, "ideal_players": 12, "max_players": 24,
  "patient_secs": 60, "backfill_secs": 0,
  "server_config": "frontress_match", "maps": ["koth_product_final"] }
```

`match_group` is TF2's own `ETFMatchGroup` (7 = Casual 12v12, 2 = Ladder 6v6).

`mode` is what the coordinator is allowed to do with the match, not a game mode:

| | |
| --- | --- |
| `frontline` | the open queue. The match keeps taking players while it runs |
| `ranked` | a roster is formed once and left alone |

Both report their result to the war identically. Scoring is not built.

The three player counts are the small-population story:

| | |
| --- | --- |
| `ideal_players` | what the coordinator forms when it can |
| `patient_secs` | how long it holds out for that |
| `min_players` | what it settles for afterwards |

Four people queueing for Casual with `min_players: 4` get a 3v3 — sorry, a 2v2 —
a minute later, instead of waiting for a twelfth that is not coming. Set
`patient_secs: 0` to always form the smallest legal match immediately.

`min_players` and `max_players` must both be even: teams are equal.

### Restrictions

Who may queue for a group, as opposed to how the group behaves. An absent
`restrictions` block is the open queue, which is what casual wants:

```json
"restrictions": {
  "max_party_size": 3,
  "min_matches_played": 5,
  "abandon_cooldown_mins": 30,
  "max_abandons": 5,
  "require_verified_auth": true
}
```

| | |
| --- | --- |
| `max_party_size` | the biggest party that may queue together. `1` is solo queue |
| `min_party_size` | for a group meant to be queued as a full team |
| `min_matches_played` | finished matches, in any group, before this one opens |
| `abandon_cooldown_mins` | how long someone who walked out waits before requeueing |
| `max_abandons` | abandons before the group closes for them entirely |
| `require_verified_auth` | refuses the group unless `auth.mode` is `webapi` |
| `allowed_steam_ids` | an allow list: a closed test, a league, an invite ladder |
| `banned_steam_ids` | a ban list. Per group, because a casual ban and a ranked ban are different punishments |

A refused queue is answered with **403** and the reason, in words meant for the
player: "you need 5 finished matches for Ranked 6v6 and have 2". Anything
unsatisfiable is refused at startup instead — `require_verified_auth` with
`auth.mode: "dev"` will not boot, rather than silently letting anybody in.

### Player records

The history the restrictions above read:

```json
"players": { "file": "players.jsonl", "abandon_grace_secs": 300 }
```

It is an append-only JSONL log folded on startup, the same shape as the war's.
An empty `file` keeps the records in memory, which a restart forgets — fine on
a LAN, and honest about what it loses.

Matches played, won, lost and abandoned are written when a match ends. An
abandon is *the server said who was there, and this player was not*, which
needs an agent reporting for the server; a result that names nobody credits the
whole roster rather than branding all of it. Matches that ended inside
`abandon_grace_secs` brand nobody either: a server that died in its first
minute is not twelve people walking out.

### Filling a match while it runs

A frontline match keeps taking players after it starts, which is the difference
between one full server and three quarter-empty ones at a peak of thirty-odd
players. One rule keeps it balanced: a party may only join a team with room
under half the match, so neither side can grow past half.

| | |
| --- | --- |
| `backfill_secs: 0` | fill for as long as the match runs (default) |
| `backfill_secs: 600` | stop accepting after ten minutes |
| `backfill_secs: -1` | never backfill, even in frontline mode |

Parties are never split to make a backfill fit. A party that does not fit waits
for a match that has room for all of it.

### Auth

`"auth": { "mode": "dev" }` believes whatever SteamID the client claims. It is
for a LAN or a laptop.

`"auth": { "mode": "webapi", "steam_api_key": "...", "app_id": 5147520 }`
verifies the client's Steam auth session ticket against
`ISteamUserAuth/AuthenticateUserTicket`. This is the only mode that produces an
identity anything should be enforced on.

The game is published under more than one AppID -- the playtest `5147520` and
the main app `5147380` -- and a ticket is only good for the app its client is
running as. `"app_ids": [5147380]` lists the rest; a ticket is offered to each
app in turn until one recognises it, and the app that answered is tried first
next time, so serving both costs one extra call rather than one per ticket.
Leave `app_ids` out if only one app is in use.

### Server pool

Providers are tried in order, so a fallback is just a second entry.

**`static`** — servers you run, driven over RCON:

```json
{ "kind": "static", "region": "eu", "servers": [
    { "name": "eu1", "connect": "10.0.0.1:27015", "rcon": "secret",
      "stv": "10.0.0.1:27020" } ] }
```

`stv` is reported to players, never configured: SourceTV has to be enabled when
the server starts. See
[`frontress_server.cfg`](../../game/tc2/cfg/frontress_server.cfg).

**`registered`** — servers that register themselves with `POST /v1/gs/register`
and keep heartbeating. Requires the top-level `secret`.

**`serveme`** — reservations from the [Frontress serveme fork](https://github.com/gru2007/serveme-frontress):

```json
{ "kind": "serveme", "region": "eu",
  "base_url": "https://serveme.example.org", "api_key": "...",
  "reserve_mins": 120, "prefer_docker": true }
```

It follows serveme's documented three-step flow (`reservations/new` →
`find_servers` → `reservations`) and ends the reservation when the match is
over. Reservations are ephemeral: nothing is returned to a free list.

Get the API key from the site with `bin/rails frontress:coordinator_key`, which
also puts that user in the Trusted API group — ranked reservations are refused
from anybody else.

Four things are specific to the fork, and all four exist because its servers
are containers started per reservation rather than machines standing by:

- **`prefer_docker`** picks a container host over a bare-metal server. serveme
  numbers container hosts from 1e9, which is what this reads;
- the reservation carries the **match's own password, first map and ruleset**,
  so the container boots configured for the match instead of being
  reconfigured after the fact. Two passwords for one server is one too many;
- `Acquire` **waits for the reservation to be `Ready`** before handing the
  server over. A container takes half a minute to come up, and RCON to an
  address that is not listening yet would be read as a broken server and cost
  the parties their match. `ready_timeout_secs` bounds the wait; by default it
  is the caller's deadline, `pool.boot_deadline_secs`;
- the reservation carries `match_id` and `match_mode`, so the container starts
  the agent that reports the result.

Note the SDR case. When a reservation comes back with `sdr_ip`/`sdr_port`, the
address players connect to is not the address we RCON. The pool keeps both and
`pool.RCONAddr` picks the right one.

### The war

Off by default. See the [strategic layer](#the-strategic-layer) below.

## What a match looks like end to end

```text
client            coordinator                pool / server
  |  POST /v1/queue    |
  |------------------->|  ticket, searching
  |  GET  /v1/queue/id |
  |------------------->|  (still searching)
  |                    |  enough players -> form match
  |                    |  Acquire ------------------->  reserve
  |                    |  Setup   ------------------->  RCON: password,
  |                    |                                sv_tags, exec, changelevel
  |  GET  /v1/queue/id |
  |------------------->|  assigned + connect + password
  |  connect ---------------------------------------->  play
  |                    |  <---- POST /v1/gs/result ----  (when the game reports)
  |                    |  Release ------------------->  end reservation
```

A match with no reported result still ends: an empty server for `idle_end_secs`,
or `max_match_secs` elapsed, and the server goes back. That is the fallback,
not the plan — see the agent below.

The match id reaches the server as `sv_tags` `tfmm:<id>`, which is a stock
convar. A server-side agent can read it back without a custom protocol.

The coordinator sets `sv_password`, `sv_tags`, `maxplayers`,
`tf_match_emulation` and `tf_mm_trusted` itself and then execs the group's
`server_config` before changing the map. Anything the server needs every match belongs in that config
— see [`frontress_match.cfg`](../../game/tc2/cfg/frontress_match.cfg). Do not
set `sv_password` there; it would overwrite the match password.

### Official-match status

`tf_match_emulation` is how a pooled server becomes an *official match* rather
than a community server that happens to have the right twelve people on it.
It is what makes `CTFGameRules` report a match group, which turns on the match
HUD, the match summary at game over, and the tournament/ready-up handling a
group's ruleset asks for.

The game knows two values — `1` is Casual 12v12, `2` is Ladder 6v6 — so the
coordinator derives it from the group's `match_group` and sends it with the
rest of the per-match setup. `match_emulation` on a match group overrides that,
including to `0` for a group the game has no match description for. Teardown
sets it back to `0`, so a server handed back to the pool does not show a match
HUD to whoever lands on it next.

### Handing the match to the server

If the server runs our game DLL it gets the match itself, not just a map change:

```
tf_mm_match_begin <match_id> <group> <map> <server_cfg> <fallback_password> <steamid:team,...>
```

That builds a real lobby object in the server's own shared object cache, which
is what makes `CTFGCServerSystem` create a `CMatchInfo` — the roster gate, team
assignment, abandon tracking, the match HUD, the match summary and the result
the game reports all hang off it. It changes the map from the lobby, which is
why nothing sends `changelevel` afterwards, and it takes `sv_password` off,
because from that point the roster is the door. See
[`MATCHMAKING.md`](../../docs/MATCHMAKING.md#the-other-half-the-game-server).

A server without the command answers "Unknown command" and the coordinator
changes the map itself, so an unmodified dedicated server still runs matches —
as a passworded community server with none of the above.

Getting into a match that is already running goes the same way:

```
tf_mm_match_add <match_id> <steamid:team,...>
```

Backfill and standby both use it, and both wait for it: a ticket seated in a
running match stays `searching` from the client's point of view until the server
has been told to expect them. If it cannot be told, the seat is given back and
the party returns to the queue rather than being sent at a door that will not
open.

`tf_mm_trusted` goes with it. That is the game's own official-server flag —
on Valve's build their backend checks it, and there is no backend here, so it
is ours to grant. It is `FCVAR_NOTIFY`, so it reaches clients, and
`CServerGameDLL::GetServerBrowserGameData` publishes it as server browser game
data. Server-side it makes a returning player go back to the team they left
(`CTFPlayer::ShouldForceAutoTeam`) and stops a spectator slot being used to
unbalance the sides.

Both are granted per match on purpose: nothing about a server is permanently
"official", and a server that leaves the pool stops being one.

### Map pools

A match group says which **modes** it plays and gets every stock map of those
modes, instead of listing a hundred and thirty map names:

```json
"modes": ["attack_defend", "cp", "koth", "payload", "plr", "ctf", "misc"],
"maps": []
```

Those seven are what Valve's own casual map picker offers. `arena`,
`passtime`, `mannpower`, `halloween` and `christmas` are available and off by
default. `maps` adds individual maps on top — a community map the table does
not know about is allowed, it just has to be on the server.

The table lives in [`internal/maps`](internal/maps/vanilla.go) and is lifted
from the game's own `items_game.txt`: the `rolling_match_tags` on each entry of
`master_maps_list`, which is exactly what the casual panel reads. Nothing in it
is invented; adding a map means the game has to ship it.

## HTTP API

Everything is JSON. SteamIDs are decimal **strings**, never numbers: a
SteamID64 does not survive a JavaScript `Number`.

| | |
| --- | --- |
| `GET /v1/status` | population, queue depths, match groups, war fronts. Public |
| `POST /v1/queue` | queue a party. Returns `{ticket_id, poll_after_ms}`. `standby_match_id` asks for a seat in one running match instead |
| `GET /v1/queue/{id}` | poll. `searching` / `assigned` / `cancelled` / `expired` / `failed` |
| `DELETE /v1/queue/{id}` | leave the queue |
| `GET /v1/player/{steamid}` | matches, wins, XP and level. Public |
| `POST /v1/gs/register` | a server joins the pool (needs `secret`) |
| `POST /v1/gs/heartbeat` | keeps it in the pool, reports player count |
| `POST /v1/gs/result` | a finished match (needs `secret`) |

A queue refused by a group's restrictions answers **403** with the reason in
`error`, which is player-facing text. A malformed request is still 400.

Polling is the whole client protocol. A ticket that stops being polled expires,
which is also how a client that crashed leaves the queue. A ticket still *in*
queue gets a shorter deadline than `ticket_ttl_secs` -- six missed polls, and
never under fifteen seconds -- because a searching client has nothing else to
do, and until its ticket goes the queue counts a player who is not there and
can form a match around them. A ticket holding an assignment keeps the full
`ticket_ttl_secs`: that client is loading a map and genuinely does go quiet.

A poll answers `searching` while a match is forming *and* while a formed match
is waiting for a server -- there is nothing to connect to in either case. What
separates them is `detail`, a player-facing line the client shows under the
queue ("Match found. Waiting for a free server. ..."). Without it a pool with
nothing free is indistinguishable from a queue that is simply short of players.

```bash
curl -s localhost:27100/v1/status | jq
curl -s -X POST localhost:27100/v1/queue -H 'Content-Type: application/json' -d '{
  "match_group": 7,
  "leader": "76561198000000001",
  "players": [{"steam_id":"76561198000000001","name":"me"}],
  "maps": ["koth_product_final"]
}'
```

## The agent on the game server

`cmd/greyline-agent` runs next to a dedicated server and tells the coordinator
what is happening on it. The container image in the serveme fork starts it
automatically when the reservation is a match; on a static server, run it
yourself:

```bash
greyline-agent -coordinator http://gc:27100 -secret "$SECRET" \
               -rcon 127.0.0.1:27015 -rcon-password "$RCON"
```

Without it the coordinator can only end a match the way it always could: an
empty server, or the clock. Which works, and means a finished match holds its
server for another five minutes, nobody's record is written, and the war never
hears who won.

It uses two stock server features and no game-side plugin:

- the match id is in `sv_tags` as `tfmm:<id>`, which the coordinator put there
  over RCON when it set the match up;
- `logaddress_add` sends the console log to the agent, which is how it sees
  `Game_Over` and the final scores.

Who was in the match comes from RCON `status` at the moment it ended, not from
the log: the roster the coordinator handed out is who was *supposed* to play,
and the difference between the two is exactly what an abandon is.

| | |
| --- | --- |
| `-coordinator` / `GC_URL` | the coordinator's base URL |
| `-secret` / `GC_SECRET` | its shared server secret |
| `-rcon` / `RCON_ADDR` | the game server, default `127.0.0.1:27015` |
| `-rcon-password` / `RCON_PASSWORD` | required: without RCON there is nothing to report |
| `-connect` / `SERVER_CONNECT` | the address players use, for the heartbeat |
| `-log-listen` / `LOG_LISTEN` | where to receive the console log, default `127.0.0.1:27115` |

## The strategic layer

`internal/war` is stage-three groundwork, off unless `war.enabled`. Two rules
hold it together:

- **the theater is data.** Nodes, adjacency, stage plans and maps come from a
  file you write (`theater.example.json`). Nothing in the code invents a place,
  a mission or a campaign;
- **the event log is the truth.** State is a fold over an append-only JSONL
  file, so a restart resumes the same campaign. Delete the file to start a new
  one.

A node is not a map. It holds an ordered plan, and each stage names a **kind**
of battle rather than one map:

```text
   SKIRMISH   ->   BREAKTHROUGH   ->   ADVANCE   ->   NODE CAPTURED, front moves on
   arena/koth      symmetric 5CP       payload
   2-12 players    8-24 players        8-24 players
```

Where each kind can be fought is `battlefields` in the theater file, keyed by
the stage's kind:

```json
"battlefields": {
  "skirmish": [
    { "map": "arena_well", "mode": "arena", "min_players": 2, "ideal_players": 6, "max_players": 12 },
    { "map": "koth_viaduct", "mode": "koth", "min_players": 4, "ideal_players": 12, "max_players": 24 }
  ]
}
```

The player counts are the point. A stage's pool is what tells the matchmaker how
big this battle may be, so it widens the group's own thresholds to cover the
pool and then, once it knows how many seats it filled, picks a battlefield that
size actually fits. That is how a node stays winnable at 2v2 without the whole
queue being configured for 2v2 — give it a `skirmish` stage and the arena maps
are what two people get.

Which map inside the pool is the matchmaker's call, not the war's: it already
knows who is queued and which maps they voted for, and it avoids what was just
played. A stage may still pin one map with `"map"`, which skips all of that.

The attacker clearing the last stage takes the node and the front advances to a
neighbour still held by the defender. The attacker losing the first stage breaks
the offensive and the front closes. Losing a later stage pushes it back one.

How many fronts are open follows the population (`fronts_by_population`), so
eight people are not scattered across six of them.

## Progression

`internal/players` already remembers matches, wins and abandons because the
queue restrictions need them. `progress.go` turns that record into the two
numbers the stock menu has a place for:

| | |
| --- | --- |
| a finished match | +100 XP |
| a win | +50 XP on top |
| an abandon | -150 XP, floored at zero |
| a level | 1000 XP, flat, capped at 150 |

Deliberately arithmetic. There is no skill model here and no attempt at one: an
untuned rating system looks authoritative and is not, and matchmaking does not
read this. `GET /v1/player/{steamid}` serves it, and the client publishes it as
a `CTFRatingData` shared object so the badge on the main menu — which reads XP
out of the SO cache and got nothing before — shows it.

## Tests

```bash
make test     # go test ./...
make race     # -race -count=2
```

The tests are written against behaviour that would be expensive to rediscover by
hand: that a party is never split across teams, that a settled match still
happens after `patient_secs`, that a late arrival joins the running match rather
than starting a second one, that backfilling cannot unbalance or overfill a
team, that a ranked match refuses to grow, that a server we failed to set up
goes back to the pool, that a broken provider does not hide a working one, that
the war replays identically from its log, and that an unparseable RCON `status`
is not read as an empty server.

`internal/api` also drives the real HTTP surface against the real matchmaker end
to end: two clients queue, poll, and are told to connect to the same server on
opposite teams.

The restriction tests are worth knowing about specifically: that a party over
the cap is refused with a reason a player can read, that a cooldown expires on
its own, that a ban refuses the whole party rather than splitting it, that a
result naming who played turns the rest of the roster into abandons — and that
a result naming nobody brands nobody, because that is a server without an
agent, not an empty one.
