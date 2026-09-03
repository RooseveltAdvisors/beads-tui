---
name: verify
description: Prove beads-tui loads a real Beads board and reports failures.
user_invocable: true
---

# /verify — prove the task before the PR

This is the independent runtime proof layer. It complements, and never
replaces, `go test`, `go vet`, or no-mistakes. Run it from a feature branch
with the change committed:

```sh
scripts/verify.sh
```

The verifier builds the static binary, starts the real interactive TUI in a
fresh tmux session, and checks all of these gates:

- `BEADS_DIR` resolves to `/opt/ra/firstmate/.beads`, or
  `BEADS_VERIFY_BEADS_DIR` names a real embedded-Dolt `.beads` fixture.
- The ready JSON is non-empty and at least one of its real bead IDs is visible
  in the loaded Ready board.
- Loading completes without `signal: killed`/SIGKILL and without a board error.
- A missing workspace produces `Could not load board` plus a diagnostic in the
  rendered TUI, never a blank table.

The operator contract is part of this proof: the deployed mapping is
`prefix+H`, and the process receives the same `BEADS_DIR` environment variable
shown in the command above. Keep both facts in any verification report.

The script writes ignored pane captures under `evidence/`. A failure is a
failure even when unit tests pass; do not self-certify from tests alone. Fix a
runtime failure, commit it, and rerun `/verify` with a fresh session.
