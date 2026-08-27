#!/bin/bash
#
# Assemble the macOS depot: a self-contained Team Frontress.app around the
# Windows x64 client.
#
# Inputs:
#   TC2_WINDOWS_DIR           packaged Windows client (game_dist from the
#                             Windows lane)
#   WINE_RUNTIME_DIR          an LGPL Wine install tree for macOS x86_64
#   WINE_LICENSE_FILE         Wine's COPYING.LIB
#   D9MT_DIST_DIR             the d9mt-x64 package
#   D9MT_LICENSE_FILE         defaults to the notice in licenses/
#   STEAMWORKS_REDIST_DYLIB   Valve's macOS libsteam_api.dylib
#
# Optional:
#   STEAM_BRIDGE_DIR          a prebuilt bridge; by default it is built here
#                             from tools/macos-port/steam-bridge
#   RELEASE_VERSION           CFBundleShortVersionString
#   BUILD_NUMBER              CFBundleVersion
#   CODESIGN_IDENTITY         signing identity; ad-hoc if unset

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
SCRIPT_DIR="${ROOT}/tools/macos-port"
BRIDGE_DIR="${SCRIPT_DIR}/steam-bridge"
OUTPUT_DIR="${1:-${ROOT}/game_dist_macos}"
APP_DIR="${OUTPUT_DIR}/Team Frontress.app"
CONTENTS_DIR="${APP_DIR}/Contents"
RESOURCES_DIR="${CONTENTS_DIR}/Resources"
D9MT_LICENSE_FILE="${D9MT_LICENSE_FILE:-${SCRIPT_DIR}/licenses/D9MT-zlib.txt}"

: "${TC2_WINDOWS_DIR:?Set TC2_WINDOWS_DIR to the packaged Windows client directory}"
: "${WINE_RUNTIME_DIR:?Set WINE_RUNTIME_DIR to an LGPL Wine install tree}"
: "${WINE_LICENSE_FILE:?Set WINE_LICENSE_FILE to Wine COPYING.LIB}"
: "${D9MT_DIST_DIR:?Set D9MT_DIST_DIR to the d9mt-x64 directory}"
: "${STEAMWORKS_REDIST_DYLIB:?Set STEAMWORKS_REDIST_DYLIB to the macOS libsteam_api.dylib from the Steamworks SDK}"

require_file() {
	if [ ! -f "$1" ]; then
		printf 'Required file is missing: %s\n' "$1" >&2
		exit 1
	fi
}

require_dir() {
	if [ ! -d "$1" ]; then
		printf 'Required directory is missing: %s\n' "$1" >&2
		exit 1
	fi
}

require_dir "${TC2_WINDOWS_DIR}"
require_dir "${WINE_RUNTIME_DIR}"
require_dir "${D9MT_DIST_DIR}"
require_file "${TC2_WINDOWS_DIR}/tc2_win64.exe"
if [ ! -x "${WINE_RUNTIME_DIR}/bin/wine" ] && [ ! -x "${WINE_RUNTIME_DIR}/bin/wine64" ]; then
	printf 'Wine runtime has neither bin/wine nor bin/wine64: %s\n' "${WINE_RUNTIME_DIR}" >&2
	exit 1
fi
require_file "${WINE_LICENSE_FILE}"
require_file "${D9MT_LICENSE_FILE}"
require_file "${D9MT_DIST_DIR}/d3d9.dll"
require_file "${D9MT_DIST_DIR}/x86_64-windows/d9mtmetal.dll"
require_file "${D9MT_DIST_DIR}/x86_64-windows/winemetal.dll"
require_file "${D9MT_DIST_DIR}/x86_64-unix/d9mtmetal.so"
require_file "${D9MT_DIST_DIR}/x86_64-unix/winemetal.so"
require_file "${STEAMWORKS_REDIST_DYLIB}"

# The bridge is source in this repository, so it is built rather than supplied.
# STEAM_BRIDGE_DIR stays available for building it once and packaging it many
# times, which is what the release workflow does.
if [ -z "${STEAM_BRIDGE_DIR:-}" ]; then
	printf '== building the Steamworks bridge\n'
	OUTPUT_DIR="${BRIDGE_DIR}/dist" "${BRIDGE_DIR}/build.sh"
	STEAM_BRIDGE_DIR="${BRIDGE_DIR}/dist"
fi

require_file "${STEAM_BRIDGE_DIR}/x86_64-windows/steam_api64.dll"
require_file "${STEAM_BRIDGE_DIR}/x86_64-unix/steam_api64.so"
require_file "${STEAM_BRIDGE_DIR}/LICENSE"

rm -rf "${OUTPUT_DIR}"
mkdir -p "${CONTENTS_DIR}/MacOS" "${RESOURCES_DIR}/licenses" \
	"${RESOURCES_DIR}/wine/lib/wine/x86_64-windows" \
	"${RESOURCES_DIR}/wine/lib/wine/x86_64-unix" \
	"${RESOURCES_DIR}/steam"

/usr/bin/ditto "${TC2_WINDOWS_DIR}" "${RESOURCES_DIR}/game"
/usr/bin/ditto "${WINE_RUNTIME_DIR}" "${RESOURCES_DIR}/wine"
cp "${SCRIPT_DIR}/Info.plist" "${CONTENTS_DIR}/Info.plist"
cp "${SCRIPT_DIR}/team-frontress" "${CONTENTS_DIR}/MacOS/team-frontress"
cp "${SCRIPT_DIR}/install-tf2" "${RESOURCES_DIR}/install-tf2"
chmod +x "${CONTENTS_DIR}/MacOS/team-frontress" "${RESOURCES_DIR}/install-tf2"

cp "${D9MT_DIST_DIR}/d3d9.dll" "${RESOURCES_DIR}/game/d3d9.dll"
cp "${D9MT_DIST_DIR}/x86_64-windows/d9mtmetal.dll" "${RESOURCES_DIR}/wine/lib/wine/x86_64-windows/"
cp "${D9MT_DIST_DIR}/x86_64-windows/winemetal.dll" "${RESOURCES_DIR}/wine/lib/wine/x86_64-windows/"
cp "${D9MT_DIST_DIR}/x86_64-unix/d9mtmetal.so" "${RESOURCES_DIR}/wine/lib/wine/x86_64-unix/"
cp "${D9MT_DIST_DIR}/x86_64-unix/winemetal.so" "${RESOURCES_DIR}/wine/lib/wine/x86_64-unix/"

# The bridge halves go where Wine looks for a builtin and its unixlib. The
# Windows client also ships Valve's own steam_api64.dll next to the .exe; that
# copy stays, and the builtin override in the launcher is what decides which
# one is loaded.
cp "${STEAM_BRIDGE_DIR}/x86_64-windows/steam_api64.dll" "${RESOURCES_DIR}/wine/lib/wine/x86_64-windows/"
cp "${STEAM_BRIDGE_DIR}/x86_64-unix/steam_api64.so" "${RESOURCES_DIR}/wine/lib/wine/x86_64-unix/"
cp "${STEAMWORKS_REDIST_DYLIB}" "${RESOURCES_DIR}/steam/libsteam_api.dylib"

cp "${WINE_LICENSE_FILE}" "${RESOURCES_DIR}/licenses/Wine-LGPL-2.1.txt"
cp "${D9MT_LICENSE_FILE}" "${RESOURCES_DIR}/licenses/D9MT.txt"
cp "${STEAM_BRIDGE_DIR}/LICENSE" "${RESOURCES_DIR}/licenses/Steam-Bridge.txt"
cp "${ROOT}/thirdpartylegalnotices.txt" "${RESOURCES_DIR}/licenses/" 2>/dev/null || true

if [ -n "${RELEASE_VERSION:-}" ]; then
	# Tag names arrive as release/1.2.3; CFBundleShortVersionString only takes
	# the numeric part.
	SHORT_VERSION="${RELEASE_VERSION##*/}"
	/usr/libexec/PlistBuddy -c "Set :CFBundleShortVersionString ${SHORT_VERSION}" \
		"${CONTENTS_DIR}/Info.plist"
fi
if [ -n "${BUILD_NUMBER:-}" ]; then
	/usr/libexec/PlistBuddy -c "Set :CFBundleVersion ${BUILD_NUMBER}" "${CONTENTS_DIR}/Info.plist"
fi

# Signing, innermost first.
#
# The entitlements have to land on the Mach-O binaries that actually do the
# thing the hardened runtime objects to -- Wine and D9MT write pages and then
# execute them -- and not on the bundle. The bundle's executable is this
# launcher script, and a script carries no signature of its own: its process is
# /bin/bash, which has Apple's entitlements, not ours. Signing the bundle with
# --deep would also be wrong, because --deep signs nested code with no
# entitlements at all.
printf '== signing\n'
IDENTITY="${CODESIGN_IDENTITY:--}"

sign_macho() {
	if /usr/bin/file -b "$1" | grep -q "Mach-O"; then
		/usr/bin/codesign --force --timestamp=none --options runtime \
			--entitlements "${SCRIPT_DIR}/Frontress.entitlements" \
			--sign "${IDENTITY}" "$1"
	fi
}

while IFS= read -r -d '' binary; do
	sign_macho "${binary}"
done < <(find "${RESOURCES_DIR}/wine" "${RESOURCES_DIR}/steam" \
	-type f \( -perm -u+x -o -name '*.dylib' -o -name '*.so' \) -print0)

/usr/bin/codesign --force --timestamp=none --sign "${IDENTITY}" "${APP_DIR}"
/usr/bin/codesign --verify --deep --strict "${APP_DIR}"

touch "${OUTPUT_DIR}/STEAM_READY"
printf 'macOS depot ready: %s\n' "${APP_DIR}"
