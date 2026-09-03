#!/usr/bin/env bash
set -euo pipefail

SESSION=beads-tui-dev
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BEADS_ROOT="${BEADS_VERIFY_BEADS_DIR:-${BEADS_DIR:-/opt/ra/firstmate/.beads}}"
TARGET="$SESSION:tui"

die() { printf '✗ %s\n' "$*" >&2; exit 1; }

preflight() {
  command -v tmux >/dev/null || die 'tmux is required'
  command -v go >/dev/null || die 'go is required'
  command -v bd >/dev/null || die 'bd is required'
  [ -d "$BEADS_ROOT" ] || die "Beads workspace not found: $BEADS_ROOT"
}

cmd_up() {
  preflight
  make -C "$ROOT" build >/dev/null
  tmux has-session -t "$SESSION" 2>/dev/null || tmux new-session -d -s "$SESSION" -n tui -c "$ROOT"
  if ! tmux list-windows -t "$SESSION" -F '#{window_name}' | grep -qx tui; then
    tmux new-window -t "$SESSION" -n tui -c "$ROOT"
  fi
  if ! tmux list-panes -t "$TARGET" -F '#{pane_current_command}' | grep -qxE 'beads-tui|go'; then
    printf -v command 'exec env BEADS_DIR=%q %q' "$BEADS_ROOT" "$ROOT/beads-tui"
    tmux send-keys -t "$TARGET" "$command" C-m
  fi
  printf '✓ tmux session %s is up (BEADS_DIR=%s)\n' "$SESSION" "$BEADS_ROOT"
}

cmd_status() {
  tmux has-session -t "$SESSION" 2>/dev/null || { printf 'session %s is down\n' "$SESSION"; return; }
  tmux list-windows -t "$SESSION" -F 'window #{window_index}: #{window_name} (#{window_panes} pane)'
}

cmd_logs() { tmux capture-pane -p -S -200 -t "$TARGET"; }
cmd_restart() { "$0" down; "$0" up; }
cmd_down() { tmux kill-session -t "$SESSION" 2>/dev/null && printf '✓ stopped %s\n' "$SESSION" || printf 'session %s is down\n' "$SESSION"; }
cmd_attach() { tmux attach -t "$SESSION"; }

case "${1:-up}" in
  up) cmd_up ;;
  status) cmd_status ;;
  logs) cmd_logs ;;
  restart) cmd_restart ;;
  down) cmd_down ;;
  attach) cmd_attach ;;
  -h|--help|help) sed -n '2,14p' "$0" ;;
  *) die "unknown command: $1 (use up|down|status|logs|restart|attach)" ;;
esac
