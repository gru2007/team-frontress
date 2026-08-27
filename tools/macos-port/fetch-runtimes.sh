#!/bin/bash
#
# Fetch the three binary runtimes the macOS depot needs but this repository
# cannot carry, and lay them out the way build-depot.sh expects.
#
# The defaults below are the builds this port is developed against. Override any
# of them to test a different one; the layout handling is written against what
# each project actually ships, not against a fixed path, so a version bump
# usually needs nothing but a new URL.
#
# Each of these takes a URL or a local path, so an archive you already have on
# disk can be used without downloading it again.
#
#   MACOS_WINE_URL           Wine for macOS x86_64, as a .tar.xz or .tar.gz
#                            holding a Wine*.app bundle
#   MACOS_WINE_LICENSE_URL   Wine's COPYING.LIB (the builds do not ship it)
#   MACOS_D9MT_URL           the d9mt-x64 package, .zip or .tar.gz
#
# Valve's Steamworks redistributable is under the Steamworks SDK Access
# Agreement, so it has no public default. Give it in whichever form you have:
#
#   STEAMWORKS_REDIST_DYLIB  a local libsteam_api.dylib
#   STEAMWORKS_SDK_ZIP       a local steamworks_sdk_*.zip
#   STEAMWORKS_REDIST_URL    a URL to either of the above (a repository secret
#                            in CI)
#
# Usage:  ./tools/macos-port/fetch-runtimes.sh [destination]
# Writes: <destination>/{wine,d9mt,steam}

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
DEST="${1:-${ROOT}/macos-runtimes}"

# Wine 11.16, the vanilla build rather than staging: D9MT's winemetal is a
# third-party Wine builtin, and staging's extra patches are a variable this port
# does not need. Swap wine-devel for wine-staging in the URL to try it.
MACOS_WINE_URL="${MACOS_WINE_URL:-https://github.com/Gcenx/macOS_Wine_builds/releases/download/11.16/wine-devel-11.16-osx64.tar.xz}"
MACOS_WINE_LICENSE_URL="${MACOS_WINE_LICENSE_URL:-https://gitlab.winehq.org/wine/wine/-/raw/wine-11.16/COPYING.LIB}"
MACOS_D9MT_URL="${MACOS_D9MT_URL:-https://github.com/gru2007/d9mt-builded/releases/download/v0.1/d9mt-x64.zip}"

WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT

# A local path is accepted anywhere a URL is, so an already-downloaded archive
# can be reused instead of pulling 190 MB again.
fetch() {
	if [ -f "$1" ]; then
		printf '   %s (local)\n' "$1"
		cp "$1" "$2"
	else
		printf '   %s\n' "$1"
		curl -sSL --fail --retry 3 "$1" -o "$2"
	fi
}

extract() {
	# Dispatch on content, not on the file name: a release URL does not always
	# end in the extension it serves.
	local archive=$1 into=$2
	mkdir -p "${into}"
	case "$(/usr/bin/file -b "${archive}")" in
		*Zip*)   /usr/bin/unzip -q "${archive}" -d "${into}" ;;
		*XZ*)    tar -xJf "${archive}" -C "${into}" ;;
		*gzip*)  tar -xzf "${archive}" -C "${into}" ;;
		*bzip2*) tar -xjf "${archive}" -C "${into}" ;;
		*)       printf 'Unrecognised archive: %s\n' "${archive}" >&2; exit 1 ;;
	esac
}

rm -rf "${DEST}"
mkdir -p "${DEST}"

# -- Wine ---------------------------------------------------------------------
#
# These releases ship a Wine Devel.app / Wine Staging.app bundle with the actual
# Wine tree several levels down, so the tree is located rather than assumed.
printf '== Wine\n'
fetch "${MACOS_WINE_URL}" "${WORK}/wine.archive"
extract "${WORK}/wine.archive" "${WORK}/wine"

WINE_TREE="$(find "${WORK}/wine" -maxdepth 5 -type d -path '*/Contents/Resources/wine' -print -quit)"
if [ -z "${WINE_TREE}" ]; then
	# A plain install tree rather than an .app bundle.
	WINE_TREE="$(find "${WORK}/wine" -maxdepth 3 -type d -name bin -print -quit)"
	WINE_TREE="${WINE_TREE%/bin}"
fi
if [ -z "${WINE_TREE}" ] || [ ! -d "${WINE_TREE}/lib/wine/x86_64-windows" ]; then
	printf 'No Wine tree with lib/wine/x86_64-windows inside %s\n' "${MACOS_WINE_URL}" >&2
	exit 1
fi

/usr/bin/ditto "${WINE_TREE}" "${DEST}/wine"
fetch "${MACOS_WINE_LICENSE_URL}" "${DEST}/wine/COPYING.LIB"
test -s "${DEST}/wine/COPYING.LIB"

# -- D9MT ---------------------------------------------------------------------
printf '== D9MT\n'
fetch "${MACOS_D9MT_URL}" "${WORK}/d9mt.archive"
extract "${WORK}/d9mt.archive" "${WORK}/d9mt"

D9MT_TREE="$(find "${WORK}/d9mt" -maxdepth 3 -type f -name d3d9.dll -print -quit)"
D9MT_TREE="${D9MT_TREE%/d3d9.dll}"
if [ -z "${D9MT_TREE}" ]; then
	printf 'No d3d9.dll inside %s\n' "${MACOS_D9MT_URL}" >&2
	exit 1
fi
/usr/bin/ditto "${D9MT_TREE}" "${DEST}/d9mt"

# -- Steamworks ---------------------------------------------------------------
printf '== Steamworks redistributable\n'
mkdir -p "${DEST}/steam"
REDIST="${DEST}/steam/libsteam_api.dylib"

if [ -n "${STEAMWORKS_REDIST_DYLIB:-}" ]; then
	cp "${STEAMWORKS_REDIST_DYLIB}" "${REDIST}"
elif [ -n "${STEAMWORKS_SDK_ZIP:-}" ]; then
	cp "${STEAMWORKS_SDK_ZIP}" "${WORK}/steamworks.archive"
elif [ -n "${STEAMWORKS_REDIST_URL:-}" ]; then
	fetch "${STEAMWORKS_REDIST_URL}" "${WORK}/steamworks.archive"
else
	printf 'Set STEAMWORKS_REDIST_DYLIB, STEAMWORKS_SDK_ZIP or STEAMWORKS_REDIST_URL.\n' >&2
	printf 'The Steamworks SDK is under the Steamworks SDK Access Agreement and has no public URL.\n' >&2
	exit 1
fi

if [ ! -f "${REDIST}" ]; then
	# Either the whole SDK zip or the bare dylib; both are accepted so the
	# secret can hold whichever is easier to host.
	if /usr/bin/file -b "${WORK}/steamworks.archive" | grep -q "Mach-O"; then
		cp "${WORK}/steamworks.archive" "${REDIST}"
	else
		extract "${WORK}/steamworks.archive" "${WORK}/steamworks"
		FOUND="$(find "${WORK}/steamworks" -type f -path '*osx*' -name libsteam_api.dylib -print -quit)"
		if [ -z "${FOUND}" ]; then
			printf 'No redistributable_bin/osx/libsteam_api.dylib in the Steamworks archive\n' >&2
			exit 1
		fi
		cp "${FOUND}" "${REDIST}"
	fi
fi

if ! /usr/bin/file -b "${REDIST}" | grep -q "Mach-O"; then
	printf '%s is not a Mach-O library\n' "${REDIST}" >&2
	exit 1
fi

printf '\nruntimes ready in %s\n' "${DEST}"
printf '  wine  %s\n' "$(/usr/bin/file -b "${DEST}/wine/bin/wine" 2>/dev/null || echo missing)"
printf '  d9mt  %s\n' "$(ls "${DEST}/d9mt" | tr '\n' ' ')"
printf '  steam %s\n' "$(/usr/bin/file -b "${REDIST}")"
printf '\nBuild the depot with:\n'
printf '  TC2_WINDOWS_DIR=/path/to/game_dist \\\n'
printf '  WINE_RUNTIME_DIR=%s/wine \\\n' "${DEST}"
printf '  WINE_LICENSE_FILE=%s/wine/COPYING.LIB \\\n' "${DEST}"
printf '  D9MT_DIST_DIR=%s/d9mt \\\n' "${DEST}"
printf '  STEAMWORKS_REDIST_DYLIB=%s \\\n' "${REDIST}"
printf '    ./tools/macos-port/build-depot.sh\n'
