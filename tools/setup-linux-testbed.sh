#!/usr/bin/env bash
#
# Provision a Linux machine as a Greyline test box: build the game, build the
# coordinator, and make the mod launchable from the Steam library.
#
# Idempotent. Safe to re-run. Every step reports what it did or why it skipped.
#
#   ./tools/setup-linux-testbed.sh              # everything
#   ./tools/setup-linux-testbed.sh deps         # just check prerequisites
#   ./tools/setup-linux-testbed.sh build        # just build game + coordinator
#   ./tools/setup-linux-testbed.sh coordinator  # just the coordinator + service
#   ./tools/setup-linux-testbed.sh shortcut     # just the Steam library entry
#
set -euo pipefail

REPO_ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

say()  { printf '\033[1;36m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m!!!\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31mXXX\033[0m %s\n' "$*" >&2; exit 1; }

STEAM_ROOT="${STEAM_ROOT:-$HOME/.steam/steam}"
if [ ! -d "$STEAM_ROOT" ]; then
  STEAM_ROOT="$HOME/.local/share/Steam"
fi

# ---------------------------------------------------------------------------
# deps
# ---------------------------------------------------------------------------
step_deps() {
  say "checking prerequisites"

  local missing=()
  # The game build runs inside the SteamRT sniper SDK container; see
  # src/sdk_container. Without podman there is no Linux game build at all.
  command -v podman  >/dev/null || missing+=(podman)
  command -v git     >/dev/null || missing+=(git)
  command -v ccache  >/dev/null || missing+=(ccache)
  command -v python3 >/dev/null || missing+=(python3)

  if [ ${#missing[@]} -gt 0 ]; then
    warn "missing: ${missing[*]}"
    if command -v apt-get >/dev/null; then
      say "installing with apt-get (needs sudo)"
      sudo apt-get update
      sudo apt-get install -y "${missing[@]}"
    elif command -v dnf >/dev/null; then
      sudo dnf install -y "${missing[@]}"
    elif command -v pacman >/dev/null; then
      sudo pacman -S --needed --noconfirm "${missing[@]}"
    else
      die "install these yourself, then re-run: ${missing[*]}"
    fi
  else
    say "podman, git, ccache, python3 all present"
  fi

  if ! command -v go >/dev/null; then
    warn "Go is not installed; the coordinator will not build here."
    warn "Either install Go 1.24+ or run the coordinator on another machine and"
    warn "point greyline_gc_address at it."
  else
    say "go $(go version | awk '{print $3}')"
  fi

  # The launcher needs the Steam Linux Runtime, not just Steam.
  local slr="$STEAM_ROOT/steamapps/common/SteamLinuxRuntime_sniper/run"
  if [ -f "$slr" ]; then
    say "Steam Linux Runtime sniper found"
  else
    warn "Steam Linux Runtime sniper is missing. Install it with:"
    warn "  steam steam://install/1628350"
    warn "or set SLR_SNIPER_PATH before launching."
  fi
}

# ---------------------------------------------------------------------------
# build
# ---------------------------------------------------------------------------
step_build_game() {
  say "building the game (SteamRT sniper container, this takes a while)"
  command -v podman >/dev/null || die "podman is required for the game build"

  # buildallprojects re-execs itself inside the container.
  ./src/buildallprojects release

  say "staging a clean game directory"
  ./game_clean/copy.sh
  say "game built"
}

step_build_coordinator() {
  command -v go >/dev/null || { warn "no Go, skipping coordinator build"; return 0; }

  say "building the coordinator"
  ( cd services/coordinator && go build -o bin/coordinator ./cmd/coordinator )
  say "coordinator built at services/coordinator/bin/coordinator"
}

# ---------------------------------------------------------------------------
# coordinator service
# ---------------------------------------------------------------------------
step_coordinator_service() {
  command -v systemctl >/dev/null || { warn "no systemd, skipping the service"; return 0; }

  local unit_dir="$HOME/.config/systemd/user"
  local secret_file="$HOME/.config/greyline/secret"
  mkdir -p "$unit_dir" "$(dirname -- "$secret_file")"

  # One secret, generated once and kept. Regenerating it on every run would
  # invalidate every match secret already in flight.
  if [ ! -s "$secret_file" ]; then
    head -c 32 /dev/urandom | base64 > "$secret_file"
    chmod 600 "$secret_file"
    say "generated a coordinator secret at $secret_file"
  fi

  cat > "$unit_dir/greyline-coordinator.service" <<EOF
[Unit]
Description=GREYLINE FRONTRESS Game Coordinator (test box)
After=network-online.target

[Service]
Type=simple
WorkingDirectory=$REPO_ROOT/services/coordinator
Environment=GREYLINE_STATE_PATH=%h/.local/share/greyline/players.json
EnvironmentFile=%h/.config/greyline/env
ExecStart=$REPO_ROOT/services/coordinator/bin/coordinator -config gc.test.json -world world.test.json -log-level debug
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
EOF

  # systemd's EnvironmentFile cannot read a bare secret, so mirror it as KEY=value.
  printf 'GREYLINE_GC_SECRET=%s\n' "$(cat -- "$secret_file")" > "$HOME/.config/greyline/env"
  chmod 600 "$HOME/.config/greyline/env"
  mkdir -p "$HOME/.local/share/greyline"

  systemctl --user daemon-reload
  systemctl --user enable --now greyline-coordinator.service || true

  # Survive logout, or the coordinator dies when the session ends.
  if command -v loginctl >/dev/null; then
    loginctl enable-linger "$USER" 2>/dev/null || \
      warn "could not enable linger; the coordinator will stop when you log out"
  fi

  say "coordinator service started; follow it with:"
  say "  journalctl --user -u greyline-coordinator -f"
}

# ---------------------------------------------------------------------------
# Steam library entry
# ---------------------------------------------------------------------------
step_shortcut() {
  local launcher="$REPO_ROOT/game/tc2.sh"
  [ -x "$launcher" ] || chmod +x "$launcher" 2>/dev/null || true
  [ -f "$launcher" ] || die "no launcher at $launcher — build the game first"

  if pgrep -x steam >/dev/null; then
    die "Steam is running. Close it first: it rewrites shortcuts.vdf on exit and would discard this change."
  fi

  python3 "$REPO_ROOT/tools/steam_shortcut.py" \
    --name "Campus Fortress" \
    --exe "$launcher" \
    --startdir "$REPO_ROOT/game" \
    --steam-root "$STEAM_ROOT"
}

# ---------------------------------------------------------------------------
main() {
  case "${1:-all}" in
    deps)        step_deps ;;
    build)       step_deps; step_build_game; step_build_coordinator ;;
    coordinator) step_build_coordinator; step_coordinator_service ;;
    shortcut)    step_shortcut ;;
    all)
      step_deps
      step_build_game
      step_build_coordinator
      step_coordinator_service
      step_shortcut
      say "done. Launch 'Campus Fortress' from the Steam library."
      ;;
    *) die "unknown step '$1' (deps | build | coordinator | shortcut | all)" ;;
  esac
}

main "$@"
