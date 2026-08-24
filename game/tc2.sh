#!/usr/bin/env bash

script=$(readlink -f -- "$0")
pushd "$(dirname -- "$script")" > /dev/null

#ulimit -c unlimited
#sudo bash -c 'echo "core.%p" > /proc/sys/kernel/core_pattern 2>/dev/null || true'

# Launch the game under the steam runtime.
#
# The runtime is looked for next to the game first: that is where
# steamcmd_update.sh puts it, which is what a dedicated server (and the docker
# image built from this payload) has -- neither of them has a Steam install to
# borrow one from.
if [ -z "${SLR_SNIPER_PATH:-}" ]; then
  for candidate in \
    "./SteamLinuxRuntime_sniper/run" \
    "$HOME/.steam/steam/steamapps/common/SteamLinuxRuntime_sniper/run" \
    "$HOME/.local/share/Steam/steamcmd/SteamLinuxRuntime_sniper/run"
  do
    if [ -f "${candidate}" ]; then
      SLR_SNIPER_PATH="${candidate}"
      break
    fi
  done
fi

if [ ! -f "${SLR_SNIPER_PATH:-}" ]; then
  echo "Steam Linux Runtime 3.0 (sniper) not found."
  echo "Run ./steamcmd_update.sh next to this script, install it through Steam"
  echo "(steam://install/1628350), or set \$SLR_SNIPER_PATH to its run file."
  exit 1
fi

#trap 'echo "Received SIGTERM, shutting down gracefully..." && kill -TERM $!' SIGTERM
#trap 'echo "Received SIGPIPE, shutting down gracefully..." && continue' SIGPIPE

# The client binds loopback; a dedicated server must not, and a server told to
# bind 0.0.0.0 used to have that overridden by the default appended after it.
# So the default is only applied when the caller did not pass one.
IP_DEFAULT=(+ip 127.0.0.1)
case " $* " in
  *" +ip "*) IP_DEFAULT=() ;;
esac

${SLR_SNIPER_PATH} --devel -- ./tc2_linux64 -steam -gathermod -particles 1 -enablefakeip "$@" "${IP_DEFAULT[@]}"

popd > /dev/null
