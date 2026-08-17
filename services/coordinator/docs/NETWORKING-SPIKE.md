# Networking: spike before code

**Status: the loopback↔SDR bridge design is withdrawn. Do not implement it.**

It was built on an inference that does not hold: that because `src/game` contains
no `ISteamNetworkingSockets` calls, TC2 gameplay must run on raw UDP. The grep
was accurate; the conclusion was not. The Steam transport is implemented below
the game DLL, in the engine, and no amount of searching `src/` could have shown
it either way.

Team Comtress states in the Blockbuster Update that all servers default to using
Steam Networking, and later disabled the legacy server file-transfer path
specifically because it was incompatible with Steam Networking. Current
mastercomfig ships `sv_use_steam_networking 1`.

The engine binary settles it beyond the changelog: `NET_SendTo` contains
`SendMessageToFakeIP`, and the receive path handles "FakeUDP" ports. The engine
already is the bridge.

## What this checkout independently confirms

| finding | where |
|---|---|
| The engine is **not in this repo**. It comes from `steamcmd +app_update 232250` (TF2 dedicated server), so transport behaviour is TF2 engine behaviour. | [game/steamcmd_update.sh](game/steamcmd_update.sh) |
| `sv_friends_only` and `sv_allow_server_adverisement_to_master_server` resolve through `ConVarRef` — they exist in the engine binary. The client forces both at startup, commented *"so map command is Friends Only by default"*. The `map` command is the listen server; its privacy and advertising are already Steam-side concerns. | [clientmode_tf.cpp:585](src/game/client/tf/clientmode_tf.cpp:585) |
| `sv_use_steam_networking` and `sdr_spew_level` appear nowhere in `src/` — consistent with engine-side cvars, not with their absence. | — |
| Adjustable team sizes are real and already here: `tf_team_size`, `tf_team_size_red`, `tf_team_size_blu`, `FCVAR_REPLICATED`. | [tf_gamerules.cpp:983](src/game/shared/tf/tf_gamerules.cpp:983) |
| **Steam Lobbies are not used by the game at all.** The only `ISteamMatchmaking` calls in the entire tree are `AddFavoriteGame` / `GetFavoriteGame` (the recent-servers list). | [clientmode_tf.cpp:1859](src/game/client/tf/clientmode_tf.cpp:1859) |
| TF2's own "lobby" is **not** a Steam lobby. `CTFLobby` is a GCSDK `CSharedObject` living in Valve's Game Coordinator. Do not assume any Steam lobby plumbing exists to reuse. | [tf_lobby_server.h:28](src/game/shared/tf/tf_lobby_server.h:28) |

## Settled by reading the engine binary

Source: `bin/x64/engine.dll` from the local TF2 install (retail Valve engine, the
one TC2 runs on — `tf/steam.inf` PatchVersion 10828683, appID 440 / server 232250).
Findings are `strings` output, quoted verbatim.

**The engine is an ISteamNetworkingSockets client, at the same interface version
as the SDK headers in our tree.**

```
SteamNetworkingSockets012
[SteamNetworkingSockets] %s
No ISteamNetworkingUtils
View/edit SteamNetworkingSockets configuration variables   ← console command: sdr
Usage: sdr <setting> [<value>]
```

**The netchannel itself already sends and receives over FakeIP.** This is the
decisive one — it is precisely the job the withdrawn bridge was designed to do:

```
NET_SendTo:            SendMessageToFakeIP to %s returned %d
NET_ReceiveDatagram:   Ignoring message on FakeUDP port %d from %s -- not a fakeUDP identity?
                       Ignoring message of size %d on FakeUDP port %d from %s
```

**FakeIP allocation is engine-native and gated on a launch option** — answering
the "do not assume either way" question outright:

```
-enablefakeip
Requesting FakeIP as per -enablefakeip
FakeIP allocation succeeded: %s
FakeIP allocation failed with error code %d
CCallback<CSteam3Server, SteamNetworkingFakeIPResult_t>
```

`-enablefakeip` occurs exactly twice, both in the server startup path. There are
no `SteamSocketMgr` / legacy `ISteamNetworking` P2P strings — this is
ISteamNetworkingSockets + FakeIP, not the old P2P layer.

**All three cvars exist, with these help strings:**

| cvar | help string in the binary |
|---|---|
| `sv_use_steam_networking` | *(none — name only)* |
| `sdr_spew_level` | "Verbosity level for SteamNetworkingSockets spew.  4=warning, 5=msg, 6=verbose, 7=debug" |
| `sv_friends_only` | "Whether or not only friends should be allowed to join the game" |

**The friends-only gotcha is real and enforced by the engine**, with its own
rejection message:

```
STEAM UserID %s is not friends with the host!
#Server_NoPerms
```

**`status` prints the endpoint, but do not parse it.** The format block is:

```
udp/ip  : %s%s          where the trailing %s is one of:
                          "  (local: %s)"
                          "  (public IP from Steam: %s)"
steamid : %s (%llu)
```

Useful for eyeballing during the test, and nothing more. The endpoint is
available as a struct from the same API the engine itself uses:

```cpp
ISteamNetworkingSockets *pSockets = SteamGameServerNetworkingSockets();

SteamNetworkingFakeIPResult_t info = {};
pSockets->GetFakeIP( 0, &info );

if ( info.m_eResult == k_EResultOK )   // until then the server is not ready
{
    info.m_unIP;        // FakeIP
    info.m_unPorts[0];  // FakePort
}
```

Check the type is `k_ESteamNetworkingFakeIPType_GlobalIPv4` — a local-scope
FakeIP would not be reachable from another connection.

## What the binary cannot tell us

Four things still need the live two-machine test. Everything else above is settled.

1. **The default of `sv_use_steam_networking`.** Defaults are interned strings;
   they cannot be read out reliably. Type the cvar name in console.
2. **Whether a *listen* server gets a FakeIP at all, under our AppID.** The
   allocation lives on `CSteam3Server`, which serves both listen and dedicated
   servers in Source — but that is inference from how Source is built, not
   evidence from this binary. This is the one unknown that actually matters,
   because "player presses HOST" is the whole model.
3. **Whether that FakeIP is `k_ESteamNetworkingFakeIPType_GlobalIPv4`** rather
   than a local-scope one.
4. **Whether `connect <FakeIP>:<port>` works from outside, without port
   forwarding, under NAT and CGNAT.**

## The test

Two stages. The first needs one machine and answers the only unknown that can
block the design.

**Stage 1 — does a listen server get a global FakeIP?** One machine.

```
launch TC2 with -enablefakeip
sv_use_steam_networking 1
sv_lan 0
sdr_spew_level 6
map cp_process_final
```

Then read the result through `GetFakeIP( 0, &info )` — not through `status`.
Pass condition:

```
info.m_eResult   == k_EResultOK
info.m_unIP      != 0
info.m_unPorts[0]!= 0
type             == k_ESteamNetworkingFakeIPType_GlobalIPv4
```

If that holds, host transport is settled and no networking code needs writing.

**Stage 2 — one vertical slice, end to end.** Two accounts, two connections.

```
CreateLobby → map → GetFakeIP → SetLobbyGameServer
                                      ↓
                         JoinLobby → GetLobbyGameServer
                                      ↓
                            connect <FakeIP>:<port>
                                      ↓
                            actually in the match
```

No port forwarding. Note whether the host is behind NAT/CGNAT. Befriend the test
account or set `sv_friends_only 0`, or the engine refuses the join with
"STEAM UserID … is not friends with the host!" before transport is even reached.

This is now a confirmation run, not research.

## If the spike passes: lobbies carry the session

Steam Lobbies are the natural fit for "find the right room and join it", and the
missing piece is small glue, not a transport. The division that falls out:

```
GREYLINE GC                          STEAM
why rooms exist                      where rooms are
─────────────                        ──────────────
world map, fronts                    lobby list + metadata
territory ownership                  membership, slots
battle progress                      owner, invites
which fronts may open                assigned game server
RED/BLU balance
results → world moves
```

DEPLOY becomes: ask the GC which front to fight on, then

```
AddRequestLobbyListStringFilter("front", …)
AddRequestLobbyListStringFilter("build", …)
AddRequestLobbyListSlotsAvailableFilter()
RequestLobbyList()
   ├── found → JoinLobby → GetLobbyGameServer → connect
   └── none  → CreateLobby → map → SetLobbyGameServer → others find you
```

`LobbyGameCreated_t` delivers the server to every member, and Steam's invite path
(`+connect_lobby <id>` when the game is cold, `GameLobbyJoinRequested_t` when it
is running) gives "play with a friend" for free.

**Rendezvous goes through the Steam API for it, not through lobby key/values.**
`SetLobbyData("connect_target", …)` reinvents an abstraction Steam already has:

```cpp
SteamMatchmaking()->SetLobbyGameServer( lobbyID, fakeIP, fakePort, serverSteamID );
SteamMatchmaking()->GetLobbyGameServer( lobbyID, &ip, &port, &steamID );
```

Members are notified by `LobbyGameCreated_t`. A player joining a battle already
in progress cannot rely on that callback — it already fired — so `LobbyEnter_t`
must call `GetLobbyGameServer` immediately and connect if a server is already
set, otherwise wait for the callback. Both paths, or late joiners hang.

If the SteamID argument turns out to be awkward for a listen server, IP + port is
the endpoint that matters; the SteamID can be verified separately.

Two things to keep honest about this plan:

- **`JoinLobby` is not `connect`.** Entering a lobby puts you in a Steam room; a
  `LobbyEnter_t` handler still has to call `GetLobbyGameServer` and issue the
  connect. That glue is ours to write.
- **None of it exists in TC2 today** — see the lobby rows in the table above.
  It is small, but it is new code, and the changelogs do not claim otherwise.

Verify the spike before writing any of it.

## What the coordinator is no longer responsible for

Steam and the Source engine own the transport. The GC does not touch:

```
connect endpoints · UDP transport · packet routing · SDR · FakeIP · gameplay traffic
```

What is left is the actual job:

```
world state · active fronts · territory · RED/BLU population
which battle you deploy to · who hosts · roster · results · the war moving
```

Steam Lobbies answer "where does this battle physically exist". The GC answers
"what does this battle mean for the war". War progress must never live in lobby
data — the lobby owner is an ordinary player.

## Cut from MVP scope

Deferred until basic transport is proven. The Go implementations stay on the
branch, frozen, out of the critical path:

- seamless host migration and match state snapshots;
- HMAC join tokens, result signing and integrity report signatures;
- the convar policy enforcement loop.

These solve problems that only exist once players are reliably reaching each
other. Nothing above them should be built on the assumption that they ship.

## Check TC2 first, every time

TC2 already ships pieces of several things GREYLINE was about to reinvent.
Before designing any further component, search the TC2 changelogs and this tree
for it:

| GREYLINE plan | already in TC2 |
|---|---|
| adaptive 4v4 / 6v6 / 12v12 | `tf_team_size`, `tf_team_size_red`, `tf_team_size_blu` |
| starting a battle at partial population | Casual pre-match starts early at 9v9, drops to 6v6 after a 90s wait |
| territory capture, per-side spawn choice, big player counts | Territorial Control 2 beta (50v50 on `tc_hydro`), maps configured by script rather than BSP edits |
| WATCH from the world map | reworked SourceTV — camera operator by SteamID, 90s default delay, demos recorded by default |
