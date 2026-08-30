// Package bd wraps the `bd` CLI (Beads) with typed, read-only access to the
// bead graph.
//
// beads-tui is deliberately read-only: it only ever invokes read-only bd
// commands (list, show, dep list, statuses) and never mutates the graph. The
// graph lives wherever the ambient `bd` configuration resolves it (BEADS_DIR,
// an active worktree, or a created workspace); beads-tui never hardcodes a
// store path.
package bd

// View selects which slice of the bead graph a board renders.
type View string

const (
	// ViewReady lists work that is claimable right now (no active blockers).
	ViewReady View = "ready"
	// ViewOpen lists open issues regardless of blockers.
	ViewOpen View = "open"
	// ViewAll lists every issue including closed ones.
	ViewAll View = "all"
)

// AllViews is the ordered set of board views, in tab order.
var AllViews = [...]View{ViewReady, ViewOpen, ViewAll}

// Valid reports whether v is a supported board view.
func (v View) Valid() bool {
	switch v {
	case ViewReady, ViewOpen, ViewAll:
		return true
	default:
		return false
	}
}

// Label is the human-readable title shown for the view.
func (v View) Label() string {
	switch v {
	case ViewReady:
		return "Ready"
	case ViewOpen:
		return "Open"
	case ViewAll:
		return "All"
	default:
		return string(v)
	}
}

// Issue is one bead in the graph. Which fields are populated depends on the
// command that produced the record:
//
//   - `bd list ... --json` fills id, title, status, priority, issue_type,
//     assignee/owner, timestamps and the three counters. The ready view also
//     carries labels; the all view carries the description.
//   - `bd show ID --json` additionally fills description and notes.
type Issue struct {
	ID              string   `json:"id"`
	Title           string   `json:"title"`
	Description     string   `json:"description"`
	Notes           string   `json:"notes"`
	Status          string   `json:"status"`
	Priority        int      `json:"priority"`
	IssueType       string   `json:"issue_type"`
	Assignee        string   `json:"assignee"`
	Owner           string   `json:"owner"`
	Labels          []string `json:"labels"`
	CreatedAt       string   `json:"created_at"`
	CreatedBy       string   `json:"created_by"`
	UpdatedAt       string   `json:"updated_at"`
	DependencyCount int      `json:"dependency_count"`
	DependentCount  int      `json:"dependent_count"`
	CommentCount    int      `json:"comment_count"`
}

// DepRecord is one edge from `bd dep list --json`. DependencyType is the
// edge kind as printed by bd's own tree view ("blocks", "tracks", ...).
type DepRecord struct {
	ID             string `json:"id"`
	Title          string `json:"title"`
	Status         string `json:"status"`
	Priority       int    `json:"priority"`
	IssueType      string `json:"issue_type"`
	DependencyType string `json:"dependency_type"`
}

// StatusInfo is one entry of the status vocabulary from
// `bd statuses --json`.
type StatusInfo struct {
	Name        string `json:"name"`
	Icon        string `json:"icon"`
	Category    string `json:"category"`
	Description string `json:"description"`
}
