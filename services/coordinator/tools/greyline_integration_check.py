#!/usr/bin/env python3
"""Checks the seams between the coordinator, the agent and the game.

Three languages meet in this project and none of them can see the others:

    war engine (Go) --> agent (Go) --RCON--> game (C++) --> language files

The agent drives the server by typing convar names into a console. Nothing
verifies those names exist until a battle is running and the briefing is silently
blank, which is the worst possible time to find out. Same for the stage kinds:
the war engine invents them, the game translates them, and a new one added on
one side just prints "BATTLE" on the other.

So this reads both sides as text and insists they agree. It is deliberately dumb
— regexes, not parsers — because the alternative is nothing.

    ./tools/greyline_integration_check.py [repo-root]
"""

import os
import re
import sys

# --- where things live -------------------------------------------------------

AGENT = "services/coordinator/cmd/greyline-agent/battle.go"
WAR_MODEL = "services/coordinator/internal/war/model.go"
BRIEFING = "src/game/server/greyline/greyline_briefing.cpp"
LOGIC = "src/game/shared/greyline/greyline_briefing_logic.cpp"
CLIENT_VPC = "src/game/client/client_tf.vpc"
SERVER_VPC = "src/game/server/server_tf.vpc"
LEGACY_DIR = "src/game/greyline_legacy"

failures = []
notes = []


def fail(msg):
    failures.append(msg)


def read(root, rel):
    path = os.path.join(root, rel)
    if not os.path.exists(path):
        fail(f"missing file: {rel}")
        return ""
    with open(path, encoding="utf-8") as f:
        return f.read()


# --- 1. the agent's console vocabulary exists in the game ---------------------


def check_convars(root):
    """Every greyline_* the agent types must be declared in the game DLL."""
    agent = read(root, AGENT)
    briefing = read(root, BRIEFING)
    if not agent or not briefing:
        return

    # ConVar greyline_x( "greyline_x", ... )  and  CON_COMMAND_F( greyline_y, ...
    declared = set(re.findall(r'ConVar\s+\w+\(\s*"(greyline_\w+)"', briefing))
    declared |= set(re.findall(r"CON_COMMAND_F\(\s*(greyline_\w+)", briefing))

    # The agent builds commands as Go string literals: "greyline_x %q" or bare.
    used = set()
    for lit in re.findall(r'[`"]([^`"\n]*)[`"]', agent):
        m = re.match(r"^(greyline_\w+)", lit.strip())
        if m:
            used.add(m.group(1))

    if not used:
        fail(f"{AGENT}: found no greyline_* commands at all — did the parser break?")
        return
    if not declared:
        fail(f"{BRIEFING}: found no greyline_* declarations — did the parser break?")
        return

    for name in sorted(used):
        if name not in declared:
            fail(
                f"the agent runs '{name}' but {os.path.basename(BRIEFING)} declares no such "
                f"convar or command — the server would answer 'Unknown command' and the "
                f"briefing would be blank"
            )

    notes.append(f"agent drives {len(used)} greyline commands, all declared in the game")

    unused = declared - used
    if unused:
        notes.append(
            "declared but never driven by the agent (fine if they are for humans): "
            + ", ".join(sorted(unused))
        )


# --- 2. stage kinds the war can produce are ones the game can name ------------


def check_stage_kinds(root):
    model = read(root, WAR_MODEL)
    logic = read(root, LOGIC)
    if not model or not logic:
        return

    kinds = set(re.findall(r'Stage\w+\s+StageKind\s*=\s*"(\w+)"', model))
    if not kinds:
        fail(f"{WAR_MODEL}: found no StageKind constants — did the parser break?")
        return

    known = set(re.findall(r'EqualsNoCase\(\s*pszKind,\s*"(\w+)"\s*\)', logic))

    for kind in sorted(kinds):
        if kind not in known:
            fail(
                f"the war engine can produce stage '{kind}' but "
                f"{os.path.basename(LOGIC)} does not recognise it — players would be told "
                f"'BATTLE' instead of the stage name"
            )

    notes.append(f"stage kinds agree: {', '.join(sorted(kinds))}")


# --- 3. the build lists files that exist, and none of the retired ones --------


def check_vpc(root):
    for vpc in (CLIENT_VPC, SERVER_VPC):
        text = read(root, vpc)
        if not text:
            continue

        vpc_dir = os.path.dirname(os.path.join(root, vpc))
        srcdir = os.path.join(root, "src")

        for raw in re.findall(r'\$File\s+"([^"]*greyline[^"]*)"', text):
            rel = raw.replace("\\", "/")
            if rel.startswith("$SRCDIR/"):
                path = os.path.join(srcdir, rel[len("$SRCDIR/"):])
            else:
                path = os.path.join(vpc_dir, rel)
            if not os.path.exists(path):
                fail(f"{vpc} lists {raw}, which does not exist")

            if "greyline_legacy" in rel:
                fail(
                    f"{vpc} lists {raw} from the retired directory — nothing in "
                    f"{LEGACY_DIR} is meant to be compiled"
                )

    notes.append("both vpc files list only greyline sources that exist")


# --- 4. live code does not reach into the retired directory ------------------


def check_legacy_isolated(root):
    legacy = os.path.join(root, LEGACY_DIR)
    if not os.path.isdir(legacy):
        notes.append(f"{LEGACY_DIR} is gone; nothing to isolate")
        return

    retired = {
        f for f in os.listdir(legacy) if f.endswith((".h", ".cpp"))
    }
    if not retired:
        return

    live_roots = [
        os.path.join(root, "src", "game", "client"),
        os.path.join(root, "src", "game", "server"),
        os.path.join(root, "src", "game", "shared"),
    ]

    for live_root in live_roots:
        for dirpath, _dirs, files in os.walk(live_root):
            for name in files:
                if not name.endswith((".cpp", ".h")):
                    continue
                path = os.path.join(dirpath, name)
                with open(path, encoding="utf-8", errors="replace") as f:
                    text = f.read()
                for header in retired:
                    if not header.endswith(".h"):
                        continue
                    if re.search(r'#include\s+"[^"]*%s"' % re.escape(header), text):
                        rel = os.path.relpath(path, root)
                        fail(
                            f"{rel} includes {header}, which is retired in {LEGACY_DIR} "
                            f"and not compiled — the link would fail"
                        )

    notes.append(f"{len(retired)} retired files, none included by live code")


# --- 5. the language files the tests check are the ones the game loads --------


def check_localization_path(root):
    localize = read(root, "src/game/client/greyline/greyline_localize.cpp")
    if not localize:
        return

    m = re.search(r'AddFile\(\s*"([^"]+)"', localize)
    if not m:
        fail("greyline_localize.cpp no longer calls AddFile — no language file is loaded")
        return

    pattern = m.group(1)  # resource/greyline_%language%.txt
    if "%language%" not in pattern:
        fail(f"greyline_localize.cpp loads {pattern}, which is not language-dependent")

    stem = pattern.replace("%language%", "")
    res_dir = os.path.join(root, "game", "tc2", "loose", os.path.dirname(pattern))
    if not os.path.isdir(res_dir):
        fail(f"the game loads {pattern} but {os.path.relpath(res_dir, root)} does not exist")
        return

    prefix = os.path.basename(stem).split(".")[0]
    found = [f for f in os.listdir(res_dir) if f.startswith(prefix) and f.endswith(".txt")]
    if not found:
        fail(f"the game loads {pattern} but no matching file ships in {res_dir}")
        return

    notes.append(f"game loads {pattern}; {len(found)} language file(s) ship: "
                 + ", ".join(sorted(found)))


def main():
    root = sys.argv[1] if len(sys.argv) > 1 else "."
    root = os.path.abspath(root)

    if not os.path.isdir(os.path.join(root, "services", "coordinator")):
        print(f"greyline_integration_check: {root} does not look like the repository root",
              file=sys.stderr)
        return 2

    check_convars(root)
    check_stage_kinds(root)
    check_vpc(root)
    check_legacy_isolated(root)
    check_localization_path(root)

    for note in notes:
        print(f"  ok   {note}")

    if failures:
        print(file=sys.stderr)
        for f in failures:
            print(f"  FAIL {f}", file=sys.stderr)
        print(f"\ngreyline_integration_check: {len(failures)} problem(s)", file=sys.stderr)
        return 1

    print("greyline_integration_check: the seams agree")
    return 0


if __name__ == "__main__":
    sys.exit(main())
