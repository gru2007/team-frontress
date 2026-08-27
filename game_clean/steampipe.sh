#!/usr/bin/env bash
#
# Upload a build to Steam through SteamPipe.
#
# Generates the app/depot build scripts from packaged build directories, then
# hands them to steamcmd's run_app_build. steamcmd is
# downloaded into cibin/ if it isn't on PATH.
#
# Usage:
#   ./steampipe.sh [content_dir]
#
#     content_dir  content for the platform this is running on (defaults to
#                  ${CLEAN_DIR}, i.e. what copy.sh just produced). Relative
#                  paths resolve against the directory you invoke this from,
#                  same as push.sh.
#
# To upload several platforms in one build (a single BuildID covering all
# depots), skip the argument and set STEAM_WIN_DIR, STEAM_LINUX_DIR and/or
# STEAM_MAC_DIR.
#
# Environment:
#   STEAM_APPID         app to build into        (default 5147520, Team Frontress Playtest)
#   STEAM_DEPOT_WIN     Windows content depot    (default STEAM_APPID + 1)
#   STEAM_DEPOT_LINUX   Linux content depot      (default STEAM_APPID + 2)
#   STEAM_DEPOT_MAC     macOS content depot      (default STEAM_APPID + 3)
#   STEAM_WIN_DIR       Windows content dir      (overrides the positional arg)
#   STEAM_LINUX_DIR     Linux content dir        (overrides the positional arg)
#   STEAM_MAC_DIR       macOS content dir        (overrides the positional arg)
#   STEAM_BRANCH        beta branch to set live  (empty = upload only, set live by hand)
#   STEAM_USERNAME      Steam builder account    (required)
#   STEAM_CONFIG_VDF    base64 of a config.vdf already logged in as that
#                       account, so CI does not need a Steam Guard code
#   STEAM_PASSWORD      password, for interactive/local use when there is no
#                       STEAM_CONFIG_VDF
#   RELEASE_VERSION     build description        (default: git describe)
#   STEAMPIPE_PREVIEW   1 = generate + validate the build without uploading
#
# Run script within the directory
BIN_DIR=$(cd "$(dirname "$0")" && pwd)
ORIGINAL_DIR=$(pwd)
cd "${BIN_DIR}" || exit 2

set -euo pipefail

source ./shared.sh

STEAM_APPID="${STEAM_APPID:-5147520}"
STEAM_DEPOT_WIN="${STEAM_DEPOT_WIN:-$((STEAM_APPID + 1))}"
STEAM_DEPOT_LINUX="${STEAM_DEPOT_LINUX:-$((STEAM_APPID + 2))}"
STEAM_DEPOT_MAC="${STEAM_DEPOT_MAC:-$((STEAM_APPID + 3))}"
STEAM_BRANCH="${STEAM_BRANCH:-}"
STEAM_USERNAME="${STEAM_USERNAME:-}"
STEAM_CONFIG_VDF="${STEAM_CONFIG_VDF:-}"
STEAM_PASSWORD="${STEAM_PASSWORD:-}"
STEAMPIPE_PREVIEW="${STEAMPIPE_PREVIEW:-0}"

# The dist dir for the platform we're running on, unless told otherwise.
PLATFORM_DIR="${CLEAN_DIR}"
if [ -n "${1:-}" ]; then
  PLATFORM_DIR="${ORIGINAL_DIR}/$1"
fi

WIN_DIR="${STEAM_WIN_DIR:-}"
LINUX_DIR="${STEAM_LINUX_DIR:-}"
MAC_DIR="${STEAM_MAC_DIR:-}"

if [ -z "${WIN_DIR}" ] && [ -z "${LINUX_DIR}" ] && [ -z "${MAC_DIR}" ]; then
  if [ "${PLATFORM}" = "win" ]; then
    WIN_DIR="${PLATFORM_DIR}"
  elif [ "${PLATFORM}" = "linux" ]; then
    LINUX_DIR="${PLATFORM_DIR}"
  else
    MAC_DIR="${PLATFORM_DIR}"
  fi
fi

if [ -n "${RELEASE_VERSION:-}" ]; then
  VERSION="${RELEASE_VERSION}"
elif [ "${GITHUB_REF_TYPE:-}" = "tag" ] && [ -n "${GITHUB_REF_NAME:-}" ]; then
  VERSION="${GITHUB_REF_NAME}"
fi

BUILD_DESC="${VERSION}"
if [ -n "${GITHUB_SHA:-}" ]; then
  BUILD_DESC="${BUILD_DESC} (${GITHUB_SHA:0:8})"
elif COMMIT=$(git rev-parse --short HEAD 2>/dev/null); then
  BUILD_DESC="${BUILD_DESC} (${COMMIT})"
fi

if [ -z "${STEAM_USERNAME}" ]; then
  echo "STEAM_USERNAME is not set: nothing to log in as." >&2
  exit 1
fi

if [ -z "${STEAM_CONFIG_VDF}" ] && [ -z "${STEAM_PASSWORD}" ]; then
  echo "Set STEAM_CONFIG_VDF (CI) or STEAM_PASSWORD (local) to log in." >&2
  exit 1
fi

# Absolute paths: steamcmd resolves everything in the build scripts relative to
# its own working directory, not to the script.
abs_path() {
  (cd "$1" >/dev/null 2>&1 && pwd)
}

check_content_dir() {
  local NAME=$1
  local DIR=$2

  if [ ! -d "${DIR}" ]; then
    echo "${NAME} content dir does not exist: ${DIR}" >&2
    exit 1
  fi

  if [ -z "$(ls -A "${DIR}")" ]; then
    echo "${NAME} content dir is empty: ${DIR}" >&2
    exit 1
  fi
}

if [ -n "${WIN_DIR}" ]; then
  check_content_dir "Windows" "${WIN_DIR}"
  WIN_DIR=$(abs_path "${WIN_DIR}")
fi

if [ -n "${LINUX_DIR}" ]; then
  check_content_dir "Linux" "${LINUX_DIR}"
  LINUX_DIR=$(abs_path "${LINUX_DIR}")
fi

if [ -n "${MAC_DIR}" ]; then
  check_content_dir "macOS" "${MAC_DIR}"
  MAC_DIR=$(abs_path "${MAC_DIR}")
fi

BUILD_DIR="${BIN_DIR}/../steam_build"
rm -rf "${BUILD_DIR}"
mkdir -p "${BUILD_DIR}/output"
BUILD_DIR=$(abs_path "${BUILD_DIR}")

# steamcmd on Windows wants Windows paths in the build scripts.
script_path() {
  if [ "${PLATFORM}" = "win" ] && command -v cygpath >/dev/null 2>&1; then
    cygpath -w "$1"
  else
    echo "$1"
  fi
}

write_depot_script() {
  local DEPOT=$1
  local CONTENT=$2

  cat > "${BUILD_DIR}/depot_${DEPOT}.vdf" <<EOF
"DepotBuildConfig"
{
	"DepotID"		"${DEPOT}"
	"ContentRoot"	"$(script_path "${CONTENT}")"

	"FileMapping"
	{
		"LocalPath"	"*"
		"DepotPath"	"."
		"recursive"	"1"
	}

	// Debug info ships out of band (game_dist_debug), and steam_appid.txt must
	// never ship in the client depot because it bypasses Steam ownership checks.
	"FileExclusion"	"*.pdb"
	"FileExclusion"	"*.dbg"
	"FileExclusion"	"steam_appid.txt"
}
EOF
}

DEPOT_ENTRIES=""

if [ -n "${WIN_DIR}" ]; then
  write_depot_script "${STEAM_DEPOT_WIN}" "${WIN_DIR}"
  DEPOT_ENTRIES="${DEPOT_ENTRIES}		\"${STEAM_DEPOT_WIN}\"	\"$(script_path "${BUILD_DIR}/depot_${STEAM_DEPOT_WIN}.vdf")\"
"
fi

if [ -n "${LINUX_DIR}" ]; then
  write_depot_script "${STEAM_DEPOT_LINUX}" "${LINUX_DIR}"
  DEPOT_ENTRIES="${DEPOT_ENTRIES}		\"${STEAM_DEPOT_LINUX}\"	\"$(script_path "${BUILD_DIR}/depot_${STEAM_DEPOT_LINUX}.vdf")\"
"
fi

if [ -n "${MAC_DIR}" ]; then
  write_depot_script "${STEAM_DEPOT_MAC}" "${MAC_DIR}"
  DEPOT_ENTRIES="${DEPOT_ENTRIES}		\"${STEAM_DEPOT_MAC}\"	\"$(script_path "${BUILD_DIR}/depot_${STEAM_DEPOT_MAC}.vdf")\"
"
fi

APP_SCRIPT="${BUILD_DIR}/app_build_${STEAM_APPID}.vdf"
cat > "${APP_SCRIPT}" <<EOF
"appbuild"
{
	"appid"			"${STEAM_APPID}"
	"desc"			"${BUILD_DESC}"
	"buildoutput"	"$(script_path "${BUILD_DIR}/output")"
	"contentroot"	""
	"setlive"		"${STEAM_BRANCH}"
	"preview"		"${STEAMPIPE_PREVIEW}"
	"local"			""

	"depots"
	{
${DEPOT_ENTRIES}	}
}
EOF

echo "SteamPipe build script:"
cat "${APP_SCRIPT}"

# --- steamcmd ---------------------------------------------------------------

# Outside the repo on purpose: steamcmd keeps the login token next to itself,
# and CI caches the workspace.
STEAMCMD_DIR="${STEAMCMD_HOME:-${HOME}/steamcmd}/${PLATFORM}"

install_steamcmd() {
  mkdir -p "${STEAMCMD_DIR}"

  if [ "${PLATFORM}" = "win" ]; then
    curl -sSL --fail --retry 3 "https://steamcdn-a.akamaihd.net/client/installer/steamcmd.zip" -o "${STEAMCMD_DIR}/steamcmd.zip"
    $CMD_7Z x -y "${STEAMCMD_DIR}/steamcmd.zip" "-o${STEAMCMD_DIR}"
    STEAMCMD="${STEAMCMD_DIR}/steamcmd.exe"
  elif [ "${PLATFORM}" = "linux" ]; then
    # steamcmd is a 32 bit binary; the runtime it needs isn't in every image.
    if ! ldconfig -p 2>/dev/null | grep -q "libgcc_s.so.1 (libc6,x86"; then
      if command -v sudo >/dev/null 2>&1 && command -v apt-get >/dev/null 2>&1; then
        sudo dpkg --add-architecture i386
        sudo apt-get update
        sudo apt-get install -y --no-install-recommends lib32gcc-s1 lib32stdc++6
      else
        echo "warning: 32 bit libgcc not found and can't install it; steamcmd may fail to start." >&2
      fi
    fi

    curl -sSL --fail --retry 3 "https://steamcdn-a.akamaihd.net/client/installer/steamcmd_linux.tar.gz" -o "${STEAMCMD_DIR}/steamcmd_linux.tar.gz"
    tar -xzf "${STEAMCMD_DIR}/steamcmd_linux.tar.gz" -C "${STEAMCMD_DIR}"
    STEAMCMD="${STEAMCMD_DIR}/steamcmd.sh"
  else
    curl -sSL --fail --retry 3 "https://steamcdn-a.akamaihd.net/client/installer/steamcmd_osx.tar.gz" -o "${STEAMCMD_DIR}/steamcmd_osx.tar.gz"
    tar -xzf "${STEAMCMD_DIR}/steamcmd_osx.tar.gz" -C "${STEAMCMD_DIR}"
    STEAMCMD="${STEAMCMD_DIR}/steamcmd.sh"
  fi

  chmod +x "${STEAMCMD}" 2>/dev/null || true
}

if [ -n "${STEAMCMD:-}" ]; then
  :
elif command -v steamcmd >/dev/null 2>&1; then
  STEAMCMD=$(command -v steamcmd)
else
  install_steamcmd
fi

echo "Using steamcmd: ${STEAMCMD}"

# Steam Guard: a config.vdf from an account that has already answered a code
# lets steamcmd log in unattended. Drop it everywhere steamcmd looks for one.
if [ -n "${STEAM_CONFIG_VDF}" ]; then
  for CONFIG_DIR in "${HOME}/Steam/config" "${HOME}/.steam/steam/config" "$(dirname "${STEAMCMD}")/config"; do
    mkdir -p "${CONFIG_DIR}"
    echo "${STEAM_CONFIG_VDF}" | base64 -d > "${CONFIG_DIR}/config.vdf"
    chmod 600 "${CONFIG_DIR}/config.vdf"
  done
fi

# --- upload -----------------------------------------------------------------

RUN_LOG="${BUILD_DIR}/steamcmd.log"

run_steamcmd() {
  if [ -n "${STEAM_CONFIG_VDF}" ]; then
    "${STEAMCMD}" +login "${STEAM_USERNAME}" +run_app_build "$(script_path "${APP_SCRIPT}")" +quit
  else
    "${STEAMCMD}" +login "${STEAM_USERNAME}" "${STEAM_PASSWORD}" +run_app_build "$(script_path "${APP_SCRIPT}")" +quit
  fi
}

# steamcmd exits 0 on some failed logins and self-updates on first run, so the
# log is what we trust. Retry a couple of times for transient CDN/login errors.
ATTEMPT=1
MAX_ATTEMPTS=3
while true; do
  echo "steamcmd attempt ${ATTEMPT}/${MAX_ATTEMPTS}"

  set +e
  run_steamcmd 2>&1 | tee "${RUN_LOG}"
  STATUS=${PIPESTATUS[0]}
  set -e

  if [ ${STATUS} -eq 0 ] && grep -qi "successfully finished appid ${STEAM_APPID}" "${RUN_LOG}"; then
    break
  fi

  if [ ${ATTEMPT} -ge ${MAX_ATTEMPTS} ]; then
    echo "steamcmd failed after ${MAX_ATTEMPTS} attempts (exit ${STATUS})." >&2
    exit 1
  fi

  ATTEMPT=$((ATTEMPT + 1))
  sleep $((ATTEMPT * 5))
done

if [ -n "${STEAM_BRANCH}" ]; then
  echo "Uploaded AppID ${STEAM_APPID} and set branch '${STEAM_BRANCH}' live."
else
  echo "Uploaded AppID ${STEAM_APPID}. No branch set live; do it from Steamworks."
fi
