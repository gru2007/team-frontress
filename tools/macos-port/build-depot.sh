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
#   WINE_COMPAT_DIR           prebuilt wine-compat pieces; by default they are
#                             built here from tools/macos-port/wine-compat
#   RELEASE_VERSION           CFBundleShortVersionString
#   BUILD_NUMBER              CFBundleVersion
#   CODESIGN_IDENTITY         signing identity; ad-hoc if unset

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
SCRIPT_DIR="${ROOT}/tools/macos-port"
BRIDGE_DIR="${SCRIPT_DIR}/steam-bridge"
COMPAT_SRC_DIR="${SCRIPT_DIR}/wine-compat"
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

# -- what the runtime pieces actually are -------------------------------------
#
# Every one of these is a "the game starts and vanishes" failure when it is
# wrong, and none of them is visible in a directory listing: a Mach-O unixlib
# built for the wrong architecture, a PE that is not x86-64, or a Wine builtin
# that was never stamped as one. They are cheap to check here and expensive to
# diagnose on a player's machine.

# file(1) rather than lipo(1): lipo is a Command Line Tools binary, and this
# script has no other reason to require them.
macho_archs() {
	/usr/bin/file -b "$1" 2>/dev/null \
		| tr ' ,[]' '\n\n\n\n' \
		| grep -oE '^(x86_64|arm64e|arm64|i386)$' \
		| sed 's/arm64e/arm64/' \
		| sort -u \
		| tr '\n' ' ' \
		| sed 's/ *$//'
}

require_macho() {
	local path=$1 label=$2 archs
	require_file "${path}"
	if ! /usr/bin/file -b "${path}" | grep -q 'Mach-O'; then
		printf '%s is not a Mach-O binary: %s\n' "${label}" "${path}" >&2
		exit 1
	fi
	archs="$(macho_archs "${path}")"
	if [ -z "${archs}" ]; then
		printf '%s has no recognisable architecture: %s\n' "${label}" "${path}" >&2
		exit 1
	fi
	printf '%s' "${archs}"
}

# Wine loads a unixlib into the Wine process itself, so an arm64 d9mtmetal.so
# next to an x86_64 Wine is not a slow path, it is a load failure -- and the
# only symptom is a client that exits before it draws a frame.
require_arch_of_wine() {
	local path=$1 label=$2 archs arch
	archs="$(require_macho "${path}" "${label}")"
	for arch in ${archs}; do
		case " ${WINE_ARCHS} " in
			*" ${arch} "*) printf '   %-22s %s\n' "${label}" "${archs}"; return 0 ;;
		esac
	done
	printf '%s is %s but the Wine runtime is %s.\n' "${label}" "${archs}" "${WINE_ARCHS}" >&2
	printf 'Wine cannot load a unixlib built for another architecture; use a build that matches.\n' >&2
	exit 1
}

# The PE machine word, read out of the header rather than out of a file(1)
# description, which differs between file versions.
pe_machine() {
	local path=$1 lfanew
	[ "$(dd if="${path}" bs=2 count=1 2>/dev/null)" = "MZ" ] || return 1
	lfanew="$(od -An -tu4 -j 60 -N 4 "${path}" 2>/dev/null | tr -d ' ')"
	[ -n "${lfanew}" ] || return 1
	[ "$(dd if="${path}" bs=1 skip=$((lfanew)) count=2 2>/dev/null)" = "PE" ] || return 1
	od -An -tx2 -j $((lfanew + 4)) -N 2 "${path}" 2>/dev/null | tr -d ' \n'
}

require_pe_x64() {
	local path=$1 label=$2 machine
	require_file "${path}"
	machine="$(pe_machine "${path}" || true)"
	if [ "${machine}" != "8664" ]; then
		printf '%s is not an x86-64 PE image (machine=%s): %s\n' \
			"${label}" "${machine:-none}" "${path}" >&2
		exit 1
	fi
	printf '   %-22s PE x86-64\n' "${label}"
}

is_wine_builtin() {
	dd if="$1" bs=1 skip=64 count=17 2>/dev/null | LC_ALL=C grep -q 'Wine builtin DLL'
}

# A PE Wine only treats as a builtin once winebuild's marker is in its DOS stub.
# Without it Wine logs "found in WINEDLLPATH but not a builtin, ignoring", never
# pairs the DLL with its .so, and D3D9 creation fails inside the client.
require_wine_builtin() {
	local path=$1 label=$2
	if is_wine_builtin "${path}"; then
		printf '   %-22s Wine builtin\n' "${label}"
		return 0
	fi
	printf '   %-22s not marked as a Wine builtin; stamping it\n' "${label}"
	python3 "${BRIDGE_DIR}/mark-builtin.py" "${path}"
	is_wine_builtin "${path}" || {
		printf '%s could not be marked as a Wine builtin: %s\n' "${label}" "${path}" >&2
		exit 1
	}
}

require_dir "${TC2_WINDOWS_DIR}"
require_dir "${WINE_RUNTIME_DIR}"
require_dir "${D9MT_DIST_DIR}"
require_file "${TC2_WINDOWS_DIR}/tc2_win64.exe"

WINE_BIN="${WINE_RUNTIME_DIR}/bin/wine64"
if [ ! -x "${WINE_BIN}" ]; then
	WINE_BIN="${WINE_RUNTIME_DIR}/bin/wine"
fi
if [ ! -x "${WINE_BIN}" ]; then
	printf 'Wine runtime has neither bin/wine nor bin/wine64: %s\n' "${WINE_RUNTIME_DIR}" >&2
	exit 1
fi

require_file "${WINE_LICENSE_FILE}"
require_file "${D9MT_LICENSE_FILE}"

printf '== checking the runtimes\n'
WINE_ARCHS="$(require_macho "${WINE_BIN}" "wine")"
printf '   %-22s %s\n' "wine" "${WINE_ARCHS}"

# D9MT is three files that have to agree with each other and with Wine: the
# PE frontend that ships next to the .exe, and a builtin/unixlib pair per
# translated DLL.
# d9mtmetal.dll is the one file of the package that is not shipped as it comes:
# d9mt builds it against CrossOver's ntdll, and wine-compat rebuilds it for the
# Wine this bundle carries. Its unixlib half below is the package's own.
require_pe_x64 "${D9MT_DIST_DIR}/d3d9.dll" "d3d9.dll"
require_pe_x64 "${D9MT_DIST_DIR}/x86_64-windows/winemetal.dll" "winemetal.dll"
require_arch_of_wine "${D9MT_DIST_DIR}/x86_64-unix/d9mtmetal.so" "d9mtmetal.so"
require_arch_of_wine "${D9MT_DIST_DIR}/x86_64-unix/winemetal.so" "winemetal.so"

# Valve's library is loaded by the bridge's unix half, which is the Wine process
# itself, so it has to carry Wine's architecture too.
require_arch_of_wine "${STEAMWORKS_REDIST_DYLIB}" "libsteam_api.dylib"

# The bridge is source in this repository, so it is built rather than supplied.
# STEAM_BRIDGE_DIR stays available for building it once and packaging it many
# times, which is what the release workflow does.
if [ -z "${STEAM_BRIDGE_DIR:-}" ]; then
	printf '== building the Steamworks bridge\n'
	OUTPUT_DIR="${BRIDGE_DIR}/dist" "${BRIDGE_DIR}/build.sh"
	STEAM_BRIDGE_DIR="${BRIDGE_DIR}/dist"
fi

require_pe_x64 "${STEAM_BRIDGE_DIR}/x86_64-windows/steam_api64.dll" "steam_api64.dll"
require_arch_of_wine "${STEAM_BRIDGE_DIR}/x86_64-unix/steam_api64.so" "steam_api64.so"
require_file "${STEAM_BRIDGE_DIR}/LICENSE"

# What D9MT needs from CrossOver's Wine and cannot get from an LGPL one; source
# in this repository, so built here for the same reason the bridge is.
if [ -z "${WINE_COMPAT_DIR:-}" ]; then
	printf '== building the Wine compatibility pieces\n'
	"${COMPAT_SRC_DIR}/build.sh" "${COMPAT_SRC_DIR}/dist"
	WINE_COMPAT_DIR="${COMPAT_SRC_DIR}/dist"
fi

require_pe_x64 "${WINE_COMPAT_DIR}/x86_64-windows/d9mtmetal.dll" "d9mtmetal.dll"
require_arch_of_wine "${WINE_COMPAT_DIR}/lib/libmacdrvshim.dylib" "libmacdrvshim.dylib"

rm -rf "${OUTPUT_DIR}"
mkdir -p "${CONTENTS_DIR}/MacOS" "${RESOURCES_DIR}/licenses" \
	"${RESOURCES_DIR}/wine/lib/wine/x86_64-windows" \
	"${RESOURCES_DIR}/wine/lib/wine/x86_64-unix" \
	"${RESOURCES_DIR}/lib" \
	"${RESOURCES_DIR}/steam"

/usr/bin/ditto "${TC2_WINDOWS_DIR}" "${RESOURCES_DIR}/game"
/usr/bin/ditto "${WINE_RUNTIME_DIR}" "${RESOURCES_DIR}/wine"
cp "${SCRIPT_DIR}/Info.plist" "${CONTENTS_DIR}/Info.plist"
cp "${SCRIPT_DIR}/team-frontress" "${CONTENTS_DIR}/MacOS/team-frontress"
cp "${SCRIPT_DIR}/install-tf2" "${RESOURCES_DIR}/install-tf2"
chmod +x "${CONTENTS_DIR}/MacOS/team-frontress" "${RESOURCES_DIR}/install-tf2"

cp "${D9MT_DIST_DIR}/d3d9.dll" "${RESOURCES_DIR}/game/d3d9.dll"
cp "${WINE_COMPAT_DIR}/x86_64-windows/d9mtmetal.dll" "${RESOURCES_DIR}/wine/lib/wine/x86_64-windows/"
cp "${WINE_COMPAT_DIR}/lib/libmacdrvshim.dylib" "${RESOURCES_DIR}/lib/"
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

# The builtin marker is checked on the copies rather than on the inputs: an
# unmarked DLL is stamped, and a fetched runtime directory somebody else also
# uses is not something this script should be editing.
printf '== checking the Wine builtins\n'
for builtin in winemetal d9mtmetal steam_api64; do
	require_wine_builtin \
		"${RESOURCES_DIR}/wine/lib/wine/x86_64-windows/${builtin}.dll" "${builtin}.dll"
done

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

# steam_appid.txt next to the binary makes Steamworks skip the ownership check,
# so it must not be in a shipped bundle even if the Windows lane produced one.
rm -f "${RESOURCES_DIR}/game/steam_appid.txt"

# -- symlinks -----------------------------------------------------------------
#
# The depot is delivered by SteamPipe, which has no symlink in its file format:
# what a player ends up with is whatever the builder made of one. A Wine tree
# that arrives with bin/wine64 or a versioned dylib missing is a client that
# exits before it prints anything, so the links are resolved here, while the
# result can still be signed and verified.
#
# A pass can expose more links, because a link to a directory is replaced by a
# copy of that directory, links and all.
printf '== resolving symlinks\n'
for pass in 1 2 3 4 5; do
	resolved=0
	while IFS= read -r -d '' link; do
		if [ ! -e "${link}" ]; then
			printf '   dangling, removed: %s -> %s\n' "${link}" "$(readlink "${link}")"
			rm -f "${link}"
			resolved=$((resolved + 1))
			continue
		fi
		if [ -d "${link}" ]; then
			/usr/bin/ditto "${link}/" "${link}.resolved"
		else
			cp -p "${link}" "${link}.resolved"
		fi
		rm -f "${link}"
		mv "${link}.resolved" "${link}"
		resolved=$((resolved + 1))
	done < <(find "${APP_DIR}" -type l -print0)

	printf '   pass %d: %d resolved\n' "${pass}" "${resolved}"
	[ "${resolved}" -gt 0 ] || break
done

REMAINING="$(find "${APP_DIR}" -type l | head -n 5)"
if [ -n "${REMAINING}" ]; then
	printf 'symlinks are still present after five passes:\n%s\n' "${REMAINING}" >&2
	exit 1
fi

# Extended attributes -- quarantine flags, resource forks left by an archive --
# are sealed into the signature and then stripped by whatever the bundle passes
# through next, which invalidates it. Clear them before signing rather than
# after.
/usr/bin/xattr -rc "${APP_DIR}" 2>/dev/null || true

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
done < <(find "${RESOURCES_DIR}/wine" "${RESOURCES_DIR}/steam" "${RESOURCES_DIR}/lib" \
	-type f \( -perm -u+x -o -name '*.dylib' -o -name '*.so' \) -print0)

/usr/bin/codesign --force --timestamp=none --sign "${IDENTITY}" "${APP_DIR}"

# -- what has to be true of the finished bundle -------------------------------
printf '== verifying\n'
/usr/bin/codesign --verify --deep --strict "${APP_DIR}"

# The entitlements are the whole reason the nested binaries are signed one by
# one; a bundle signed without them runs until the first page Wine writes and
# then executes.
# The output format of -d --entitlements has changed more than once; whatever
# shape it comes out in, the entitlement's name is in it.
ENTITLEMENTS="$(/usr/bin/codesign -d --entitlements - \
	"${RESOURCES_DIR}/wine/bin/$(basename "${WINE_BIN}")" 2>/dev/null || true)"
case "${ENTITLEMENTS}" in
	*allow-jit*) ;;
	*)
		printf 'the Wine binary in the bundle carries no JIT entitlement\n' >&2
		exit 1
		;;
esac

# The bundle's executable is what LaunchServices and Steam actually run, and a
# depot that lost its executable bit is the quietest possible failure.
if [ ! -x "${CONTENTS_DIR}/MacOS/team-frontress" ]; then
	printf 'the bundle executable is not executable\n' >&2
	exit 1
fi
if [ "$(head -c 2 "${CONTENTS_DIR}/MacOS/team-frontress")" != "#!" ]; then
	printf 'the bundle executable has no interpreter line\n' >&2
	exit 1
fi
/usr/bin/plutil -lint "${CONTENTS_DIR}/Info.plist" >/dev/null

for required in \
	"${RESOURCES_DIR}/game/tc2_win64.exe" \
	"${RESOURCES_DIR}/game/d3d9.dll" \
	"${RESOURCES_DIR}/wine/lib/wine/x86_64-windows/winemetal.dll" \
	"${RESOURCES_DIR}/wine/lib/wine/x86_64-windows/d9mtmetal.dll" \
	"${RESOURCES_DIR}/wine/lib/wine/x86_64-windows/steam_api64.dll" \
	"${RESOURCES_DIR}/wine/lib/wine/x86_64-unix/winemetal.so" \
	"${RESOURCES_DIR}/wine/lib/wine/x86_64-unix/d9mtmetal.so" \
	"${RESOURCES_DIR}/wine/lib/wine/x86_64-unix/steam_api64.so" \
	"${RESOURCES_DIR}/steam/libsteam_api.dylib" \
	"${RESOURCES_DIR}/lib/libmacdrvshim.dylib" \
	"${RESOURCES_DIR}/install-tf2" \
; do
	require_file "${required}"
done

touch "${OUTPUT_DIR}/STEAM_READY"
printf 'macOS depot ready: %s (%s)\n' "${APP_DIR}" "$(du -sh "${APP_DIR}" | cut -f1)"
