# Frontress offline demo launch

The offline demo is an explicit launch mode. It is not archived in player
settings and does not affect ordinary or Playtest launches.

Steam launch options (recommended for demo AppID `5260620`):

```text
-frontressdemo
```

The runtime also activates demo mode when Steam, `steam_appid.txt`, or the
engine reports AppID `5260620`. The explicit argument remains useful for local
QA of a normal main/playtest install.

Direct launch examples:

```sh
# Linux, from the game directory
./tc2.sh -frontressdemo

# Windows
tc2.bat -frontressdemo
```

In this mode the campaign card and theater use the local coordinator. A fresh
run opens the full map and asks for RED or BLU. DEPLOY persists a battle ticket
before starting the authored listen-server map, puts the player on the chosen
team, and executes `cfg/frontress_demo.cfg` for LAN-only bot play.

## Player journey / Путь игрока

The full map keeps a four-step route visible in the sidebar:

1. **Choose side** — select RED or BLU once for the local campaign.
2. **Choose front** — inspect the active sector, map, force size and the exact
   win/loss consequences, then confirm deployment.
3. **Fight with bots** — play one normal full round offline. The human is put
   on the selected side and bots fill both teams.
4. **Change the war** — the debrief names the result, updates Battles / Wins /
   Losses and shows whether the operation advanced, fell back or captured the
   sector. Return to the map for the next stage.

На полной карте этот же маршрут всегда виден в боковой панели:

1. **Выбрать сторону** — один раз выбрать RED или BLU для локальной кампании.
2. **Выбрать фронт** — проверить сектор, карту, размер команд и точные
   последствия победы и поражения, затем подтвердить высадку.
3. **Бой с ботами** — сыграть один обычный полный раунд офлайн. Игрок попадает
   за выбранную сторону, а боты заполняют обе команды.
4. **Изменить фронт** — послебоевой отчёт называет результат, обновляет бои,
   победы и поражения и показывает продвижение, откат или захват сектора.
   После этого игрок возвращается на карту к следующему этапу.

The web UI, saved-event history and native in-match result notification follow
the game's UI language (`english` or `russian`). Saved events use semantic keys,
so switching language also translates the existing war log and debrief. Older
demo saves are migrated from their former English event titles at render time.

The listen server is explicitly marked as offline practice. It starts with bot
quota 0, joins the human to the authored faction, and only then enables `fill`
quota. Source's fill calculation subtracts humans already on game teams, so
targets 8/12/18 produce 7/11/17 bots: exactly 4v4, 6v6 and 9v9 including the
player. All selected maps are in `mapcycle_quickplay_bots.txt`.

The tactical scoreboard remains the normal TF scoreboard: objective, player
and team scores are calculated by the game rules. The campaign intentionally
consumes only the decisive full-round winner. Mini-round events are ignored;
one full-round result increments Battles and exactly one of Victories/Defeats.
Kills, deaths and raw scoreboard points are not used as strategic currency.
If a full round ends in a stalemate, the attacking operation is considered
repulsed: Battles and Defeats increment, the front falls back by the normal
loss rule, and the player returns to a debrief instead of getting stuck on the
tactical map.

The directed Second Gravel War operation has three tactical stages:

| Stage | Force size | BLU route | RED route |
|---|---:|---|---|
| Breakthrough | 4v4 | `cp_gorge` | `koth_viaduct` |
| Advance | 6v6 | `pl_badwater` | `cp_badlands` |
| Assault | 9v9 | `cp_dustbowl` | `cp_foundry` |

BLU's route intentionally uses maps where BLU is the built-in attacker. RED's
route uses symmetric KOTH/5CP maps; stock Payload and Attack/Defend hard-code
BLU as attacker and would contradict a RED offensive at the tactical layer.
Every selected map is also present in the shipped bot mapcycle, so the demo
does not depend on runtime navigation generation.

A full-round RED/BLU result is resolved exactly once while the battle ticket is
pending. An attacker win advances the operation; a loss moves it back one
stage. Assault victory captures the target and opens a locked preview of the
next Playtest front. To bound the festival session, an uncaptured operation
ends with a War Report after four battles.

The transition table is deliberately small and visible:

| Current stage | Player win | Player loss |
|---|---|---|
| 1 Breakthrough | 2 Advance | 1 Breakthrough |
| 2 Advance | 3 Assault | 1 Breakthrough |
| 3 Assault | territory captured | 2 Advance |

Every resolved ticket increments `battlesPlayed` and exactly one of
`victories`/`defeats`. A ticket is cleared before any later round event can be
accepted. If the fourth result did not capture the territory, the local
operation ends and its counters remain visible in the War Report.

A second, visibly non-deployable front is advanced by deterministic world ticks
between battles, and launches after five minutes away add a local
`WHILE YOU WERE AWAY` bulletin. No fake online population is shown.

The local snapshot lives at `cfg/frontress_demo_state.vdf`. It includes the
faction, stage, counters, pending ticket, debrief and the last 12 war events.
Writes go through a temporary file and retain
`cfg/frontress_demo_state.backup.vdf`. An interrupted deployment offers REJOIN
or ABANDON; abandon never changes strategic state. The map also exposes Reset
Campaign.

Without `-frontressdemo`, the same UI uses `/v1/campaign` and keeps its normal
coordinator-facing behavior. The intentionally specific parameter name avoids
Source's existing `-demo`/demo-playback terminology.

## Browser check

With Node 20+ and Playwright installed:

```sh
PLAYWRIGHT_MODULE=/path/to/node_modules/playwright node --test tools/test-campaign.mjs
```

The tests cover the bot-supported map/config contract, redirect/query
preservation, faction choice, GeoJSON rendering, DEPLOY/ticket recovery,
debrief/history safety, captured-territory finale, live-feed fallback, and
embedded close handling.

## Developer controls

These console commands shorten manual QA in a packaged demo build. They are
no-ops outside demo mode, and result commands require an existing pending
BattleTicket:

```text
frontress_demo_status
frontress_demo_validate
frontress_demo_force_player_win
frontress_demo_force_player_loss
frontress_demo_force_stalemate
frontress_demo_force_red_win
frontress_demo_force_blu_win
frontress_demo_set_stage 1|2|3
frontress_demo_reset
```

Useful stock commands during a battle:

```text
status
tf_bot_quota
tf_bot_quota_mode
mp_winlimit
mp_maxrounds
```

`status` should show the human plus 7, 11 or 17 bots for the current stage.

The shortest victory-path smoke test is:

1. Launch with `-frontressdemo`, choose a faction and deploy.
2. Run `frontress_demo_force_player_win`; return to the map and verify stage 2,
   `Battles 1`, `Victories 1`, `Defeats 0`.
3. Run `frontress_demo_set_stage 3`, deploy again, then run
   `frontress_demo_force_player_win`; verify the captured-sector animation and
   final War Report.

For the defeat path, reset, choose/deploy and run
`frontress_demo_force_player_loss`. Stage 1 must remain stage 1, while Battles
and Defeats each become 1. The RED/BLU variants are retained for validating raw
team-event mapping. All force commands preserve the same
`BattleTicket -> BattleResult -> CampaignState` boundary as normal play.
