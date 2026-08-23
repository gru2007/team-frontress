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

# sv_pure follows the same rule as +ip: a default, not an override. The match
# config the coordinator execs can still set it either way.
PURE_DEFAULT=(+sv_pure 1)
case " $* " in
  *" +sv_pure "*) PURE_DEFAULT=() ;;
esac

./tc2.sh -console -dedicated -gatherdedi "$@" "${PURE_DEFAULT[@]}"

mv tc2/gameinfo_client.txt tc2/gameinfo.txt

# Desktop launches have a terminal to restore; Docker/SSH supervisors usually
# do not. Calling stty on a non-TTY only adds a misleading error after the real
# server exit reason.
if [ -t 0 ]; then
    stty sane
fi

popd > /dev/null
