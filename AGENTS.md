# Project agent memory

This file is the project's committed home for project-intrinsic agent knowledge: build, test, release, architecture, and sharp-edge notes that should travel with the code.

## Beads read-only contract

beads-tui renders the Beads store strictly read-only via the `bd` CLI. The
exact invocation contract lives in `internal/bd/bd.go` (see `Client`):
`bd list --status STATUS --json -n 0`, `bd list --all --json -n 0` (graph snapshot),
`bd show ID --json`, `bd dep list ID --json [--direction up]`,
`bd statuses --json`. Any change to
bd's flag surface or JSON field names must be mirrored there and in
`internal/tui/app.go`'s `Backend`/`graphBackend` interfaces.

## Build

Static single binary: `CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o beads-tui ./cmd/beads-tui` (also `make build`). The store is auto-discovered by `bd` itself - beads-tui just inherits the ambient environment (`.beads`, `BEADS_DIR`, `--db`), never hardcodes a path.

## Runtime proof gate

`go test` alone never proves this TUI loads. `scripts/verify.sh` (skill:
`/verify`) builds the binary, drives the real interactive TUI in a throwaway
tmux session against a real `.beads` workspace, and fails on SIGKILL, an empty
Ready board, or a missing workspace that renders blank instead of a loud error.
Defaults to `/opt/ra/firstmate/.beads`; override with `BEADS_VERIFY_BEADS_DIR`
to point at any real embedded-Dolt `.beads` fixture. The gate TUI runs with a
temporary `BEADS_TUI_CONFIG_DIR` and expects the newest open root bead (the
deterministic first row under the default sort), so a shared user state.json
cannot flip the expectation. It is deliberately not in
GitHub CI - runners have no `bd` and no fleet workspace. It also asserts the
durable log: a timestamped start line, a clean `exit: ok`, and a
`board load failed` entry from the missing-workspace phase. `scripts/dev-local.sh`
(skill: `/dev-local`) is the same launch path for interactive use.

## Crash and error log

The TUI's stderr is invisible (alt screen) and dies with its host window, and
Bubbletea recovers panics itself rather than re-panicking. `internal/logfile`
closes both holes: `Init` redirects the standard logger and `os.Stderr` to
`$XDG_STATE_HOME/beads-tui/beads-tui.log` (override: `BEADS_TUI_LOG_DIR`,
printed by `beads-tui log-path`), and `Guard` wraps the model so a panic value
is logged before Bubbletea swallows it. Every `log.Printf` in the codebase
therefore lands on disk - keep them diagnostic (ids, counts, bd errors) and
never log bead titles, descriptions, or other issue content.

## Sharp edges

- Store lock contention is retried inside the bd client, not per-feature:
  every `bd` invocation gets bounded attempts (see the constants in
  `internal/bd/bd.go`) and, when exhausted, one sanitized busy/locked error.
  TUI loads therefore carry a single generous 60s cap (`boardRetryTimeout`),
  never a short first-attempt deadline that would cut retries off.
- Board data (`bd list --json`) already holds every detail field, and a
  complete graph pass holds all dep edges: `fetchDetail` seeds from
  `detailSeedFor` and only calls bd for what is missing, so navigating the
  board normally makes zero bd round-trips.
- The Graph never leaks: `bd` stdout/stderr failures are reduced to a single sanitized error (`jsonCall` in `internal/bd/bd.go`); tests assert raw output stays internal (`TestJsonCallNeverLeaksRawOutput`).
- Board reloads never blank the screen: `applyBoard` keeps the last good rows on
  failure, shows a status-line notice, and retries with backoff (2/5/15 s, then
  capped) using an extended 60 s board deadline while a retry is in flight
  (`boardRetryBackoff`/`boardLoadTimeout` in `internal/tui/app.go`).
- Detail loads never block navigation: selection changes are debounced (120 ms)
  and detail is cached per `id+updated_at` (`detailCache`) with neighbour
  prefetch (selected±1..3); all bd work happens after the debounce settles.
- Status vocabulary loads live from `bd statuses --json`; on failure the built-in fallback in `internal/tui/render.go` (`NewVocab`) takes over.
- Board sorting/filtering primitives and prompt syntax live in `internal/tui/filter.go`; the key dispatch and derived-row lifecycle live in `internal/tui/app.go`.

## Maintaining this file

Keep this file for knowledge useful to almost every future agent session in this project.
Do not repeat what the codebase already shows; point to the authoritative file or command instead.
Prefer rewriting or pruning existing entries over appending new ones.
When updating this file, preserve this bar for all agents and keep entries concise.
