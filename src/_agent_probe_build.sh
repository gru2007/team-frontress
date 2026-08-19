#!/usr/bin/env bash
set -euo pipefail

script=$(readlink -f -- "$0")
pushd "$(dirname -- "$script")" > /dev/null

source sdk_container
run_in_sniper "$@"

export CCACHE_SLOPPINESS="pch_defines,time_macros"
export VPC_ENABLE_CCACHE="1"

solution_out="_vpc_/ninja/agent_probe"

echo "=== generating VPC projects (client+server only, avoids the unrelated tools that crash VPC) ==="
devtools/bin/vpc /tf /linux64 /ninja /define:SOURCESDK +gamedlls /mksln "$solution_out"

echo "=== generating compile_commands.json ==="
ninja -f "$solution_out.ninja" -t compdb > compile_commands.json

echo "=== isolating compile commands for the new greyline files ==="
python3 - <<'PYEOF'
import json
targets = [
    "greyline_bridge.cpp",
    "greyline_http.cpp",
    "greyline_hoststate.cpp",
]
with open("compile_commands.json") as f:
    db = json.load(f)
found = {}
for entry in db:
    for t in targets:
        if entry["file"].endswith(t):
            found.setdefault(t, []).append(entry)
with open("/tmp/greyline_probe_commands.json", "w") as f:
    json.dump(found, f, indent=2)
for t in targets:
    print(t, "->", len(found.get(t, [])), "compile command(s) found")
PYEOF

popd
