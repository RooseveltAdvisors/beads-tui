#!/bin/bash
set -u
cd "$(dirname "$0")"
S=btold$$
rm -rf cfg-old; mkdir -p cfg-old
tmux new-session -d -s $S -x 120 -y 40 -c $PWD
tmux send-keys -t $S "env BEADS_TUI_CONFIG_DIR=$PWD/cfg-old BEADS_DIR=$PWD/scale-ws/.beads /tmp/beads-tui-base-bin" C-m
t0=$(date +%s%3N)
while ! tmux capture-pane -p -t $S | grep -q "view:open"; do
  sleep 0.05; now=$(date +%s%3N)
  [ $((now - t0)) -gt 30000 ] && { echo TIMEOUT-start; tmux kill-session -t $S; exit 1; }
done
t1=$(date +%s%3N); echo "old_startup_ms=$((t1 - t0))"
sleep 2
first_id=$(tmux capture-pane -p -t $S | grep -oE "ID syn-[a-z0-9]+" | head -1)
t0=$(date +%s%3N)
tmux send-keys -t $S j
while true; do
  cur=$(tmux capture-pane -p -t $S | grep -oE "ID syn-[a-z0-9]+" | head -1)
  [ -n "$cur" ] && [ "$cur" != "$first_id" ] && break
  now=$(date +%s%3N); [ $((now - t0)) -gt 15000 ] && { echo TIMEOUT-sel; break; }
  sleep 0.01
done
t1=$(date +%s%3N); echo "old_selection_ms=$((t1 - t0))"
tmux capture-pane -p -t $S | tail -1
tmux send-keys -t $S q; sleep 0.3; tmux kill-session -t $S 2>/dev/null
