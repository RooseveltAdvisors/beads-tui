#!/bin/bash
# Measure beads-tui at scale against the synthetic 842-issue store.
# All stores are synthetic, worktree-local; nothing touches ~/.beads or /opt/ra/firstmate.
set -u
cd "$(dirname "$0")"
STORE=$PWD/scale-ws/.beads
BIN=$PWD/beads-tui
S=bt-scale$$

# fresh config so state doesn't leak between runs
rm -rf cfg-scale; mkdir -p cfg-scale

tmux new-session -d -s $S -x 120 -y 40 -c $PWD
tmux send-keys -t $S "env BEADS_TUI_CONFIG_DIR=$PWD/cfg-scale BEADS_DIR=$STORE $BIN" C-m

# startup: poll until footer (last line) is non-empty
t0=$(date +%s%3N)
while true; do
  footer=$(tmux capture-pane -p -t $S | grep -c "view:ready")
  if [ "$footer" -ge 1 ]; then break; fi
  sleep 0.05
  now=$(date +%s%3N)
  if [ $((now - t0)) -gt 30000 ]; then echo "TIMEOUT waiting for board"; tmux kill-session -t $S; exit 1; fi
done
t1=$(date +%s%3N)
echo "startup_ms=$((t1 - t0))"

# selection latency: press j, poll until the detail ID changes from syn-269-style first row
first_id=$(tmux capture-pane -p -t $S | grep -oE "ID syn-[a-z0-9]+" | head -1)
t0=$(date +%s%3N)
tmux send-keys -t $S j
while true; do
  cur=$(tmux capture-pane -p -t $S | grep -oE "ID syn-[a-z0-9]+" | head -1)
  if [ -n "$cur" ] && [ "$cur" != "$first_id" ]; then break; fi
  now=$(date +%s%3N)
  if [ $((now - t0)) -gt 10000 ]; then echo "TIMEOUT waiting for selection"; break; fi
  sleep 0.01
done
t1=$(date +%s%3N)
echo "selection_ms=$((t1 - t0))"

# deep selection: jump to bottom (G opens graph... use b paging instead: many j presses)
t0=$(date +%s%3N)
tmux send-keys -t $S j j j j j j j j j j
for i in $(seq 12); do tmux capture-pane -p -t $S > /dev/null; done
t1=$(date +%s%3N)
echo "ten_moves_ms=$((t1 - t0))"

# refresh latency
before=$(tmux capture-pane -p -t $S | grep -oE "ID syn-[a-z0-9]+" | head -1)
t0=$(date +%s%3N)
tmux send-keys -t $S r
while true; do
  after=$(tmux capture-pane -p -t $S | grep -oE "ID syn-[a-z0-9]+" | head -1)
  gone=$(tmux capture-pane -p -t $S | grep -c "stale board")
  if [ "$gone" -ge 1 ]; then echo "REFRESH FAILED (stale notice)"; break; fi
  now=$(date +%s%3N)
  if [ $((now - t0)) -gt 30000 ]; then echo "TIMEOUT waiting for refresh"; break; fi
  # refresh is done when the footer no longer shows the refreshing state; approximate by fixed wait below
  break
done
sleep 3
t1=$(date +%s%3N)
echo "refresh_ms_upper=$((t1 - t0))"
tmux capture-pane -p -t $S | tail -1
tmux send-keys -t $S q
sleep 0.3
tmux kill-session -t $S 2>/dev/null
