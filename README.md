# beads-tui

A read-only terminal UI for [Beads](https://github.com/steveyegge/beads)
(`bd`) - the dependency-graph issue tracker built as coding-agent memory.

One static binary, no daemon, no network. It renders the same embedded-Dolt
store `bd` works against and never writes to it: beads-tui only ever invokes
bd's read-only commands (`list`, `show`, `dep list`, `statuses`) and renders
what they return.

## Status tabs

Tabs are the native `bd` statuses: `open`, `in_progress`, `blocked`, `closed`,
and `deferred`, followed by configured custom statuses from `bd statuses --json`.
Keys `1` through `9` select the visible tabs, and each tab loads with
`bd list --status STATUS`.

The status vocabulary (colors and categories) is loaded live from
`bd statuses --json`; if that call fails the built-in vocabulary and tabs are
used.
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
beads-tui                   # interactive board (open status by default)
beads-tui list [--status STATUS]         # board as JSON (no TTY needed)
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
  active search first, then returns from the detail pane when pressed again
- `1`-`9` switch native and custom status tabs, `R` resets view/sort/search,
  `?` shows help, and `q` (or `ctrl+c`) quits
- `s` cycles created, updated, alphabetical, dependencies (`⇣N` blocked-by),
  depends (`⇡N` blocks), and priority sorting; created is the default newest-first order
- `/` opens the incremental search prompt. Search by bead id, title, or
  description, or use `status:open`, `priority:P1`, or `label:frontend`;
  `enter` applies and `esc` cancels/restores the prior context.
- `t` searches the selected bead's labels.
- `y` opens a yank menu for the selected bead's ID, title, and URL (when present);
  `enter` copies through `clipboard-copy` or OSC52.

Each list row carries its bd status as a glyph (never the word `open`),
priority (`P0`-`P4`), id, title, and subdued dim-gray labels, plus
`⇣N blocked-by`/`⇡N blocks` dependency chips. Deferred rows include their `defer_until` date.
In-progress rows include the owner beside their glyph when available, and the status is vibrant. Blocked status is red, closed is dim,
and deferred is orange. Priority colors are red, orange, yellow, and cyan for
P0 through P3. View, search, and sort persist under the user's config directory.
The footer reports the number of graph edges loaded, so dependency counts are
observable rather than inferred from the list response.
At normal terminal widths, the
persistent bottom bar shows the view, sort, active search,
selection, total count, and scroll position; below 48 columns it compacts to the view, search
indicator, and scroll position. The detail pane shows the full issue: status
pill, Markdown-rendered description, notes, and the dependency edges in both
directions with their edge type (`blocks`, `tracks`, `parent-child`, ...).

Rows and markers:

```
○ open        ● in_progress   ⊘ blocked   ✓ closed   ◷ deferred   📌 hold
⇣N depends on N   ⇡N has N dependents
```

When stdin is not a TTY, `beads-tui` degrades to a one-shot JSON dump of the
open-status board, so scripts and agents get content instead of a pager.

`R` clears the search and restores the open status with created-newest-first
sorting.

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
