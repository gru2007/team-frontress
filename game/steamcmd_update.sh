#!/usr/bin/env bash

set -euo pipefail

ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"

# These are dependencies of our server build, not the Tool itself. Keep them
# beside tc2/ so gameinfo_server.txt can mount ../tf2 deterministically.
steamcmd +force_install_dir "${ROOT}/SteamLinuxRuntime_sniper" +login anonymous +app_update 1628350 validate +quit
steamcmd +force_install_dir "${ROOT}/tf2" +login anonymous +app_update 232250 validate +quit
