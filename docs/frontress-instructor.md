# Frontress contextual instructor

`frontress_instructor.cpp` is a small native HUD presentation layer for persistent-war context. It deliberately does **not** import the full Momentum/Mapbase locator stack and it does not own campaign rules.

## Current integration

The component is included by `gamestate.vpc` and reacts to existing networked TF events, so it works without a new event schema:

- first `teamplay_round_start` on a map explains the persistent war and War Map;
- `teamplay_point_captured` explains that tactical objectives decide the local battle, while the confirmed result moves the campaign;
- `teamplay_overtime_begin` explains the contested operation state;
- `teamplay_round_win` explains coordinator/campaign result application.

Hints support priority, timeout and replacement keys. A same-key hint replaces the visible one; a different lower-priority hint cannot interrupt a higher-priority one.

Client settings:

```text
frontress_instructor_enable 1
frontress_instructor_round_hints 1
```

Visual smoke test:

```text
frontress_hint_test
frontress_hint_hide
```

## API for campaign-specific lessons

Campaign/game-rule code can add richer sector-specific messages without touching the HUD implementation:

```cpp
FrontressInstructor_Show(
    "FOUNDRY 17",
    "BLU broke the outer line. Winning this battle can advance the persistent front.",
    100,
    8.0f,
    "operation"
);
```

Recommended future call sites are the code that already knows about `first_deploy`, operation stage changes, reinforcements and coordinator-confirmed front changes. Those systems should pass presentation-ready text to the instructor rather than teaching the HUD campaign rules.

## Why this is not a wholesale Momentum/Mapbase copy

Team Frontress currently does not carry Mapbase's locator/Game Instructor stack. Importing it wholesale would pull world-space locator targets, scripted lesson parsing, persistence and several UI/resource dependencies at once. This implementation takes the useful architecture first — native HUD lessons, priority, replacement, timeout and event-driven presentation — while staying small enough for the current TC2 base. World-space target arrows can be added later as a separate locator feature.
