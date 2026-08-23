#!/usr/bin/env bash

set -euo pipefail

steamcmd +force_install_dir SteamLinuxRuntime_sniper +login anonymous +app_update 1628350 validate +quit
steamcmd +force_install_dir tf2 +login anonymous +app_update 232250 validate +quit

# Source dedicated servers look for Steam's client library in ~/.steam/sdk64,
# while SteamCMD usually installs it under ~/.local/share/Steam/steamcmd (the
# Ubuntu package) or one of the traditional SteamCMD directories. Keep the SDK
# links in sync whenever dependencies are installed or updated.
steamcmd_root=""
for candidate in \
    "$HOME/.local/share/Steam/steamcmd" \
    "$HOME/.steam/steamcmd" \
    "$HOME/Steam" \
    "$HOME/steamcmd"
do
    if [ -f "$candidate/linux64/steamclient.so" ]; then
        steamcmd_root="$candidate"
        break
    fi
done

if [ -z "$steamcmd_root" ]; then
    echo "ERROR: SteamCMD steamclient.so not found after update" >&2
    exit 1
fi

mkdir -p "$HOME/.steam/sdk64"
ln -sfn "$steamcmd_root/linux64/steamclient.so" "$HOME/.steam/sdk64/steamclient.so"

if [ -f "$steamcmd_root/linux32/steamclient.so" ]; then
    mkdir -p "$HOME/.steam/sdk32"
    ln -sfn "$steamcmd_root/linux32/steamclient.so" "$HOME/.steam/sdk32/steamclient.so"
fi

test -e "$HOME/.steam/sdk64/steamclient.so"
