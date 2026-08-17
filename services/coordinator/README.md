# GREYLINE FRONTRESS — Game Coordinator

> **Transport is not this service's problem.** Since the February 2025 Steam
> Networking change, a listen server started with `map` is reachable from
> outside without port forwarding. Steam lobbies carry the rendezvous. The
> coordinator never sees an address — it hands out a battle, and the game side
> ([src/game/client/greyline](../../src/game/client/greyline),
> [src/game/server/greyline](../../src/game/server/greyline)) does the rest.
> Host migration and the HMAC/policy layer remain implemented but **out of MVP
> scope**. Background: [docs/NETWORKING-SPIKE.md](docs/NETWORKING-SPIKE.md).

The coordinator is the authority that turns a pile of individual TC2 matches into
one war. It owns matchmaking, host election, match integrity and result
recording. It does **not** own the war simulation — that lives behind
`internal/war.Provider`, and the coordinator only reads fronts from it and hands
back finished outcomes.

```
   coordinator                    Steam                      engine
   ───────────                    ─────                      ──────
   fronts, roster,      "battle 381    lobby list,        listen server,
   who hosts,            at Foundry"   SetLobbyGameServer  Steam Networking
   results, the war
        │                              │                        │
        ├── AssignHost ────────────────┤                        │
        │      host: CreateLobby ──────┤                        │
        │             map cp_process ──┼────────────────────────┤
        │             SetLobbyGameServer ◄── game server id ────┤
        ├── JoinBattle ────────────────┤                        │
        │      guests: JoinLobby ──────┤                        │
        │              LobbyGameCreated_t ─► connect ───────────┤
        ◄── MatchResult ───────────────┴────────────────────────┘
```

Steam lobbies answer *where a battle physically exists*. The coordinator answers
*what that battle means for the war*. War progress never lives in lobby data —
the lobby owner is an ordinary player.

## Running it

```bash
cd services/coordinator
export GREYLINE_GC_SECRET="$(head -c 32 /dev/urandom | base64)"
go run ./cmd/coordinator -world world.example.json
```

Write a config file starting from the defaults:

```bash
go run ./cmd/coordinator -print-config > gc.json
```

Flags: `-config`, `-world`, `-log-level`, `-print-config`. Environment overrides:
`GREYLINE_LISTEN`, `GREYLINE_ADMIN_LISTEN`, `GREYLINE_STEAM_WEBAPI_KEY`,
`GREYLINE_GC_SECRET`, `GREYLINE_STATE_PATH`.

The admin endpoint (default `127.0.0.1:27101`) serves `/healthz`, `/state`,
`/policy` and `/world`. It exposes rosters and SteamIDs, so keep it on
localhost or behind an authenticating proxy.

## Tests

```bash
cd services/coordinator && go test -race ./...
```

`internal/gc/integration_test.go` runs the whole MVP acceptance path against a
real listener: two clients deploy, a battle forms, a host is elected, the host
reports a signed result, the client corroborates it, and the front advances. It
also covers the failure paths — disputed results, forged signatures, a host
lying about its own scoreline, host migration, and a fatal client cheat convar.

## Wire protocol

`proto/greyline.proto`, proto2 syntax so the game side can compile it against
the in-tree `src/thirdparty/protobuf-2.6.1` that TC2 already links for gcsdk.

Framing is `uint32 length` (little-endian) followed by a serialized
`CMsgEnvelope`. Every message rides inside the envelope's `oneof`; requests set
`job_id` and replies echo it in `job_id_target`.

Regenerate the Go bindings after editing the proto:

```bash
protoc --proto_path=proto --go_out=. --go_opt=module=github.com/greyline-frontress/coordinator proto/greyline.proto
```

### Flow

```
client                          coordinator                      host client
  │── Hello ──────────────────────►│
  │◄───────────────────── Welcome ─│
  │── DeployRequest ──────────────►│
  │◄───────────────── QueueStatus ─│   (repeats while queued)
  │                                │── AssignHost ─────────────────►│
  │                                │◄─── HostReady (lobby id) ──────│
  │◄──────────────────── JoinBattle│
  │── ClientJoinState ────────────►│
  │                                │◄──────────── HostHeartbeat ────│  (snapshot)
  │                                │◄────────── IntegrityReport ────│
  │                                │◄────────────── MatchResult ────│
  │── ResultAttestation ──────────►│
  │◄───────────────────  MatchOver │── MatchOver ──────────────────►│
```

## Host election

`internal/hostelect` scores every roster member and keeps the full ranking on the
match, so a bad pick is traceable. Hard floors (upload, CPU, memory, worst-case
RTT, `can_host`) disqualify outright; the remaining candidates are scored on:

| term | weight | note |
|---|---|---|
| upload | 0.30 | saturates at 12 Mbit/s |
| latency | 0.35 | scored on the **worst** roster member, not the average |
| CPU | 0.15 | client-reported relative score |
| stability | 0.20 | hosted-OK vs failed/abandoned, confidence-weighted by sample size |
| bonuses | — | dedicated server, public IP; minus a recent-abandon penalty |

Latency comes from `PopOracle`: measured client→host RTTs from previous matches
win, and Steam Datagram Relay POP identity is the cold-start estimate. Ties break
on the lower SteamID so repeated elections on identical input do not oscillate.

## Host migration — deferred, not MVP

Implemented and tested, but out of scope until transport is proven. The host sends a `CMsgMatchSnapshot` with every heartbeat (score, round, time
left, plus a mode-specific opaque blob the game writes). When the host's link
drops — or a *majority* of the remaining players report they lost it, which stops
one player with a bad connection from forcing migrations — the coordinator:

1. marks the old host as a failed host for this match, so it cannot be re-elected;
2. applies the abandon penalty if the host actually left;
3. re-runs the election over the surviving roster;
4. sends `AssignHost` with `is_migration` and the snapshot to the new host, and
   `MigrateHost` with a hold time to everyone else.

After `match.max_migrations` attempts the match aborts instead. An aborted match
never advances the war.

## Security model — deferred, not MVP

Implemented and tested, but out of scope until transport is proven. A P2P host is an ordinary player, so nothing it says is trusted on its own.

**Admission.** Every player is given a join token: `HMAC(match_secret, match_id ‖
steam_id ‖ side ‖ expiry)`. The host can verify it but cannot mint one for
someone the coordinator did not roster. Match secrets are derived per match from
the coordinator's root secret, so a leaked one reveals nothing about other
matches.

**Policy.** The coordinator issues an authoritative convar contract with each
match (`internal/security.DefaultPolicy`) — `sv_cheats 0`, `sv_lan 0`,
`sv_allow_wait_command 0`, `host_timescale 1`, `tf_bot_quota 0`,
`sv_allow_votes 0` and the rest, plus the client-side rules the host queries with
`IServerPluginHelpers::StartQueryCvarValue` (`r_drawothermodels`,
`mat_wireframe`, `cl_interp_ratio`, `mat_picmip`, …). The policy carries a
SHA-256 digest; a host reporting a digest other than the one it was issued has
its match voided.

**Corroboration.** The coordinator re-checks every reported convar observation
itself rather than trusting the host's violation list, and clients may only speak
about themselves. `sv_cheats` is replicated, so a host that lies about it
disagrees with its own players.

**Results.** The host signs the scoreline with the match secret. The declared
winner must be consistent with the scores the host itself sent. A result only
advances the war if enough non-host players attest to the same outcome
(`match.result_quorum`); anything contradicted is recorded as disputed and
discarded.

**Penalties.** Abandoning a live battle or tripping a fatal policy rule earns a
matchmaking ban, extended rather than replaced on repeat, persisted in the player
store so reconnecting does not clear it.

### Authentication caveat

`auth.mode=webapi` validates Steam auth tickets through
`ISteamUserAuth/AuthenticateUserTicket`, which needs a **publisher** Web API key
for the AppID the game ships under. `game/tc2/gameinfo.txt` currently declares
`SteamAppId 243750` (Valve's Source SDK Base 2013 MP), for which no such key can
be obtained — so until the project has its own AppID, run `auth.mode=dev` on a
network you control. Identity inside a match does not depend on this: Steam
authenticates every P2P connection, so the host reports the SteamIDs Steam
actually handed it and the coordinator cross-checks them against the roster.

The coordinator link itself is plain TCP. Terminate TLS in front of it for any
deployment that crosses the public internet.

## Layout

| path | what |
|---|---|
| `cmd/coordinator` | entry point, flags, admin HTTP server |
| `internal/config` | configuration, defaults, validation |
| `internal/wire` | length-prefixed protobuf framing |
| `internal/wire/pb` | generated bindings (do not edit) |
| `internal/steam` | auth ticket validation |
| `internal/war` | war-layer interface + JSON-file implementation |
| `internal/security` | convar policy, digests, join tokens, signatures |
| `internal/hostelect` | host scoring and the latency oracle |
| `internal/gc` | sessions, queue, match lifecycle, migration, results |

`internal/gc` runs a single core goroutine that owns all mutable state. Socket
goroutines only move frames and post events, so the match lifecycle reads as
ordinary sequential code with no locks.
