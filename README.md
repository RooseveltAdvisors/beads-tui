# beads-tui

A keyboard-driven terminal UI for [Beads](https://github.com/steveyegge/beads)
(`bd`) - the dependency-graph issue tracker built as coding-agent memory.

One static binary, no daemon, no network. It renders the same embedded-Dolt
store `bd` works against. Board data is read-only; issue comments can be
viewed and added without leaving the TUI.

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
beads-tui log-path          # where crashes and errors are recorded
beads-tui --version
```

The TUI is keyboard-driven:

- `j`/`k` or arrow keys move through the board; `g`/`G` top/bottom, `space`/`b` page.
  When the focused bead has dependency edges, `G` instead opens its two-hop
  ASCII dependency graph; cycles are called out in the graph header.
- `ctrl-u`/`ctrl-d` move by half a page in the board and detail pane
- The default board is an indented hierarchy tree: `enter`/`tab` toggles a
  subtree, `h` (or `←`) collapses it, `l` (or `→`) unfolds a folded node or
  opens the detail pane, `*` expands every fold, and `v` toggles the flat list
- `L` always focuses the detail pane
- `l` (or `→`) focuses the detail pane; `j`/`k` scroll it; `esc` clears an
  active search first, then returns from the detail pane when pressed again
- `1`-`9` switch native and custom status tabs, `r` reloads the board keeping
  the current view/sort/search, `R` resets view/sort/search, `?` shows help, and
  `q` (or `ctrl+c`) quits
- `V` cycles the pane layout: side-by-side, stacked (list above detail), or
  auto; the choice persists across restarts
- `o` opens persisted view options for task-row fields, detail sections, and
  detail-pane visibility; `r` in that screen restores the defaults. Labels are
  hidden from rows by default but remain available here and in task detail.
- `s` cycles created, updated, alphabetical, dependencies (`⇣N` blocked-by),
  depends (`⇡N` blocks), and priority sorting; created is the default newest-first order
- `/` opens the incremental search prompt. Search by bead id, title, or
  description, or use `status:open`, `priority:P1`, `label:frontend`,
  `assignee:pi`, `comments:true`, `recurring`, or `recurring:false`. Spaces
  combine conditions with AND, `|` means OR, `!` negates, and parentheses
  group expressions;
  `enter` applies and `esc` cancels/restores the prior context.
- `t` searches the selected bead's labels.
- `y` opens a yank menu for the selected bead's ID, title, and URL (when present);
  `enter` copies through `clipboard-copy` or OSC52.

## Layout

Like gh-dash, beads-tui adapts the split to the terminal. On wide terminals
(140 columns or more) the list and detail panes sit side by side and the list
takes the majority (~60% of the width), so titles and descriptions stay
readable. Below 140 columns the layout stacks: the full-width list on top
(~60% of the height) with the detail pane below. `V` cycles
side-by-side -> stacked -> auto manually, and the choice is saved with the
rest of the session state.

## Color legend

Colors live in one palette and each family owns disjoint ANSI codes, so a
color never carries two meanings:

- Priority (the `P0`-`P4` glyph only) runs a red -> orange -> yellow -> blue ->
  gray ramp:

  | P0 | P1 | P2 | P3 | P4 |
  |----|----|----|----|----|
  | red | orange | yellow | blue | gray |

- Status (the glyph, detail pill, and tabs only) uses a separate family:

  | open | in_progress | blocked | deferred | closed | hold | hooked |
  |------|-------------|---------|----------|--------|------|--------|
  | green | cyan | magenta | purple | dim | pink | teal |

- Errors and dependency cycles keep bold red but always carry their own glyph
  (`✗` for load errors, `⚠` for cycles), so red next to a `P0` is the only
  place priority red appears.

Custom statuses inherit their category's color (`bd statuses --json`). The
`?` help screen renders the same legend live.

Each list row carries its bd status as a glyph (never the word `open`),
priority (`P0`-`P4`), id, and title, plus at most two subdued dim labels
inline (`[tag] [tag] +N` marks overflow); the full label set appears in the
detail pane. Rows also carry `⇣N blocked-by`/`⇡N blocks` dependency chips and
deferred rows include their `defer_until` date.
In-progress rows include the owner beside their glyph when available. Recurring
rows carry `↻` and show their canonical agent assignee. The status is vibrant.
View, search, sort, layout, and the tree fold state persist
under the user's config directory.
The footer reports the number of graph edges loaded, so dependency counts are
observable rather than inferred from the list response.
At normal terminal widths, the
persistent bottom bar shows the view, sort, active search,
selection, total count, and scroll position; below 48 columns it compacts to the view, search
indicator, and scroll position. The detail pane shows the full issue: status
pill, parent-chain breadcrumb (`Path: root › parent`), direct children with
their status glyphs, Markdown-rendered description, notes, and the dependency
edges in both directions with their edge type (`blocks`, `tracks`,
`parent-child`, ...).

## Hierarchy

The board renders beads as a tree. Top-level beads sit at depth 0 and their
descendants are indented beneath them with `├──`/`└──` guides, up to five
levels deep. Deeper descendants collapse into the fifth level with a
`+N deeper` marker on the last visible row, so a deep parent-child chain can
never push the board into unbounded indenting.

The tree is built entirely from data the board already loads: `parent_id`
fields from `bd list --json` plus the batched dependency pass that enriches
the graph view. No extra bd process is spawned per row, and navigation never
waits on bd.

Sorting and searching apply within sibling groups, so the active sort orders
children under their parent. Searches and filters keep the parent chain of
every match visible, so a matching child is always reachable in context.
Fold state (`h`-collapsed subtrees) is saved with the board state and restored
on the next launch.

Rows and markers:

```
○ open        ● in_progress   ⊘ blocked   ✓ closed   ◷ deferred   📌 hold
⇣N depends on N   ⇡N has N dependents
```

When stdin is not a TTY, `beads-tui` degrades to a one-shot JSON dump of the
open-status board, so scripts and agents get content instead of a pager.

`r` reloads the board in place, keeping the current view, sort, and search.
`R` clears the search and restores the open status with created-newest-first
sorting. A failed or slow reload never discards the board that is already on
screen: beads-tui keeps the last good rows interactive, shows a status-line
notice, and retries automatically with backoff (2 s, 5 s, then every 15 s)
using an extended 60 s deadline once a retry is in flight. Details are cached
per bead (keyed by its last update) and prefetched around the selection, so
moving through the list renders instantly and never blocks on `bd`; a dim
"refreshing…" marker in the detail title shows when a background refresh is
running.

## Comments

Select an issue and press `c` to open its comment thread. Press `a` to enter a
comment, then `Enter` to submit or `Esc` to cancel. `C` from the board opens
the thread with the input focused. Threads load through `bd comments ID
--json`, and submissions use `bd comment ID --stdin`; `j`/`k` scroll and
`Esc`/`q` return to the board.

## Data safety

beads-tui never creates, edits, or closes beads. The only write is an explicit
comment submission after the user presses `Enter`. Board-load failures keep
the loaded rows on screen, surface bd's diagnostic, and retry with backoff
instead of freezing or blanking.
Dependency metadata is best effort: a failed graph lookup is logged
without hiding the loaded list rows. Missing `bd`, a store it cannot reach, or
an empty board all render as explicit states rather than crashes or raw command
output.

## Logs

The TUI owns the screen, so anything it writes to stderr is painted over or
lost outright when the host window closes - a crash in a Herdr `prefix+h`
popup would otherwise leave no trace. Every interactive run therefore appends a
timestamped trail to disk:

```sh
beads-tui log-path          # print the path
tail -f "$(beads-tui log-path)"
```

The default is `$XDG_STATE_HOME/beads-tui/beads-tui.log`, falling back to
`~/.local/state/beads-tui/beads-tui.log` when `XDG_STATE_HOME` is unset.
`BEADS_TUI_LOG_DIR` overrides the directory (the verification gate uses it to
stay hermetic). Session state lives separately under the config dir; only the
diagnostic trail is state-dir material.

It records the start and exit of each run, panics with their stack, board and
detail load failures with bd's diagnostic, and state read/write errors. It is a
diagnostic trail only - bead titles, descriptions, and other issue content are
never written to it. The file rotates to `beads-tui.log.old` once it passes
1 MiB, so it stays bounded at roughly 2 MiB while the previous session's crash
context survives one restart. Logging failures are never fatal: the TUI reports
them on stderr once and runs unlogged.

## Planned mapping

Mapped to `prefix+H` in the operator's Herdr config, mirroring the
`prefix+u` -> `quota-axi --tui` pattern.

## License

MIT
