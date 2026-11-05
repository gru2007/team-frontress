# Team Frontress game coordinator

`frontress-gc` replaces Valve's game coordinator. The game client and the
dedicated server connect to it the way they connected to Valve's GC, with the
same protobuf messages, and the stock game code does the rest: the party panel,
the matchmaking dashboard, the "match found" dialog, `CTFGCServerSystem`,
`CMatchInfo`, the roster gate, abandons, the match summary.

```text
game client ─┐  GC protocol (tf_gcmessages.proto)
             ├─────────── frontress-gc ──────────── webhooks (other projects)
game servers ┘   over HTTP: session / send / recv
 (the pool)
```

Nothing in the game emulates a GC. The engine-side code is a transport
(`src/game/shared/tf/frontress_gc.cpp`, a few hundred lines): Valve's gcsdk
still contains the whole client half of the GC protocol, and the public SDK
only removed the pipe to Steam. That pipe now points here.

## How a match happens

1. **A client connects.** It sends a Steam web API ticket; the GC verifies it
   with Steam, opens a session, puts the player in a party of one and sends
   the party object into the player's shared object cache.
2. **Parties form.** Invites, join requests, leader changes, kicks and chat are
   the stock `k_EMsgGCParty_*` messages. The GC keeps the party and writes it
   into every member's cache.
3. **The leader queues.** The GC checks the group's restrictions and adds a
   queue entry to the party object; the dashboard shows "searching".
4. **A match forms.** Every tick the GC first seats late-join parties in
   running matches with room, then forms new matches -- only as many as there
   are free servers in the pool, so nobody is told "match found" with nowhere
   to play: parties are never split,
   teams are balanced by size and rating, the map is what the parties voted for
   (if `map_index` is configured), otherwise the least recently played.
5. **A server takes it.** A free server from the pool (the dedicated servers
   connected with a server key) is given the lobby in state `SERVERSETUP`.
   The server's own `CTFGCServerSystem` builds the match and changes level.
6. **Players connect.** When the server's heartbeat says it is on the map, the
   lobby goes `RUN` with the server's address; the stock "match found" dialog
   connects the players. The server admits the lobby's roster and nobody else.
7. **It ends.** The server reports `CMsgGC_Match_Result` (records, rating, XP,
   the war) and `CMsgGameServerKickingLobby`; the GC destroys the lobby and
   the server rests a moment before it takes the next one.

Every step has a deadline (`timing` in the config). A match whose server never
arrives is abandoned and its parties go back to the front of the queue; a match
whose server disappears, that nobody joins, or that runs too long is ended. No
state waits forever.

## Reliability

- **One owner.** All GC state belongs to one goroutine. HTTP handlers decode
  and hand messages to it; slow work (a webhook) runs
  elsewhere and posts its result back. There are no locks around game state.
- **Exactly-once messages.** Every message is numbered per session in both
  directions. The GC keeps outbound messages until the peer acknowledges them,
  so a lost response is resent; it drops inbound messages it has already seen,
  so a retried request is not acted on twice.
- **Restarts.** Running matches are saved (`data/matches.json`, written
  atomically) and restored. Clients and servers reconnect on their own and are
  sent everything they need again. Parties and queues are rebuilt by the
  clients within seconds.
- **The protocol cannot drift.** `internal/gcpb` is generated from the game's
  own `.proto` files (`proto/gen.sh`); CI regenerates it and fails if it
  differs.

## The transport

| | |
| --- | --- |
| `POST /gc/v1/session` | form: `protocol`, `kind` (`client`/`server`), `steam_id`; a client sends `ticket`, a server `key`, `connect`, `match_id`, `region`, `name`. Answers `key=value` lines: `session`, `poll_ms`, `name` |
| `POST /gc/v1/send` | header `X-GC-Session`; body: frames `[u64 seq][u32 type][u32 len][message]` |
| `GET /gc/v1/recv?ack=N` | header `X-GC-Session`; long-poll. Answers frames in the same format, or 204 |

A message is exactly the bytes gcsdk builds: `[u32 emsg|0x80000000][u32 header
length][CMsgProtoBufHeader][body]`. A reply carries the request's
`job_id_source` as its `job_id_target`, which is how gcsdk wakes the waiting
job.

Public JSON: `GET /v1/status` (what the main menu shows), `GET
/v1/player/{steamid}` (record, XP, level, rating), `GET /healthz`.

Match history for the tf2pickup portal: `GET /v1/matches` and
`GET /v1/matches/{match_id}`. The list accepts `offset` (0..1000000), `limit`
(1..100, default 20), `steam_id` and `state` (`setup`, `run`, `ended`). IDs are
decimal strings. Records retain all roster participants, including leavers,
and the reported result after the lobby is destroyed. They contain no server
keys, GC sessions or private connection addresses.

With `data_dir` set, the archive lives in `history/<match_id>.json`, separately
from the live recovery snapshot `matches.json`. Back up both. The archive starts
with this version; old finished matches cannot be reconstructed from player
totals. Existing tf2pickup Mongo history remains separate. A restored match is
reconciled with its archive; an archive entry with no live snapshot is closed
with an explicit restart reason. Archive write failures are logged as errors;
alert on them. The initial archive is indexed in memory and needs retention or
a database index before a large deployment.

## Running it

```bash
cd services/gc
cp gc.example.json gc.json   # then edit it
go run ./cmd/frontress-gc -config gc.json
```

Point the game client at it with `tf_gc_url "http://host:27100"`. `tf_gc_status`
prints the connection state, `tf_gc_debug 1` logs every message.

A dedicated server needs, in its `server.cfg`:

```text
tf_gc_server_url "http://host:27100"
tf_gc_server_key "<a key from server_keys>"
tf_mm_servermode 1
tf_mm_strict 1
sv_password ""
tf_allow_server_hibernation 0
```

In a container the URL and key can come from `GC_URL` and `GC_SECRET`
instead, which the game server reads by itself.

## Configuration

See `gc.example.json`. The parts that matter:

- **`auth`** — `dev` believes the client; `webapi` verifies the ticket with
  Steam and is the only mode for anything public.
- **`server_keys`** — who may host matches. One per community that runs
  servers, so they can be told apart (`community`) and revoked on their own.
- **`match_groups`** — `match_group` is TF2's `ETFMatchGroup` (7 is Casual,
  2 is Ladder 6v6). `min_players` / `ideal_players` / `max_players` and
  `patient_secs` are the small-population knobs: hold out for the ideal size
  that long, then settle for the minimum. `backfill_secs` decides whether a
  running match takes late joiners. `restrictions` are who may queue: party
  size, finished matches, abandon cooldown and limit, allow and ban lists.
- **`war`** — the strategic layer (`internal/war`). The theater is data; the
  event log is the truth. How many fronts are open follows from the people
  online (`fronts_by_population`) and is capped by the server pool: a front
  needs somewhere to be fought.
- **`chat`** — the menu's global, clan and party chat (`internal/gc/social.go`):
  how many lines a channel keeps for latecomers, who is muted, which words
  are starred out. Every player also gets a token bucket (a burst of five,
  then one line every two seconds).
- **`support`** — the "support the developers" tab: one-off donations and a
  monthly subscription through Steam microtransactions (`internal/support`).
  It needs a publisher web API key with the microtransaction permission
  (`publisher_key` or `$STEAM_PUBLISHER_KEY`; it never leaves the GC). Keep
  `sandbox` on (`ISteamMicroTxnSandbox`, no money moves) until the store is
  live; `fake` approves everything without Steam and is for working on the
  screens with `auth.mode` `dev` only. `prices` override the built-in tiers
  per currency, in cents.
- **`webhooks`** — other projects subscribe here.
- **`map_index`** — map name → `master_maps_list` def index, so the casual
  map picker's votes are honoured.

## Other community projects

The GC owns everything a match needs; other projects hang off it rather than
sit in its path.

- **Servers.** A community runs servers by giving them a server key. They join
  the pool and get matches like any other.
- **Webhooks.** `match_started`, `match_result` and `match_ended` are POSTed as
  JSON, signed with `X-Frontress-Signature: sha256=<hmac>` when the hook has a
  secret, and retried with backoff. A pickup site, a stats page or a Discord
  bot builds on these. A hook that is down never holds anything up.
- **Public API.** `/v1/status`, `/v1/player/{steamid}` and `/v1/clans` (the
  clan leaderboard) are open and CORS enabled.

## Social, clans, support

One pair of messages carries the menu's social side: the game sends ops
(51010) and the GC sends events (51011), JSON in a `CMsgPartyChatMsg`. On
connect the game says `hello` (its name and language) and gets the chat so
far, the online list, its clan and its supporter badge.

- **Who is online** is pushed every few seconds when it changes: totals (in
  the menu, searching, fighting, looking for a squad) and the first 48
  players, squad-seekers first.
- **The feed**: war battles and captured sectors, new clans, clan levels and
  contracts, and (unless they asked not to be named) supporters show up in
  global chat as system lines the game translates.
- **Clans** (`internal/clans`, `clans.jsonl`): members earn clan XP by
  playing, half again when two or more of them were in the match. Levels
  unlock perks the GC enforces: +10% (then +20%) match XP beside a clanmate,
  the clan's banner on the war map over the sector it won most battles in, a
  weekly clan contract that pays everyone who helped, a clan order (+50 XP per
  battle in the chosen operation), a bigger roster, a gold tag, and the war
  feed naming the clan when its sector falls.
- **Support** (`internal/support`, `support-<appid>-<realm>.jsonl`): InitTxn opens the order
  and Steam asks the player in the overlay; the game passes Steam's answer
  back and the GC finalizes it. A monthly tier is the same order with a
  Steam-billed agreement (`billingtype` Steam, `period` Month); players cancel
  it in the tab or in their Steam account. Donations buy a badge and a place
  on the thank-you wall, nothing that matters in a fight.

From tf2pickup the GC takes its rating (Elo, K 32 for the first ten matches
then 16, scored against the other team's average) and its rule that every
match ends on its own deadlines.

## Tests

```bash
make test    # go test ./...
make race    # -race -count=2
```

`internal/httpapi/e2e_test.go` drives the real HTTP transport with fake
clients and a fake dedicated server that keep their own shared object caches,
the way the game does: invite, join, queue, a match that must not split a
party, the server taking the lobby, going live, the result, the rating, the
lobby going away. Also: a server that arrives late still gets the match, a
restarted GC hands a reconnecting server and player their running match, and a
replayed request is not processed twice.
