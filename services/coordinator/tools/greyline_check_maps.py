#!/usr/bin/env python3
"""Checks a theater's battlefields against a real game install.

The theater names maps; the maps live in whatever TF2/TC2 install the dedicated
servers run. Nothing connects the two until a battle is assigned and the server
answers the changelevel with nothing at all — at which point the war is stuck on
a front waiting for a result that will never come.

So point this at a maps directory before an evening of testing:

    ./tools/greyline_check_maps.py theater.industrial.json /path/to/tf/maps

Without a maps directory it still checks the parts that need no game: that the
envelopes make sense, that every stage a profile plans for has battlefields, and
that no stage's pool is so small the same map comes round every battle.
"""

import json
import os
import re
import sys

STAGES = ("skirmish", "breakthrough", "advance", "assault")

# Below this, a front plays the same handful of maps all evening — which is the
# thing the rotation exists to prevent.
MIN_POOL = 2

failures = []
warnings = []
notes = []


def load_theater(path):
    with open(path, encoding="utf-8") as f:
        return json.load(f)


def battlefields(theater):
    """Yields (profile_id, stage, entry)."""
    for prof in theater.get("battle_profiles", []):
        for stage, entries in prof.get("battlefields", {}).items():
            for entry in entries:
                yield prof.get("id", "?"), stage, entry


def check_structure(theater):
    profiles = {p.get("id"): p for p in theater.get("battle_profiles", [])}
    if not profiles:
        failures.append("the theater defines no battle profiles")
        return

    # Every node must name a profile that exists, or opening a front there fails
    # at the moment somebody presses DEPLOY.
    for node in theater.get("nodes", []):
        prof = node.get("profile")
        if prof not in profiles:
            failures.append(
                f"node {node.get('id')} uses profile {prof!r}, which is not defined"
            )

    for pid, prof in profiles.items():
        stages = prof.get("stages", [])
        if not stages:
            failures.append(f"profile {pid} plans no stages")
        fields = prof.get("battlefields", {})

        for stage in stages:
            pool = fields.get(stage, [])
            if not pool:
                failures.append(
                    f"profile {pid} plans stage {stage!r} but lists no battlefield for it"
                )
            elif len(pool) < MIN_POOL:
                warnings.append(
                    f"profile {pid} stage {stage} has only {len(pool)} map — a front "
                    f"stuck on this stage replays it every battle"
                )

        # The skirmish list is the fallback whenever the queue is too small for
        # the stage's own maps, so it carries every low-population evening.
        skirmish = fields.get("skirmish", [])
        if not skirmish:
            failures.append(
                f"profile {pid} has no skirmish list — a small queue would have "
                f"nothing to fall back to"
            )
        elif len(skirmish) < MIN_POOL:
            warnings.append(f"profile {pid} has only {len(skirmish)} skirmish map")

        for stage in fields:
            if stage not in STAGES:
                failures.append(f"profile {pid} lists unknown stage {stage!r}")


def check_envelopes(theater):
    for pid, stage, e in battlefields(theater):
        name = e.get("map", "?")
        where = f"{pid}/{stage}/{name}"

        mn, ideal, mx = e.get("min_players"), e.get("ideal_players"), e.get("max_players")
        if not isinstance(mn, int) or not isinstance(mx, int):
            failures.append(f"{where}: min_players and max_players are required")
            continue
        if mn < 2:
            failures.append(f"{where}: min_players {mn} is below one player per team")
        if mx < mn:
            failures.append(f"{where}: max_players {mx} is below min_players {mn}")
        if mn % 2 or mx % 2:
            warnings.append(f"{where}: envelope {mn}-{mx} is odd, so a team would be short")
        if ideal is not None and not (mn <= ideal <= mx):
            failures.append(f"{where}: ideal_players {ideal} is outside {mn}-{mx}")
        if mx > 32:
            warnings.append(f"{where}: max_players {mx} exceeds a Source server's slots")

        if not e.get("mode"):
            failures.append(f"{where}: no mode, so the agent cannot set win conditions")


def check_coverage(theater):
    """Every population the coordinator can form must have somewhere to play."""
    for prof in theater.get("battle_profiles", []):
        pid = prof.get("id", "?")
        fields = prof.get("battlefields", {})
        everything = [e for s in fields.values() for e in s]
        if not everything:
            continue

        for players in range(4, 25, 2):
            fits = [e for e in everything
                    if e.get("min_players", 99) <= players <= e.get("max_players", 0)]
            if not fits:
                failures.append(
                    f"profile {pid} has no battlefield at all for {players} players — "
                    f"the war would fall back to its smallest map"
                )


def check_modes(theater, repo_root):
    """Modes the theater uses must be ones the agent knows win conditions for."""
    agent = os.path.join(repo_root, "cmd", "greyline-agent", "battle.go")
    if not os.path.exists(agent):
        return

    with open(agent, encoding="utf-8") as f:
        text = f.read()
    m = re.search(r"func modeRules\(mode string\) \[\]string \{(.*?)\n\}", text, re.S)
    if not m:
        return
    known = set(re.findall(r'case ((?:"[a-z_0-9]+"(?:,\s*)?)+):', m.group(1)))
    known = set(re.findall(r'"([a-z_0-9]+)"', " ".join(known)))

    used = {e.get("mode", "").lower() for _p, _s, e in battlefields(theater)}
    for mode in sorted(used - known):
        warnings.append(
            f"mode {mode!r} has no entry in the agent's modeRules, so the battle "
            f"runs on whatever the map's defaults are and may never end"
        )
    if used & known:
        notes.append(f"modes with win conditions: {', '.join(sorted(used & known))}")


def check_maps_exist(theater, maps_dir):
    present = set()
    for name in os.listdir(maps_dir):
        if name.endswith(".bsp"):
            present.add(name[: -len(".bsp")])

    if not present:
        failures.append(f"{maps_dir} contains no .bsp files")
        return

    wanted = {e.get("map") for _p, _s, e in battlefields(theater)}
    missing = sorted(wanted - present)

    for name in missing:
        failures.append(f"{name}.bsp is not in {maps_dir} — a battle assigned it would hang")

    notes.append(f"{len(wanted - set(missing))}/{len(wanted)} maps present in {maps_dir}")


def main():
    if len(sys.argv) < 2:
        print(__doc__, file=sys.stderr)
        return 2

    theater_path = sys.argv[1]
    maps_dir = sys.argv[2] if len(sys.argv) > 2 else None
    repo_root = os.path.dirname(os.path.abspath(theater_path))

    theater = load_theater(theater_path)

    check_structure(theater)
    check_envelopes(theater)
    check_coverage(theater)
    check_modes(theater, repo_root)

    if maps_dir:
        if not os.path.isdir(maps_dir):
            failures.append(f"{maps_dir} is not a directory")
        else:
            check_maps_exist(theater, maps_dir)
    else:
        wanted = {e.get("map") for _p, _s, e in battlefields(theater)}
        notes.append(
            f"{len(wanted)} distinct maps named; pass a maps directory to check they exist"
        )

    for n in notes:
        print(f"  ok   {n}")
    for w in warnings:
        print(f"  warn {w}")

    if failures:
        print(file=sys.stderr)
        for f in failures:
            print(f"  FAIL {f}", file=sys.stderr)
        print(f"\ngreyline_check_maps: {len(failures)} problem(s)", file=sys.stderr)
        return 1

    print(f"greyline_check_maps: {os.path.basename(theater_path)} is playable")
    return 0


if __name__ == "__main__":
    sys.exit(main())
