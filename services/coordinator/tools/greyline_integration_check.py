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
# Every server-side greyline source, because the console vocabulary a battle is
# driven by is not all in one file: the briefing declares the war context, the
# muster declares the hold, the host state declares what it publishes back.
SERVER_GREYLINE_DIR = "src/game/server/greyline"
# The menu page is the second driver of that same vocabulary: when a player
# hosts the battle on their own machine there is no agent, and the page types
# the very same commands into the game's own console. It drifting out of step
# with the agent fails exactly as silently.
MENU_PAGE = "game/tc2/loose/resource/html/greyline.html"
# What the page asks the engine over the web RPC, which the client registers.
MENU_RPC = "src/game/client/greyline/greyline_menu_rpc.cpp"
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


def server_sources(root):
    """Every .cpp under the server's greyline directory, path-relative."""
    d = os.path.join(root, SERVER_GREYLINE_DIR)
    if not os.path.isdir(d):
        fail(f"missing directory: {SERVER_GREYLINE_DIR}")
        return []
    return [
        os.path.join(SERVER_GREYLINE_DIR, name)
        for name in sorted(os.listdir(d))
        if name.endswith(".cpp")
    ]


def check_convars(root):
    """Every greyline_* the agent types must be declared in the game DLL."""
    agent = read(root, AGENT)
    sources = server_sources(root)
    game = "".join(read(root, rel) for rel in sources)
    if not agent or not game:
        return

    # ConVar greyline_x( "greyline_x", ... )  and  CON_COMMAND_F( greyline_y, ...
    declared = set(re.findall(r'ConVar\s+\w+\(\s*"(greyline_\w+)"', game))
    declared |= set(re.findall(r"CON_COMMAND_F\(\s*(greyline_\w+)", game))

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
        fail(f"{SERVER_GREYLINE_DIR}: found no greyline_* declarations — did the parser break?")
        return

    for name in sorted(used):
        if name not in declared:
            fail(
                f"the agent runs '{name}' but nothing in {SERVER_GREYLINE_DIR}/ declares such a "
                f"convar or command — the server would answer 'Unknown command' and whatever "
                f"that line was setting up would silently not happen"
            )

    notes.append(
        f"agent drives {len(used)} greyline commands, all declared across "
        f"{len(sources)} server source(s)"
    )

    # The menu page drives the same console when a player hosts the battle
    # themselves. Everything it types has to exist too, and it is the half
    # nothing else in CI reads.
    page_used = set()
    page = read(root, MENU_PAGE)
    if page:
        # Bridge.call names are web RPC methods, not console commands — they
        # are checked separately, against what the client registers.
        rpc_calls = set(re.findall(r'Bridge\.call\(\s*"(greyline_\w+)"', page))
        for lit in re.findall(r'"([^"\n]*)"', page):
            m = re.match(r"^(greyline_\w+)", lit.strip())
            if m and m.group(1) not in rpc_calls:
                page_used.add(m.group(1))

        if not page_used:
            fail(f"{MENU_PAGE}: found no greyline_* commands at all — did the parser break?")
        for name in sorted(page_used):
            if name not in declared:
                fail(
                    f"the menu page runs '{name}' when a player hosts the battle, but nothing "
                    f"in {SERVER_GREYLINE_DIR}/ declares such a convar or command"
                )
        if page_used:
            notes.append(
                f"menu page drives {len(page_used)} greyline commands, all declared too"
            )

        # And the methods it asks the engine for have to be registered.
        rpc_src = read(root, MENU_RPC)
        if rpc_src:
            registered = set(re.findall(r'RegisterMethod\(\s*"(greyline_\w+)"', rpc_src))
            for name in sorted(rpc_calls):
                if name not in registered:
                    fail(
                        f"the menu page calls the '{name}' bridge method, but "
                        f"{os.path.basename(MENU_RPC)} registers no such method — the web RPC "
                        f"answers an empty string and the page waits for something that "
                        f"never comes"
                    )
            if rpc_calls:
                notes.append(
                    "menu page's engine queries all registered: " + ", ".join(sorted(rpc_calls))
                )

    unused = declared - used - page_used
    if unused:
        notes.append(
            "declared but driven by neither the agent nor the menu page "
            "(fine if they are for humans, or read rather than set): "
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
    checked = 0

    for vpc in (CLIENT_VPC, SERVER_VPC):
        text = read(root, vpc)
        if not text:
            continue

        vpc_dir = os.path.dirname(os.path.join(root, vpc))
        srcdir = os.path.join(root, "src")

        for m in re.finditer(r'\$File\s+"([^"]*greyline[^"]*)"', text):
            raw = m.group(1)
            rel = raw.replace("\\", "/")
            if rel.startswith("$SRCDIR/"):
                path = os.path.join(srcdir, rel[len("$SRCDIR/"):])
            else:
                path = os.path.join(vpc_dir, rel)
            if not os.path.exists(path):
                fail(f"{vpc} lists {raw}, which does not exist")
                continue

            if "greyline_legacy" in rel:
                fail(
                    f"{vpc} lists {raw} from the retired directory — nothing in "
                    f"{LEGACY_DIR} is meant to be compiled"
                )

            if rel.endswith(".cpp"):
                checked += 1
                check_pch(vpc, raw, path, text, m.end())

    notes.append(f"both vpc files list only greyline sources that exist ({checked} .cpp)")


def check_pch(vpc, raw, path, vpc_text, after):
    """A source file that skips cbase.h must opt out of the precompiled header.

    The game DLLs compile with /Yu through cbase.h, so a .cpp that does not
    include it first fails on MSVC with C1010 — 'unexpected end of file while
    looking for precompiled header'. gcc ignores the whole thing, so a Linux
    build and every test here would pass while the Windows build broke.
    """
    with open(path, encoding="utf-8", errors="replace") as f:
        source = f.read()

    includes = re.findall(r'^\s*#include\s+[<"]([^>"]+)[>"]', source, re.M)
    uses_pch = bool(includes) and includes[0] == "cbase.h"
    if uses_pch:
        return

    # Whatever follows this $File up to the next one is its own configuration.
    tail = vpc_text[after:]
    nxt = re.search(r"\$File|\$Folder|\$DynamicFile", tail)
    block = tail[: nxt.start()] if nxt else tail

    if "Not Using Precompiled Headers" not in block:
        first = includes[0] if includes else "nothing"
        fail(
            f"{vpc} compiles {raw} with the project's precompiled header, but the "
            f"file includes {first} rather than cbase.h — MSVC would fail with "
            f"C1010. Either include cbase.h first or give the $File a "
            f'$Configuration with $Create/UsePrecompiledHeader "Not Using '
            f'Precompiled Headers"'
        )
        return

    notes.append(f"{os.path.basename(raw)} correctly opts out of the precompiled header")


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
