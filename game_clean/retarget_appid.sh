#!/usr/bin/env bash
#
# Re-stamp a packaged content directory with a different Steam AppID.
#
# The same build is published under two apps -- the playtest and the main app --
# and the AppID is not only in the SteamPipe build script: it is baked into the
# content itself. Rebuilding the whole client just to change a number would cost
# a second full CI run, so the packaged directory is stamped instead, once per
# app, right before it is handed to steampipe.sh.
#
# Usage:
#   ./retarget_appid.sh <content_dir> <appid>
#
# What gets stamped:
#   tc2/steam.inf            appID=            (ServerAppID is left alone)
#   tc2/gameinfo.txt         FileSystem/SteamAppId
#   tc2/gameinfo_server.txt  FileSystem/SteamAppId
#   steam_appid.txt          dedicated content only; never shipped in a client
#                            depot, but the dedicated payload runs off it
#
# What does not, and why it does not matter:
#   The launcher binaries carry MOD_APPID from launcher_main_tc2.vpc. It is only
#   used when nothing set SteamAppId in the environment, and Steam always sets it
#   for an app it launched, so a client started from either app initialises as
#   the app it was started from. The number only shows through when the .exe is
#   run outside Steam, where it decides which of the two apps a hand-run build
#   attaches to.
#
#   The macOS bundle is signed, so it cannot be edited after the fact without
#   invalidating the seal. It does not need to be: its launcher stamps the copy
#   it stages out of the bundle from the AppID Steam launched it with, so one
#   bundle serves both apps.
#
# The script is idempotent -- stamping content that already carries the target
# AppID is a no-op -- and it fails rather than silently doing nothing if a file
# it expects is missing or a stamp did not take.

set -euo pipefail

CONTENT_DIR="${1:-}"
APPID="${2:-}"

if [ -z "${CONTENT_DIR}" ] || [ -z "${APPID}" ]; then
  echo "usage: $0 <content_dir> <appid>" >&2
  exit 2
fi

if ! [[ "${APPID}" =~ ^[0-9]+$ ]]; then
  echo "AppID must be numeric: ${APPID}" >&2
  exit 2
fi

if [ ! -d "${CONTENT_DIR}" ]; then
  echo "content dir does not exist: ${CONTENT_DIR}" >&2
  exit 1
fi

CONTENT_DIR="$(cd "${CONTENT_DIR}" && pwd)"

if [ ! -f "${CONTENT_DIR}/tc2/gameinfo.txt" ]; then
  echo "${CONTENT_DIR} does not look like packaged content: no tc2/gameinfo.txt." >&2
  exit 1
fi

# sed -i is spelled differently on macOS.
sed_inplace() {
  local expr=$1
  local file=$2
  local tmp
  tmp="$(mktemp "${file}.XXXXXX")"
  sed -E "${expr}" "${file}" > "${tmp}"
  # Keep the mode the packaging gave it; mktemp makes the copy 0600.
  chmod --reference="${file}" "${tmp}" 2>/dev/null || chmod 644 "${tmp}"
  mv -f "${tmp}" "${file}"
}

# Stamp one file and prove it took: a key that moved or was renamed upstream
# would otherwise leave the old AppID in content published under the new app,
# which Steam accepts and players cannot start.
stamp() {
  local file=$1
  local expr=$2
  local verify=$3

  if [ ! -f "${file}" ]; then
    echo "missing: ${file}" >&2
    exit 1
  fi

  sed_inplace "${expr}" "${file}"

  if ! grep -Eq "${verify}" "${file}"; then
    echo "stamp did not take in ${file}: no line matching /${verify}/ after rewriting." >&2
    exit 1
  fi

  echo "  $(basename "${file}"): $(grep -E "${verify}" "${file}" | head -n 1 | tr -s '[:space:]' ' ' | sed -E 's/^ +| +$//g')"
}

echo "Stamping ${CONTENT_DIR} as AppID ${APPID}"

# PatchVersion/ClientVersion/ServerVersion and ServerAppID stay as they are:
# only the app the content belongs to changes.
stamp "${CONTENT_DIR}/tc2/steam.inf" \
  "s/^([[:space:]]*appID=)[0-9]+[[:space:]]*$/\\1${APPID}/" \
  "^[[:space:]]*appID=${APPID}[[:space:]]*$"

# The key is indented with tabs inside the FileSystem block; keep whatever
# whitespace is between the key and the value so the file stays readable.
for GAMEINFO in gameinfo.txt gameinfo_server.txt; do
  stamp "${CONTENT_DIR}/tc2/${GAMEINFO}" \
    "s/^([[:space:]]*SteamAppId[[:space:]]+)[0-9]+[[:space:]]*$/\\1${APPID}/" \
    "^[[:space:]]*SteamAppId[[:space:]]+${APPID}[[:space:]]*$"
done

# Only the dedicated payload ships one; steampipe.sh excludes it from client
# depots because it would bypass Steam's ownership check.
if [ -f "${CONTENT_DIR}/steam_appid.txt" ]; then
  printf '%s\n' "${APPID}" > "${CONTENT_DIR}/steam_appid.txt"
  echo "  steam_appid.txt: ${APPID}"
fi

# A second copy of a key -- a gameinfo with two FileSystem blocks, a steam.inf
# that grew an override -- would leave the first stamp looking like it took
# while the value the engine reads is still the old app's.
STALE=0
for FILE in tc2/steam.inf tc2/gameinfo.txt tc2/gameinfo_server.txt; do
  while IFS= read -r LINE; do
    echo "::warning::${FILE} still carries another AppID:${LINE}"
    STALE=1
  done < <(grep -E "^[[:space:]]*(appID=|SteamAppId[[:space:]]+)[0-9]+" "${CONTENT_DIR}/${FILE}" \
    | grep -vE "(appID=|SteamAppId[[:space:]]+)${APPID}[[:space:]]*$" || true)
done
if [ "${STALE}" = "1" ]; then
  echo "Content was not fully retargeted to ${APPID}." >&2
  exit 1
fi

echo "Stamped ${CONTENT_DIR} as AppID ${APPID}."
