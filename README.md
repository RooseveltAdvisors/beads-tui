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
| `1` | Ready (actionable) | `bd list --ready` | Work claimable now (no active blockers) |
| `2` | Open     | `bd list`                | Open issues regardless of blockers |
| `3` | All      | `bd list --all`          | Everything, including closed |

The status vocabulary (colors and categories) is loaded live from
`bd statuses --json`; if that call fails the built-in vocabulary is used.
Core work-state glyphs remain fixed so rows and the help legend agree.

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

- `j`/`k` or arrow keys move through the board; `g`/`G` top/bottom, `space`/`b` page.
  When the focused bead has dependency edges, `G` instead opens its two-hop
  ASCII dependency graph; cycles are called out in the graph header.
- `ctrl-u`/`ctrl-d` move by half a page in the board and detail pane
- The default board is an indented dependency tree: `enter`/`tab` toggles a
  parent subtree, `enter` opens a leaf's detail, `h` collapses, and `v` toggles
  the flat list
- `l` (or `→`) focuses the detail pane; `j`/`k` scroll it; `esc` clears an
  active filter first, then returns from the detail pane when pressed again
- `1`/`2`/`3` switch views, `r` refreshes, `?` shows help, `q` (or `ctrl+c`) quits
- `s` cycles priority, created, updated, alphabetical, and leverage sorting;
  created is the default newest-first order, while leverage ranks the work by
  the graph-backed `⇡N blocks` count, with the most dependents first
- `f` opens a filter prompt. Use `status:open`, `priority:P1`, `label:frontend`, or free text; `enter` applies and `esc` clears.
- `/` opens a flat, incremental result list across bead id, title, and
  description; `j`/`k` navigates matches, `enter` commits, and `esc` cancels
  and restores the previous filter, selection, and detail context.
- `t` filters to the selected bead's labels.

Each list row carries its native bd status (never the computed `ready` view
bucket), priority (`P0`-`P4`), id, title, and subdued dim-gray labels, plus
`⇣N blocked-by`/`⇡N blocks` dependency chips. Deferred rows include their `defer_until` date.
Ready rows use the hollow glyph alone for claimable open work and include the
owner beside `in_progress` when available. Blocked status is red, closed is
dim, and deferred is orange. Priority colors are red,
orange, yellow, and gray for P0 through P3. At normal terminal widths, the
persistent bottom bar shows the view, sort, active filter,
selection, total count, and scroll position; below 48 columns it compacts to the view, filter
indicator, and scroll position. The detail pane shows the full issue: status
pill, Markdown-rendered description, notes, and the dependency edges in both
directions with their edge type (`blocks`, `tracks`, `parent-child`, ...).

Rows and markers:

```
○ claimable   ● in_progress   ⊘ blocked   ✓ closed   ◷ deferred   📌 hold
⇣N depends on N   ⇡N has N dependents
```

When stdin is not a TTY, `beads-tui` degrades to a one-shot JSON dump of the
ready board, so scripts and agents get content instead of a pager.

## Read-only guarantee

beads-tui never creates, edits, or closes beads. All data comes from read-only
`bd` invocations; board-load failures surface bd's diagnostic and the UI keeps
running. Dependency metadata is best effort: a failed graph lookup is logged
without hiding the loaded list rows. Missing `bd`, a store it cannot reach, or
an empty board all render as explicit states rather than crashes or raw command
output.

## Planned mapping

Mapped to `prefix+H` in the operator's Herdr config, mirroring the
`prefix+u` -> `quota-axi --tui` pattern.

## License

MIT
