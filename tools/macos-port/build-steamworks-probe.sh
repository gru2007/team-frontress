#!/bin/bash

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
OUTPUT="${1:-${SCRIPT_DIR}/steamworks-probe}"

/usr/bin/xcrun clang -std=c11 -Wall -Wextra -Werror -O2 \
	"${SCRIPT_DIR}/steamworks-probe.c" -o "${OUTPUT}"
printf 'Steamworks probe built: %s\n' "${OUTPUT}"
