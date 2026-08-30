# beads-tui

A terminal UI for [Beads](https://github.com/steveyegge/beads) (`bd`) - the
dependency-graph issue tracker built as coding-agent memory.

**Status: early scaffold.** The TUI is being built; the first milestone is a
read-only board: `bd ready` / `bd list` rendered as a navigable pane with
issue detail, dependency edges, and status vocabulary.

## Why

Beads is the fleet's task system of record. The CLI is great for agents;
humans want a fast keyboard-driven view of the same graph. This repo is that
view - one binary, no daemon, reads the same embedded-Dolt store `bd` writes.

## Planned mapping

Mapped to `prefix+H` in the operator's Herdr config, mirroring the
`prefix+u` -> `quota-axi --tui` pattern.

## License

MIT
