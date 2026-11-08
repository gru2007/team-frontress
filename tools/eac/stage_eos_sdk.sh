#!/usr/bin/env bash
#
# Stage the EOS SDK for an Easy Anti-Cheat build.
#
# The EOS SDK is Epic's licensed software and this repository is public, so the
# SDK is never committed. This script fetches it (CI) or takes a local copy
# (developers) and lays out what the build and the packaging need:
#
#   src/thirdparty/eos_sdk/include/      SDK headers
#   src/thirdparty/eos_sdk/lib/          link libraries (Windows .lib, Linux .so, macOS .dylib)
#   src/thirdparty/eos_sdk/bin/          runtime libraries shipped with the game
#   src/thirdparty/eos_sdk/tools/<os>/   EOS_AntiCheatTools: bootstrapper, EasyAntiCheat/, integrity tool
#   src/thirdparty/eos_sdk/config/frontress_eos_config.h   product identity, compiled into the game
#
# Then build with VPC's /define:FRONTRESS_EAC (VPC_EXTRA_ARGS) and package with
# tools/eac/package_eac.py.
#
# Where the SDK comes from, one of:
#   EOS_SDK_DIR      an extracted SDK (the folder that contains SDK/)
#   EOS_SDK_URL      a zip of that folder; EOS_SDK_TOKEN is sent as a bearer
#                    token if set (a private GitHub release asset works:
#                    https://api.github.com/repos/<owner>/<repo>/releases/assets/<id>)
#   EOS_SDK_SHA256   optional, checked against the downloaded zip
#
# Product identity (EOS Developer Portal -> Product Settings / Clients). The
# client credentials are the GameClient policy ones that every game client
# carries anyway; a dedicated server's TrustedServer credentials never pass
# through here (see frontress_eos.h).
#   FRONTRESS_EOS_PRODUCT_ID
#   FRONTRESS_EOS_SANDBOX_ID
#   FRONTRESS_EOS_DEPLOYMENT_ID
#   FRONTRESS_EOS_CLIENT_ID
#   FRONTRESS_EOS_CLIENT_SECRET

set -euo pipefail

ROOT="$(cd -- "$(dirname -- "$0")/../.." && pwd)"
OUT="${ROOT}/src/thirdparty/eos_sdk"
WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT

PY="$(command -v python3 || command -v python || true)"
if [ -z "${PY}" ]; then
  echo "python3 is required (zip extraction, config generation)." >&2
  exit 1
fi

for v in FRONTRESS_EOS_PRODUCT_ID FRONTRESS_EOS_SANDBOX_ID FRONTRESS_EOS_DEPLOYMENT_ID FRONTRESS_EOS_CLIENT_ID FRONTRESS_EOS_CLIENT_SECRET; do
  if [ -z "${!v:-}" ]; then
    echo "${v} is not set. See tools/eac/README.md." >&2
    exit 1
  fi
done

# --- the SDK ------------------------------------------------------------------

SDK_ROOT="${EOS_SDK_DIR:-}"
if [ -z "${SDK_ROOT}" ]; then
  if [ -z "${EOS_SDK_URL:-}" ]; then
    echo "Set EOS_SDK_DIR (a local SDK) or EOS_SDK_URL (a zip of it)." >&2
    exit 1
  fi
  echo "Downloading the EOS SDK"
  CURL_ARGS=(-sSL --fail --retry 3 -o "${WORK}/eos-sdk.zip")
  if [ -n "${EOS_SDK_TOKEN:-}" ]; then
    CURL_ARGS+=(-H "Authorization: Bearer ${EOS_SDK_TOKEN}" -H "Accept: application/octet-stream")
  fi
  curl "${CURL_ARGS[@]}" "${EOS_SDK_URL}"
  if [ -n "${EOS_SDK_SHA256:-}" ]; then
    echo "${EOS_SDK_SHA256}  ${WORK}/eos-sdk.zip" | sha256sum -c - >/dev/null 2>&1 \
      || echo "${EOS_SDK_SHA256}  ${WORK}/eos-sdk.zip" | shasum -a 256 -c - >/dev/null \
      || { echo "EOS SDK checksum mismatch." >&2; exit 1; }
  fi
  "${PY}" -I - "${WORK}/eos-sdk.zip" "${WORK}/sdk" <<'EOF'
import sys, zipfile, os
src, dst = sys.argv[1], sys.argv[2]
with zipfile.ZipFile(src) as z:
    for info in z.infolist():
        name = info.filename.replace("\\", "/")
        if name.endswith("/"):
            continue
        parts = [p for p in name.split("/") if p not in ("", ".", "..")]
        path = os.path.join(dst, *parts)
        os.makedirs(os.path.dirname(path), exist_ok=True)
        with z.open(info) as fin, open(path, "wb") as fout:
            fout.write(fin.read())
EOF
  # The zip may hold SDK/ at its root or one folder down.
  SDK_ROOT="$(dirname "$(find "${WORK}/sdk" -type d -name Include -path '*/SDK/Include' | head -n 1)")/.."
fi

SDK="$(cd "${SDK_ROOT}" && pwd)/SDK"
if [ ! -f "${SDK}/Include/eos_sdk.h" ]; then
  echo "No SDK/Include/eos_sdk.h under ${SDK_ROOT}." >&2
  exit 1
fi
echo "EOS SDK: $(grep -E '^#define EOS_(MAJOR|MINOR|PATCH)_VERSION' "${SDK}/Include/eos_version.h" | awk '{print $3}' | paste -sd. -)"

rm -rf "${OUT}"
mkdir -p "${OUT}/include" "${OUT}/lib" "${OUT}/bin" "${OUT}/tools" "${OUT}/config"

cp -R "${SDK}/Include/." "${OUT}/include/"

# Link libraries. Windows links an import library; Linux and macOS link the
# shared library itself (its SONAME / install name is what gets recorded).
cp "${SDK}/Lib/EOSSDK-Win64-Shipping.lib" "${OUT}/lib/" 2>/dev/null || true
cp "${SDK}/Bin/libEOSSDK-Linux-Shipping.so" "${OUT}/lib/" 2>/dev/null || true
cp "${SDK}/Bin/libEOSSDK-Mac-Shipping.dylib" "${OUT}/lib/" 2>/dev/null || true

# Runtime libraries, x64 only: the game has no 32-bit or ARM build.
for f in EOSSDK-Win64-Shipping.dll libEOSSDK-Linux-Shipping.so libEOSSDK-Mac-Shipping.dylib; do
  cp "${SDK}/Bin/${f}" "${OUT}/bin/" 2>/dev/null || true
done

# The anti-cheat tools, one folder per OS. The Windows zip stores paths with
# backslashes, which only a normalising extractor gets right.
"${PY}" -I - "${SDK}/Tools" "${OUT}/tools" <<'EOF'
import sys, zipfile, os, glob, stat
tools, out = sys.argv[1], sys.argv[2]
names = {"win32-x64": "win", "linux-x64": "linux", "mac": "mac"}
found = []
for zpath in glob.glob(os.path.join(tools, "EOS_AntiCheatTools-*.zip")):
    base = os.path.basename(zpath)
    key = next((v for k, v in names.items() if "-" + k + "-" in base), None)
    if not key:
        continue
    dst = os.path.join(out, key)
    with zipfile.ZipFile(zpath) as z:
        for info in z.infolist():
            name = info.filename.replace("\\", "/")
            if name.endswith("/"):
                continue
            parts = [p for p in name.split("/") if p not in ("", ".", "..")]
            path = os.path.join(dst, *parts)
            os.makedirs(os.path.dirname(path), exist_ok=True)
            with z.open(info) as fin, open(path, "wb") as fout:
                fout.write(fin.read())
            mode = (info.external_attr >> 16) & 0o777
            if mode & 0o111 or os.path.basename(path) in ("start_protected_game", "anticheat_integritytool"):
                os.chmod(path, os.stat(path).st_mode | stat.S_IXUSR | stat.S_IXGRP | stat.S_IXOTH)
    found.append("%s (%s)" % (key, base))
if not found:
    sys.exit("No EOS_AntiCheatTools-*.zip in " + tools)
print("Anti-cheat tools: " + ", ".join(sorted(found)))
EOF

# --- product identity -----------------------------------------------------------

"${PY}" -I - "${OUT}/config/frontress_eos_config.h" <<'EOF'
import os, sys, json
def lit(name):
    v = os.environ[name]
    if any(c in v for c in '\r\n'):
        sys.exit(name + " contains a line break")
    return json.dumps(v)  # a valid C string literal for these values
with open(sys.argv[1], "w") as f:
    f.write("// Generated by tools/eac/stage_eos_sdk.sh. Do not edit, do not commit.\n")
    f.write("#pragma once\n")
    for key in ("PRODUCT_ID", "SANDBOX_ID", "DEPLOYMENT_ID", "CLIENT_ID", "CLIENT_SECRET"):
        f.write("#define FRONTRESS_EOS_%s %s\n" % (key, lit("FRONTRESS_EOS_" + key)))
EOF

echo "Staged ${OUT}"
echo "  build:   VPC_EXTRA_ARGS=/define:FRONTRESS_EAC"
echo "  package: tools/eac/package_eac.py --dist game_dist --platform <win|linux>"
