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

## The other half: the game server

The client owns `CTFParty` and `CTFGSLobby` in the *player's* shared object
cache. `src/game/server/tf/frontress/tf_mm_server.cpp` owns `CTFGSLobby` in the
*game server's* cache, and that one object is what the whole server-side half of
matchmaking hangs off:

```text
   coordinator --RCON--> tf_mm_match_begin
                              |
                         CTFGSLobby in the server's SO cache
                              |  CTFGCServerSystem::SOCreated
                              v
                          CMatchInfo
                              |
      +-----------+-----------+-----------+--------------+
      |           |           |           |              |
  roster gate  match group  teams     abandons     the match result
 (tf_mm_strict) (match HUD,          (who left,   (CMsgGC_Match_Result,
                 summary)             when)        with per-player stats)
```

Without it `GetCurrentMatchGroup()` is `k_eTFMatchGroup_Invalid`,
`SteamIDAllowedToConnect` lets anybody in, teams are advisory and nothing
reports a result. With it, all of that is stock Valve code doing what it always
did.

Two seams, and they mirror the client's exactly:

- **the lobby** goes into the cache with `AddLocalSOCache`, the same hand-written
  `CMsgSOCacheSubscribed` trick and for the same reason;
- **the outgoing messages** are answered in-process. `tf_gc_shared.h` and
  `gc_clientsystem.cpp` now offer server messages to `CTFMMServer::BHandleServerMsg`
  the way they already offered client messages to the backend. Without that the
  reliable queue wedges on the first message the GC does not answer, and
  `UpdateConnectedPlayersAndServerInfo` stops running entirely.

### The password is gone, and that is the point

`tf_mm_servermode` — which is what makes `SOCreated` accept a lobby at all —
refuses to stay on while `sv_password` is set. So the handoff takes the password
off and puts the roster in its place: `tf_mm_strict 1` makes
`SteamIDAllowedToConnect` the door, and it opens for exactly the SteamIDs the
coordinator put in the lobby.

That is a better door. A password is one screenshot away from being public; a
roster is not. The coordinator still sends the password as a **fallback**: if
the lobby cannot be published — no GSLT, most likely — the server puts the
password straight back on and runs as an ordinary passworded community server
rather than as an open one.

### Getting into a match that is already running

The gate is the lobby, so letting somebody in and putting them in the lobby are
the same action — there is no separate allow-list that could drift from the
match. `tf_mm_match_add <match_id> <steamid:team,...>` adds seats to the running
lobby; `CTFGCServerSystem::SOUpdated` is already watching for a member in
`RESERVATION_PENDING` that the match does not have yet, and acknowledges them on
the spot.

Two things use it, and they are the same thing for different reasons:

| | |
| --- | --- |
| **backfill** | the coordinator sends the queue into a running match |
| **standby** | a party member asks to join the match their party is in |

Both go through one path in the coordinator: seats are sold under the lock, the
ticket is left `matched`, and `admit()` tells the server *before* the client is
given anything to connect to. If the server cannot be told, the seats are given
back and the party goes back in the queue — a player left waiting is a far
better outcome than one bounced off a server they were sent to with no
explanation.

Standby is one field on the ordinary queue request (`standby_match_id`), so the
ticket, the poll and the assignment are the same code as everything else. A
refusal is a refusal with a reason: that match is over, is full, is not this
group, or neither side has room.

A party member with no seat is deliberately *not* given a lobby object. That
object means "I am in this match" — `BHaveLiveMatch` reads it — and claiming it
would both send them at a closed door and switch off the standby button that is
the way through it.

### What the game reports back

`CMsgGC_Match_Result` is the message the server sends when a match ends, and it
carries what only the game knows: the real roster, per-player score, kills,
deaths, damage, healing, support, and who left and why. The backend answers it
and prints it as `[frontress] match_result …` lines, which `greyline-agent`
already receives over `logaddress_add` and forwards to `POST /v1/gs/result`.

No new socket in the game DLL, and the agent's log-scraping path stays as the
fallback for a server that is not running our build.

## What makes a matchmade server "official"

Nothing about a server is permanently official. A server in the pool is a
community server until the coordinator hands it a match, and it goes back to
being one when the match ends.

The switch is `tf_match_emulation`, which the coordinator sets over RCON with
the rest of the per-match setup and clears again on teardown. It is what makes
`CTFGameRules` report a match group at all — without it `GetCurrentMatchGroup`
returns `k_eTFMatchGroup_Invalid`, because the server-side answer comes from
`GTFGCClientSystem()->GetMatch()` and there is no GC to fill that in. The match
group is what turns on the match HUD, the match summary at game over, and the
tournament/ready-up handling a group's ruleset asks for.

The game knows two values: `1` is Casual 12v12 and `2` is Ladder 6v6, so the
coordinator derives it from the group's `match_group`. `match_emulation` on the
group overrides it.

Two things worth knowing about it:

- **achievements were never the thing gating on this.** `CheckAchievementsEnabled`
  asks about Steam, commentary mode, demo playback and training — not about
  whether the server is official. They already unlock on any server here. What
  official status buys is the match *presentation*, and the post-match flow;
- **Steam is not in this loop.** Nothing in this repo configures an
  "official server" concept on the partner site, and nothing in the game reads
  one: there is no `sv_official`, no official-server flag in the browser code,
  and the master list only knows AppID, tags and whether the server is secure.
  Official status here is entirely the coordinator's to grant. The only things
  Steam genuinely needs uploaded for matchmaking are in `steamworks/` (rich
  presence localization) and the GSLT in `server.cfg`. If you want certainty
  about the partner site rather than about this codebase, that is a question
  for Steamworks support, not one this repo can answer.

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
2. **The war does not read the game's result in full.** The game now reports
   per-player score, kills, deaths, damage, healing and support (see "What the
   game reports back"), and `greyline-agent` forwards it — but
   `POST /v1/gs/result` still only records who played and who won. The detail
   arrives and is dropped.
3. **Teams are assigned but not locked.** The server has the roster's teams in
   its match object now, so it puts people where the coordinator said and
   `CMsgGCChangeMatchPlayerTeamsRequest` keeps the lobby in step when it
   rebalances. Nothing stops a player choosing the other side on arrival;
   casual leans on TF2's autobalance, which is fine there and not fine for
   ranked.
4. **Standby has no UI beyond the stock button.** It works —
   `standby_match_id` on the queue request, seated and announced to the server
   like a backfill — but the only way to reach it is the stock "join your
   party's match" panel. A member who arrives mid-match is told in the console,
   not on screen.
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
9. **A seat announced to the server is not retried.** `admit()` tells the
   server about new seats once; if that RCON call fails the party goes back in
   the queue and tries again on the next tick, which is correct but means a
   server having a bad minute churns the queue rather than waiting it out.
10. **`min_players` is per match group, not per front.** A small population gets
    small matches, but the war does not yet ask for a *particular* size.
11. **A match server is unlisted and roster-gated, so the queue is the only way
    in.** That is deliberate. Watching is the way around it: a player in a match
    publishes its SourceTV relay as `tf_stv` rich presence, any friend can read
    it, and `tf_mm_watch [steamid]` connects there — from the console or from
    the friends panel's "watch their match". What is still missing is a list:
    nothing shows the live matches of people who are not on your friends list.
12. **XP is a match count, not a rating.** `internal/players/progress.go` turns
    the record into XP and a level — 100 a match, 50 for a win, 150 off for an
    abandon — and the client publishes it as `CTFRatingData` so the stock badge
    reads it. It is arithmetic, deliberately: there is no skill model, matchmaking
    does not use it, and nothing on the ranked side is gated by it yet.
13. **The lobby settings panel is Valve's, and mostly dead here.**
    `k_eMMSettings` is `CTFPingPanel`: a ping slider and a datacenter list fed
    by `GTFGCClientSystem()->BHavePingData()`, which is never true without a
    GC, so the list is always empty and the slider gates on a ping to nothing.
    Of the controls that do render, "keep party on the same team" is disabled
    and marked coming-soon even though the coordinator has always kept parties
    together, and the invite-mode combo writes `tf_party_join_request_mode`,
    which nothing reads — the Steam lobby's visibility comes from
    `tf_mm_party_type`. Two convars for one setting, and the one on screen is
    the one that does nothing.

## What stage three plugs into

The war layer already exists in the coordinator and is off by default. The seam
into matchmaking is one field: an assignment can carry a `war` briefing —
which front, which node, which stage of the offensive, which side is attacking
and which uniform it wears. The client receives it today and ignores it.

When the game side is ready, the two things it needs are the briefing (state the
battle's place in the war) and the result (`POST /v1/gs/result`, so the front
moves). Everything between those two is already written and tested.
