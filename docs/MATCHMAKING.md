# Matchmaking without a game coordinator

Team Fortress' matchmaking talks to a Valve backend we do not have and cannot
run. This document is how we get matchmaking anyway, what is built, and what has
not been compiled yet.

How to actually run it is in [`RUNNING.md`](RUNNING.md).

## The idea

The stock matchmaking UI — the dashboard, the playlist, the party panel, the
"match found" popup — looks like a lot of code to replace. It is not, because
almost none of it talks to the GC directly. What it reads is **two shared
objects** in the local player's SO cache:

| | |
| --- | --- |
| `CTFParty` | who is in my party, what am I queued for, what are our criteria |
| `CTFGSLobby` | what match am I in, and where do I connect |

So we do not reimplement the UI. We run a small **local game coordinator** in
the client that owns those two objects and keeps them true.

```text
   stock TF2 matchmaking UI
             |  reads
             v
   CTFParty / CTFGSLobby  in the local SO cache
             ^  written by
             |
      CTFMMBackend  (src/game/client/tf/frontress/)
        |                    |
        | party              | queue
        v                    v
   Steam lobby         Go coordinator  --->  serveme.tf fork / your servers
   (invites, chat,     (queue, teams,
    membership)         map, server)
```

Two halves, split along the line of what actually needs a backend:

- **the party is Steam's.** Invites, joining, membership and chat are a Steam
  lobby. This is the part [Momentum Mod](https://github.com/momentum-mod/game)
  solved for the same reason, and `tf_mm_party.cpp` is adapted from its
  `mom_lobby_system.cpp`;
- **the queue is ours.** Only the queue needs to see everybody, and only it
  needs a server to run on. That is the Go coordinator, and it is the only thing
  a community has to host.

Momentum's lobby *is* the session — players are in it while they play. Ours is
only the party: the match itself runs on a dedicated server, and the lobby's job
ends when the assignment arrives.

## The three seams into stock code

Everything else is unmodified Valve code. There are exactly three places we cut
in, and it is worth knowing all three before changing any of them.

**1. Outgoing messages.** The client sends matchmaking messages to the GC two
ways: `CReliableMessageQueue` for anything that needs an answer, and
`CGCClientSystem::BSendMessage` for fire-and-forget. Both now offer the message
to `CTFMMBackend::BHandleClientMsg` first. A message we handle completes
immediately; a message we do not still goes to the (absent) GC, so anything
unimplemented stays visibly unimplemented rather than silently swallowed.

- `src/game/shared/tf/tf_gc_shared.h` — the reliable path
- `src/game/shared/gc_clientsystem.cpp` — the fire-and-forget path

**2. Incoming state.** The backend writes `CTFParty` and `CTFGSLobby` straight
into the local player's SO cache with `BCreateFromMsg` / `BUpdateFromMsg`. This
is the same mechanism the game already uses to inject the web-API inventory, so
it is a road that has been driven.

One consequence worth remembering: the inventory fetch *re-subscribes* the
cache, which destroys everything in it. The backend listens for that and
republishes. If party state ever vanishes a few seconds after login, that is the
thing that broke.

**3. Reading it back.** `CTFGCClientSystem::GetParty`, `GetLobby`,
`UpdateAssignedLobby`, `BHaveLiveMatch`, `GetLiveMatchGroup`,
`BConnectedToMatchServer` and `JoinMMMatch` were stubbed out to return
nothing. They now read the SO cache, which is what they did in retail.

`BConnectedtoGC()` — which the UI gates almost everything on — now reports
whether the backend is up.

## Where the buttons are

Under `SOURCESDK` (which is how this project is built), the dashboard's
`find_game` used to go straight to quickplay, because there was no coordinator
to queue against. It now opens the stock playlist when the backend is active,
and falls back to quickplay when it is not.

The main menu is the HTML page, and it already reaches this: its "find a game"
card sends `mmcmd find_game` over the websocket bridge, which calls
`CTFMatchmakingDashboard::OnCommand("find_game")`. Nothing in the menu had to
change.

## What Steam does for us

The party is a Steam lobby, and once it is, three things stop needing a backend
at all.

**Invites** are the Steam overlay's own dialog. **Join Game** in the friends
list works because the client publishes `+connect_lobby <id>` as its rich
presence connect string — Steam launches the joiner's game with that argument
and `secure_command_line.cpp` turns it back into a lobby join. It works when
their game is not running yet, which the request-to-join path never did.
**Requesting to join** a friend who is already playing reads their `tf_lobby`
rich presence key and joins that lobby directly.

`tf_mm_party_autocreate` hosts a lobby as soon as matchmaking comes up. Without
it a player who has not deliberately made a party cannot be joined or invited,
because there is nothing for Steam to point at. Retail hides this by having the
GC put everyone in a party of one.

The rest of rich presence — searching, playing, which match group, which map,
party grouping in the friends list — was already written in `clientmode_tf.cpp`
and reads the party and lobby objects. Supplying those objects turned it on;
the only line we changed is where the connect string points.

### Party members never talk to the coordinator

The leader queues for everybody. When the assignment arrives the leader
publishes it into the lobby data — `connect`, `password`, `match_id`, `stv`, and
a `teams` list pairing each SteamID with its side. Members watch for that and
build their own lobby object from it.

That means a member who joins the party mid-queue is still pulled into the
match, and it means the coordinator never has to know that a party has more than
one client.

## Filling a match while it runs

`"mode": "frontline"` keeps a match taking players after it starts. This is the
behaviour a small population needs: a match that formed as a 4v4 because that is
who was in queue becomes a 12v12 as people arrive, rather than a second
half-empty server starting beside it.

One rule keeps it balanced with no arithmetic anywhere else: **a party may only
join a team with room under half the match**. Neither side can grow past half,
so they cannot drift apart, and parties are still never split.

`"mode": "ranked"` forms a roster once and leaves it alone.

Both modes report their result to the war identically. Scoring is not built.

## Ranked, and what "restricted" means

Ranked differs from casual in two places, and they are deliberately separate.

**In the game**, `frontress_ranked.cfg` is the competitive ruleset: 6v6 class
limits (`mp_sixes 1`), no random crits, no damage spread, the competitive
weapon whitelist, tournament mode with ready-up, no autobalance or scramble
(the roster is fixed), and no votes that could change any of it. The
coordinator execs it before the map change, exactly as it execs
`frontress_casual.cfg` for the open queue.

**In the coordinator**, a `restrictions` block on the match group says who may
queue at all:

```json
"restrictions": {
  "max_party_size": 3,
  "min_matches_played": 5,
  "abandon_cooldown_mins": 30,
  "max_abandons": 5
}
```

A refusal is a 403 with the reason written for the player, not a 400. The
history those rules read -- matches played, matches abandoned -- lives in
`players.jsonl`, folded on startup, so a deploy does not clear anybody's
cooldown. An abandon is "the server reported who was there and this player was
not", which needs the agent below; a result that names nobody credits the whole
roster instead of branding it.

The full list is in [`services/coordinator/README.md`](../services/coordinator/README.md).

## Where the servers come from

A match needs a server, and the coordinator does not care which kind:

| | |
| --- | --- |
| `static` | servers you run and it drives over RCON |
| `registered` | servers that register themselves and heartbeat |
| `serveme` | reservations from the [serveme fork](https://github.com/gru2007/serveme-frontress) |

The serveme path is the one that scales without anybody setting up a machine
for the game: a reservation starts a container running the game, the match is
played on it, and ending the reservation destroys it. `prefer_docker` asks for
exactly that.

## Reporting the result

`cmd/greyline-agent` runs next to the dedicated server -- inside the container,
when serveme started one -- and closes the loop the coordinator cannot close by
itself. It reads the match id from `sv_tags`, receives the console log through
`logaddress_add`, and when the game ends it reports the score and, from RCON
`status`, who was actually there.

Without it a match still ends, on an empty server or a clock; nobody's record
is written and the war hears nothing.

## Console commands

| | |
| --- | --- |
| `tf_mm_status` | what the backend thinks is happening |
| `tf_mm_queue <group>` | queue directly (7 = casual 12v12) |
| `tf_mm_cancel` | leave the queue |
| `tf_mm_join` | connect to the assigned match |
| `tf_mm_watch` | connect to the match's SourceTV relay instead |
| `tf_mm_party_create` / `_invite` / `_join <id>` / `_leave` | the party lobby |

| convar | |
| --- | --- |
| `tf_mm_enable` | master switch. `0` puts the client back to no-GC behaviour |
| `tf_mm_coordinator` | base URL of the Go coordinator |
| `tf_mm_autojoin` | connect automatically when a match is found |
| `tf_mm_party_type` | 0 invite-only, 1 friends, 2 public |
| `tf_mm_party_autocreate` | host a party lobby at startup so friends can join you |
| `tf_mm_debug` | spew every state change and HTTP round trip |

## Trying it

1. Run the coordinator (see [`services/coordinator/README.md`](../services/coordinator/README.md)).
   `auth.mode` `dev` and one `static` server is enough.
2. In the game: `tf_mm_coordinator "http://127.0.0.1:27100"`, `tf_mm_debug 1`.
3. `tf_mm_status` should say `active: yes`.
4. Pick maps in the casual panel — casual refuses to queue with none selected —
   then press play, or `tf_mm_queue 7`.
5. With `min_players: 2` and `patient_secs: 0` one player forms a match, which
   is the fastest way to see the whole path work.

For a `serveme` provider instead of a static one, see
[`RUNNING.md`](RUNNING.md#5-servers-from-serveme-the-container-path): the fork
hands out containers, so nothing has to be installed on the machine that runs
them.

## State of the work

Two levels, and the difference matters more than the feature list:

| | |
| --- | --- |
| **tested** | a test fails if it breaks |
| **written** | the code exists and a compiler has not seen it |

### The coordinator (Go) — tested

Queue and settling for small populations, parties never split across teams, team
balance, map choice, server pool and provider fallback, the serveme reservation
flow (including waiting for a container to come up and preferring one),
per-group queue restrictions and the player records they read, Steam ticket
verification, the log and `status` parsing the agent reports from, the war
engine and its log replay, config validation.
`cd services/coordinator && make race`.

### The game (C++) — written

**Nothing in `src/game/client/tf/frontress/` has been compiled.** The build
needs a toolchain that was not available here. Assume a first build fails
somewhere and budget for it. The code is mechanically simple and has been read
carefully against the headers it uses, but that is not the same thing as
building.

The things most likely to be wrong on a first build are the protobuf accessor
names and the exact `CUtlBuffer` / `CWebAPIValues` signatures.

## Known gaps

1. **Nothing game-side has been compiled.** See above.
2. **Match results come from the agent, not the game.** `greyline-agent` reads
   the console log and RCON `status`, which is enough for a winner, a score and
   who was present. It is not enough for anything finer — per-player stats, a
   round-by-round record — and a game-side reporter is still the better answer
   if the war grows to want them.
3. **Teams are advisory.** The coordinator decides who is on which side and the
   client is told, but nothing on the server enforces it: a player can pick the
   other team on arrival. Frontline leans on TF2's own autobalance instead,
   which is fine for casual and not fine for ranked.
4. **Standby is answered but does nothing.** Asking to join a match your party
   is already in is a different thing from backfill — it needs the coordinator
   to seat a specific player in a specific match on request.
5. **Kicking a party member is impossible.** A Steam lobby has no kick. The
   backend says so rather than pretending.
6. **Abandon penalties are enforced at the queue, not in the game.** The
   coordinator records abandons and refuses to queue somebody on cooldown, but
   `GetAssignedMatchAbandonStatus` still reports "no penalty" to the client:
   the game does not yet ask the coordinator what it owes. The refusal arrives
   when they press play instead, with the reason.
7. **The queue is in memory.** A coordinator restart drops the queue and the
   live matches. Player records are not: they are a file, so cooldowns and
   match counts survive.
8. **One request in flight per client.** The backend serializes its HTTP; a
   cancel while a poll is in flight is handled, but the pattern is a
   simplification, not a general one.
9. **Backfill does not tell the server anything.** New arrivals just connect
   with the password. Once a server-side agent exists it should be told who to
   expect, so a roster gate becomes possible.
10. **`min_players` is per match group, not per front.** A small population gets
    small matches, but the war does not yet ask for a *particular* size.

## What stage three plugs into

The war layer already exists in the coordinator and is off by default. The seam
into matchmaking is one field: an assignment can carry a `war` briefing —
which front, which node, which stage of the offensive, which side is attacking
and which uniform it wears. The client receives it today and ignores it.

When the game side is ready, the two things it needs are the briefing (state the
battle's place in the war) and the result (`POST /v1/gs/result`, so the front
moves). Everything between those two is already written and tested.
