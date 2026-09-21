#!/usr/bin/env bash
#
# Compatibility wrapper. Team Frontress no longer downloads mastercomfig/tc2-pak;
# the VPK is built from the tracked game_src tree.

set -euo pipefail
BIN_DIR="$(cd -- "$(dirname -- "$0")" && pwd)"
exec "${BIN_DIR}/buildpak.sh" "$@"
