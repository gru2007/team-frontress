#!/usr/bin/env bash
#
# Build Team Frontress' own pak1.vpk from the tracked game source tree.
# The resulting VPK is shared by the client and dedicated-server packages.

set -euo pipefail

ROOT="$(cd -- "$(dirname -- "$0")/.." && pwd)"
SOURCE_DIR="${ROOT}/game_src/tc2/pak1"
OUTPUT_DIR="${ROOT}/game/tc2"
OUTPUT_VPK="${OUTPUT_DIR}/pak1.vpk"

if [ ! -d "${SOURCE_DIR}" ]; then
  echo "VPK source directory does not exist: ${SOURCE_DIR}" >&2
  exit 1
fi

VPKEDITCLI="${VPKEDITCLI:-}"
if [ -z "${VPKEDITCLI}" ]; then
  VPKEDITCLI="$(command -v vpkeditcli || true)"
fi
if [ -z "${VPKEDITCLI}" ] || [ ! -x "${VPKEDITCLI}" ]; then
  echo "vpkeditcli was not found." >&2
  echo "Install VPKEdit or set VPKEDITCLI=/path/to/vpkeditcli." >&2
  exit 1
fi

mkdir -p "${OUTPUT_DIR}"
rm -f "${OUTPUT_DIR}"/pak1*.vpk

# Source 1 VPK v1, stored as one file. gameinfo.txt mounts pak1.vpk directly,
# and the tracked payload is far below the 4 GiB single-file VPK limit.
"${VPKEDITCLI}" "${SOURCE_DIR}" \
  --type vpk \
  --version 1 \
  --single-file \
  --output "${OUTPUT_VPK}" \
  --no-progress

test -s "${OUTPUT_VPK}"

# VPK v1 stores CRCs per entry; make the build fail instead of shipping a VPK
# whose directory or payload was written incorrectly.
"${VPKEDITCLI}" "${OUTPUT_VPK}" --verify-checksums files

echo "Team Frontress VPK ready: ${OUTPUT_VPK}"
du -h "${OUTPUT_VPK}" 2>/dev/null || true
