#!/usr/bin/env bash

script=$(readlink -f -- "$0")
pushd "$(dirname -- "$script")" > /dev/null

# Second stage of a protected launch, already inside the runtime container
# (see the Easy Anti-Cheat notes further down).
if [ "${1:-}" = "--tc2-protected" ]; then
  shift
  TF2_DIR="$1"
  shift
  if [ -n "${TF2_DIR}" ] && [ -d "${TF2_DIR}/bin/linux64" ]; then
    export LD_LIBRARY_PATH="${PWD}/bin/linux64:${TF2_DIR}/bin/linux64${LD_LIBRARY_PATH:+:${LD_LIBRARY_PATH}}"
    export TC2_LAUNCH_ENV=1
    export __GL_THREADED_OPTIMIZATIONS=1
    [ -f "${TF2_DIR}/pathmatch.inf" ] && export ENABLE_PATHMATCH=1
  fi
  ulimit -n 2048 2>/dev/null || true
  exec ./start_protected_game "$@"
fi

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
# Easy Anti-Cheat: a client starts through the anti-cheat bootstrapper, which
# loads the anti-cheat and then runs tc2_linux64 itself (the "executable" in
# EasyAntiCheat/Settings.json), passing every argument on. A dedicated server
# is not a protected client and starts the binary directly, as does -noeac
# (offline and modding; protected servers refuse such a client).
#
# Under the anti-cheat the game must stay in the process the bootstrapper
# started, so tc2_linux64 loads TF2's engine itself (as on Windows) instead of
# handing off to hl2.sh and TF2's tf_linux64 (-gathermod). That needs TF2's
# engine libraries on LD_LIBRARY_PATH from the start, inside the runtime
# container: this script runs itself again in there to set it up.

# Where Team Fortress 2 is installed: TC2_TF2_DIR, next to this game in the
# same Steam library, or any library Steam knows about. tc2_linux64 asks Steam
# itself when this finds nothing.
find_tf2_dir() {
  local dir lib vdf
  for dir in "${TC2_TF2_DIR:-}" "$(dirname -- "$PWD")/Team Fortress 2"; do
    if [ -n "${dir}" ] && [ -f "${dir}/bin/linux64/launcher.so" ]; then
      echo "${dir}"
      return
    fi
  done
  for vdf in "$HOME/.steam/steam/steamapps/libraryfolders.vdf" \
             "$HOME/.local/share/Steam/steamapps/libraryfolders.vdf"; do
    [ -f "${vdf}" ] || continue
    while IFS= read -r lib; do
      dir="${lib}/steamapps/common/Team Fortress 2"
      if [ -f "${dir}/bin/linux64/launcher.so" ]; then
        echo "${dir}"
        return
      fi
    done < <(sed -n 's/^[[:space:]]*"path"[[:space:]]*"\(.*\)"[[:space:]]*$/\1/p' "${vdf}" | sed 's/\\\\/\\/g')
  done
}

PROTECTED=0
if [ -x ./start_protected_game ]; then
  case " $* " in
    *" -dedicated "*|*" -noeac "*) ;;
    *) PROTECTED=1 ;;
  esac
fi

if [ "${PROTECTED}" -eq 1 ]; then
  ${SLR_SNIPER_PATH} --devel -- "${script}" --tc2-protected "$(find_tf2_dir)" -steam -particles 1 "$@" "${IP_DEFAULT[@]}"
else
  ${SLR_SNIPER_PATH} --devel -- ./tc2_linux64 -steam -gathermod -particles 1 "$@" "${IP_DEFAULT[@]}"
fi

popd > /dev/null
