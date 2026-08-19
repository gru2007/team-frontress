# Testing the GREYLINE MVP

Four levels, each one adding a real piece. Do them in order: every level tells
you which half is broken when the next one misbehaves.

| Level | What runs for real | What is faked | Takes |
| --- | --- | --- | --- |
| 0 | the coordinator's own logic | everything else | 30 seconds |
| 1 | coordinator, HTTP, war | the game entirely | 2 minutes |
| 2 | + a real srcds and the agent | the players | 15 minutes |
| 3 | + real players | nothing | an evening |

Level 1 is the one to run most: it plays a whole campaign in a couple of
minutes, which is the only practical way to see whether the war is any good.

---

## Level 0 — the tests

```bash
cd services/coordinator
make check
```

That is four things, and each can be run on its own.

**`make race`** — the war. The stage rule, capture and collapse, the
counter-offensive, defensive mobilization, campaign end, log replay, the log
parser, the side/team translation, map rotation, and one end-to-end run over
HTTP: four clients deploy, a server takes the battle, a result moves the front.

**`make game-test`** — the game's own logic, built with a plain `g++` and no
Source engine at all. `greyline_briefing_logic.cpp` deliberately includes
nothing from the tree, which is what makes this possible:

```bash
cd src/game/shared/greyline/tests
make test     # ~1 second
make asan     # the same, under AddressSanitizer and UBSan
```

It asserts the briefing a given battle produces line by line — including the one
that tells a RED player they are wearing BLU — then reads the shipped
`greyline_%language%.txt` files the way the engine does and replays every
briefing through them. That catches the three ways the two halves drift apart
without either failing to build: a token nobody translated (the player reads
`#Greyline_Chat_Stage`), a `%sN` the server never sends (the player reads a
blank), and a sentence longer than the client's 256-character buffer (the player
reads half of it).

**`make check-seams`** — the joins. The agent drives the game by typing convar
names into a console; nothing else notices when one is renamed, and the symptom
is a blank briefing during a live battle. This reads both sides as text and
insists they agree, along with the stage kinds, the VPC file lists, and the
language file the client loads.

**`make check-maps`** — the theater. Envelopes, mode coverage, and a pool size
per stage large enough to rotate. Point it at a real install to check the maps
are actually there, which is worth doing before an evening rather than during
one:

```bash
make check-maps MAPS=~/.steam/steam/steamapps/common/Team\ Fortress\ 2/tf/maps
```

## Level 1 — a whole war, no game at all

Start a coordinator on the testbed settings:

```bash
cd services/coordinator
make build
./bin/coordinator -config gc.test.json
```

Then play a war against it:

```bash
./tools/greyline_sim.py --players 4 --battles 40
```

The simulator is a real HTTP client: real sessions, real DEPLOY, a real pool
registration, real results. Only the game is missing — no map ever loads.

```
before: RED 3 · BLU 3 · neutral 1 | BATTLE FOR RAIL YARD [BLU 1/3]

[ 17] BATTLE FOR BLU COMMAND  breakthrough 1/3  arena_well (arena)
      -> BLU BROKE THE RED OFFENSIVE AT BLU Command
      ** the offensive collapsed
[ 19] BATTLE FOR BLU COMMAND  breakthrough 1/3  arena_well (arena)
      -> RED BREAKTHROUGH COMPLETE AT BLU Command — NEXT: ADVANCE
[ 21] BATTLE FOR BLU COMMAND  assault 3/3  arena_well (arena)
      -> RED WINS THE SECOND GRAVEL WAR — CAMPAIGN 01
      ** CAMPAIGN OVER — RED wins

after:  RED 5 · BLU 1 · neutral 1 | none
```

Useful runs:

```bash
# The fastest way to see a capture and the front move along the graph.
./tools/greyline_sim.py --winner attacker --battles 6

# Watch a defence break an offensive and take the initiative.
./tools/greyline_sim.py --winner defender --battles 6

# Twenty people online: the war should open a second front.
./tools/greyline_sim.py --players 20 --servers 2 --battles 20

# Repeat exactly the same war.
./tools/greyline_sim.py --seed 7 --battles 40
```

### What to check

- **A win moves a stage, not a percentage.** `breakthrough 1/3` becomes
  `advance 2/3`, and the map changes with it.
- **A loss pushes back rather than resetting**, and a loss at the bottom
  collapses the offensive and hands the other side a counter-attack.
- **A capture moves the front.** `BATTLE FOR RAIL YARD` finishing should be
  followed by a front on a node adjacent to the one just taken — not silence.
- **Small queues get small formats.** At four players every battle is arena or
  koth even at the ADVANCE stage: the stage is strategic, the format follows the
  population. Run with `--players 20` and the same stage becomes 5CP and payload.
- **The map changes between battles.** Thirty battles at four players should
  draw five or six different maps, and never the same one twice running on one
  front. One map all evening is a bug, not a small pool — count them:

  ```bash
  ./tools/greyline_sim.py --players 4 --battles 30 \
    | grep -oE '  [a-z]+_[a-z0-9_]+ \(' | sort | uniq -c | sort -rn
  ```
- **The war survives a restart.** Stop the coordinator, start it again, and
  `GET /api/v1/status` reports the same campaign and revision. Delete
  `war-events.jsonl` to start a new war.

```bash
curl -s localhost:27100/api/v1/status | python3 -m json.tool
curl -s 'localhost:27100/api/v1/world/timeline?limit=20' | python3 -m json.tool
curl -s localhost:27100/api/v1/world/fronts | python3 -m json.tool
```

The timeline is the whole war in one file — it is also `war-events.jsonl`, which
is readable by eye:

```bash
grep '"kind":"node_captured"' war-events.jsonl | python3 -m json.tool
```

## Level 2 — a real game server

Now put a real srcds behind the pool. The players are still faked, so this level
answers exactly one question: does the agent drive the server and read the
result correctly?

**1. Start a dedicated server** with RCON on:

```bash
./srcds_run -game tf2 +map ctf_2fort +maxplayers 24 \
    +rcon_password "$RCON" +sv_lan 0 +ip 0.0.0.0 -port 27015
```

**2. Start the agent** next to it:

```bash
cd services/coordinator
./bin/greyline-agent \
  -gc http://127.0.0.1:27100 \
  -key testbed-pool-key-change-me \
  -connect 127.0.0.1:27015 \
  -rcon 127.0.0.1:27015 -rcon-password "$RCON" \
  -log-listen 127.0.0.1:27500 \
  -idle-map ctf_2fort \
  -log-level debug
```

It should print `joined the server pool` and
`listening to the game server log`. If it does not, nothing else will work —
see the table at the bottom.

**3. Check the coordinator sees it:**

```bash
curl -s localhost:27101/servers | python3 -m json.tool
```

**4. Make it host a battle** — the simulator's players are enough, because the
battle only needs a roster to exist:

```bash
./tools/greyline_sim.py --players 4 --battles 1 --winner attacker
```

Watch the agent: it should set the password and the roster, `changelevel` to the
assigned map, report **ready**, and the server should actually be on that map.

Because no real player joins, the battle will not produce a `Game_Over` on its
own — the simulator reports the result instead. To test the agent's own result
path, end the round by hand over RCON:

```bash
rcon mp_winlimit 1
rcon mp_forcewin 2      # 2 = RED, 3 = BLU
```

The agent should log `battle over` with a scoreline, and the coordinator should
log `battle recorded`.

### What to check

- `sv_password` is set while the battle runs and cleared afterwards.
- The server goes back to the idle map when the battle ends.
- `greyline_front_name`, `greyline_stage`, `greyline_stage_kind` are set on the
  server (`rcon greyline_front_name`).
- Killing the agent takes the server out of the pool within `offline_after`.

## Level 3 — real players

Four people, one server, one coordinator.

**1.** Everyone runs the game. The main menu now *is* the test UI: a war map,
the active fronts, and a DEPLOY button. It is a plain page —
`game/tc2/loose/resource/html/greyline.html` — and the menu loads it because of
`greyline_menu_page`. Set that convar to `ui/index.html` to get the stock menu
back.

**2.** In the menu: point **Coordinator** at the machine running it, put in a
SteamID64 (dev auth trusts whatever is claimed — the RANDOM ID button makes a
throwaway one), pick a side, press **CONNECT**, then **DEPLOY**.

With **auto-join** ticked, the page runs `password` and `connect` for you the
moment the battle server is up. Untick it to press JOIN BATTLE yourself.

The same page opens in an ordinary browser during development:

```bash
cd game/tc2/loose/resource/html && python3 -m http.server 8765
# then open http://localhost:8765/greyline.html
```

There it cannot drive the console, so it prints the `password` / `connect` line
to copy instead. Everything else — map, DEPLOY, queue, result — is identical.

### Without the menu

If you would rather not start the game at all, deploy real accounts with the
simulator using their SteamID64s:

```bash
./tools/greyline_sim.py --players 4 --battles 1 --winner attacker
```

…or by hand with curl, one per player:

```bash
TOKEN=$(curl -s localhost:27100/api/v1/client/hello \
  -H 'Content-Type: application/json' \
  -d '{"steam_id":76561198000000000,"name":"me","side":"RED"}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])')

curl -s localhost:27100/api/v1/client/deploy \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"accept_contract":true}'

curl -s "localhost:27100/api/v1/client/poll?since=0&wait=30" \
  -H "Authorization: Bearer $TOKEN" | python3 -m json.tool
```

The poll reply carries `connect` and `password`. In the game console:

```
password <the password>
connect <the address>
```

**3.** Play. At round start each player should see, in their own language:

```
▌ GREYLINE: BATTLE FOR FOUNDRY 17
▌ ADVANCE — stage 2 of 3, RED on the offensive
▌ Push the offensive deeper into Foundry 17.
```

plus one line in the middle of the screen, once.

### The colours

On payload and attack/defend maps the map itself decides that **BLU attacks**.
So when RED is on the offensive, RED's mercenaries play in BLU uniforms: blue
spawn, blue half of the scoreboard, "BLU WINS" at the end. Nothing can repaint
a map, so the game says it out loud instead:

```
▌ You fight for RED here. This battlefield is built for BLU to attack, so you
  wear BLU colours: the scoreboard and the end-of-round banner will say BLU.
```

Check that this line appears on `advance` and `assault` stages when RED attacks,
and **does not** appear on 5CP, KOTH or arena, where the two agree. The war is
never confused by it: the coordinator translates the reported scoreline back
into war sides before it touches a front. Verify that by winning as the
BLU-coloured RED team and confirming the post-match headline says RED.

### What to check

- The war map in the menu shows the same owners as `GET /api/v1/world`, and the
  front redraws within a few seconds of somebody else's battle finishing.
- Players end up on the team the coordinator assigned, and switching team puts
  them back within a second.
- Somebody without the password cannot get in.
- After the battle everyone is back on the map, and the front has moved.
- Two players deploying again should get the *next stage* of the same offensive.

---

## When something does not work

| Symptom | Where to look |
| --- | --- |
| `pool.key is empty` at startup | set `GREYLINE_POOL_KEY` or `pool.key` |
| Simulator: `no battle was formed` | not enough queued players for `min_team_size`, or no server in the pool — check `curl localhost:27101/servers` |
| Players queue forever, coordinator logs `formations_without_a_server` | every server is busy or offline; `offline_after` may have retired one |
| Agent: `could not reach the game server over RCON` | `rcon_password`, and srcds bound where you think it is |
| Agent joins but a battle never goes `ready` | the log stream is not arriving: check `-log-advertise` is an address srcds can reach, and `rcon logaddress_list` |
| Battle runs forever, no result | the mode's win conditions did not end it; check `mp_winlimit` / `mp_maxrounds` after the changelevel |
| Result reported but the war does not move | coordinator logs `battle did not advance the war` with a reason — usually a front that was already decided |
| Briefing shows raw `#Greyline_...` tokens | `resource/greyline_%language%.txt` is not being loaded on the client |
| Menu is blank or shows the stock TC2 menu | `greyline_menu_page` — it must be `ui/greyline.html`, and the file must be in `tc2/loose/resource/html/` |
| Menu is a blue error page with `-324` (`ERR_EMPTY_RESPONSE`) | something else on this machine holds `127.0.0.1:58270`, the port the game serves the menu on. From the host shell: `curl -sv http://127.0.0.1:58270/ui/greyline.html`. A second copy of the game is the usual answer |
| Menu says "could not reach the coordinator" | the address in its settings, and that the coordinator is listening on something the game can route to |
| Menu works in the game but not in a browser | that is CORS; the coordinator sends `Access-Control-Allow-Origin: *`, so check you are not on an old build |
| Every battle is arena at any population | the front's profile has no battlefield whose envelope fits; widen `min_players` in the theater |

Two logs answer most questions:

```bash
# what the coordinator decided
./bin/coordinator -config gc.test.json -log-level debug

# what the war did about it
tail -f war-events.jsonl
```
