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
`docs/STEAMPIPE.md` for the four files that decide this and why dedicated
servers stay on 243750/244310.

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

The ban list lives in `bans_path` as an append-only JSON-lines log, replayed on
start. A ban that a restart undoes is not a ban.

```bash
# permanent
curl -XPOST localhost:27101/bans -H 'Content-Type: application/json' \
  -d '{"steam_id":"765611980…","reason":"aimbot","issued_by":"gru"}'

# temporary — any Go duration
curl -XPOST localhost:27101/bans -H 'Content-Type: application/json' \
  -d '{"steam_id":"765611980…","reason":"griefing","duration":"72h"}'

curl localhost:27101/bans
curl -XPOST localhost:27101/bans/lift -H 'Content-Type: application/json' \
  -d '{"steam_id":"765611980…","by":"gru","reason":"appealed"}'
```

These are on the **admin** listener (`admin_listen`, loopback by default). Bind
it anywhere else and set `pool.admin_key`.

A ban does four things, and the last two are what make it stick:

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
