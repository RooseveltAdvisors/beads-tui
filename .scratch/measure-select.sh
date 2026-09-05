#!/bin/bash
set -u
cd "$(dirname "$0")"
STORE=$PWD/scale-ws/.beads
BIN=$PWD/beads-tui
S=btsel$$
rm -rf cfg-sel; mkdir -p cfg-sel
tmux new-session -d -s $S -x 120 -y 40 -c $PWD
tmux send-keys -t $S "env BEADS_TUI_CONFIG_DIR=$PWD/cfg-sel BEADS_DIR=$STORE $BIN" C-m
# wait for graph fully loaded
t0=$(date +%s%3N)
while ! tmux capture-pane -p -t $S | grep -q "graph:[0-9]* edges"; do
  sleep 0.05; now=$(date +%s%3N)
  [ $((now - t0)) -gt 30000 ] && { echo TIMEOUT-graph; tmux kill-session -t $S; exit 1; }
done
t1=$(date +%s%3N); echo "startup_full_ms=$((t1 - t0))"
first_id=$(tmux capture-pane -p -t $S | grep -oE "ID syn-[a-z0-9]+" | head -1)
for round in 1 2 3; do
  t0=$(date +%s%3N)
  tmux send-keys -t $S j
  while true; do
    cur=$(tmux capture-pane -p -t $S | grep -oE "ID syn-[a-z0-9]+" | head -1)
    [ -n "$cur" ] && [ "$cur" != "$first_id" ] && break
    now=$(date +%s%3N); [ $((now - t0)) -gt 5000 ] && { echo TIMEOUT-sel; break; }
    sleep 0.005
  done
  t1=$(date +%s%3N); echo "selection_round${round}_ms=$((t1 - t0))"
  first_id=$(tmux capture-pane -p -t $S | grep -oE "ID syn-[a-z0-9]+" | head -1)
done
tmux capture-pane -p -t $S | tail -1
tmux send-keys -t $S q; sleep 0.3; tmux kill-session -t $S 2>/dev/null
