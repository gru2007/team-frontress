#!/usr/bin/env bash
#
# Build a separate Linux dedicated-server payload in ../game_server_dist.
# Compilation can be shared with the client, but the shipped artifact is not.

set -euo pipefail

BIN_DIR="$(cd -- "$(dirname -- "$0")" && pwd)"
cd "${BIN_DIR}"

source ./shared.sh
SERVER_CLEAN_DIR="../game_server_dist"

if [ "${PLATFORM}" != "linux" ]; then
  echo "Dedicated packaging currently supports Linux only." >&2
  exit 1
fi

# Reuse the normal compile output. If game_dist was not produced yet, create it.
if [ ! -d "${CLEAN_DIR}" ]; then
  ./copy.sh
fi

rm -rf "${SERVER_CLEAN_DIR}"
mkdir -p "${SERVER_CLEAN_DIR}"
cp -a "${CLEAN_DIR}/." "${SERVER_CLEAN_DIR}/"

# Dedicated does not need the client game DLL or render shader DLL.
rm -f   "${SERVER_CLEAN_DIR}/tc2/bin/${PLAT_DIR}/client${DLL_EXT}"   "${SERVER_CLEAN_DIR}/tc2/bin/${PLAT_DIR}/game_shader_generic_std${DLL_EXT}"

# Client-only helper. Keep tc2_linux64 itself: it is also the dedicated launcher.
rm -f "${SERVER_CLEAN_DIR}/hl2.sh"

# This legacy helper downloads an upstream TC2 release and can overwrite our
# custom build. The Tool is updated with `app_update 5150320`; dependencies are
# updated by steamcmd_update.sh.
rm -f "${SERVER_CLEAN_DIR}/update_dedicated.sh"

# Valve expects the dedicated package to contain the GAME AppID here, even
# though the depot itself belongs to Tool AppID 5150320.
cp -f "${DEV_DIR}/tc2/gameinfo_server.txt" "${SERVER_CLEAN_DIR}/tc2/gameinfo.txt"
cp -f "${DEV_DIR}/tc2/gameinfo_server.txt" "${SERVER_CLEAN_DIR}/tc2/gameinfo_server.txt"
printf '5147520\n' > "${SERVER_CLEAN_DIR}/steam_appid.txt"

chmod +x   "${SERVER_CLEAN_DIR}/tc2_linux64"   "${SERVER_CLEAN_DIR}/start_dedicated_tc2.sh"   "${SERVER_CLEAN_DIR}/steamcmd_update.sh"   "${SERVER_CLEAN_DIR}/srcds_run_64"

echo "Dedicated artifact ready: ${SERVER_CLEAN_DIR}"
echo "  SteamPipe Tool: 5150320"
echo "  Runtime AppID:   5147520"
echo "  dependencies:    cd game_server_dist && ./steamcmd_update.sh"
echo "  run:             GSLT=... ./start_dedicated_tc2.sh +map ctf_2fort +ip 0.0.0.0 -port 27015"
