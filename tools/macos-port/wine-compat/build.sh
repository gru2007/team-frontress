#!/bin/bash
#
# Build the two pieces that make D9MT run on the LGPL Wine runtime this port
# ships rather than on CrossOver's. See README.md for what each one is for.
#
# Outputs:
#   <dist>/x86_64-windows/d9mtmetal.dll   replaces d9mt-x64's copy
#   <dist>/lib/libmacdrvshim.dylib        inserted into the Wine process
#
# Usage:  ./tools/macos-port/wine-compat/build.sh [destination]

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
BRIDGE_DIR="$(cd "${SCRIPT_DIR}/../steam-bridge" && pwd)"
DIST="${1:-${SCRIPT_DIR}/dist}"

MINGW="${MINGW_PREFIX_X64:-x86_64-w64-mingw32}"
# /usr/bin/clang, not whatever clang is first on PATH: a Swift toolchain
# installed for something else shadows it and cannot build for macOS.
CLANG="${CLANG:-/usr/bin/clang}"

for tool in "${MINGW}-gcc" "${MINGW}-objdump" python3; do
	command -v "${tool}" >/dev/null 2>&1 || {
		printf 'missing required tool: %s\n' "${tool}" >&2
		printf '(brew install mingw-w64)\n' >&2
		exit 1
	}
done
[ -x "${CLANG}" ] || { printf 'no clang at %s\n' "${CLANG}" >&2; exit 1; }

rm -rf "${DIST}"
mkdir -p "${DIST}/x86_64-windows" "${DIST}/lib"

# -- d9mtmetal.dll ------------------------------------------------------------
#
# --file-alignment matching the section alignment is what winebuild --builtin
# produces, and Wine maps builtin PEs as flat files; -s keeps the file from
# growing past SizeOfImage, which Wine's mapping refuses outright.
printf '== d9mtmetal.dll\n'
"${MINGW}-gcc" -shared -O2 -s \
	-o "${DIST}/x86_64-windows/d9mtmetal.dll" \
	"${SCRIPT_DIR}/d9mtmetal_dll.c" \
	-Wl,--file-alignment,0x1000
python3 "${BRIDGE_DIR}/mark-builtin.py" "${DIST}/x86_64-windows/d9mtmetal.dll"

# The whole point of the rebuild, so it is checked rather than assumed: this
# DLL reaches ntdll through GetProcAddress and must import nothing from it.
if "${MINGW}-objdump" -p "${DIST}/x86_64-windows/d9mtmetal.dll" \
	| grep -i 'DLL Name:' | grep -qi 'ntdll'; then
	printf 'd9mtmetal.dll imports ntdll directly; on upstream Wine that is a stub\n' >&2
	exit 1
fi

# -- libmacdrvshim.dylib ------------------------------------------------------
#
# x86_64 because it is loaded into the Wine process, which is x86_64 and runs
# under Rosetta on Apple silicon.
printf '== libmacdrvshim.dylib\n'
"${CLANG}" -arch x86_64 -ObjC -dynamiclib -O2 -fno-objc-arc \
	-mmacosx-version-min=11.0 \
	-install_name @rpath/libmacdrvshim.dylib \
	-o "${DIST}/lib/libmacdrvshim.dylib" \
	"${SCRIPT_DIR}/macdrv_shim.m" \
	-framework AppKit -framework Metal -framework QuartzCore

for symbol in _macdrv_functions _get_win_data _macdrv_view_create_metal_view; do
	nm -gU "${DIST}/lib/libmacdrvshim.dylib" | grep -q " ${symbol}\$" || {
		printf 'libmacdrvshim.dylib does not export %s\n' "${symbol}" >&2
		exit 1
	}
done

printf 'wine-compat ready: %s\n' "${DIST}"
