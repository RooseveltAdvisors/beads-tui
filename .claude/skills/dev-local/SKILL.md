---
name: dev-local
description: Start beads-tui against a real Beads workspace in tmux.
user_invocable: true
---

# /dev-local

Use `scripts/dev-local.sh up` to start one idempotent `beads-tui-dev` tmux
session. It runs the built binary with `BEADS_DIR` set to
`/opt/ra/firstmate/.beads` by default. Set `BEADS_VERIFY_BEADS_DIR` to a
different real `.beads` fixture with the same embedded-Dolt shape.

Use `status`, `logs`, `restart`, `attach`, or `down` for the session. The
launcher owns only `beads-tui-dev`; it does not touch the fleet workspace.
