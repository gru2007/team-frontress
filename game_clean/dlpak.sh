#!/usr/bin/env bash
#
# Run script within the directory
BIN_DIR=$(dirname "$(readlink -fn "$0")")
cd "${BIN_DIR}" || exit 2

set -e

source ./shared.sh

PAK_VERSION_FILE="${DEV_DIR}/version.txt"
PAK_REPO="mastercomfig/tc2-pak"

CUR_PAK_VERSION=$( (cat "$PAK_VERSION_FILE" 2>/dev/null || echo "") | tr -d '\n')

if [[ -f "${DEV_DIR}/tc2/pak1_dir.vpk" && $CUR_PAK_VERSION == $PAK_VERSION ]]; then
  echo "pak already downloaded"
  exit 0
fi

# GitHub's release download link (github.com/.../releases/download/...) has
# been seen answering 504 for older releases for hours, while the same file
# comes through the API's asset endpoint. --fail keeps an error page from
# being saved as pak.zip; the API is the fallback.
CURL_AUTH=()
GH_AUTH_TOKEN="${GITHUB_TOKEN:-${GH_TOKEN:-}}"
if [ -n "${GH_AUTH_TOKEN}" ]; then
  # Unauthenticated API calls share a 60-per-hour limit per address, and CI
  # runners share addresses.
  CURL_AUTH=(-H "Authorization: Bearer ${GH_AUTH_TOKEN}")
fi

download_release_link() {
  curl -fL --retry 3 --retry-delay 5 --retry-all-errors \
    "https://github.com/${PAK_REPO}/releases/download/${PAK_VERSION}/pak.zip" -o pak.zip
}

download_api_asset() {
  local asset
  asset=$(curl -fsSL --retry 3 --retry-delay 5 ${CURL_AUTH[@]+"${CURL_AUTH[@]}"} \
      "https://api.github.com/repos/${PAK_REPO}/releases/tags/${PAK_VERSION}" \
    | tr -d '\n' \
    | grep -o "\"url\": *\"https://api.github.com/repos/${PAK_REPO}/releases/assets/[0-9]*\"[^{}]*\"name\": *\"pak.zip\"" \
    | grep -o "https://api.github.com/repos/${PAK_REPO}/releases/assets/[0-9]*" \
    | head -n 1)
  if [ -z "${asset}" ]; then
    echo "release ${PAK_VERSION} of ${PAK_REPO} has no pak.zip" >&2
    return 1
  fi
  curl -fL --retry 3 --retry-delay 5 --retry-all-errors ${CURL_AUTH[@]+"${CURL_AUTH[@]}"} \
    -H "Accept: application/octet-stream" "${asset}" -o pak.zip
}

rm -f pak.zip
if ! download_release_link; then
  echo "pak ${PAK_VERSION}: the release link failed; trying the GitHub API" >&2
  rm -f pak.zip
  download_api_asset
fi

if ! $CMD_7Z t pak.zip > /dev/null; then
  echo "pak ${PAK_VERSION}: the download is not a valid archive ($(wc -c < pak.zip) bytes)" >&2
  rm -f pak.zip
  exit 1
fi

# A pak of another version can have more archives (pak1_001.vpk...) than
# this one: leftovers would ship with it.
rm -f "${DEV_DIR}"/tc2/pak1*.vpk
$CMD_7Z e pak.zip -y "-o${DEV_DIR}/tc2"

echo $PAK_VERSION > $PAK_VERSION_FILE
