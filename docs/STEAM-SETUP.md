# Connecting the game to Steam for real

Everything in the war that depends on knowing *who* a player is — auth tickets,
bans, verified rosters, signed results later — hangs off one thing: the
coordinator being able to prove a SteamID instead of believing one. This is the
whole path from "auth_mode=dev on a LAN" to that, in the order it has to happen.

Related: `docs/STEAMPIPE.md` is how builds get to Steam. This is how the game
talks to Steam once they are there.

## What the pieces are

```
game client                      coordinator                    Steam
  GetAuthTicketForWebApi   ──►  /client/hello  ──►  ISteamUserAuth/
     ("greyline")                 ticket + id       AuthenticateUserTicket
                                                     appid 5147520
                                                     identity "greyline"
                                                     publisher key
                            ◄──  proven SteamID  ◄──
                                       │
                                  ban list, roster, war
```

Four values must agree or every login is refused, with the same message a
forged ticket gets:

| Value | Client | Coordinator |
| --- | --- | --- |
| AppID | `MOD_APPID` / `gameinfo.txt` → 5147520 | `auth.app_id` |
| Identity | `kGreylineTicketIdentity` in `greyline_menu_rpc.cpp` → `"greyline"` | `auth.identity` |
| Ticket call | `ISteamUser::GetAuthTicketForWebApi` | — |
| Publisher key | — | `GREYLINE_STEAM_WEBAPI_KEY` |

## Step 1 — the AppID

Already done: the client runs as **Team Frontress Playtest, 5147520**. See
`docs/STEAMPIPE.md` for the four files that decide this. Dedicated servers are
their own app — step 6.

The coordinator defaults `auth.app_id` to the same number. If you point a
coordinator at a different app, change it in both places.

## Step 2 — a publisher Web API key

`ISteamUserAuth/AuthenticateUserTicket` needs a **publisher** key, not the
ordinary per-account key from `steamcommunity.com/dev/apikey`. That one cannot
validate tickets.

Steamworks → **Users & Permissions** → **Manage Groups** → your publisher group
→ **Create Web API Key**. Only an account with publisher-level permission sees
this. The key covers every app the publisher owns, so treat it like a root
password: it goes in the environment, never in the config file and never in the
repository.

```bash
export GREYLINE_STEAM_WEBAPI_KEY="…"
```

## Step 3 — turn the coordinator on

```json
{
  "auth": {
    "mode": "webapi",
    "app_id": 5147520,
    "identity": "greyline",
    "reject_vac_banned": true,
    "require_ownership": true,
    "ticket_cache_ttl": "10m"
  },
  "bans_path": "bans.jsonl"
}
```

The coordinator refuses to start on `mode=webapi` without a key or an app id,
rather than quietly falling back to trusting clients.

Three things change the moment this is on:

- **Rosters become enforceable.** `Assignment.VerifiedIdentities` goes true, so
  a game server may turn away anyone not on the roster. Under dev auth it must
  not, because the roster is full of SteamIDs the server will never see.
- **VAC and publisher bans are honoured** (`reject_vac_banned`).
- **Abandon bans start biting** — see below.

## Step 4 — the ticket the client sends

The client asks Steam for the ticket with `GetAuthTicketForWebApi`, not
`GetAuthSessionTicket`. This is not a preference. Steam's own header says of
the session ticket:

> not to be used for "ISteamUserAuth\AuthenticateUserTicket" - it will fail

and it fails with the same rejection a forged ticket gets, so a client on the
wrong call is indistinguishable from an attack in the coordinator's log. The
web API ticket is asynchronous: `greyline_identity` answers with
`ticket_pending: true` until Steam calls back, and the menu page waits for it
before connecting.

If logins fail here, the useful test is to take the ticket out of the client
log and validate it by hand:

```bash
curl -s "https://api.steampowered.com/ISteamUserAuth/AuthenticateUserTicket/v1/\
?key=$GREYLINE_STEAM_WEBAPI_KEY&appid=5147520&identity=greyline&ticket=<hex>"
```

Which answer means what:

| Response | Cause |
| --- | --- |
| `params.result: OK` | the ticket is fine; the problem is elsewhere |
| `error.errorcode: 3` | malformed ticket — usually a session ticket, see above |
| `error.errorcode: 101` | invalid ticket, or the wrong `identity` |
| `error.errorcode: 102` | the account does not own 5147520 |
| HTTP 403 | the key is not a publisher key, or not for this app |

## Step 5 — bans

There are two kinds, they do different jobs, and both are worth having.

| | Coordinator ban | Steam game ban (*игровая блокировка*) |
| --- | --- | --- |
| Where it lives | `bans_path`, our file | the account's Steam profile |
| Who enforces it | us, instantly | Steam, on secure servers of the same AppID — ours once step 6 is done |
| Visible to | operators | everyone, through `GetPlayerBans` |
| Undone by | a lift, cleanly | a lift removes it, but the record stays public forever |
| Works when Steam is down | yes | no |

The coordinator's list is what actually turns a player away. A game ban is the
public record, and it follows the account rather than living in one
coordinator's file. So the game ban is opt-in, per ban.

### The console

```bash
make ban                     # builds bin/greyline-ban

greyline-ban list
greyline-ban check 76561198…
greyline-ban add  76561198… -reason "aimbot" -for 72h -steam -by gru
greyline-ban add  76561198… -reason "griefing"          # ours only, permanent
greyline-ban lift 76561198… -by gru -reason "appealed"
```

`-for` sets the length of both bans; leaving it out means permanent in both.
`-steam` is what adds the game ban — without it nothing touches Steam. The
SteamID may go before or after the flags.

It talks to the admin listener (`admin_listen`, loopback by default), so it
normally runs on the coordinator's own machine. `-admin` and `-key`, or
`$GREYLINE_ADMIN` and `$GREYLINE_ADMIN_KEY`, point it elsewhere. The same three
routes are plain HTTP if you would rather curl them: `GET /bans`, `POST /bans`,
`POST /bans/lift`.

If the game ban fails but the local one is recorded, the command says so and
exits non-zero. That combination matters: the player is kept out either way,
but an operator who thinks an account is game-banned when it is not will make
decisions on it later.

## Step 6 — dedicated servers under our own AppID

A Steam game ban, and Steam's own client↔server authentication, both work off
one question: *is this server a server of the app the client is running?* Until
we had a dedicated-server app the answer was no, and neither worked. Now there
is one: **Team Frontress Dedicated Server, 5150320**.

### What the numbers are for

The thing to keep straight is that a server has two AppIDs and they mean
different things:

| | AppID | What it is |
| --- | --- | --- |
| Identity | **5150320** | who the server *is* to Steam. `ServerAppID` in `tc2/steam.inf` and `SteamAppId` in `tc2/gameinfo_server.txt` |
| Files | **232250** | where the engine binaries and TF2 content come *from*. What `steamcmd_update.sh` installs into `../tf2` |

Changing the identity does not change where files come from, and 5150320 needs
no depots for any of this to work. That is why `steamcmd_update.sh` is
unchanged and why the launcher still looks for the SDK binaries where it always
did.

### In Steamworks, once

1. **5150320 must be the dedicated-server app of 5147520.** This is the whole
   point: it is what makes Steam accept a 5147520 client ticket on this
   server. If the two apps are not associated, `BeginAuthSession` refuses
   every client and nothing below will help. Check it on the app's landing
   page; if it is not linked, that is a Steamworks support request, not
   something configuration can fix.
2. **Publish the app.** A dedicated-server app that has never had its changes
   published does not exist as far as the login servers are concerned.
3. **Issue a login token per server machine.** Either through
   [steamcommunity.com/dev/managegameservers](https://steamcommunity.com/dev/managegameservers),
   or with the publisher key:

   ```bash
   curl -s -XPOST "https://partner.steam-api.com/IGameServersService/CreateAccount/v1/" \
     -d key="$GREYLINE_STEAM_WEBAPI_KEY" -d appid=5150320 -d memo="frontress-eu-1"
   ```

   It comes back with a `steamid` and a `login_token`. One per machine — a
   token shared between two running servers logs the second one out.

   To see what already exists:

   ```bash
   curl -s "https://partner.steam-api.com/IGameServersService/GetAccountList/v1/?key=$GREYLINE_STEAM_WEBAPI_KEY"
   ```

### On each server machine

```bash
GSLT=<login_token> ./start_dedicated_tc2.sh +map ctf_2fort +maxplayers 24 \
    +rcon_password "$RCON" +sv_lan 0 +ip 0.0.0.0 -port 27015
```

`start_dedicated_tc2.sh` turns `$GSLT` into `+sv_setsteamaccount`, which has to
be on the command line — Steam logs the server on during startup, long before
RCON exists to set anything. Without it the server logs on anonymously, the
script says so on stderr, and an anonymous server is not a server of our app.

`sv_lan 0` is required. On `sv_lan 1` the server never talks to Steam at all.

### Checking it actually worked

In order, because each one depends on the last:

| Check | Where | What you want |
| --- | --- | --- |
| The server logged in | server console at startup | a successful Steam logon, and no "unable to log on" loop |
| It logged in as *us* | `status` over RCON | it reports secure rather than insecure |
| Client tickets validate | coordinator log | `hello` succeeds on `auth.mode=webapi` instead of "steam authentication failed" |
| Rosters are enforceable | an assignment | `verified_identities: true` |

If the first passes and the third still fails, the two apps are not associated
in Steamworks — step 1 above. That is the one failure this repository cannot
fix.

### What this does not change

Steam still only enforces a game ban on a secure server of the banned AppID.
Bans go on 5147520, the account the player plays under, so once 5150320 is
properly the dedicated-server app of 5147520 those bans are enforced on our
servers too. The coordinator's own ban list stays the thing that acts first
and the thing that works when Steam does not — keep both.

VAC is separate again, and not something this configuration turns on: it is
enabled per app by Valve, and a mod running on another game's engine binaries
is not a likely candidate.

A coordinator ban does four things, and the last two are what make it stick:

1. `hello` is refused with 403 and the sentence the player is shown.
2. `DEPLOY` is refused, for a session that was already open when the ban landed.
3. The session is ended, so the client falls back to the menu with the reason.
4. If they are in a battle, the game server is sent a kick: `kickid` **and**
   `greyline_roster_remove`. Without the second, the roster gate lets them
   straight back in through the connect screen the kick sent them to.

An existing ban is never shortened. A ten minute abandon ban landing on an
account you banned permanently leaves the permanent one alone.

### Automatic bans

`match.abandon_ban_duration` bans a player who walks out of a battle that is
still being played. Leaving a queue, or a battle that never started, costs
nothing — the coordinator cannot tell an impatient player from one whose host
never came up.

It only takes effect under `auth.mode=webapi`. Under dev auth an abandon ban
costs a griefer one reconnect under a different SteamID, and costs an honest
player whose game crashed ten real minutes.

`security.violation_ban` is deliberately **not** wired to anything yet. A
policy violation today is one client's unverified word, and it already taints
the result and penalises the host's election record; auto-banning on it would
let one malicious client ban any host it played against. It becomes safe once
result signing exists (`security.require_signed_results`).

### Bans and dev auth

The ban list works under `auth.mode=dev`, but only against honest mistakes: a
client states its own SteamID there, so a banned player states another one. The
coordinator says so in its log on startup if bans exist and webapi is off.

## What is still not done

- **Result signing.** `security.require_signed_results` needs the HMAC half on
  the C++ side, which is not written. Until then a host can lie about a
  scoreline and only the other players' corroboration catches it.
- **`security.violation_ban`**, for the reason above.
- **Ban list compaction.** The log only grows. It is one line per ban and lift,
  so this is a problem for a future with far more players than this one.
- **Reading game bans back.** Nothing polls `ISteamUser/GetPlayerBans`, so a
  game ban placed from Steamworks by hand — rather than through
  `greyline-ban -steam` — is invisible to the coordinator.
- **A dedicated-server app for 5147520**, which is what would make Steam
  enforce any of this on our own servers.
