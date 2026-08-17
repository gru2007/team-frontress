# Retired: the peer-to-peer game code

Nothing in this directory is compiled. It is the game half of the peer-to-peer
prototype, kept for reference next to its coordinator half in
`services/coordinator/internal/legacy`.

| File | What it was |
| --- | --- |
| `greyline_gc.{h,cpp}` | the client's link to the coordinator — framed protobuf over TCP |
| `greyline_battle.{h,cpp}` | hosting, joining and host migration on the client |
| `greyline_host.cpp` | the server half: the hosting contract, the advertised address, score for migration |
| `greyline_shared.{h,cpp}` | the contract table and the replicated convars those two shared |

## Why it was retired

Battles run on dedicated servers now. The coordinator hands a battle to a server
in its pool over HTTP, `greyline-agent` drives that server over RCON, and the
player's client only ever needs `password` and `connect` — which the war map page
in the main menu already runs through the game's websocket bridge.

That left this code speaking a protocol nothing serves. The coordinator's
`cmd/coordinator` has not imported `internal/wire` since the war engine landed,
so `greyline_gc.cpp` was dialing port 27100 expecting framed protobuf and finding
an HTTP server. It was disabled by default (`greyline_gc_enable 0`), so it did no
harm — it simply could not have worked, and the next person to switch it on would
have spent an evening finding that out.

`game/tc2/cfg/greyline.cfg` went with them. Nothing but `greyline_gc.cpp` ever
exec'd it, and every convar it set is in this directory now — a settings file
that configures nothing, exec'd by nobody, is worse than no file at all. The one
client convar that is still live, `greyline_menu_page`, is `FCVAR_ARCHIVE` and
needs no cfg.

## What is still live

| File | Where |
| --- | --- |
| `greyline_briefing.cpp` | `src/game/server/greyline/` — states the battle's place in the war, enforces the roster |
| `greyline_briefing_logic.{h,cpp}` | `src/game/shared/greyline/` — what the briefing says, with no engine underneath; unit-tested |
| `greyline_localize.cpp` | `src/game/client/greyline/` — loads `resource/greyline_%language%.txt` |

## If host-run battles come back

They plausibly will: a coordinator that can hand a battle to a player's own
listen server needs no server pool, which matters for a community too small to
run one. The parts worth taking are the hosting contract in `greyline_shared.cpp`
(the three convars a matched-strangers host must change, each with the reason)
and the host-migration flow in `greyline_battle.cpp`. The transport in
`greyline_gc.cpp` is the part to throw away — the HTTP API replaced it, and the
client can speak that with far less code.
