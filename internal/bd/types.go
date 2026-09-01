// Package bd wraps the `bd` CLI (Beads) with typed, read-only access to the
// bead graph.
//
// beads-tui is deliberately read-only: it only ever invokes read-only bd
// commands (list, show, dep list, statuses) and never mutates the graph. The
// graph lives wherever the ambient `bd` configuration resolves it (BEADS_DIR,
// an active worktree, or a created workspace); beads-tui never hardcodes a
// store path.
package bd

import "strings"

// View selects which native status a board renders.
type View string

const (
	ViewOpen       View = "open"
	ViewInProgress View = "in_progress"
	ViewBlocked    View = "blocked"
	ViewClosed     View = "closed"
	ViewDeferred   View = "deferred"
)

var defaultViews = [...]View{ViewOpen, ViewInProgress, ViewBlocked, ViewClosed, ViewDeferred}

// DefaultViews returns the built-in status tabs in their stable order.
func DefaultViews() []View {
	return append([]View(nil), defaultViews[:]...)
}

// ViewsFromStatuses returns the built-in tabs followed by configured statuses.
func ViewsFromStatuses(statuses []StatusInfo) []View {
	views := DefaultViews()
	seen := make(map[View]struct{}, len(views)+len(statuses))
	for _, view := range views {
		seen[view] = struct{}{}
	}
	for _, status := range statuses {
		view := View(strings.ToLower(strings.TrimSpace(status.Name)))
		if view == "" {
			continue
		}
		if _, ok := seen[view]; ok {
			continue
		}
		seen[view] = struct{}{}
		views = append(views, view)
	}
	return views
}

// Valid reports whether v names a native or configured status.
func (v View) Valid() bool {
	return strings.TrimSpace(string(v)) != ""
}

// Label is the status name shown for the view.
func (v View) Label() string {
	return string(v)
}

// TabLabel is the concise, user-facing label used in the board's status tabs.
func (v View) TabLabel() string {
	return v.Label()
}

// Issue is one bead in the graph. Which fields are populated depends on the
// command that produced the record. `bd list ... --json` fills the board row
// fields; `bd show ID --json` additionally fills description and notes.
type Issue struct {
	ID              string   `json:"id"`
	Title           string   `json:"title"`
	Description     string   `json:"description"`
	Notes           string   `json:"notes"`
	Status          string   `json:"status"`
	Priority        int      `json:"priority"`
	IssueType       string   `json:"issue_type"`
	ParentID        string   `json:"parent_id"`
	Assignee        string   `json:"assignee"`
	Owner           string   `json:"owner"`
	URL             string   `json:"url"`
	Labels          []string `json:"labels"`
	DeferUntil      string   `json:"defer_until"`
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
