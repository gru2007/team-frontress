# GREYLINE FRONTRESS — what is built and what is not

The design is in [`GREYLINE-WAR.md`](GREYLINE-WAR.md), the implementation
reference in [`services/coordinator/README.md`](../services/coordinator/README.md),
and how to run it in [`GREYLINE-MVP-TESTING.md`](GREYLINE-MVP-TESTING.md).

This file is the other question: of the things those describe, which ones
actually work, which ones have never been run, and which are half-done. It is
written to be read before a test session so nobody spends an evening finding a
gap that was already known.

## How to read the confidence column

Three levels, and the difference between them matters more than the feature
list does:

| | |
| --- | --- |
| **tested** | an automated test fails if it breaks. Named where it is not obvious |
| **built** | it compiles in CI and has been exercised by hand, but nothing guards it |
| **written** | the code exists and has never been compiled or run by anyone |

Most of the Go is tested. Most of the game-side C++ from the current round of
work is **written** — it is mechanically simple and it has been read carefully,
but a compiler has not seen it. Treat a first build as the real test.

## The war (Go, `internal/war`)

| Thing | State |
| --- | --- |
| Theater, nodes, adjacency, HQs | tested |
| Fronts, stage plans, capture moving the front along the graph | tested |
| Population deciding how many fronts are open | tested |
| Defensive mobilization | tested |
| Campaigns, intermission, the event log as the source of truth | tested |
| Battles weighted by headcount (`full_strength_players`, `Front.Push`) | tested — `weight_test.go` |
| Neither side being the structural attacker | tested — `roles_test.go` measures it over 60 campaigns |
| The attacking side always wearing BLU, and reading the scoreline back | tested |

## Matchmaking and the server pool (Go, `internal/mm`, `internal/pool`)

| Thing | State |
| --- | --- |
| Queue, parties landing together, front choice, widening | tested |
| Forming battles, team sizes, contracts | tested |
| Dedicated server pool: register, assign, heartbeat, result | tested |
| P2P host election when nothing dedicated is free | tested |
| A P2P host's result closes immediately; no hidden quorum | tested — `TestP2PHostResultFinishesWithoutHiddenQuorum` |
| Battles growing while they run or boot (lobby expansion) | tested — `lobby_test.go` |
| A failed host sitting out a cooldown instead of being re-elected | tested — `hostloop_test.go` |
| A booting server judged by the boot deadline, not the heartbeat rule | tested — `pool_test.go` |
| DEPLOY leaving a battle that never started | tested |
| Allegiance fixed for the length of a battle, on hello as well as SetSide | tested — `hostloop_test.go` |
| SteamID and auth ticket sourced from Steam in the active client | written; HTTP round-trip tested |

## The game (C++)

Nothing in this column has been compiled by the author of the current round of
work; the build needs a steamrt container that was not available. Everything
marked **written** should be assumed to have a compile error in it until a
build says otherwise.

| Thing | State |
| --- | --- |
| Briefing: what battle this is, in the player's own language | built |
| Briefing wording follows whether the uniforms were swapped | tested — the logic layer and both language files are unit tested |
| Roster: teams assigned by the coordinator, enforced once a second | built |
| Seating a player on arrival, before the team menu | written |
| Refusing a `jointeam` the war did not assign | written |
| Refusing a connection from an account not on the roster | written — **off unless the coordinator runs verified Steam auth**, see below |
| Holding the round until the roster is on the server (muster) | written |
| Publishing a P2P host's own address and score | built |
| Menu RPC for state, Steam identity and native menu actions | written |
| Player **models** drawn in the war's colours | written — see below |
| `sv_friends_only` no longer forced on at launch | written |

### The uniform swap is deliberately partial

`greyline_uniform_by_war_side` is on, and it covers models only: bodies and
ragdolls, world weapons, cosmetics, viewmodels and their attachments,
buildings and the ammo they drop. The exact list is in
[`greyline_uniform.h`](../src/game/shared/greyline/greyline_uniform.h).

It does **not** cover:

- **particle effects with a team in their name** — rockets, stickies, medic
  beams, crit and burning effects. Around 190 call sites of the shape
  `..._red" : "..._blue"`. This is the largest single piece of unfinished work
  in the visual layer.
- **UI and map** — the HUD, the scoreboard, name colours, spawn room signs,
  respawn visualizers, the cart. These belong to the map and the interface
  rather than to us, and are not obviously ours to repaint at all.

So in a swapped battle a player is red while their rocket trail is blue and the
scoreboard says BLU. That is a known cost, not a bug report; the briefing says
which is which. `greyline_uniform_by_war_side 0` turns the whole thing off.

## The menu (`game/tc2/loose/resource/html/greyline.html`)

It is a test UI, not the game's menu — one plain file, no build step, deletable
without anything else noticing. Everything it does goes through the
coordinator's public HTTP API.

| Thing | State |
| --- | --- |
| War map, fronts, DEPLOY, queue, joining a battle | built |
| Standing up a P2P battle when elected host | built |
| Retrying a connect that does not take, then giving the slot up | written |
| Applying roster additions to a running battle, with delivery failure rollback | tested — `lobby_test.go` |
| Reporting the real player count and the battle's progress | written |

Only its syntax is checked automatically (`node --check`). Nothing tests its
behaviour.

### Steam identity

The active in-game menu obtains SteamID64 and an auth session ticket from
`ISteamUser`. SteamID is serialized as a decimal JSON string end to end; it is
never converted to JavaScript `Number`, which cannot represent SteamID64.
Browser-only development still permits manual identity under `auth.mode=dev`.

If a non-game client claims an identity that does not match the connecting
Steam account:

- team assignment does nothing, silently. The player is not on the roster as
  far as the server is concerned, so nothing moves them;
- the briefing has no side to describe for them;
- and the roster gate, if it were on, would turn away the entire battle.

The roster gate remains off unless the assignment says `verified_identities`,
which only `auth.mode=webapi` sets. Dev auth is still intentionally not proof of
identity, but the shipped game client no longer invents or rounds the ID.

## Known gaps, roughly in the order they will bite

1. **Nothing game-side from the current round has been compiled.** Assume a
   first build fails somewhere and budget for it.
2. **The terminal match lifecycle is still split between game C++, the menu and
   the agent.** There is no single authoritative C++ terminal record yet.
3. **P2P results are trusted immediately.** The unusable quorum is gone, but
   signed or independently witnessed result evidence is not implemented yet.
4. **Particle effects ignore the uniform swap** (above).
5. **`timing.migration_hold` does nothing.** It is validated in config and read
   only by the retired `internal/legacy/gc`. Host migration is not implemented
   in the current matchmaker: a host that disappears aborts the battle and
   re-queues the roster.
6. **Host reputation is per-process.** `hostelect.History` and the failure
   cooldown live in memory and reset when the coordinator restarts.
7. **No latency in host election.** `hostelect` accepts a `LatencyOracle` and
   is given `nil`; every candidate scores an equal zero on that term. Election
   works on upload, CPU, memory and hosting history.
8. **Coordinator and host networking still lives mostly in the web page.** Steam
   identity and native menu actions moved to C++, but the host FSM has not.
9. **Steam peer-to-peer joins can fail on the handshake** — the transport
   flapping between ICE and relay invalidates the connect challenge. The menu
   retries and then releases the slot; there is nothing else we can do from
   this side.

## Deliberately not here

Supply lines, encirclement, infrastructure effects, PvE, an economy, a season
pass, and the rest — see the closing section of
[`GREYLINE-WAR.md`](GREYLINE-WAR.md). Those are absent on purpose and are not
gaps.
