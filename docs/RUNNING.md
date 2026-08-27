# Running matchmaking on our own AppID

How to get from a fresh build to two clients finding each other and landing on
the same server. Sections 1-4 are the static-server path: one dedicated server
you run. Section 5 is the other path — servers as containers, handed out by the
serveme fork — which is what a community that does not want to maintain
machines should use.

The design behind it is in [`MATCHMAKING.md`](MATCHMAKING.md); this is the
operational half.

## The AppIDs

| | | |
| --- | --- | --- |
| `5147520` | Team Frontress Playtest | the game. Clients and dedicated servers both run **as** this |
| `5147380` | Team Frontress | the main app. The same client build, published under it as well |
| `5150320` | Team Frontress Dedicated Server | a Steam **Tool**. Only how the server payload is shipped and updated |

The distinction bites once: the dedicated package ships in the Tool's depot, but
`steam_appid.txt` inside it says `5147520`, because that is the app the server
is serving. `game_clean/copy_server.sh` writes it, copying the AppID out of the
payload's own `steam.inf`; do not "fix" it.

`game/tc2/steam.inf` carries the same pair and the build version. Steam refuses
to authenticate a server whose `steam.inf` version does not match what the app
expects, so a hand-edited `steam.inf` is worth checking first when nothing
authenticates.

### Two apps, one build

A release is compiled and packaged once, with the playtest AppID in it, and then
published twice. The AppID lives in the content as well as in the SteamPipe
build script -- `tc2/steam.inf`, `tc2/gameinfo.txt`, `tc2/gameinfo_server.txt` --
so between the two uploads CI runs

```bash
./game_clean/retarget_appid.sh <content_dir> 5147380
```

which rewrites those three files and refuses to continue if a stamp did not
take. Which app, which depots and which beta branch the second upload goes to
are the `STEAM_MAIN_APPID`, `STEAM_MAIN_DEPOT_WIN` / `_LINUX` / `_MAC` and
`STEAM_MAIN_BRANCH` repository variables; the defaults are `5147380` and its
three `+1/+2/+3` depots. Set `STEAM_MAIN_APPID` to `none` to publish the
playtest alone.

The playtest is set live on its `prerelease` branch; the main app is **uploaded
only**, and which build goes live there is a click in Steamworks. Point
`STEAM_MAIN_BRANCH` at a branch that exists on the app to change that -- and
only at one that exists: a `setlive` naming a branch the app does not have fails
the commit *after* the content has been uploaded, which is what
`ERROR! Failed to commit build for AppID ... : Failure` at the end of a
successful-looking upload usually means. The other two things it means are a
depot that is not assigned to the app (or assigned only in an app configuration
that was never published) and a builder account that may upload content but not
publish builds. `steampipe.sh` prints all three and stops rather than
re-uploading the content twice more.

Nothing in the game code decides which app it is: `engine->GetAppID()` and
`SteamUtils()->GetAppID()` answer with whatever the client was launched as, and
the inventory request, the friends panel, the server browser and the GC
messages all go through them. What is written down anywhere is only these:

| Where | What it is | Stamped? |
| --- | --- | --- |
| `tc2/steam.inf`, both `gameinfo*.txt` | the app the content belongs to | yes, per upload |
| `steam_appid.txt` in the dedicated payload | the app the server runs as | derived from `steam.inf` by `copy_server.sh` |
| `MOD_APPID` in `launcher_main_tc2.vpc` | which app a build run **outside** Steam attaches to | no -- one binary ships in both depots, and Steam's `SteamAppId` wins whenever it launched us |
| The macOS bundle | -- | no -- editing it breaks its signature; its launcher stamps the staged copy from the AppID Steam launched it with |
| `auth.app_id` / `auth.app_ids` in the coordinator | whose tickets matchmaking accepts | operator config, see below |
| `steamworks/rich_presence_*.vdf` | friends-list tokens | uploaded per app on the partner site -- do both |

Two of those are worth saying out loud.

**Matchmaking.** A Steam auth ticket is only good for the app its client is
running as, so a coordinator that knows one AppID rejects everyone on the other.
`"auth": { "app_id": 5147520, "app_ids": [5147380] }` accepts both; see the
[coordinator README](../services/coordinator/README.md).

**The dedicated server.** It ships under Tool `5150320` and keeps running as
`5147520`. That is the app its GSLT is issued for, the app Steam registers the
server under, and the app whose clients can authenticate on it -- so a server
serves players of one app, not both. To move a server to the main app, stamp
its payload before uploading it:

```bash
./game_clean/retarget_appid.sh game_server_dist 5147380
```

`steam_appid.txt` follows `steam.inf` automatically, and the Tool it ships in
does not change.

## 1. The dedicated server

Two ways to have one. **In a container**, which needs nothing installed on the
machine — that is section 5, and it is the one to use if you are hosting for
other people. **By hand**, which is the rest of this section and is what you
want on a laptop.

Build it, take the `steam-dedicated-linux` artifact from CI, or download
`frontress-dedicated-linux.tar.gz` from a release.

```bash
cd game_server_dist
./steamcmd_update.sh                 # Steam Linux Runtime + TF2's content files
cp ../game/tc2/cfg/frontress_server.cfg tc2/cfg/server.cfg
$EDITOR tc2/cfg/server.cfg           # four values marked CHANGE
./start_dedicated_tc2.sh +map koth_product_final +ip 0.0.0.0 -port 27015
```

`steamcmd_update.sh` installs both dependencies next to the game, and the
launcher looks there first, so there is no Steam install to borrow one from.
`+ip` and `+sv_pure` on that command line are honoured: the launcher only
applies its own defaults when you pass none, which it did not always do — a
server told to bind `0.0.0.0` used to end up on loopback.

The build packs a tarball for the container image with:

```bash
PACK_TARBALL=1 ./game_clean/copy_server.sh   # -> frontress-dedicated-linux.tar.gz
```

It also writes `VERSION` (steam.inf's `PatchVersion`) into the payload, which
is how a container can tell whether the build it has is the build it should
have — Steam is not in that loop for our AppID.

The four values in `server.cfg`:

| | |
| --- | --- |
| `rcon_password` | how the coordinator drives the server. Without it a match can be assigned but never set up |
| `sv_setsteamaccount` | a GSLT for the app the server runs as -- **5147520** unless the payload was stamped for another one -- from [managegameservers](https://steamcommunity.com/dev/managegameservers). Without it players connect with no inventory and the server is invisible to Steam |
| `ip` | the address players are told to connect to. It has to match what the coordinator hands out |
| `tv_*` | SourceTV. It only takes effect at start, which is why it is here and not in the per-match config |

The per-match settings live in the ruleset the coordinator execs before every
map change: [`frontress_casual.cfg`](../game/tc2/cfg/frontress_casual.cfg) or
[`frontress_ranked.cfg`](../game/tc2/cfg/frontress_ranked.cfg), both of which
exec [`frontress_match.cfg`](../game/tc2/cfg/frontress_match.cfg) first. The one line in it worth
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
to `webapi` needs a Steam Web API key and verifies every ticket. List every app
whose players may queue there (`app_id` plus `app_ids`): a ticket is only valid
for the app its client is running as.

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

The HTML menu and the VGUI one are two layouts of the same panel. The menu's
`.res` carries both: everything the web page draws itself sits in an
`if_htmlmenu` block, and the game picks the set of conditions when it loads the
layout, so only one of the two menus is ever up.

```
tf_main_menu_html 0     // the stock TF2 menu (default)
tf_main_menu_html 1     // the HTML menu
```

The HTML menu is off by default: the matchmaking dashboard is a separate panel
that sits above everything on the main menu, and it hangs over the web page.
Until that is sorted out the stock menu is the one that works.

It takes effect immediately and is archived, so it survives a restart. Nothing
is destroyed when it is switched; the menu layout is reloaded, the web panel is
hidden, and the page is told the menu closed so anything it was polling stops.

The VGUI menu is three things: the matchmaking dashboard's top bar (find a
game, host, quit), the column of buttons on the left, and the row of small
glyph buttons along the bottom (server browser, options, advanced options,
character setup).

The column comes from `resource/gamemenu.res`, which ships **loose**, in
`game/tc2/loose/resource/`. That is deliberate: `pak1.vpk` is not built from
this repo, it is a prebuilt download (`game_clean/dlpak.sh` pulls
`PAK_VERSION` from `mastercomfig/tc2-pak`), it has never carried a
`gamemenu.res`, and without one the menu falls through to Team Fortress' own,
which does not match our panels. Everything in `loose` is mounted ahead of the
pak, so it is also the only way to override a pak file without republishing the
pak.

Same reason `CHudMainMenuOverride::UpdateMainMenuChrome` sets the visibility
and z-order of the pieces the web page draws itself, instead of leaving it to
the `if_htmlmenu` blocks in `game_src/tc2/pak1/resource/ui/mainmenuoverride.res`:
edits there do not reach the game until someone rebuilds the pak and bumps
`PAK_VERSION`. The two say the same thing, so when the pak is rebuilt nothing
changes.

Both menus reach the same matchmaking: the dashboard is its own panel and is
not part of either. Everything the stock menu does not handle itself is passed
through to GameUI, which is where the server browser, the options dialogs and
the create-server dialog live.

Two things stay running with the HTML menu off, harmlessly: the local Crow
HTTP server on `127.0.0.1:58270` that serves the page, and the websocket
bridge. Neither costs anything when nobody is connected to them.

### The information column

Down the right of the VGUI menu, where Team Fortress puts its MOTD, are the
play buttons and three cards.

**Find a game, host a game, server browser** sit at the top of the column. They
are built in code, so they are there whatever `GameMenu.res` does or does not
contain -- which is the whole reason they are on this side of the screen and
not in the menu's own button list. The first one follows the queue: while a
search is running it turns red and cancels it.

- **Campaign** -- the war line: nodes coloured by who holds them, the edges
  between them, and a pulsing ring on the front that is live.
- **Matchmaking** -- what the queue is doing. While queued it shows the match
  group, how long you have been waiting, how many players are in the queue and
  how many more the coordinator needs, with a bar that fills as they arrive.
  Along the bottom is the population: players online, matches live, servers
  free. That line comes from `GET /v1/status`, polled every 30 seconds while
  you are out of a game, and says so plainly when the coordinator does not
  answer.
- **News** -- whatever is in the news file.

The buttons on the left, if there are any, come from `GameMenu.res` and are the
rest of the menu: resume, change server, character setup, and the in-game ones.
When that file cannot be loaded the game says so in the console --
`Could not load Resource/GameMenu.res` -- and `developer 1` adds a line saying
how many entries it did find.

The cards are drawn in code (`src/game/client/tf/frontress/tf_mainmenu_info.cpp`)
rather than laid out in a `.res`, because the menu's `.res` is in a pak we do
not build. What they draw is not in the code:

```
game/tc2/loose/resource/ui/frontress_campaign.res
game/tc2/loose/resource/ui/frontress_news.res
```

Both ship loose, so they can be edited in an installed game. `x` and `y` on a
campaign node are 0..1 across the map area, so a line drawn there survives any
resolution. Delete the `front` block and the map draws quiet.

```
tf_mainmenu_info_reload
```

re-reads both without restarting.

The campaign file is a demo -- it is `services/coordinator/theater.example.json`
laid out by hand. Its shape is the war layer's own shape (nodes, edges, the
live front), which is the point: `wire.WarStatus.ActiveFronts` already carries
exactly this, so pointing the panel at the coordinator later is a change to
where the data comes from, not to what the panel knows.

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
`tf_main_menu_html 0` -- the default -- there is a button for it as well, in
the row along the bottom of the menu.

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

### Ranked

`tf_mm_queue 2` is the ladder group. It plays `frontress_ranked.cfg` -- class
limits, no random crits, no damage spread, the competitive whitelist, no votes
-- and its queue has entry rules the open queue does not:

```json
"restrictions": {
  "max_party_size": 3,
  "min_matches_played": 5,
  "abandon_cooldown_mins": 30
}
```

A refused queue answers 403 with the reason in words: "you need 5 finished
matches for Ranked 6v6 and have 2". While testing, drop the block or set
`min_matches_played: 0` -- otherwise nobody can queue for it on a fresh
coordinator, which looks exactly like a broken queue.

The records those rules read live in `players.jsonl` and survive a restart. To
forget everyone, delete the file.

## 5. Servers from serveme: the container path

Nothing above scales past the machines you are willing to set up by hand. The
[serveme fork](https://github.com/gru2007/serveme-frontress) is the answer to
that: it hands out **containers**, and the container image carries the game
payload, the Steam Linux Runtime, TF2's content files, the rulesets and the
result-reporting agent. A host that can run a container can host a match
without knowing anything about the game.

Run the site:

```bash
git clone https://github.com/gru2007/serveme-frontress
cd serveme-frontress
cp .env.example .env
$EDITOR .env          # SECRET_KEY_BASE, STEAM_API_KEY, POSTGRES_PASSWORD, SITE_URL
docker compose up -d
```

Then in `.env`, the parts that make it host game servers:

```bash
FRONTRESS_LOCAL_DOCKER=1                 # this machine runs the containers
DOCKER_GID=999                           # getent group docker | cut -d: -f3
DOCKER_HOST_IP=203.0.113.10              # this machine, as a container sees it
CLOUD_CALLBACK_HOST=203.0.113.10:3000
FRONTRESS_COORDINATOR_URL=http://203.0.113.10:27100
FRONTRESS_COORDINATOR_SECRET=<the coordinator's own `secret`>
```

Give the coordinator an API key:

```bash
docker compose exec web bin/rails frontress:coordinator_key
```

That prints the provider block to paste into `coordinator.json`:

```json
{ "kind": "serveme", "region": "eu",
  "base_url": "https://serveme.example.org",
  "api_key": "...", "prefer_docker": true, "reserve_mins": 120 }
```

Providers are tried in order, so keeping a `static` entry after it is a
fallback for when the site is down.

What happens per match, and why each step is there:

```text
   coordinator forms a match
        |  find_servers            container hosts are listed first
        |  POST /api/reservations  match_id, match_mode, first_map, password
        v
   serveme starts a container, pushes reservation.cfg into it
        |
   coordinator polls the reservation until it is "Ready"
        |  a container takes ~30s; RCON to an address that is not listening
        |  yet would be read as a broken server and requeue everybody
        v
   RCON: sv_password, sv_tags tfmm:<id>, maxplayers, exec <ruleset>, changelevel
        |
   players connect; greyline-agent in the container heartbeats the match
        |
   game over -> agent POSTs /v1/gs/result -> coordinator ends the reservation
        -> the container is destroyed
```

Ranked reservations are refused unless the API user is in serveme's Trusted API
group, which `frontress:coordinator_key` puts the coordinator in. A ranked
server booked by hand is a match with no roster, and any result it reports is a
result against players who never agreed to play it.

## 6. Steam: parties, invites, joining

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

## 7. Filling servers while they run

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

## 8. Inventory

Short answer: **your change was the right one, and it should work.**

`https://www.teamfortress.com/webapi/ISDK/GetInventory/v0001` is Valve's own
endpoint for Source SDK 2013 mods, not a TC2 service. The client asks Steam for
a `GetAuthTicketForWebApi("tf2sdk")` ticket, sends it with the AppID the client
is running as (`engine->GetAppID()`, so 5147520 or 5147380), and
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
| ranked refuses to queue | its restrictions: matches played, party size, a cooldown. The 403 says which |
| matches never end on their own | no agent on the server; it ends on the idle or match timeout instead |
| serveme reservation times out | the container did not come up. `docker compose logs` on the host, and the reservation page shows the phase it stalled in |
| "no server" in the coordinator log | RCON password wrong, or the server is not up |
| connected but on the wrong map | server hibernating; `sv_hibernate_when_empty 0` |
| connected but kicked for the password | something set `sv_password` after the coordinator did |
| players have no items | GSLT missing, or the inventory endpoint refused |
| friends cannot join | `tf_mm_party_autocreate 0`, or they are not on your friends list and the lobby is friends-only |
| HTML menu is blank, or the dashboard hangs over it | `tf_main_menu_html 0` (the default) uses the stock TF2 menu |
| server list is empty | the HTML menu's list points at `api.teamcomtress.com`. Use `openserverbrowser` |

`tf_mm_debug 1` prints every state change and HTTP round trip on the client;
`-log-level debug` does the same on the coordinator.
