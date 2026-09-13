# Frontress contextual instructor

`frontress_instructor.cpp` is the small native presentation layer for persistent-war context. It deliberately does **not** own campaign rules.

## Current integration

The HUD listens to ordinary TF round events and already shows two useful campaign messages:

- first `teamplay_round_start`: explains that the battle belongs to the persistent war;
- `teamplay_round_win`: explains that the result is being applied to the campaign.

It also exposes a direct client API:

```cpp
FrontressInstructor_Show(
    "FOUNDRY 17",
    "BLU broke the outer line. Winning opens the final assault.",
    100,
    8.0f,
    "operation"
);
```

Hints with the same `replace_key` replace one another. A different hint only interrupts the current one when its priority is at least as high. `frontress_instructor_enable` is archived per client.

For visual testing in a local build:

```text
frontress_hint_test
```

## Campaign event contract

The HUD is prepared to consume a `frontress_instructor_hint` game event with these fields once the campaign producer registers/fires it:

- `title` — short heading;
- `text` — one- or two-line explanation;
- `priority` — higher wins;
- `duration` — seconds;
- `replace_key` — logical slot such as `operation`, `front`, `reinforcements`.

Recommended semantic producers are `first_deploy`, `operation_started`, `stage_changed`, `final_stage`, `front_changed`, `reinforcements_needed` and `party_deploy_available`. Those names belong to campaign code; the HUD should continue receiving only presentation-ready hints.

## Why this is not a wholesale Momentum/Mapbase copy

Team Frontress currently does not carry Mapbase's locator/Game Instructor stack. Importing it wholesale would pull world-space locator targets, scripted lesson parsing, persistence and several UI/resource dependencies at once. This module takes the useful architecture first (priority, replacement, timeout, event-driven presentation) without making TC2 depend on that whole subsystem. World-space target arrows can be added later as a separate locator feature.
