#!/bin/bash
#
# Prove that mingw-w64 and clang agree on every params struct.
#
# The bridge writes each struct with one compiler and reads it with the other.
# That is only safe while both lay the struct out the same way, so the sizes and
# alignments the Unix build reports are turned into static_asserts and fed back
# through the PE compiler.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
STEAM_SDK_DIR="${STEAM_SDK_DIR:-${ROOT}/src/public}"
MINGW_PREFIX="${MINGW_PREFIX:-x86_64-w64-mingw32}"
WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT

INCLUDES=(
	"-I${SCRIPT_DIR}/src"
	"-I${SCRIPT_DIR}/generated"
	"-I${STEAM_SDK_DIR}"
	"-I${STEAM_SDK_DIR}/steam"
)

# Built for the host, not for x86_64. macOS lays these structs out the same way
# on both architectures -- everything in them is a fixed-width integer, a float
# or a pointer, and #pragma pack(4) applies identically -- so probing natively
# measures the same thing and does not need Rosetta on an Apple silicon runner.
PROBE_ARCH="${PROBE_ARCH:-}"
printf '== probing layouts with clang%s\n' "${PROBE_ARCH:+ (${PROBE_ARCH})}"
# shellcheck disable=SC2086
/usr/bin/xcrun clang++ ${PROBE_ARCH:+-arch ${PROBE_ARCH}} -std=c++17 -w "${INCLUDES[@]}" \
	-o "${WORK}/probe" "${SCRIPT_DIR}/src/layout_probe.cpp"

if ! "${WORK}/probe" > "${WORK}/sizes.txt"; then
	printf 'the layout probe did not run on this host\n' >&2
	exit 2
fi

printf '== turning %d measurements into assertions\n' "$(wc -l < "${WORK}/sizes.txt" | tr -d ' ')"
{
	printf '#include "bridge_types.h"\n#include "bridge_calls.h"\n'
	while read -r name size align; do
		if [ "${name}" = "steam_bridge_call_count" ]; then
			printf 'static_assert( steam_bridge_call_count == %s, "call count differs" );\n' "${size}"
			continue
		fi
		printf 'static_assert( sizeof(struct %s) == %s, "%s size differs" );\n' "${name}" "${size}" "${name}"
		printf 'static_assert( alignof(struct %s) == %s, "%s alignment differs" );\n' "${name}" "${align}" "${name}"
	done < "${WORK}/sizes.txt"
} > "${WORK}/assertions.cpp"

printf '== checking them with %s\n' "${MINGW_PREFIX}"
"${MINGW_PREFIX}-g++" -fsyntax-only -m64 -std=c++17 -w \
	-DSTEAM_BRIDGE_PE=1 -D_WIN32_WINNT=0x0601 \
	"${INCLUDES[@]}" "${WORK}/assertions.cpp"

printf 'layouts agree across both toolchains\n'
