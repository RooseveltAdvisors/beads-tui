# GPU install report: beads-tui PR26

Date: 2026-09-09
Host: `gpu`
Repository: `RooseveltAdvisors/beads-tui`

## Source provenance

GitHub was queried with `gh-axi` before the install:

- PR26 is merged and green: `fix(tui): search finds beads outside the active status tab`.
- PR26 base: `main` at `b52996cf1f159f25d734c66e16eea4edd1dc927b`.
- PR26 source: `fm/fm-isv6` at `f9e058a27ccd8ffae5490b757f1ab69f5689c49f`.
- PR26 merge commit: `94ea4357615f183f9194cc3c09e25a0bc1ecf288`.
- Authoritative GitHub `main` tip: `94ea4357615f183f9194cc3c09e25a0bc1ecf288`, matching the merge commit.
- PR checks: `1 passed, 0 failed, 1 total`.

The authoritative Zeta manifest at `/opt/ra/firstmate/projects/Zeta/distribution.json`
has no `beads` or `beads-tui` entries. No Zeta pin was changed.

## GPU install

The existing GPU operator workflow was dispatched with:

```text
gh-axi workflow run deploy.yml -R RooseveltAdvisors/beads-tui --ref main
```

Run `34412781195` completed successfully on runner `gpu-beadstui`, machine
`gpu`, and checked out `main` at `94ea4357615f183f9194cc3c09e25a0bc1ecf288`.
The workflow's existing build recipe omitted `CGO_ENABLED=0`; its output was
therefore checked and found dynamically linked. The final installed artifact
was converged on the same GPU host with the repository-documented static build
procedure, without a force flag or repository reset:

```text
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=94ea4357615f183f9194cc3c09e25a0bc1ecf288" -o /home/jon/.local/bin/beads-tui ./cmd/beads-tui
```

Installed artifact evidence:

```text
path: /home/jon/.local/bin/beads-tui
version: beads-tui 94ea4357615f183f9194cc3c09e25a0bc1ecf288
sha256: e622849ae9c7a4f0d5f326421bde78b83deb3dce05735bf9e5c1e8d1730ae8bd
file: ELF 64-bit LSB executable, x86-64, statically linked, stripped
ldd: not a dynamic executable
```

## Runtime proof

The repository runtime verifier passed against `/opt/ra/firstmate/.beads`:

```text
real board loaded (480 ready rows; visible ready bead fm-51ma)
durable log written
missing workspace renders a loud error and logs it
comments view loaded, submitted, and matched bd JSON
comment badge appeared on the board row
inline detail refreshed without opening c view
verify passed
```

The installed binary was then driven in a real interactive TUI pane through
Herdr with `BEADS_DIR=/opt/ra/firstmate/.beads`. Search query `fm-0nli` returned
two matching rows, including `fm-0nli`, on every status view:

```text
view:ready     query:fm-0nli total:2
view:open      query:fm-0nli total:2
view:in_progress query:fm-0nli total:2
view:blocked   query:fm-0nli total:2
view:closed    query:fm-0nli total:2
view:deferred  query:fm-0nli total:2
view:pinned    query:fm-0nli total:2
view:hooked    query:fm-0nli total:2
```

The selected detail pane showed `ID fm-0nli`; `bd show` independently reported
`{"id":"fm-0nli","status":"in_progress","updated_at":"2026-09-09T20:16:09Z"}`.
The hermetic proof session logged the installed version and exited cleanly:

```text
start: beads-tui 94ea4357615f183f9194cc3c09e25a0bc1ecf288
exit: ok
```

The operator's existing state at `~/.config/beads-tui/state.json` was not
targeted. The proof used a temporary `BEADS_TUI_CONFIG_DIR` and
`BEADS_TUI_LOG_DIR`, leaving the existing GPU configuration in place.
