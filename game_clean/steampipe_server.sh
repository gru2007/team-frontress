#!/usr/bin/env bash
#
# Upload the Linux dedicated artifact to Steam Tool AppID 5150320.
#
# Required:
#   STEAM_DS_DEPOT_LINUX  Linux depot belonging to Tool 5150320
#   STEAM_USERNAME
#   STEAM_CONFIG_VDF      base64 config.vdf for unattended CI
# or STEAM_PASSWORD for interactive/local use.
#
# Optional:
#   STEAM_DS_APPID=5150320
#   STEAM_DS_BRANCH
#   STEAMPIPE_PREVIEW=1

set -euo pipefail

ROOT="$(cd -- "$(dirname -- "$0")/.." && pwd)"
APPID="${STEAM_DS_APPID:-5150320}"
DEPOT="${STEAM_DS_DEPOT_LINUX:-}"
BRANCH="${STEAM_DS_BRANCH:-}"
PREVIEW="${STEAMPIPE_PREVIEW:-0}"
USERNAME="${STEAM_USERNAME:-}"
CONFIG_VDF="${STEAM_CONFIG_VDF:-}"
PASSWORD="${STEAM_PASSWORD:-}"
CONTENT="${1:-${ROOT}/game_server_dist}"

if [[ "${CONTENT}" != /* ]]; then
  CONTENT="$(pwd)/${CONTENT}"
fi
if [ -z "${DEPOT}" ]; then
  echo "STEAM_DS_DEPOT_LINUX is required; use the actual depot owned by ${APPID}." >&2
  exit 1
fi
if [ ! -f "${CONTENT}/steam_appid.txt" ] || [ "$(tr -d '\r\n ' < "${CONTENT}/steam_appid.txt")" != "5147520" ]; then
  echo "Dedicated content must contain steam_appid.txt = 5147520." >&2
  exit 1
fi
if [ -z "${USERNAME}" ]; then
  echo "STEAM_USERNAME is required." >&2
  exit 1
fi
if [ -z "${CONFIG_VDF}" ] && [ -z "${PASSWORD}" ]; then
  echo "Set STEAM_CONFIG_VDF (CI) or STEAM_PASSWORD (local)." >&2
  exit 1
fi

BUILD_DIR="${ROOT}/steam_build_server"
rm -rf "${BUILD_DIR}"
mkdir -p "${BUILD_DIR}/output"

VERSION="${RELEASE_VERSION:-$(git -C "${ROOT}" describe --always --dirty 2>/dev/null || echo dedicated)}"
cat > "${BUILD_DIR}/depot_${DEPOT}.vdf" <<EOF
"DepotBuildConfig"
{
    "DepotID"       "${DEPOT}"
    "ContentRoot"   "${CONTENT}"

    "FileMapping"
    {
        "LocalPath" "*"
        "DepotPath" "."
        "recursive" "1"
    }

    "FileExclusion" "*.pdb"
    "FileExclusion" "*.dbg"
    "FileExclusion" ".itch.toml"
}
EOF

cat > "${BUILD_DIR}/app_build_${APPID}.vdf" <<EOF
"appbuild"
{
    "appid"         "${APPID}"
    "desc"          "${VERSION}"
    "buildoutput"   "${BUILD_DIR}/output"
    "contentroot"   ""
    "setlive"       "${BRANCH}"
    "preview"       "${PREVIEW}"
    "local"         ""

    "depots"
    {
        "${DEPOT}"  "${BUILD_DIR}/depot_${DEPOT}.vdf"
    }
}
EOF

if command -v steamcmd >/dev/null 2>&1; then
  STEAMCMD="$(command -v steamcmd)"
else
  STEAMCMD_HOME="${STEAMCMD_HOME:-${HOME}/steamcmd/team-frontress-ds}"
  mkdir -p "${STEAMCMD_HOME}"
  if command -v sudo >/dev/null 2>&1 && command -v apt-get >/dev/null 2>&1; then
    sudo dpkg --add-architecture i386
    sudo apt-get update
    sudo apt-get install -y --no-install-recommends lib32gcc-s1 lib32stdc++6
  fi
  curl -sSL --fail --retry 3     https://steamcdn-a.akamaihd.net/client/installer/steamcmd_linux.tar.gz     -o "${STEAMCMD_HOME}/steamcmd_linux.tar.gz"
  tar -xzf "${STEAMCMD_HOME}/steamcmd_linux.tar.gz" -C "${STEAMCMD_HOME}"
  STEAMCMD="${STEAMCMD_HOME}/steamcmd.sh"
fi

if [ -n "${CONFIG_VDF}" ]; then
  for DIR in "${HOME}/Steam/config" "${HOME}/.steam/steam/config" "$(dirname "${STEAMCMD}")/config"; do
    mkdir -p "${DIR}"
    echo "${CONFIG_VDF}" | base64 -d > "${DIR}/config.vdf"
    chmod 600 "${DIR}/config.vdf"
  done
fi

LOG="${BUILD_DIR}/steamcmd.log"
if [ -n "${CONFIG_VDF}" ]; then
  "${STEAMCMD}" +login "${USERNAME}" +run_app_build "${BUILD_DIR}/app_build_${APPID}.vdf" +quit 2>&1 | tee "${LOG}"
else
  "${STEAMCMD}" +login "${USERNAME}" "${PASSWORD}" +run_app_build "${BUILD_DIR}/app_build_${APPID}.vdf" +quit 2>&1 | tee "${LOG}"
fi

grep -qi "successfully finished appid ${APPID}" "${LOG}" || {
  echo "SteamPipe did not report a successful AppID ${APPID} build." >&2
  exit 1
}

echo "Uploaded dedicated Tool AppID ${APPID}, depot ${DEPOT}."
