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

# Headless dedicated servers must not initialise the desktop Steam client.
# launcher_main already has a dedicated -nosteamclient path for this, but its
# legacy POSIX fallback expects the Valve dedicated runtime in ../tf2ds. Our
# supported layout installs AppID 232250 as ./tf2 (steamcmd_update.sh and the
# Frontress Docker image both do this), so provide the legacy path as a symlink.
# This keeps older launcher binaries working while avoiding SteamAPI_Init's
# dependency on a running Steam desktop client.
if [ -d "$PWD/tf2" ]; then
    if [ -L ../tf2ds ]; then
        ln -sfn "$PWD/tf2" ../tf2ds
    elif [ ! -e ../tf2ds ]; then
        ln -s "$PWD/tf2" ../tf2ds
    fi
fi

# sv_pure follows the same rule as +ip: a default, not an override. The match
# config the coordinator execs can still set it either way.
PURE_DEFAULT=(+sv_pure 1)
case " $* " in
  *" +sv_pure "*) PURE_DEFAULT=() ;;
esac

# Submit the Steam account token before the first map, not from the map config.
STEAM_ACCOUNT=()
if [ -n "${GSLT:-}" ]; then
    case " $* " in
        *" +sv_setsteamaccount "*) ;;
        *) STEAM_ACCOUNT=(+sv_setsteamaccount "$GSLT") ;;
    esac
fi

# Easy Anti-Cheat replaces VAC. -insecure keeps Steam from advertising the
# server as VAC-secured; Steam still authenticates every player. The anti-cheat
# itself runs inside server.so and needs this server's EOS credentials:
#   FRONTRESS_EOS_SERVER_CLIENT_ID / FRONTRESS_EOS_SERVER_CLIENT_SECRET
# Without them the server runs unprotected and the GC sends it no matches
# that require anti-cheat. Pass -secure to keep VAC instead.
VAC_DEFAULT=(-insecure)
case " $* " in
  *" -secure "*|*" -insecure "*) VAC_DEFAULT=() ;;
esac
if [ -z "${FRONTRESS_EOS_SERVER_CLIENT_ID:-}" ] || [ -z "${FRONTRESS_EOS_SERVER_CLIENT_SECRET:-}" ]; then
    echo "WARNING: FRONTRESS_EOS_SERVER_CLIENT_ID/SECRET are not set; Easy Anti-Cheat is off on this server." >&2
fi

./tc2.sh -console -dedicated -nosteamclient -gatherdedi "${VAC_DEFAULT[@]}" "${STEAM_ACCOUNT[@]}" "$@" "${PURE_DEFAULT[@]}"

mv tc2/gameinfo_client.txt tc2/gameinfo.txt

# Desktop launches have a terminal to restore; Docker/SSH supervisors usually
# do not. Calling stty on a non-TTY only adds a misleading error after the real
# server exit reason.
if [ -t 0 ]; then
    stty sane
fi

popd > /dev/null
