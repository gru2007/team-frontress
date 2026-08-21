<div align="center">

# TEAM FRONTRESS

### ONE WAR. THOUSANDS OF BATTLES.

**A Team Fortress / Team Comtress 2 multiplayer experiment where every match belongs to one persistent RED vs BLU war.**

[![Status](https://img.shields.io/badge/status-early%20development-d46a32?style=for-the-badge)](#project-status)
[![Source](https://img.shields.io/badge/engine-Source%201-6b6258?style=for-the-badge)](#technology)
[![War](https://img.shields.io/badge/world-persistent%20war-496979?style=for-the-badge)](docs/GREYLINE-WAR.md)
[![License](https://img.shields.io/badge/license-Source%201%20SDK-b7a58f?style=for-the-badge)](LICENSE)

</div>

---

> **The war does not count matches. It decides what the next match is.**

**Team Frontress** is an experimental multiplayer project built from **Team Comtress 2** and the Source 1 SDK.

The gunplay is intentionally familiar. The layer around it is not.

Instead of treating every match as an isolated round that disappears when the scoreboard closes, Team Frontress connects battles into a **single persistent campaign**. RED and BLU fight across a strategic theater. A victory advances an offensive, a defeat can push it back, capturing a region moves the front, and the new state of the war determines what battle should happen next.

```text
GLOBAL STRATEGY ── decides ──▶ NEXT BATTLE ── result ──▶ GLOBAL STRATEGY
```

The current strategic implementation is still named **Greyline** internally and in the technical documentation. Greyline is the war/theater layer inside Team Frontress, not the public name of the game.

## The idea

A normal session should be simple:

```text
OPEN THE WAR MAP
      ↓
CHOOSE RED / BLU
      ↓
     DEPLOY
      ↓
PLAY A REAL TF2 / TC2 MATCH
      ↓
SEE THE FRONT MOVE
      ↓
PLAY THE NEXT BATTLE
```

The strategic layer exists to give ordinary Team Fortress combat **continuity, consequence and context** — not to replace it with a strategy game.

### One theater, seven strategic nodes

```text
                     IRON JUNCTION
                    /             \
              FOUNDRY 17         QUARRY
                  │                 │
              RAIL YARD ─────── RESERVOIR
                  │                 │
               RED HQ            BLU HQ
```

A strategic node is **not** a Source map. A region such as `FOUNDRY 17` can contain several existing battlefields and several stages of an offensive.

A campaign can therefore turn familiar maps and modes into parts of one larger operation:

```text
BREAKTHROUGH  →  ADVANCE  →  ASSAULT  →  REGION CAPTURED
      5CP          Payload       A/D
```

If the defender wins, the offensive can be pushed back a stage or collapse entirely. If the attacker clears the final stage, the node changes owner and the front moves deeper into enemy territory.

[Read the complete war design →](docs/GREYLINE-WAR.md)

---

## Designed for a small population first

Team Frontress is not designed around the assumption that hundreds of people are online.

The coordinator changes the **width of the war** according to the active population:

| Players online | Strategic activity |
| ---: | --- |
| `0–15` | 1 active front |
| `16–31` | 2 active fronts |
| `32–47` | 3 active fronts |
| `48+` | up to the configured maximum |

The battle itself scales separately. A breakthrough with six players may become a compact 3v3 skirmish; the same strategic stage with twelve players can become a 6v6 control-point battle.

The goal is that **eight people still feel like they are participating in a war**, rather than being scattered across an empty server browser.

---

## What makes a battle part of the war?

The coordinator owns three things:

<table>
<tr>
<td width="33%" valign="top">

### ⚔️ The war

Territory, fronts, offensive stages, campaigns, mobilization and the append-only world event log.

</td>
<td width="33%" valign="top">

### 🎯 Matchmaking

Allegiance, parties, front selection, team formation, map choice and live battle expansion.

</td>
<td width="33%" valign="top">

### 🖥️ Server pool

Dedicated servers first, with elected player-hosted sessions available as a fallback for small test populations.

</td>
</tr>
</table>

A game server never edits strategic state directly. It receives an assignment, runs the match and reports the result.

One finished battle produces exactly one war event.

```text
GAME CLIENT
    │
    │ DEPLOY
    ▼
COORDINATOR ───────────────▶ WAR ENGINE
    │                         │
    │ assignment              │ battle result
    ▼                         │
SERVER POOL / P2P HOST ──────┘
```

[Coordinator architecture →](services/coordinator/README.md)

---

## The Second Gravel War

The first Team Frontress campaign begins with a simple premise.

After the Mann vs. Machine conflict, Mann Co. loses control of large parts of its industrial and logistical network. RED and BLU move in to claim what remains.

Factories. Rail yards. Warehouses. Power infrastructure. Research sites.

The result is a new corporate conflict:

### **THE SECOND GRAVEL WAR**

The first version focuses on RED and BLU. Story systems, machine incursions, PvE operations and the stranger parts of the Greyline network are intentionally **not required for the initial war loop**.

The foundation has to work first:

> **Deploy → fight → change the front → want to fight again.**

---

## Project status

> [!WARNING]
> **Team Frontress is in early development.** Some systems are tested, some are only exercised manually, and several game-side C++ changes are still unverified in a complete build. Do not read this repository as a finished release.

The current repository already contains substantially more than a design document, but confidence differs by subsystem.

| Area | Current state |
| --- | --- |
| Strategic theater, node graph and front movement | **Tested** |
| Population-driven active fronts | **Tested** |
| Campaign lifecycle and persistent event log | **Tested** |
| Queue, parties, contracts and team formation | **Tested** |
| Dedicated server pool and assignments | **Tested** |
| Player-host election and result verification fallback | **Tested** |
| In-game Greyline command board / war map | **Built**, behaviour still needs broader testing |
| Steam identity integration | **Written**, HTTP path tested |
| Several native roster / team-enforcement changes | **Written**, first full build remains an important validation step |
| Full RED/BLU visual remapping | **Incomplete** — models are covered more broadly than HUD/particles |
| Supply, encirclement, economy, PvE campaign | **Not part of the current MVP** |

For the detailed and intentionally less flattering version:

### [Read `GREYLINE-STATUS.md` before testing →](docs/GREYLINE-STATUS.md)

---

## Current MVP

The first meaningful milestone is deliberately narrow.

- persistent RED vs BLU theater;
- seven strategic nodes;
- one or more population-driven active fronts;
- `DEPLOY` into the battle the war currently needs;
- adaptive battlefield selection;
- real match results advancing or reversing an offensive;
- territory capture;
- campaign persistence across coordinator restarts;
- live war map and operation briefing;
- dedicated-server orchestration;
- player-hosted fallback where appropriate.

### Explicitly not required for the MVP

- supply lines;
- encirclement;
- infrastructure bonuses;
- large economy systems;
- battle pass / seasonal progression;
- a full PvE campaign;
- machine-controlled territory;
- UNIT 0 gameplay events;
- bespoke maps built only to make the strategic layer work.

The MVP exists to answer one question:

> **Does changing the war make people want to play the next match?**

---

## Technology

Team Frontress currently combines:

- **Source 1 / Source SDK** game code;
- **Team Comtress 2** fixes, performance work and quality-of-life improvements as the gameplay foundation;
- a custom **Go Game Coordinator**;
- a persistent append-only **war event log**;
- dedicated server agents using **RCON + Source server logs**;
- Steam identity / session integration;
- an in-game HTML command board for the strategic layer;
- experimental player-hosted fallback for deployments where no dedicated node is available.

The transport is an implementation detail. The game design should remain the same whether a battle runs on a dedicated server or a temporary player-hosted instance.

---

## Repository map

```text
team-frontress/
├── game/                       game content and runtime files
├── src/                        Source / TC2 game code
├── services/
│   └── coordinator/            war engine, matchmaking and server pool
├── docs/
│   ├── GREYLINE-WAR.md         strategic design as implemented
│   ├── GREYLINE-STATUS.md      what actually works and what does not
│   ├── GREYLINE-MVP-TESTING.md end-to-end MVP testing
│   ├── GREYLINE-TESTING.md     broader testing notes
│   ├── STEAM-SETUP.md          Steam integration setup
│   ├── STEAMPIPE.md            build / deployment notes
│   └── DEDICATED-SERVER.md     dedicated node setup
└── LICENSE
```

---

## Start here

If you want to understand the project rather than just browse code:

1. **[`docs/GREYLINE-WAR.md`](docs/GREYLINE-WAR.md)** — what the war is supposed to do.
2. **[`docs/GREYLINE-STATUS.md`](docs/GREYLINE-STATUS.md)** — what is actually built, tested, written or missing.
3. **[`services/coordinator/README.md`](services/coordinator/README.md)** — coordinator architecture and API.
4. **[`docs/GREYLINE-MVP-TESTING.md`](docs/GREYLINE-MVP-TESTING.md)** — how to exercise the current MVP path.
5. **[`docs/DEDICATED-SERVER.md`](docs/DEDICATED-SERVER.md)** — bringing up a game-server node.

---

## Development philosophy

This project previously accumulated large systems faster than they could be validated. Team Frontress is being developed under a simpler rule:

### **Build one real loop, prove it works, then generalize it.**

That means:

- no giant feature framework before one working piece of content exists;
- no strategic mechanic that only changes a number on a screen;
- no fragmentation into separate queues unless the population can support it;
- no pretending `written` means `working`;
- no story system that becomes a prerequisite for having a good TF2 match.

The war should create gameplay — **not merely count gameplay**.

---

## Contributing

This is an active experimental fork. Useful contributions include:

- reproducing and fixing Source / TC2 regressions;
- building and testing game-side Team Frontress changes;
- coordinator tests and failure-case coverage;
- dedicated-server and Steam networking validation;
- UI work for the war command board;
- map-pool and player-count testing;
- documentation that distinguishes tested behaviour from assumptions.

Before opening a large feature PR, please read the current status and war design documents so new work does not accidentally rebuild retired systems.

---

## Credits

Team Frontress is built on the work of **Valve**, the **Source SDK** and the **Team Comtress 2** community.

Team Comtress 2 exists to fix bugs, improve performance and add quality-of-life improvements to Team Fortress 2. Team Frontress uses that work as its technical gameplay foundation while developing a separate persistent-war multiplayer layer on top.

## Legal

Valve, Steam, Source, Team Fortress and related names, logos and assets are trademarks and/or registered trademarks of Valve Corporation.

Team Frontress is a community project and is **not sponsored, endorsed, licensed by, or affiliated with Valve Corporation**.

Source SDK-derived code in this repository is distributed under the terms in [`LICENSE`](LICENSE) and the accompanying third-party notices.

---

<div align="center">

### TEAM FRONTRESS

**ONE WAR. THOUSANDS OF BATTLES.**

`RED` **────── ⚔ ──────** `BLU`

</div>
