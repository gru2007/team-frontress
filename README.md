<div align="center">

[![Logo](https://github.com/gru2007/team-frontress/blob/gf-mod/.github/logo.png?raw=true)]()

### ONE WAR. THOUSANDS OF BATTLES.

**A Team Fortress 2 multiplayer experiment where every match belongs to one persistent RED vs BLU war.**
<br>
<sub>**combines best from Helldivers, Foxhole, Planetside 2, HOI4**</sub>

[Воу! Русский язык поддерживается](README_RU.md)

</div>

---

> **The war does not count matches. It decides what the next match is.**

**Team Frontress** is an experimental multiplayer project built from **Team Comtress 2** and the Source 1 SDK.

The gunplay is intentionally familiar. The layer around it is not.

Instead of treating every match as an isolated round that disappears when the scoreboard closes, Team Frontress connects battles into a **single persistent campaign**. RED and BLU fight across a strategic theater. A victory advances an offensive, a defeat can push it back, capturing a region moves the front, and the new state of the war determines what battle should happen next.

```text
GLOBAL STRATEGY ── decides ──▶ NEXT BATTLE ── result ──▶ GLOBAL STRATEGY
```

### How map is working?

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

---

## The Second Gravel War

The first Team Frontress campaign begins with a simple premise.

After the Mann vs. Machine conflict, Mann Co. loses control of large parts of its industrial and logistical network. RED and BLU move in to claim what remains.

Factories. Rail yards. Warehouses. Power infrastructure. Research sites.

The result is a new corporate conflict

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
