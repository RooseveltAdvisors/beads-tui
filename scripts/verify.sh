#!/usr/bin/env bash
set -Eeuo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BEADS_ROOT="${BEADS_VERIFY_BEADS_DIR:-${BEADS_DIR:-/opt/ra/firstmate/.beads}}"
WAIT_SECONDS="${BEADS_VERIFY_WAIT_SECONDS:-30}"
EVIDENCE_DIR="${BEADS_VERIFY_EVIDENCE_DIR:-$ROOT/evidence}"
SESSION="beads-tui-verify-$$"
TARGET="$SESSION:tui"
TMP_DIR=""

die() { printf '✗ %s\n' "$*" >&2; exit 1; }
capture() { tmux capture-pane -p -t "$TARGET" 2>/dev/null || true; }
pane_state() { tmux display-message -p -t "$TARGET" 'dead=#{pane_dead} status=#{pane_dead_status}' 2>/dev/null || true; }
cleanup() {
  tmux kill-session -t "$SESSION" 2>/dev/null || true
  if [ -n "$TMP_DIR" ]; then
    rm -rf -- "$TMP_DIR"
  fi
}
trap cleanup EXIT

preflight() {
  command -v tmux >/dev/null || die 'tmux is required'
  command -v go >/dev/null || die 'go is required'
  command -v bd >/dev/null || die 'bd is required'
  command -v jq >/dev/null || die 'jq is required'
  [ -d "$BEADS_ROOT" ] || die "Beads workspace not found: $BEADS_ROOT"
  mkdir -p "$EVIDENCE_DIR"
}

build() {
  TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/beads-tui-verify.XXXXXX")"
  CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o "$TMP_DIR/beads-tui" "$ROOT/cmd/beads-tui"
}

ready_snapshot() {
  BEADS_DIR="$BEADS_ROOT" bd list --ready --json -n 0
}

start_tui() {
  local beads_dir=$1
  local config_dir=${2:-}
  tmux new-session -d -s "$SESSION" -n tui -c "$ROOT"
  local env_prefix=''
  if [ -n "$config_dir" ]; then
    env_prefix="BEADS_TUI_CONFIG_DIR=$(printf %q "$config_dir") "
  fi
  printf -v command 'exec env %sBEADS_DIR=%q %q' "$env_prefix" "$beads_dir" "$TMP_DIR/beads-tui"
  tmux send-keys -t "$TARGET" "$command" C-m
}

stop_tui() {
  tmux send-keys -t "$TARGET" q
  for _ in $(seq 1 10); do
    if ! tmux has-session -t "$SESSION" 2>/dev/null; then
      return 0
    fi
    case "$(pane_state)" in
      *'dead=1'*)
        case "$(pane_state)" in
          *'status=9'*|*'status=137'*) die 'TUI exited by SIGKILL' ;;
        esac
        tmux kill-session -t "$SESSION" 2>/dev/null || true
        return 0
        ;;
    esac
    sleep 0.1
  done
  die 'TUI did not exit after q'
}

preflight
build

ready_json="$(ready_snapshot)" || die 'bd list --ready failed'
ready_count="$(jq -e 'if type == "array" then length else error("expected array") end' <<<"$ready_json")" || die 'bd list --ready did not return a JSON array'
[ "$ready_count" -gt 0 ] || die 'fixture has no ready rows; verification requires a non-empty ready board'

# The TUI's first visible row is deterministic only under its default state
# (created-newest-first, no folds). A shared user state.json can carry a
# different sort or folds, so the gate TUI gets a throwaway config dir and
# the expectation is computed from the same data the board loads: the newest
# open root bead.
open_json="$(BEADS_DIR="$BEADS_ROOT" bd list --status open --json -n 0)" || die 'bd list --status open failed'
expected_top="$(jq -er '[.[] | select((.parent_id // "") == "")] | sort_by(.created_at) | reverse | .[0].id' <<<"$open_json")" || die 'open JSON has no root bead IDs'

printf 'BEADS_DIR=%s\nready_rows=%s\noperator_mapping=prefix+H\n' "$BEADS_ROOT" "$ready_count" >"$EVIDENCE_DIR/beads-tui-verify.txt"
start_tui "$BEADS_ROOT" "$TMP_DIR/config"
loaded=0
for _ in $(seq 1 "$WAIT_SECONDS"); do
  pane="$(capture)"
  if printf '%s\n' "$pane" | grep -qE 'signal: killed|SIGKILL|Could not load board\.'; then
    printf '%s\n' "$pane" >>"$EVIDENCE_DIR/beads-tui-verify.txt"
    die "real TUI failed to load the Ready board ($(pane_state))"
  fi
  if printf '%s\n' "$pane" | grep -Fq "$expected_top"; then
    loaded=1
    break
  fi
  case "$(pane_state)" in
    *'dead=1'*)
      printf '%s\n' "$pane" >>"$EVIDENCE_DIR/beads-tui-verify.txt"
      die "TUI exited before loading the Ready board ($(pane_state))"
      ;;
  esac
  sleep 1
done
pane="$(capture)"
printf '%s\n' "$pane" >>"$EVIDENCE_DIR/beads-tui-verify.txt"
[ "$loaded" -eq 1 ] || die "Ready board did not render $expected_top within ${WAIT_SECONDS}s"
printf '✓ real Ready board loaded (%s; first row %s)\n' "$ready_count" "$expected_top"
stop_tui

missing_root="$TMP_DIR/missing-beads"
start_tui "$missing_root"
missing_ok=0
for _ in $(seq 1 10); do
  pane="$(capture)"
  if printf '%s\n' "$pane" | grep -q 'Could not load board\.' && printf '%s\n' "$pane" | grep -qE 'Error:|beads database found|BEADS_DIR'; then
    missing_ok=1
    break
  fi
  sleep 0.5
done
printf '\n--- missing workspace ---\n%s\n' "$pane" >>"$EVIDENCE_DIR/beads-tui-verify.txt"
[ "$missing_ok" -eq 1 ] || die 'missing workspace did not produce a loud board error'
printf '✓ missing workspace renders a loud error\n'
stop_tui

printf '✓ verify passed; evidence: %s\n' "$EVIDENCE_DIR/beads-tui-verify.txt"
