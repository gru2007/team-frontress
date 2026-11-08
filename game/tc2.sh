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
# Steam starts the game inside the runtime already when the app is set to use
# it; a second container inside the first does not start, so the game runs
# as it is then.
in_runtime_container() {
  [ -d /run/pressure-vessel ] || [ -e /run/host/container-manager ] || [ -n "${PRESSURE_VESSEL_RUNTIME:-}" ]
}

SLR=()
if in_runtime_container; then
  :
elif [ -z "${SLR_SNIPER_PATH:-}" ]; then
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

if ! in_runtime_container; then
  if [ ! -f "${SLR_SNIPER_PATH:-}" ]; then
    echo "Steam Linux Runtime 3.0 (sniper) not found."
    echo "Run ./steamcmd_update.sh next to this script, install it through Steam"
    echo "(steam://install/1628350), or set \$SLR_SNIPER_PATH to its run file."
    exit 1
  fi
  SLR=("${SLR_SNIPER_PATH}" --devel --)
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
# Easy Anti-Cheat: a client starts through the anti-cheat bootstrapper, which
# loads the anti-cheat and then runs tc2_linux64 itself (the "executable" in
# EasyAntiCheat/Settings.json), passing every argument on. tc2_linux64 then
# loads TF2's engine into that same process and finds TF2 through Steam --
# nothing to set up here. Steam can just as well start start_protected_game
# directly.
#
# A dedicated server is not a protected client: it hands off to srcds_run_64
# (-gatherdedi). -noeac (offline and modding; protected servers refuse such a
# client) runs the game the way it ran before the anti-cheat, through hl2.sh.
PROTECTED=0
if [ -x ./start_protected_game ]; then
  case " $* " in
    *" -dedicated "*|*" -noeac "*) ;;
    *) PROTECTED=1 ;;
  esac
fi

if [ "${PROTECTED}" -eq 1 ]; then
  "${SLR[@]}" ./start_protected_game -steam -particles 1 "$@" "${IP_DEFAULT[@]}"
else
  "${SLR[@]}" ./tc2_linux64 -steam -gathermod -particles 1 "$@" "${IP_DEFAULT[@]}"
fi

popd > /dev/null
