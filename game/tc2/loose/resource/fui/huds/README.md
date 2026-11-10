# HUDs

Each folder here is a HUD: a `hud.rml` (HTML-like, RmlUi) and its stylesheets.
The game draws the one `fui_hud_style` names over the match.

| `fui_hud_style` | What you get |
|---|---|
| `auto` (default) | `frontress`, unless a classic custom HUD is installed in `custom/`: then `minimal`, so that HUD keeps working |
| `frontress` | The Frontress HUD |
| `minimal` | The game's own HUD (or your classic custom HUD), plus quests and notifications |
| `<your folder>` | Yours |

`fui_hud_list` in the console lists what is installed. `fui_hot_reload 1`
reloads a HUD the moment you save it, so you can edit it with the game running.

## Making your own

1. Copy `frontress/` to `custom/<anything>/resource/fui/huds/myhud/` (or here).
2. `fui_hud_style myhud`.
3. Edit. Everything is plain RML/RCSS: see `frontress/hud.rml`, and the menus
   in `../screens/` for more examples.

Classic HUDs keep working as before: `custom/` overrides `resource/ui/*.res`
and `scripts/hudlayout.res` for the game's own HUD, which shows wherever the
Frontress HUD does not replace it.

### Taking over parts of the game's HUD

`<body data-replaces="...">` lists the game's HUD parts your HUD draws itself;
they stay hidden while it is up. Leave a word out and the game's part stays.

| Word | The game's part |
|---|---|
| `health` | health and class (CTFHudPlayerStatus), the floating +health numbers |
| `ammo` | ammo (CTFHudWeaponAmmo) |
| `medic` | ÜberCharge meter (CHudMedicChargeMeter) |
| `class` | what a class carries: item meters (cloak, drinks, banners, heads...), stickybombs and charge meters, a spy's disguise, metal, an engineer's buildings |
| `buildmenu` | an engineer's build and destroy menus, a spy's disguise menu (CHudMenuEngyBuild, CHudMenuEngyDestroy, CHudMenuSpyDisguise); their keys still work |
| `objective` | timer, points, cart, flags (CTFHudObjectiveStatus, CTFHudKothTimeStatus, CTFHudMatchStatus), only in modes `hud.mode` covers |
| `goal` | the map's goal across the top at spawn (CHudTeamGoal); it is in Esc |
| `killfeed` | kill feed (CTFHudDeathNotice) |
| `scoreboard` | the scoreboard on Tab (CTFClientScoreBoardDialog); not in MvM |
| `winpanel` | the end of a round and of a match (CTFWinPanel, CTFArenaWinPanel, CTFMatchSummary); not in MvM |
| `freezecam` | who killed you (CTFFreezePanel) |
| `spectator` | the spectator bars (CTFSpectatorGUI); not in MvM |
| `chat` | chat (CHudChat): the lines and typing them |
| `vote` | votes (CHudVotePanel); calling a vote is still the game's window |
| `notices` | the game's notices: can't change class now, teams switched, sudden death, arena, streaks, "game paused" |
| `quests` | Valve's contract tracker and quest notices |

The stock panels keep running behind a HUD that replaces them (their
events, strings and timers), so a part a HUD does not draw itself comes back
the moment the word is left out.

### What a HUD can read

All of `hud` (defaults and comments in `../schema.json`), plus `quests`,
`toasts`, `player`, `match` (the map, its mode and goal, a war battle) and
the rest of the menus' state.

| Field | |
|---|---|
| `hud.visible` | false under `cl_drawhud 0` |
| `hud.alive`, `hud.class`, `hud.team` | `team`: red / blu |
| `hud.health`, `hud.max`, `hud.frac`, `hud.overheal`, `hud.low` | `frac`, `overheal`: 0..1 |
| `hud.weapon`, `hud.weapon_icon` | name in your language; `tex:` image |
| `hud.clip`, `hud.reserve`, `hud.clip_max`, `hud.clip_low` | -1 when there is none |
| `hud.charge`, `hud.charge_ready` | ÜberCharge 0..1, -1 for non-medics |
| `hud.respawn`, `hud.killed_by` | seconds while dead, -1 alive |
| `hud.mode` | cp, koth, pl, ctf, or "" (the game draws that objective) |
| `hud.timer`, `hud.setup`, `hud.overtime`, `hud.waiting` | seconds, -1 for no clock |
| `hud.score_red`, `hud.score_blu`, `hud.koth_red`, `hud.koth_blu` | |
| `hud.points[]` | `owner`, `capping`, `frac`, `cappers`, `locked`, `blocked` |
| `hud.cart` | `progress` 0..1, `speed` (-1 rolling back), `team` |
| `hud.flags[]` | `team`, `status` (home, stolen, dropped), `return` |
| `hud.kills[]` | `killer`, `killer_team`, `assist`, `victim`, `victim_team`, `icon`, `crit`, `me` |
| `hud.restart` | seconds until the round starts, -1 none |
| `hud.conds[]` | `id` (burning, bleeding, jarate, milk, gas, marked, slowed, uber, crits, minicrits, resist_*, buff_*, rune_*, healed, cloaked, parachute, speed), `icon`, `bad` |
| `hud.disguise` | `on`, `disguising`, `class`, `team`, `name`, `weapon` |
| `hud.meters[]` | the item meters: `label` (in your language), `frac`, `count` (-1 none), `percent`, `bars`, `flash`, `streak` |
| `hud.stickies`, `hud.power`, `hud.metal` | stickybombs out, a weapon charging (0..1), metal; -1 when not yours |
| `hud.charge_kind`, `hud.charge_resist`, `hud.charges`, `hud.charge_active` | uber / kritz / quickfix / vaccinator; the Vaccinator's resist and charges |
| `hud.buildings[]` | an engineer's four slots: `kind`, `built`, `level`, `mini`, `health`, `building`, `progress`, `carried`, `sapped`, `alert`, `upgrade`, `ammo`, `rockets`, `kills`, `charge` (-1 where it has none) |
| `hud.next_class` | the class you come back as, "" if the same |
| `hud.scores` | `shown`, `summary` (the match is over), `mouse`, `winner`, `red` / `blu` { `score`, `count`, `players[]` }, `spec[]`, `time_left`, `me` (your round) |
| `hud.round` | the end of a round: `shown`, `winner`, `complete`, `reason`, `cappers`, `score_red`, `score_blu`, `top[]`, `streak_name`, `streak` |
| `hud.killer` | who killed you: `shown`, `self`, `object`, `name`, `team`, `cls`, `avatar`, `health`, `max`, `frac`, `weapon`, `weapon_icon`, `rivalry` |
| `hud.spec` | spectating: `shown`, `mode`, `team_spec`, `target` { `on`, `object`, `name`, `team`, `cls`, `avatar`, `health`, `max`, `frac` } |
| `hud.menu` | the class menu up: `shown`, `kind` (build, destroy, disguise), `metal`, `team`, `group`, `simple`, `slots[]` { `key`, `kind`, `state`, `cost` } or { `key`, `cls`, `group`, `selected` } |
| `hud.notices[]` | `kind`, `lines[]` |
| `hud.votes[]` | `header`, `issue`, `options[]` { `label`, `count` }, `voted`, `yesno`, `result` (passed / failed), `result_text` |
| `hud.chat` | `open`, `mode` (all / team / party), `lines[]` { `fade`, `segs[]` { `t`, `c` } }; type into `<input id="say" data-value="ui.say_draft" data-event-change="cmd('say_input', ev.linebreak, ev.value)">` |

Commands a HUD may send: `cmd('say_input', ...)`, `cmd('say_close')`,
`cmd('scores_pick', <steam id>)` (the report dialog), `cmd('scores_mute', <player index>)`.
Interactive parts need `pointer-events: auto` (the HUD's body has none).

Images: `tex:<material path>` is any game texture (`tex:hud/class_soldierred`,
`tex:sprites/obj_icons/icon_obj_red`); `hudicon:<name>` is a HUD icon from
`scripts/mod_textures.txt` (`hudicon:d_scattergun` is the kill icon,
`hudicon:dneg_scattergun` the dark one).

Transforms: `| clock` (4:07), `| percent`, `| thousands`, `| t(...)` (a string
from `../lang/`, with %1 %2 filled in).
