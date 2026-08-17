# The war

This is the design of GREYLINE FRONTRESS's strategic layer as it is actually
implemented, in `services/coordinator/internal/war`.

The point of the whole thing is one sentence:

> **The war does not count matches. It decides what the next match is.**

A war layer that only tallies results is a progress bar over ordinary TF2. This
one chooses the front, the stage, and therefore the mode and the map — and the
result of that battle chooses the next one.

```
GLOBAL STRATEGY ──decides──> NEXT BATTLE ──result──> GLOBAL STRATEGY
```

## The theater

One theater, seven nodes. Small enough that after two evenings a player says
"Foundry is the middle" and "we lost Rail Yard again" — which is the difference
between a place and a menu.

```
                     IRON JUNCTION
                    /             \
              FOUNDRY 17         QUARRY
                  │                 │
              RAIL YARD ------- RESERVOIR
                  │                 │
               RED HQ            BLU HQ
```

A **strategic node is not a Source map**. Foundry 17 is an industrial district;
`cp_process_final`, `pl_badwater` and `cp_dustbowl` are all places inside it.
That indirection is what lets seven nodes carry a war with no bespoke content,
and it is why the same map can turn up in completely different strategic
situations.

Each node names a **battle profile**: the stage plan an offensive against it has
to clear, and the battlefields available at each stage.

```json
"industrial": {
  "stages": ["breakthrough", "advance", "assault"],
  "battlefields": {
    "skirmish":     [ "koth_viaduct", "arena_badlands" ],
    "breakthrough": [ "cp_process_final", "cp_snakewater_final1" ],
    "advance":      [ "pl_badwater", "pl_upward" ],
    "assault":      [ "cp_dustbowl", "cp_gravelpit" ]
  }
}
```

The theater lives in `theater.industrial.json`. Nothing about the map is
compiled in.

## Fronts and stages

A **front** is one side's offensive against one adjacent enemy node. It holds
very little: who attacks, what they attack, from where, and how far in they are.

```json
{
  "id": "foundry_17-a91c",
  "attacker": "RED", "defender": "BLU",
  "target_node": "foundry_17", "source_node": "rail_yard",
  "plan": ["breakthrough", "advance", "assault"],
  "stage": 1,
  "collapse_at_stage": 0
}
```

What a battle does to it:

| Result | Effect |
| --- | --- |
| Attacker wins | Clears the stage. Clearing the last one **captures the node** |
| Defender wins | Pushes the offensive **back one stage** |
| Defender wins at `collapse_at_stage` | The offensive **collapses** |
| Stalemate | Costs a battle, moves nothing |

So a series of matches becomes a story on its own:

```
BREAKTHROUGH won      → ADVANCE
ADVANCE lost          → back to BREAKTHROUGH
BREAKTHROUGH lost     → RED OFFENSIVE COLLAPSED
                      → BLU counter-offensive at Rail Yard
```

Losing does something. It does not reset the front and it does not throw the
evening away — that is the difference between "we lost, oh well" and "they
almost had Foundry and we pushed them back to their own yard".

## Capture moves the front along the graph

When an offensive clears its last stage the node flips, the front closes, and
the coordinator immediately opens the next one from the ground just taken.

```
before:  RED ── ⚔ ── FOUNDRY(BLU) ── BLU
after:   RED ── FOUNDRY(RED) ── ⚔ ── BLU
```

The war visibly moves rather than resetting to a fresh scoreboard, which is the
PlanetSide-shaped feeling the project is after.

A node nobody owns — Iron Junction starts neutral — is still fought over by both
sides: whoever pushes into it, the other side is defending its claim.

## Population decides how wide the war is

This is the mechanic that keeps a fifteen-player community feeling like a war
instead of a thinly spread server browser.

| Online | Active fronts |
| --- | --- |
| 0–15 | 1 |
| 16–31 | 2 |
| 32–47 | 3 |
| 48+ | up to `war.max_fronts` |

Everything else stays on the map as territory nobody is currently fighting over.
A front that has battles running or players queued on it is never stood down.

## The same stage, at any population

The strategic event does not change with how many people are online; the shape
of the battle does.

```
front says:  BREAKTHROUGH at Foundry
6 online  →  koth_viaduct, 3v3        (a skirmish still decides the stage)
12 online →  cp_process_final, 6v6
20 online →  cp_badlands, 10v10
```

The coordinator picks the battlefield whose ideal population is closest to the
queue, and falls back to the profile's skirmish list when nothing at that stage
fits. Players see `BREAKTHROUGH`; the map underneath is whatever runs well with
the people who are actually there.

### Directional maps

Payload and attack/defend maps are built for BLU attacking. When RED is on the
offensive its mercenaries play as the BLU team for that battle, and the
coordinator does the translation both ways: it assigns in-game teams in the
roster, and reads the reported scoreline back into war sides. The war never sees
the swap; the players are told which side they fight for in the briefing.

## Defensive mobilization

A side that falls two territories behind is put under **defensive
mobilization**. It never fakes a result — if BLU are simply better, BLU win. It
does three honest things:

- offensives against that side **collapse one stage earlier** (`collapse_at_stage` 1);
- the coordinator prefers to open fronts where that side attacks or defends;
- new mercenaries default to it, and it is the side contracts are offered for.

The aim is a war that lasts days, not one that is over in forty minutes because
six people had a good evening.

## Campaigns

A campaign ends when a side takes the enemy **headquarters**. Then a short
armistice, then campaign 2 from a different opening.

```
THE SECOND GRAVEL WAR — CAMPAIGN 01
BLU victory, 9 days, 327 battles
```

That is what gives a community memory: "remember the first war, when BLU took
the whole north". An endless bar cannot be remembered, because it never ends.

## The event log is the world

Every mutation is an appended event, and nothing else writes state:

```
campaign_started · front_opened · battle_recorded · node_captured
front_closed · mobilization · campaign_ended · story
```

Each carries the decision that produced it — which front to open, which map to
play — so replaying the log rebuilds the identical war without re-deciding
anything. One file, `war-events.jsonl`, is:

- the world's persistence (restart replays it);
- the world recap ("while you were away");
- the audit trail for a disputed capture;
- and, later, where story events land — a signal lost at North Terminal is the
  same kind of entry, so UNIT 0 will not need a second system to interfere with
  a live campaign.

```
18:42  RED won Battle #381
19:07  RED BREAKTHROUGH COMPLETE AT Iron Junction — NEXT: ASSAULT
19:31  BLU HELD AT Iron Junction — RED PUSHED BACK TO BREAKTHROUGH
20:44  RED CAPTURED Iron Junction
```

## In the game

A battle that does not say what it is part of leaves the entire strategic layer
invisible from the only place players spend their time. So the coordinator sends
a briefing with every assignment, the agent pushes it into the server over RCON,
and the game states it in chat at round start plus one line in the middle of the
screen:

```
▌ GREYLINE: BATTLE FOR FOUNDRY 17
▌ ADVANCE — stage 2 of 3, RED on the offensive
▌ Push the offensive deeper into Foundry 17.
```

It is built from localization tokens (`resource/greyline_%language%.txt`, English
and Russian ship today), so one server briefs players in their own language at
the same time. Only the proper nouns — front and district names the coordinator
generated — pass through as text.

Team assignment goes the same way: the roster the coordinator picked is pushed
to the server, which puts each mercenary on the side the war put them on.

## What is deliberately not here

Supply lines, encirclement, breakout battles, infrastructure effects, Machines,
UNIT 0, PvE operations, an economy, a season pass. The MVP exists to answer one
question — *do people play another battle to finish the offensive they are in?*
— and every one of those makes that question harder to ask.

The parts that are here are the ones that question needs: a map that generates
matches, a loss that means something, and a war that is still there tomorrow.
