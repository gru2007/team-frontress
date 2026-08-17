# GREYLINE FRONTRESS — how to test it

> **Superseded.** This document describes the retired peer-to-peer coordinator,
> where a player's own machine was elected to host the battle. Battles now run
> on a pool of dedicated servers driven over HTTP: see
> [`services/coordinator/README.md`](../services/coordinator/README.md) for how
> to run the coordinator and join a game server to the pool, and
> [`GREYLINE-WAR.md`](GREYLINE-WAR.md) for the war it feeds.
>
> It is kept because the FakeIP and Steam Networking findings below are still
> the accurate account of how a Source listen server becomes reachable, which is
> the thing to reread if community-hosted nodes ever come back.

Everything below runs the real thing end to end: coordinator, listen server,
migration, war state. No mocks.

## What it does

```
player presses DEPLOY
      │
      ▼
coordinator ── picks the front, forms the roster, elects a host
      │
      ├── host  ──► map ──► engine allocates an address ──► "ready, I am at X"
      │                                                          │
      └── others ◄── "the battle is at X" ──────────────────────┘
                       │
                       ▼
                    connect X
                       │
                       ▼
           battle over ──► result ──► the front moves
```

X is a Steam **FakeIP** — an address-shaped handle for a Steam P2P/SDR route,
not a real internet address. The engine carries the traffic over its own Steam
Networking; nothing in Greyline touches gameplay packets, and no Steam lobby is
involved anywhere.

**The host must be launched with `-enablefakeip`.** The engine only requests a
FakeIP when that is on the command line. `game/tc2.sh` and `game/tc2.bat` pass
it; if you launch the binary some other way, pass it yourself, or the host will
have no address to advertise and will say so in the console.

## What is already covered by automated tests

```bash
cd services/coordinator && go test -race ./...
```

Covers the coordinator half against a real listener: two clients deploy, a
battle forms, a host is elected, a result is corroborated and the front
advances; plus disputed results, forged signatures, a host contradicting its own
scoreline, host migration carrying the snapshot and sending the survivors to the
new host's address, migration hold times, a fatal client cheat convar,
host-election scoring, the convar policy digest and the token/signature
primitives.

The C++ half has no automated tests — Source has no test harness here, and it
needs Steam, a running game and two accounts. That is what the manual runs below
are for.

## 1. Build

**Coordinator**

```bash
cd services/coordinator && go build -o bin/coordinator ./cmd/coordinator
```

**Game** — regenerate projects first, because the Greyline files and the
`.proto` build step are new:

```bash
cd src && ./createallprojects.bat
```

Linux:

```bash
cd src && ./buildallprojects
```

The `.proto` is compiled by the protoc 2.6.1 bundled at
`src/thirdparty/protobuf-2.6.1/bin/`, driven by the `$DynamicFile` rule in
`client_tf.vpc`. There is one schema file,
[services/coordinator/proto/greyline.proto](../services/coordinator/proto/greyline.proto),
and both halves generate from it — if you edit it, rebuild both.

### What CI builds for you

`.github/workflows/build_greyline.yml` runs on every push touching `services/`
and takes a couple of minutes: gofmt, vet, `go test -race -count=2`, a build, a
smoke test that every shipped config actually loads, and — the useful one — it
compiles `greyline.proto` with the **protoc 2.6.1 bundled in the tree**, which is
the exact version the game build uses. A schema the game cannot build fails
there rather than forty minutes into a game compile.

The Linux and Windows workflows build the game itself, including the Greyline
C++ and its generated protobuf. Until that has run green once, treat the C++ as
unverified.

Release publishing is gated on `github.repository_owner == 'gru2007'` and the
`campus-fortress` branch, so forks build but do not publish. The Windows job
needs a runner with Visual Studio 2026 because `createallprojects.bat` passes
`/define:VS2026`; if yours is labelled differently, set the `WINDOWS_RUNNER`
repository variable.

`game_clean/copy.sh` ships `tc2/cfg` files by `git ls-files`, so
`game/tc2/cfg/greyline.cfg` only reaches a built package once it is committed.

## 2. Start the coordinator

```bash
cd services/coordinator
GREYLINE_GC_SECRET="$(head -c 32 /dev/urandom | base64)" \
  ./bin/coordinator -config gc.test.json -world world.test.json -log-level debug
```

`gc.test.json` allows 1v1 so two people are enough; `world.test.json` has one
active front whose maps start at two players. Production uses `world.example.json`
and the defaults.

Watch it live:

```bash
curl -s http://127.0.0.1:27101/state | less
```

Leave this running and watch its log during every test below — it narrates the
whole match lifecycle.

## 3. Test A — one machine, five minutes

Confirms the link, host election and the hosting contract. No second account
needed.

Launch TC2, open the console:

```
greyline_gc_address 127.0.0.1:27100
greyline_gc_reconnect
greyline_gc_status
```

Expect `greyline coordinator: ready`. The coordinator log shows
`session authenticated`.

```
greyline_deploy red
```

With only you queued, nothing forms — expect `waiting for 1 more mercenaries`.
Now force the host path directly:

```
greyline_host_battle arena_badlands
```

Then, once the map has loaded:

```
greyline_status
greyline_host_status
```

**Pass conditions**

| check | expected |
|---|---|
| `greyline_status` | `hosting, battle live`, and an `address` line that is not empty |
| `greyline_host_status` | every contract convar `want` = `have` |
| — | `sv_friends_only` is `0`, not `1` |
| console | `battle server address 169.254.x.y:port (Steam Networking)` |
| console | `battle '…' is up at …` |

If instead you get *"this server advertises no address"*, the engine did not get
a FakeIP: check the game was launched with `-enablefakeip`, that
`sv_use_steam_networking` is `1`, and raise `sdr_spew_level 6` to watch the
allocation. Everything after this point depends on that address existing.

`sv_friends_only` is forced to 0 for you, per battle, by the hosting contract —
you do not need to configure it. It is listed here only as a diagnostic: if it is
still 1 the contract did not apply, and every later test will fail at the door
with *"STEAM UserID … is not friends with the host!"*. It is deliberately not
turned off globally, because a player hosting a private game outside Greyline
should keep it on.

### Setting up a Linux test box

`tools/setup-linux-testbed.sh` does the whole machine in one command, and is safe
to re-run:

```bash
./tools/setup-linux-testbed.sh          # deps, game build, coordinator, Steam entry
./tools/setup-linux-testbed.sh deps     # or one step at a time
```

What it handles:

- **prerequisites** — podman (the Linux game build runs inside the SteamRT sniper
  SDK container, see `src/sdk_container`), git, ccache, python3, Go, and it
  checks for Steam Linux Runtime sniper, which the launcher needs;
- **the build** — `src/buildallprojects release` then `game_clean/copy.sh`;
- **the coordinator** — built, plus a `systemd --user` service with a secret
  generated once and kept, and `loginctl enable-linger` so it survives logout;
- **the Steam library entry** — a non-Steam shortcut pointing at `game/tc2.sh`.

The library entry is written by `tools/steam_shortcut.py` directly into
`userdata/<id>/config/shortcuts.vdf`, the binary VDF Steam keeps shortcuts in.
It refuses to run while Steam is up, because Steam rewrites that file on exit and
would discard the change; it backs the file up before every write; and re-running
updates the existing entry instead of filling your library with duplicates.
Restart Steam afterwards and launch **Campus Fortress** from the library.

**If a Linux host cannot be reached by remote players, try this first.**
`game/tc2.sh` passes `+ip 127.0.0.1`, which binds the game socket to loopback.
Steam Networking should carry the traffic regardless, but that flag is the first
thing to remove when testing hosting from a Linux box. The same script also
passes `-enablefakeip`, which is required rather than optional — if you replace
the launcher, keep it.

**Enabling SSH on the test box.** If the machine answers ARP but refuses port 22,
sshd is not running or is firewalled. From the machine's own keyboard:

```bash
sudo apt-get install -y openssh-server
sudo systemctl enable --now ssh
sudo ufw allow 22/tcp        # only if ufw is active
ip -4 addr show scope global | grep inet
```

## 4. Test B — two machines, the real thing

Two Steam accounts, two different internet connections, no port forwarding. The
coordinator must be reachable from both — run it somewhere both can see, and set
`greyline_gc_address` accordingly on each client.

On **both** clients:

```
greyline_gc_address <coordinator host>:27100
greyline_gc_reconnect
greyline_gc_status          → ready
```

Then, on both, within a few seconds of each other:

```
greyline_deploy red         (machine A)
greyline_deploy blu         (machine B)
```

Nobody types anything else. From here it is automatic.

**What should happen, in order**

1. Coordinator: `match formed` with a map and `1v1`, then `host elected` with
   the full scoring breakdown.
2. One client prints `you are hosting battle …` and loads the map.
3. That client prints `battle server address …` and then `battle '…' is up at …`.
4. Coordinator: `host ready`.
5. The other client prints `deploying to battle …` and connects **without any
   address being typed**.
6. Coordinator: `match live`.

**Pass condition:** both players are in the same match, and neither typed an IP.

Play a round out, then let the match end. Both clients report; the coordinator
logs `match resolved … counted=true`, and:

```bash
curl -s http://127.0.0.1:27101/world
```

shows the front's points advanced. That is the MVP acceptance test from the
design document, top to bottom.

## 5. Test C — host migration

Same setup as Test B, but with four players it is much more informative
(two survivors instead of one). With two it still works.

While the battle is live, note the score, then **kill the host's game process**
(Alt+F4 or `taskkill` — a clean `disconnect` is a different path).

**What should happen**

1. Coordinator: `host link lost`, then `migrating host` with the attempt number.
2. Coordinator: `host elected` again, excluding the dead host.
3. Survivors print `holding up to 60s`.
4. The promoted player loads the map and prints
   `restoring battle score RED n - BLU m`.
5. Everyone reconnects on their own.

**Pass conditions**

| check | expected |
|---|---|
| the battle continues | nobody returned to the main menu |
| score | the same round score as before the host died, not 0-0 |
| address | the survivors connect to a **new** address, the promoted player's |
| the round in progress | restarts — this is by design, see the limits below |

The survivors do not have to find anything: the new host reports its address the
same way the first one did, and the coordinator sends everyone a fresh join.

To rehearse the takeover without killing anyone, do it by hand:

```
greyline_takeover_battle arena_badlands 2 1 3
```

That stands the battle back up here and resumes at RED 2 – BLU 1 after 3 rounds.

## 6. When something does not work

| symptom | cause |
|---|---|
| `greyline_gc_status` stays `connecting` | coordinator not running, wrong `greyline_gc_address`, or a firewall on 27100 |
| `refused: protocol version mismatch` | game and coordinator built from different `greyline.proto` — rebuild both |
| `refused: matchmaking ban active` | you abandoned a live battle; default penalty is 10 minutes, and it stacks |
| deploy sits at `waiting for N more mercenaries` | fewer players queued than `min_team_size × 2` in the config |
| `no map fits queued population` | the front's `map_pool` has no entry whose envelope covers the queued count |
| `host election found no eligible candidate` | nobody has `greyline_can_host 1`; everything else relaxes automatically |
| coordinator warns `host elected below the preferred floors` | it formed the battle anyway on a weak host — expected at low population, a steady stream of these means the queue is pairing distant players |
| `this game mode cannot be resumed on another host` | the map is marked `"migratable": false`, or the game detected MvM/tournament rules — the battle is abandoned rather than resumed wrongly |
| guest never connects | check the host's `sv_friends_only` is 0, that its `greyline_status` reached `hosting, battle live`, and that the address in the guest's `connecting to battle …` line is the host's FakeIP and not a `192.168.*` / `127.0.0.1` one |
| host reports ready with no address | launched without `-enablefakeip`, or `sv_use_steam_networking 0` |
| `host reported ready without an address or a game server identity` | the host's engine never produced either; the coordinator re-elects someone else |
| the link reconnects every ~45 seconds | fixed: an idle client now pings inside the coordinator's `session_timeout`. If it comes back, the two `session_timeout` and `heartbeat_interval` settings have been configured so that the second is not comfortably below the first |
| migrated battle resumes 0-0 | the host died before ever sending a heartbeat with a snapshot, or `greyline_restore_score` fired before the teams existed — the coordinator log shows which |

Useful spew:

```
greyline_gc_debug 1
greyline_battle_debug 1
greyline_host_debug 1
sdr_spew_level 6
```

## 7. Which game modes are supported

`CTeamplayRoundBasedRules::SetWinningTeam` adds to the team score once per round
when `ShouldScorePerRound()` is set, so in most TF2 modes the team score really
is the match state — which is exactly what migration restores.

| mode | migration | why |
|---|---|---|
| Arena | full | score is rounds won; rounds are short, so replaying one costs little |
| KOTH | full | score is rounds won; each team's clock resets with the round, which is correct |
| CTF | full | score is captures; the flag returns to base, as it would on a round reset |
| 5CP | full | score is rounds won |
| Payload, A/D | works, but hurts | score is rounds won, so it restores — but a payload round is most of the match, and replaying ten minutes of pushing is a real loss |
| **Mann vs. Machine** | **not supported** | the match is the wave number, the credits earned and every upgrade bought. None of that is team score, so "restoring the score" would hand players a wrong game |
| **Tournament / stopwatch** | **not supported** | the time to beat and which side attacks are part of the match, not the scoreline |
| PASS Time | untested | has its own scoring and a jack whose state does not survive; treat as unsupported until someone tries it |

Mark anything unsupported in the world file and the coordinator will abandon
such a battle instead of resuming it wrongly:

```json
{ "map": "mvm_coaltown", "mode": "mvm", "min_players": 6,
  "ideal_players": 6, "max_players": 6, "migratable": false }
```

The game checks the same thing independently at restore time, so a map pool that
claims one mode and a map that turns out to be another is still caught.

**Practical consequence for front design:** a front whose map pool is arena and
KOTH degrades gracefully when a host drops. A payload front does not. That is a
map-pool decision, not a code one.

## 8. Limits worth knowing before you test

**Migration restores the score, not the round.** Cart position, point ownership,
the round timer, who is alive, uber charge — none of it survives, and none of it
can without engine-level save/restore that a mod does not have. The interrupted
round replays. Players lose a round, not the battle.

**There is no DEPLOY button yet.** Everything is automatic once a deployment
starts, but the trigger is the console command `greyline_deploy`. A player-facing
world map and a DEPLOY button are the remaining UI work; until then, ship a bind:

```
bind "F5" "greyline_deploy any"
```

`game/tc2/cfg/greyline.cfg` is exec'd at startup and holds the coordinator
address, so a player never has to type one.

**Hardware figures are self-reported.** `upload_kbps`, `cpu_score` and
`memory_mb` come from the client. CPU is derived from the real core count and
clock; upload defaults to a deliberately conservative 3000 kbps unless
`greyline_upload_kbps` is set, because guessing high wins elections the machine
cannot honour. Nothing verifies any of it yet, so a modified client could claim
anything. The one input a client cannot forge is its hosting history, which the
coordinator keeps.

**Result signing is off.** `security.require_signed_results` defaults to false,
so a host's scoreline is only checked against what the other players report.
Turning it on requires the HMAC half on the C++ side, which is not written.

**Auth is `dev` mode.** The coordinator trusts the SteamID a client claims.
Real ticket validation needs a publisher Web API key, which needs the project's
own AppID; `gameinfo.txt` still declares Valve's 243750. Test on a network you
control.

**The C++ has never been compiled.** It was written against the tree's own APIs
and cross-checked against the SDK headers, but this machine cannot build the
Source SDK. Expect the first build to find something.
