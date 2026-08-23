# Running matchmaking on our own AppID

How to get from a fresh build to two clients finding each other and landing on
the same server. Everything here is the static-server path: one dedicated
server you run, no serveme.tf yet.

The design behind it is in [`MATCHMAKING.md`](MATCHMAKING.md); this is the
operational half.

## The AppIDs

| | | |
| --- | --- | --- |
| `5147520` | Team Frontress Playtest | the game. Clients and dedicated servers both run **as** this |
| `5150320` | Team Frontress Dedicated Server | a Steam **Tool**. Only how the server payload is shipped and updated |

The distinction bites once: the dedicated package ships in the Tool's depot, but
`steam_appid.txt` inside it says `5147520`, because that is the app the server
is serving. `game_clean/copy_server.sh` writes it; do not "fix" it.

`game/tc2/steam.inf` carries the same pair and the build version. Steam refuses
to authenticate a server whose `steam.inf` version does not match what the app
expects, so a hand-edited `steam.inf` is worth checking first when nothing
authenticates.

## 1. The dedicated server

Build it, or take the `steam-dedicated-linux` artifact from CI.

```bash
cd game_server_dist
./steamcmd_update.sh                 # pulls the SDK Base 2013 DS dependencies
cp ../game/tc2/cfg/frontress_server.cfg tc2/cfg/server.cfg
$EDITOR tc2/cfg/server.cfg           # four values marked CHANGE
./start_dedicated_tc2.sh +map koth_product_final +ip 0.0.0.0 -port 27015
```

The four values in `server.cfg`:

| | |
| --- | --- |
| `rcon_password` | how the coordinator drives the server. Without it a match can be assigned but never set up |
| `sv_setsteamaccount` | a GSLT for **5147520** from [managegameservers](https://steamcommunity.com/dev/managegameservers). Without it players connect with no inventory and the server is invisible to Steam |
| `ip` | the address players are told to connect to. It has to match what the coordinator hands out |
| `tv_*` | SourceTV. It only takes effect at start, which is why it is here and not in the per-match config |

The per-match settings live in [`frontress_match.cfg`](../game/tc2/cfg/frontress_match.cfg),
which the coordinator execs before every map change. The one line in it worth
knowing about is `sv_hibernate_when_empty 0` — a hibernating server defers the
map change until somebody connects, so the first player to arrive lands on the
*previous* map and nothing looks wrong anywhere else.

Do not set `sv_password` in either file. The coordinator sets it per match, and
overwriting it locks the players out of their own game.

## 2. The coordinator

```bash
cd services/coordinator
cp coordinator.example.json coordinator.json
$EDITOR coordinator.json
go run ./cmd/coordinator -config coordinator.json -log-level debug
```

The example is already the static path. What to change:

```json
"secret": "a long random string",
"pool": { "providers": [ { "kind": "static", "servers": [
  { "name": "eu1", "connect": "203.0.113.10:27015",
    "rcon": "the rcon_password from server.cfg",
    "stv":  "203.0.113.10:27020" } ] } ] }
```

Leave `auth.mode` as `dev` while testing on a LAN. It believes whatever SteamID
a client claims — fine on your own network, not fine on the internet. Switching
to `webapi` needs a Steam Web API key and verifies every ticket.

Check it:

```bash
curl -s localhost:27100/v1/status | jq
```

`free_servers` should be `1`. If it is `0`, the pool has nothing — check the
`providers` block.

## 3. The client

```
tf_mm_coordinator "http://192.0.2.5:27100"
tf_mm_debug 1
tf_mm_status
```

`tf_mm_status` should print `active: yes`. If it says no, Steam is not logged
on or `tf_mm_enable` is `0`.

## 4. Finding a game, from the main menu

The path from clicking a button to being on a server, in the order it happens:

```text
  HTML main menu, "find a game" card
        |  websocket bridge:  mmcmd find_game
        v
  CTFMatchmakingDashboard::OnCommand("find_game")
        |  the backend is up, so: the stock playlist, not quickplay
        v
  Playlist  ->  Casual  ->  map selection  ->  Start Search
        |
        v
  CTFPartyClient::RequestQueueForMatch()
        |  reliable message towards a GC that is not there
        v
  CTFMMBackend::BHandleClientMsg( k_EMsgGCParty_QueueForMatch )
        |  POST /v1/queue   { match_group, leader, players[], maps[] }
        v
  coordinator: queue -> form -> reserve a server -> RCON it -> assigned
        |  GET /v1/queue/{ticket}  every 2s
        v
  CTFMMBackend writes CTFGSLobby into the local SO cache
        |  the stock UI sees a live match
        v
  password <match password>; connect <ip:port>
```

Two things about that path are worth knowing.

**Casual refuses to queue with no maps selected.** That is stock TF2 behaviour
(`BCanQueueForMatch`), not ours. The Start Search button stays disabled and the
reason is shown on hover.

**The map you get is not always the one you picked.** The coordinator takes the
maps every party in the match agreed on; when preferences do not overlap it
falls back to the union, and then to the group's own list. With the war enabled
it ignores preferences entirely and plays what the front demands.

### The stock TF2 menu instead of the HTML one

The HTML main menu is drawn on top of Team Fortress' own VGUI menu, which is
still built and still works underneath it.

```
tf_main_menu_html 0     // the stock TF2 menu
tf_main_menu_html 1     // the HTML menu (default)
```

It takes effect immediately and is archived, so it survives a restart. Nothing
is destroyed when it is off; the web panel is only hidden, and the page is told
the menu closed so anything it was polling stops.

Both menus reach the same matchmaking: the dashboard is its own panel and is
not part of either. Everything the stock menu does not handle itself is passed
through to GameUI, which is where the server browser, the options dialogs and
the create-server dialog live.

Two things stay running with the HTML menu off, harmlessly: the local Crow
HTTP server on `127.0.0.1:58270` that serves the page, and the websocket
bridge. Neither costs anything when nobody is connected to them.

### The server browser

The HTML menu's own server list is not usable: it fetches from
`api.teamcomtress.com`, which is Team Comtress' service and not ours. It lives
inside the built Astro bundle, so fixing it means rebuilding the menu from its
sources.

Team Fortress' own server browser has nothing to do with either menu — it is
GameUI's, it works, and it can be opened from the console:

```
openserverbrowser
opencreateserverdialog
```

Those are the same thing as `gamemenucommand openserverbrowser`, which also
works and is what the dashboard's own button runs. Under `SOURCESDK` the stock
menu additionally has a `ServerBrowserButtonSDK`, so with
`tf_main_menu_html 0` there is a button for it as well.

Note that a matchmade server is passworded, so it will not accept a connection
from the browser. That is deliberate.

### Doing it without the menu

```
tf_mm_queue 7        // 7 = casual 12v12, the ETFMatchGroup value
tf_mm_cancel
tf_mm_join           // connect to the assigned match by hand
tf_mm_watch          // connect to the match's SourceTV relay instead
tf_mm_status
```

Setting `min_players: 2` and `patient_secs: 0` on the casual group lets one
player form a match, which is the fastest way to prove the whole path works
before you have a second person.

## 5. Steam: parties, invites, joining

The party is a Steam lobby. There is no party service anywhere — Steam is the
party service.

| | |
| --- | --- |
| `tf_mm_party_create` | host a party lobby |
| `tf_mm_party_invite` | the Steam overlay invite dialog |
| `tf_mm_party_join <lobbyid>` | join by id |
| `tf_mm_party_leave` | leave |
| `tf_mm_party_type` | 0 invite-only, 1 friends, 2 public |

`tf_mm_party_autocreate` (on by default) hosts a lobby as soon as matchmaking
comes up, so a friend can always invite you or join you. Retail hides this by
having the GC put everyone in a party of one; we host one instead.

**Join Game** in the friends list works through rich presence. The client
publishes `+connect_lobby <id>` as its connect string, so Steam launches the
joiner's game with that argument, `secure_command_line.cpp` accepts it, and they
land in the lobby. It works whether or not their game was already running.

**Invites** are the Steam overlay's own dialog, which produces the same
`+connect_lobby`.

**Party members do not talk to the coordinator.** The leader queues for
everybody and publishes the result into the lobby data (`connect`, `password`,
`match_id`, `stv`, `teams`). Members act on that. So a member who joins the
party mid-queue still gets pulled into the match.

**Rich presence** was already fully built in `clientmode_tf.cpp` and reads the
party and lobby objects — which the backend now supplies. Searching, playing,
which match group, which map, and party grouping in the friends list all light
up on their own. The only thing we changed is where the connect string points.

### SourceTV

The coordinator reports the relay address in the assignment (`stv`), from the
static server's `stv` field or from a serveme reservation's TV port. Anyone in
the match can `tf_mm_watch` to spectate. The relay is not password-protected by
the match password — spectating is not playing.

## 6. Filling servers while they run

Casual Frontline is `"mode": "frontline"`, which means matches keep taking
players after they start. A match that formed as a 4v4 because that is who was
in queue becomes a 12v12 as people arrive, instead of a second half-empty server
starting next to it. That is the behaviour that matters at a peak of thirty-odd
players.

The rule that keeps it balanced: a party may only join a team that has room
under half the match. Neither side can grow past half, so they cannot drift
apart.

| | |
| --- | --- |
| `backfill_secs: 0` | fill for as long as the match runs (default) |
| `backfill_secs: 600` | stop accepting after ten minutes |
| `backfill_secs: -1` | never backfill, even in frontline mode |

`"mode": "ranked"` forms a roster once and leaves it alone. Nobody is added to a
ranked match after it starts.

Both modes report their result to the war the same way. Scoring is not built.

## 7. Inventory

Short answer: **your change was the right one, and it should work.**

`https://www.teamfortress.com/webapi/ISDK/GetInventory/v0001` is Valve's own
endpoint for Source SDK 2013 mods, not a TC2 service. The client asks Steam for
a `GetAuthTicketForWebApi("tf2sdk")` ticket, sends it with `appid=5147520`, and
gets back a serialized SO cache that goes into the local player's inventory with
`AddLocalSOCache`. The old `api.teamcomtress.com` URL was Team Comtress' own
relay, which we cannot use and which is presumably why nothing worked.

What to check if it still does not:

1. `mod_inventory_request_timeout` defaults to 300s. A failure backs off and
   retries, so watch for `Steam inventory request failed` in the console rather
   than expecting an immediate error.
2. `Received unexpected result code N` means Valve answered and refused. The
   endpoint keys off the AppID, so this is where an unregistered or
   not-yet-approved AppID would show up. That is a Valve-side question, not a
   code one.
3. The inventory refresh **re-subscribes the SO cache**, which destroys
   everything in it — including the matchmaking party and lobby objects. The
   backend listens for that and republishes. If party state ever vanishes a few
   seconds after login, that is the interaction to look at.

Unrelated but worth knowing: the HTML main menu's server browser still fetches
`https://api.teamcomtress.com/servers`, which is not ours. It is inside the
built Astro bundle (`game/tc2/loose/resource/html/_astro/ServerBrowser.*.js`),
so fixing it means rebuilding the menu from its own source. Until then,
`openserverbrowser` opens Team Fortress' own. The matchmaking path does not
touch either.

The menu's assets come from `pak1.vpk`, which `game_clean/dlpak.sh` downloads
from `mastercomfig/tc2-pak` releases. It is a build-time dependency on someone
else's repository rather than a runtime service, but it is a dependency.

## When it does not work

| symptom | first thing to check |
| --- | --- |
| `tf_mm_status` says `active: no` | Steam not logged on, or `tf_mm_enable 0` |
| Start Search is greyed out | no maps selected in the casual panel |
| queue never finds anything | `curl /v1/status` — is `free_servers` above zero? |
| "no server" in the coordinator log | RCON password wrong, or the server is not up |
| connected but on the wrong map | server hibernating; `sv_hibernate_when_empty 0` |
| connected but kicked for the password | something set `sv_password` after the coordinator did |
| players have no items | GSLT missing, or the inventory endpoint refused |
| friends cannot join | `tf_mm_party_autocreate 0`, or they are not on your friends list and the lobby is friends-only |
| HTML menu is blank or broken | `tf_main_menu_html 0` falls back to the stock TF2 menu |
| server list is empty | the HTML menu's list points at `api.teamcomtress.com`. Use `openserverbrowser` |

`tf_mm_debug 1` prints every state change and HTTP round trip on the client;
`-log-level debug` does the same on the coordinator.
