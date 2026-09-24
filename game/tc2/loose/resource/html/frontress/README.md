# Team Frontress — Next Fest demo UI

The page the game opens **instead of the main menu** on the `demo` branch.
Everything the player sees before and after a battle lives here: choosing a
side, the war map, operations and their stages, landing zones, class choice,
DEPLOY, the debrief, the war journal and the "how the war works" pitch.

The campaign is entirely local. Battles are ordinary listen servers with bots.

## Run it without the game

ES modules need HTTP, so serve the `html` directory (one level up):

```sh
cd game/tc2/loose/resource/html
python3 -m http.server 8765
# http://127.0.0.1:8765/frontress/
```

Without the game attached the page runs in *browser mode*: the campaign lives
in `localStorage` and every battle ends with "We won / We lost / Stalemate"
buttons. Query flags: `?dev` (developer tools in Settings), `?screen=war`
(skip the title), `?bridge=browser|game`.

Rules tests (no browser needed):

```sh
node --test tools/test-frontress-demo.mjs
```

**With a stand-in for the game** — the page in real *game mode*, talking the
same protocol the client does (RPC over `/ws`, `/v1/demo/*`, the deploy
command, the round result, the return to the menu):

```sh
node tools/frontress-demo-mock-game.mjs
# http://127.0.0.1:58270/ui/frontress/index.html
# curl '127.0.0.1:58270/mock/outcome?next=enemy'   # ally | enemy | none | leave
# curl  127.0.0.1:58270/mock/esc                   # pause menu in battle
# curl  127.0.0.1:58270/mock/log                   # every console line the page sent
```

Don't run it while the game is up: both listen on 58270.

## Layout

```
index.html            shell: #app (screen) + #overlay (modals)
css/frontress.css     the whole look, in screen order
js/main.js            controller: state, screens, the deploy → result loop
js/core/bridge.js     game vs browser: RPC, state persistence, battle results
js/core/i18n.js       strings; the running game's UI language picks the table
js/core/dom.js        h(): tiny hyperscript, views rebuild on every change
js/game/scenario.js   ALL authored content: sectors, roads, operations, maps, world events
js/game/campaign.js   ALL rules, as pure functions over a plain state object
js/ui/*.js            the theater (hex map), the sector map, glyphs, names
js/views/*.js         one file per screen/overlay
js/i18n/{en,ru}.js    every visible string
```

Views never change state; they call `ctx.act.*` in `main.js`, which runs a
rule from `campaign.js` and saves. Changing the demo's content is editing
`scenario.js`; changing what a victory does is editing `campaign.js`.

## The war, as scripted

* The script is written as **ally / enemy**, so RED and BLU play the same
  campaign; BLU sees the theater mirrored and in its own colours.
* An **operation** against an enemy sector on the front line has three stages
  — Breakthrough (4v4) → Advance (6v6) → Assault (9v9) — and each stage offers
  two **landing zones**, i.e. two battles (map + mode) to choose from.
* Win: next stage; win the Assault: the sector changes hands and the front
  moves. Lose (or stalemate): back one stage and −1 **momentum**; at zero
  momentum the offensive collapses and has to be started again.
* After every battle one **world event** fires (enemy probes, allied squads
  taking ground on their own, a counter-attack that takes a sector back), and
  coming back after 5+ minutes shows "While you were away".
* Taking the enemy headquarters wins the war: front line → second line → HQ,
  three operations, nine battles if every one is won.

Both sides fight the same maps. Payload and Attack/Defend maps hard-code BLU
as the attacker, so on those the attacking side always plays the game's BLU
team; for RED the page sends `swap 1` and the game sets
`greyline_uniform_swap 1` (`src/game/shared/greyline/greyline_uniform.h`),
which draws both teams in the war's colours. The HUD still says BLU; the
coordinator and the pause screen tell the player why. Every map must be in
`cfg/mapcycle_quickplay_bots.txt`; the tests enforce it.

## Contract with the game

The page is served by the game's Crow server (`src/game/shared/gamestate`) at
`http://127.0.0.1:58270/ui/frontress/index.html` and uses:

| What | How |
|---|---|
| Load / save the campaign | `GET` / `PUT /v1/demo/state` → `cfg/frontress_demo_state.json` |
| Start a battle | RPC `cmd`: `frontress_demo_deploy <ticket> <map> <red\|blue> <players> <class\|any> <swap 0\|1>` |
| Battle result | `GET /v1/demo/battle` → `{ticket, map, team, players, swap, winner?: red\|blue\|none, error?}` — `winner` is a *game team*; the page maps it to a war side |
| Forget the result | RPC `cmd`: `frontress_demo_clear` |
| Language | RPC `getlanguage` → `engine->GetUILanguage()`; the demo has no language setting of its own |
| In a battle? | RPC `getingame`, events `openedmenu` / `closedmenu` |
| Resume / leave / options / quit | `gameui_hide`, `disconnect`, `gamemenucommand OpenOptionsDialog`, `quit` |

Game side: `src/game/client/tf/frontress/tf_frontress_demo.cpp`. It loads the
map, waits for the local player, runs `cfg/frontress_demo.cfg`, sets
`greyline_uniform_swap`, puts the player on the page's team and class, closes
the MOTD and team menu, raises `tf_bot_quota` so fill mode makes 4v4 / 6v6 / 9v9, takes the
first **full-round** `teamplay_round_win` as the result, and disconnects back
to the menu after `tf_frontress_demo_return_delay` seconds. The page reads the
result only once the player is back at the menu.

`tf_frontress_demo 0` or `-nofrontressdemo` brings back the stock menu.

### QA console commands

```
frontress_demo_status
frontress_demo_force_win red|blue|none    # end the running battle now
frontress_demo_clear
```
