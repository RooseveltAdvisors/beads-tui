# beads-tui

A read-only terminal UI for [Beads](https://github.com/steveyegge/beads)
(`bd`) - the dependency-graph issue tracker built as coding-agent memory.

One static binary, no daemon, no network. It renders the same embedded-Dolt
store `bd` works against and never writes to it: beads-tui only ever invokes
bd's read-only commands (`list`, `show`, `dep list`, `statuses`) and renders
what they return.

## Views

The board has three views, mapped to the same stored-status vocabulary bd
uses (`open`, `in_progress`, `blocked`, `closed`, `deferred`, plus any custom
statuses configured in the store):

| Key | View     | bd invocation            | Shows |
|-----|----------|--------------------------|-------|
| `1` | Ready    | `bd list --ready`        | Work claimable now (no active blockers) |
| `2` | Open     | `bd list`                | Open issues regardless of blockers |
| `3` | All      | `bd list --all`          | Everything, including closed |

The status vocabulary (icons, colors, categories) is loaded live from
`bd statuses --json`; if that call fails the built-in vocabulary is used.

## Install / build

Go 1.26+ is required. The result is a single static binary:

```sh
make            # or: CGO_ENABLED=0 go build -trimpath -o beads-tui ./cmd/beads-tui
make test       # unit tests (parsing + view-model)
```

## Usage

Run it from anywhere `bd` would find the store - an active beads workspace
with `./.beads`, or anywhere with `BEADS_DIR` set:

```sh
beads-tui                   # interactive board (ready view)
beads-tui list [--view ready|open|all]   # board as JSON (no TTY needed)
beads-tui show <id>         # one bead as JSON
beads-tui --version
```

The TUI is keyboard-driven:

- `j`/`k` or arrow keys move through the board; `g`/`G` top/bottom, `space`/`b` page
- `s` cycles priority, created, updated, alphabetical, and dependency sorting
- `f` opens a filter prompt (`status:blocked priority:P1 words`); `esc` clears it
- `v` toggles a dependency tree; `enter` expands or collapses a tree node
- `enter` (or `→`) focuses the detail pane; `j`/`k` scroll it; `esc` back
- `1`/`2`/`3` switch views; `tab`/`shift+tab` cycle views; `r` refreshes
- `?` shows the grouped key reference; `q` (or `ctrl+c`) quits

Each list row carries a status icon, priority (`P0`-`P4`), id and title, plus
`⇣N`/`⇡N` dependency counts. The detail pane shows the full issue: status
pill, description, notes, and the dependency edges in both directions with
their edge type (`blocks`, `tracks`, `parent-child`, ...).

The footer always shows the active view, sort mode, filter, selection, count,
and position. Markdown descriptions are rendered with Glamour. Optional key
overrides use `~/.config/beads-tui/config.toml`:

```toml
[keybindings]
filter = "/"
sort = "o"
```

Rows and markers:

```
○ open   ◐ in_progress   ● blocked   ✓ closed   ❄ deferred   📌 pinned   ◇ hooked
⇣N depends on N   ⇡N has N dependents
```

When stdin is not a TTY, `beads-tui` degrades to a one-shot JSON dump of the
ready board, so scripts and agents get content instead of a pager.

## Read-only guarantee

beads-tui never creates, edits, or closes beads. All data comes from read-only
`bd` invocations; every failure surfaces bd's diagnostic and the UI keeps
running. Missing `bd`, a store it cannot reach, or an empty board all render
as explicit states rather than crashes or raw command output.

## Planned mapping

Mapped to `prefix+H` in the operator's Herdr config, mirroring the
`prefix+u` -> `quota-axi --tui` pattern.

## License

MIT
