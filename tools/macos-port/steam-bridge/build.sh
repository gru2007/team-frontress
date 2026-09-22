#!/bin/bash
#
# Build the macOS Steamworks bridge.
#
# Two objects come out of this: a PE steam_api64.dll the Windows game loads,
# and a Mach-O steam_api64.so Wine loads next to it.  Neither needs a Wine
# source tree -- the PE half is mingw-w64 and the Unix half is plain clang.
#
# Environment:
#   STEAM_SDK_DIR   Steamworks headers   (default src/public/steam)
#   MINGW_PREFIX    mingw-w64 triple     (default x86_64-w64-mingw32)
#   OUTPUT_DIR      where to write       (default ./dist)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"

STEAM_SDK_DIR="${STEAM_SDK_DIR:-${ROOT}/src/public}"
MINGW_PREFIX="${MINGW_PREFIX:-x86_64-w64-mingw32}"
OUTPUT_DIR="${OUTPUT_DIR:-${SCRIPT_DIR}/dist}"

MINGW_CXX="${MINGW_CXX:-${MINGW_PREFIX}-g++}"
CLANG="${CLANG:-/usr/bin/xcrun clang++}"

if ! command -v "${MINGW_CXX}" >/dev/null 2>&1; then
	printf 'mingw-w64 C++ compiler not found: %s\n' "${MINGW_CXX}" >&2
	printf 'Install it with: brew install mingw-w64\n' >&2
	exit 1
fi

if [ ! -f "${STEAM_SDK_DIR}/steam/steam_api.json" ]; then
	printf 'Steamworks SDK not found under %s\n' "${STEAM_SDK_DIR}" >&2
	exit 1
fi

printf '== generating the bridge from steam_api.json\n'
python3 "${SCRIPT_DIR}/generate.py" "${STEAM_SDK_DIR}/steam/steam_api.json"

mkdir -p "${OUTPUT_DIR}/x86_64-windows" "${OUTPUT_DIR}/x86_64-unix"

INCLUDES=(
	"-I${SCRIPT_DIR}/src"
	"-I${SCRIPT_DIR}/generated"
	"-I${STEAM_SDK_DIR}"
	"-I${STEAM_SDK_DIR}/steam"
)

# -Wno-invalid-offsetof and friends: the SDK headers are not warning-clean and
# they are not ours to fix.  The bridge's own code is built with warnings on.
COMMON_WARNINGS=(
	-Wall
	-Wno-unused-parameter
	-Wno-unknown-pragmas
	-Wno-invalid-offsetof
)

# -static, not merely -static-libgcc/-static-libstdc++: those still leave a
# dynamic import of libwinpthread-1.dll, which Wine does not ship. A builtin
# with an import Wine cannot resolve fails to load at all, and the game sees
# nothing but ERROR_MOD_NOT_FOUND.
printf '== building the PE half (%s)\n' "${MINGW_PREFIX}"
"${MINGW_CXX}" \
	-shared -m64 -O2 -fno-strict-aliasing \
	-std=c++17 -static \
	-DSTEAM_BRIDGE_PE=1 -D_WIN32_WINNT=0x0601 \
	"${COMMON_WARNINGS[@]}" \
	"${INCLUDES[@]}" \
	-o "${OUTPUT_DIR}/x86_64-windows/steam_api64.dll" \
	"${SCRIPT_DIR}/src/pe_main.cpp" \
	"${SCRIPT_DIR}/src/pack_manual.cpp" \
	"${SCRIPT_DIR}/src/path_convert.cpp" \
	"${SCRIPT_DIR}/generated/pack_convert.cpp" \
	"${SCRIPT_DIR}/generated/pe_flat.cpp" \
	"${SCRIPT_DIR}/generated/pe_local.cpp" \
	"${SCRIPT_DIR}/generated/exports.def" \
	-Wl,--enable-stdcall-fixup \
	-lntdll

python3 "${SCRIPT_DIR}/mark-builtin.py" "${OUTPUT_DIR}/x86_64-windows/steam_api64.dll"

printf '== building the Unix half (x86_64 Mach-O)\n'
${CLANG} \
	-shared -arch x86_64 -O2 -fno-strict-aliasing \
	-std=c++17 -fPIC -fvisibility=hidden \
	-mmacosx-version-min=11.0 \
	"${COMMON_WARNINGS[@]}" \
	"${INCLUDES[@]}" \
	-o "${OUTPUT_DIR}/x86_64-unix/steam_api64.so" \
	"${SCRIPT_DIR}/src/unix_main.cpp"

printf '== verifying\n'
if ! nm -gU "${OUTPUT_DIR}/x86_64-unix/steam_api64.so" | grep -q "___wine_unix_call_funcs"; then
	printf 'the Unix half does not export __wine_unix_call_funcs\n' >&2
	exit 1
fi

# Anything Wine does not provide here is a load failure at runtime, not a link
# error now, so the import list is checked rather than trusted.
UNEXPECTED="$("${MINGW_PREFIX}-objdump" -p "${OUTPUT_DIR}/x86_64-windows/steam_api64.dll" \
	| awk '/DLL Name:/ {print tolower($3)}' \
	| grep -vE '^(kernel32\.dll|ntdll\.dll|user32\.dll|advapi32\.dll|msvcrt\.dll|api-ms-win-crt-)' || true)"
if [ -n "${UNEXPECTED}" ]; then
	printf 'the PE half imports libraries Wine does not ship:\n%s\n' "${UNEXPECTED}" >&2
	exit 1
fi

# Every name the game's import library references has to exist, or the game
# fails to load with a bare "procedure entry point not found".
IMPORT_LIB="${IMPORT_LIB:-${ROOT}/src/lib/public/x64/steam_api64.lib}"
if [ -f "${IMPORT_LIB}" ] && command -v "${MINGW_PREFIX}-nm" >/dev/null 2>&1; then
	"${MINGW_PREFIX}-nm" --defined-only "${IMPORT_LIB}" 2>/dev/null \
		| grep -oE "__imp_[A-Za-z_][A-Za-z0-9_]*" | sed 's/^__imp_//' | sort -u \
		> "${OUTPUT_DIR}/.wanted-exports"
	"${MINGW_PREFIX}-objdump" -p "${OUTPUT_DIR}/x86_64-windows/steam_api64.dll" \
		| awk '/Ordinal\/Name Pointer/,0' | awk '{print $NF}' \
		| grep -E "^[A-Za-z_][A-Za-z0-9_]*$" | sort -u \
		> "${OUTPUT_DIR}/.have-exports"
	MISSING="$(comm -23 "${OUTPUT_DIR}/.wanted-exports" "${OUTPUT_DIR}/.have-exports")"
	rm -f "${OUTPUT_DIR}/.wanted-exports" "${OUTPUT_DIR}/.have-exports"
	if [ -n "${MISSING}" ]; then
		printf 'the bridge does not export everything the game imports:\n%s\n' "${MISSING}" >&2
		exit 1
	fi
	printf 'every import the game needs is exported\n'
fi

cp "${SCRIPT_DIR}/LICENSE" "${OUTPUT_DIR}/LICENSE"

printf 'bridge built:\n  %s\n  %s\n' \
	"${OUTPUT_DIR}/x86_64-windows/steam_api64.dll" \
	"${OUTPUT_DIR}/x86_64-unix/steam_api64.so"
