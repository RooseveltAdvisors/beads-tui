# Project agent memory

This file is the project's committed home for project-intrinsic agent knowledge: build, test, release, architecture, and sharp-edge notes that should travel with the code.

## Beads read-only contract

beads-tui renders the Beads store strictly read-only via the `bd` CLI. The
exact invocation contract lives in `internal/bd/bd.go` (see `Client`):
`bd list --status STATUS --json -n 0`, `bd list --all --json -n 0` (graph snapshot),
`bd show ID --json`, `bd dep list ID --json [--direction up]`,
`bd statuses --json`. Any change to
bd's flag surface or JSON field names must be mirrored there and in
`internal/tui/app.go`'s `Backend` interface.

## Build

Static single binary: `CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o beads-tui ./cmd/beads-tui` (also `make build`). The store is auto-discovered by `bd` itself - beads-tui just inherits the ambient environment (`.beads`, `BEADS_DIR`, `--db`), never hardcodes a path.

## Sharp edges

- The Graph never leaks: `bd` stdout/stderr failures are reduced to a single sanitized error (`jsonCall` in `internal/bd/bd.go`); tests assert raw output stays internal (`TestJsonCallNeverLeaksRawOutput`).
- Status vocabulary loads live from `bd statuses --json`; on failure the built-in fallback in `internal/tui/render.go` (`NewVocab`) takes over.
- Board sorting/filtering primitives and prompt syntax live in `internal/tui/filter.go`; the key dispatch and derived-row lifecycle live in `internal/tui/app.go`.

## Maintaining this file

Keep this file for knowledge useful to almost every future agent session in this project.
Do not repeat what the codebase already shows; point to the authoritative file or command instead.
Prefer rewriting or pruning existing entries over appending new ones.
When updating this file, preserve this bar for all agents and keep entries concise.
