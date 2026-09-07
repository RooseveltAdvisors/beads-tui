#!/usr/bin/env bash
set -Eeuo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BEADS_ROOT="${BEADS_VERIFY_BEADS_DIR:-${BEADS_DIR:-/opt/ra/firstmate/.beads}}"
WAIT_SECONDS="${BEADS_VERIFY_WAIT_SECONDS:-120}"
# A board-error screen only counts as a failure once it has persisted this many
# consecutive seconds; transient store-lock timeouts self-heal via auto-retry.
BOARD_ERROR_GRACE_SECONDS="${BEADS_VERIFY_ERROR_GRACE_SECONDS:-10}"
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
  local beads_dir=$1 config_dir=$2
  tmux new-session -d -s "$SESSION" -n tui -c "$ROOT"
  # Hermetic per-run config/state so the operator's persisted view/sort/layout
  # cannot change which rows the gate expects to see, and so each phase starts
  # without another phase's board snapshot. The log dir is hermetic for the
  # same reason: the gate asserts on this run's trail, not the operator's.
  mkdir -p "$config_dir" "$LOG_DIR"
  printf -v command 'exec env BEADS_DIR=%q BEADS_TUI_CONFIG_DIR=%q BEADS_TUI_LOG_DIR=%q %q' "$beads_dir" "$config_dir" "$LOG_DIR" "$TMP_DIR/beads-tui"
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
LOG_DIR="$TMP_DIR/state"

ready_json="$(ready_snapshot)" || die 'bd list --ready failed'
ready_count="$(jq -e 'if type == "array" then length else error("expected array") end' <<<"$ready_json")" || die 'bd list --ready did not return a JSON array'
[ "$ready_count" -gt 0 ] || die 'fixture has no ready rows; verification requires a non-empty ready board'
first_id="$(jq -er '.[0].id' <<<"$ready_json")" || die 'ready JSON has no bead IDs'
# The TUI renders its own default view/sort over the whole open board, so a
# specific ready bead is usually below the fold. Instead, require that the
# painted rows are real beads from this store and include ready work.
jq -r '.[].id' <<<"$ready_json" | sort -u >"$TMP_DIR/ready_ids.txt"
[ -s "$TMP_DIR/ready_ids.txt" ] || die 'ready JSON has no bead IDs'

printf 'BEADS_DIR=%s\nready_rows=%s\noperator_mapping=prefix+H\n' "$BEADS_ROOT" "$ready_count" >"$EVIDENCE_DIR/beads-tui-verify.txt"
start_tui "$BEADS_ROOT" "$TMP_DIR/config-ready"
loaded=0
board_error_streak=0
for _ in $(seq 1 "$WAIT_SECONDS"); do
  pane="$(capture)"
  # A bd child killed by its own timeout logs "signal: killed" to stderr; that
  # is the TUI handling store contention, not a failure. The TUI itself being
  # SIGKILLed is detected via pane state. A board-error screen only fails the
  # gate once it persists past the auto-retry grace; a panic always fails fast.
  if printf '%s\n' "$pane" | grep -qF 'panic:'; then
    printf '%s\n' "$pane" >>"$EVIDENCE_DIR/beads-tui-verify.txt"
    die "real TUI panicked ($(pane_state))"
  fi
  if printf '%s\n' "$pane" | grep -qF 'Could not load board.'; then
    board_error_streak=$((board_error_streak + 1))
    if [ "$board_error_streak" -ge "$BOARD_ERROR_GRACE_SECONDS" ]; then
      printf '%s\n' "$pane" >>"$EVIDENCE_DIR/beads-tui-verify.txt"
      die "board error persisted past ${BOARD_ERROR_GRACE_SECONDS}s of retries ($(pane_state))"
    fi
  else
    board_error_streak=0
  fi
  ready_visible=$(printf '%s\n' "$pane" | grep -oE '[a-z0-9][a-z0-9-]{3,}' | sort -u | comm -12 - "$TMP_DIR/ready_ids.txt" | wc -l)
  if [ "$ready_visible" -gt 0 ]; then
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
[ "$loaded" -eq 1 ] || die "board did not render ready beads within ${WAIT_SECONDS}s"
first_id="$(printf '%s\n' "$pane" | grep -oE '[a-z0-9][a-z0-9-]{3,}' | sort -u | comm -12 - "$TMP_DIR/ready_ids.txt" | head -1)"
printf '✓ real board loaded (%s ready rows; visible ready bead %s)\n' "$ready_count" "$first_id"
stop_tui

# A run that dies with its host window (the Herdr prefix+h popup) must still be
# reconstructable, so require a timestamped start/exit trail on disk.
log_file="$LOG_DIR/beads-tui.log"
[ -s "$log_file" ] || die "no durable log written to $log_file"
grep -qE '^[0-9]{4}/[0-9]{2}/[0-9]{2} [0-9:.]+ start: beads-tui ' "$log_file" || die 'log has no timestamped start line'
grep -q 'exit: ok' "$log_file" || die 'log did not record the clean exit'
printf '\n--- log ---\n%s\n' "$(cat "$log_file")" >>"$EVIDENCE_DIR/beads-tui-verify.txt"
printf '✓ durable log written (%s)\n' "$log_file"

missing_root="$TMP_DIR/missing-beads"
start_tui "$missing_root" "$TMP_DIR/config-missing"
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
grep -q 'board load failed' "$log_file" || die 'missing workspace left no board-error trail in the log'
printf '✓ missing workspace renders a loud error and logs it\n'
stop_tui

printf '✓ verify passed; evidence: %s\n' "$EVIDENCE_DIR/beads-tui-verify.txt"
