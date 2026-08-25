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

# Bind the Source networking sockets on all interfaces by default.
# Keep an explicit caller-supplied +ip untouched.
IP_DEFAULT=(+ip 0.0.0.0)
case " $* " in
  *" +ip "*) IP_DEFAULT=() ;;
esac

# FakeIP is not passed here.
#
# It is a client-side convenience, and this script is also how every dedicated
# server starts (start_dedicated_tc2.sh calls it). A server launched with
# -enablefakeip asks Steam for a FakeIP and, once that allocation lands, starts
# advertising and answering on it instead of on its real address -- so the
# address players were handed goes dead the moment Steam is reachable, and no
# convar undoes a launch parameter.
#
# Clients that want it add -enablefakeip to their Steam launch options; "$@"
# carries it through to the binary.
${SLR_SNIPER_PATH} --devel -- ./tc2_linux64 -steam -gathermod -particles 1 "$@" "${IP_DEFAULT[@]}"

popd > /dev/null
