#!/usr/bin/env bash

set -u

ROOT="$(cd -- "$(dirname -- "$0")" && pwd)"
cd "${ROOT}"

# In a developer/client tree gameinfo.txt is the client variant. In the
# dedicated artifact copy_server.sh already installs the server variant.
RESTORE_GAMEINFO=0
if [ -f tc2/gameinfo_server.txt ] && ! cmp -s tc2/gameinfo.txt tc2/gameinfo_server.txt; then
    cp tc2/gameinfo.txt tc2/gameinfo_client.txt
    cp tc2/gameinfo_server.txt tc2/gameinfo.txt
    RESTORE_GAMEINFO=1
fi

cleanup() {
    if [ "${RESTORE_GAMEINFO}" -eq 1 ] && [ -f tc2/gameinfo_client.txt ]; then
        mv tc2/gameinfo_client.txt tc2/gameinfo.txt
    fi
    stty sane 2>/dev/null || true
}
trap cleanup EXIT

if [ ! -f bin/linux64/libtinfo.so.5 ]; then
    ln -s /lib/x86_64-linux-gnu/libtinfo.so.6 bin/linux64/libtinfo.so.5
fi

# Prefer the runtime installed by steamcmd_update.sh beside this package.
SLR="${SLR_SNIPER_PATH:-${ROOT}/SteamLinuxRuntime_sniper/run}"
if [ ! -x "${SLR}" ]; then
    SLR="${HOME}/.steam/steam/steamapps/common/SteamLinuxRuntime_sniper/run"
fi
if [ ! -x "${SLR}" ]; then
    echo "[greyline] Steam Linux Runtime sniper not found; run ./steamcmd_update.sh" >&2
    exit 1
fi

# Runtime game identity is 5147520. Tool 5150320 only distributes this package.
# Create the GSLT for 5147520 and pass it through the GSLT environment variable.
STEAM_ACCOUNT=()
if [ -n "${GSLT:-}" ]; then
    STEAM_ACCOUNT=(+sv_setsteamaccount "${GSLT}")
else
    echo "[greyline] no \$GSLT set: server will use anonymous Steam logon." >&2
fi

"${SLR}" --devel -- ./tc2_linux64 -steam -gathermod -particles 1 -nobreakpad -nominidump -console -dedicated -gatherdedi +sv_lan 0 "${STEAM_ACCOUNT[@]}" "$@" +sv_pure 1
