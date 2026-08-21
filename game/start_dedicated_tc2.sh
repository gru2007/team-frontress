#!/usr/bin/env bash

script=$(readlink -f -- "$0")
pushd "$(dirname -- "$script")" > /dev/null

if [ ! -f tc2/gameinfo_client.txt ]; then
    cp tc2/gameinfo.txt tc2/gameinfo_client.txt
    cp tc2/gameinfo_server.txt tc2/gameinfo.txt
fi

if [ ! -f bin/linux64/libtinfo.so.5 ]; then
    ln -s /lib/x86_64-linux-gnu/libtinfo.so.6 bin/linux64/libtinfo.so.5
fi

# A Game Server Login Token identifies this machine to Steam as a server of our
# dedicated-server app (5150320, see tc2/steam.inf). Without one the server logs
# on anonymously, and an anonymous server cannot be a secure server of our app —
# which is what client auth tickets and Steam game bans are checked against.
#
# It has to be on the command line: Steam logs the server on during startup,
# long before RCON exists to set anything. Issue tokens for 5150320 at
# https://steamcommunity.com/dev/managegameservers — see docs/STEAM-SETUP.md.
STEAM_ACCOUNT=()
if [ -n "${GSLT:-}" ]; then
    STEAM_ACCOUNT=(+sv_setsteamaccount "${GSLT}")
else
    echo "[greyline] no \$GSLT set: this server will log into Steam anonymously," >&2
    echo "[greyline] so client auth tickets will not validate against it." >&2
fi

./tc2.sh -console -dedicated -gatherdedi "${STEAM_ACCOUNT[@]}" "$@" +sv_pure 1

mv tc2/gameinfo_client.txt tc2/gameinfo.txt

stty sane

popd > /dev/null
