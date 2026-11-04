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
| `health` | health and class (CTFHudPlayerStatus) |
| `ammo` | ammo (CTFHudWeaponAmmo) |
| `medic` | ÜberCharge meter (CHudMedicChargeMeter) |
| `objective` | timer, points, cart, flags (CTFHudObjectiveStatus, CTFHudKothTimeStatus, CTFHudMatchStatus), only in modes `hud.mode` covers |
| `killfeed` | kill feed (CTFHudDeathNotice) |

### What a HUD can read

All of `hud` (defaults and comments in `../schema.json`), plus `quests`,
`toasts`, `player` and the rest of the menus' state.

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

Images: `tex:<material path>` is any game texture (`tex:hud/class_soldierred`,
`tex:sprites/obj_icons/icon_obj_red`); `hudicon:<name>` is a HUD icon from
`scripts/mod_textures.txt` (`hudicon:d_scattergun` is the kill icon,
`hudicon:dneg_scattergun` the dark one).

Transforms: `| clock` (4:07), `| percent`, `| thousands`, `| t(...)` (a string
from `../lang/`, with %1 %2 filled in).
