#!/usr/bin/env bash
# Measure selection-change -> correct-detail-paint latency in the real TUI.
set -u
cd "$(dirname "$0")"
S="bt-measure-$$"
tmux new-session -d -s $S -x 120 -y 35 -c "$PWD"
tmux send-keys -t $S "env BEADS_TUI_CONFIG_DIR=$PWD/cfg BEADS_DIR=$PWD/store $PWD/beads-tui" C-m
sleep "${SETTLE:-10}"   # let initial board+graph settle
send_and_wait() {
  local key=$1 target=$2
  tmux send-keys -t $S "$key"
  local start=$(date +%s%N) end
  for i in $(seq 1 300); do
    if tmux capture-pane -p -t "$S" | grep -q "ID $target "; then
      end=$(date +%s%N); echo "key=$key target=$target latency_ms=$(( (end-start)/1000000 ))"; return
    fi
    sleep 0.02
  done
  echo "key=$key target=$target latency_ms=TIMEOUT"
}
# flat mode for predictable ordering
tmux send-keys -t $S v
sleep 0.3
send_and_wait j "scratch-5ed.1.1.1.1.1.1"
send_and_wait j "scratch-5ed.1.1.1.1.1"
send_and_wait k "scratch-5ed.1.1.1.1.1.1"
send_and_wait j "scratch-5ed.1.1.1.1.1"
tmux send-keys -t $S q
sleep 0.3
tmux kill-session -t $S 2>/dev/null
