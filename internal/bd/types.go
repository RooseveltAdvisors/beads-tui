// Package bd wraps the `bd` CLI (Beads) with typed access to the bead graph
// and its comment thread.
//
// The graph lives wherever the ambient `bd` configuration resolves it
// (BEADS_DIR, an active worktree, or a created workspace); beads-tui never
// hardcodes a store path.
package bd

import (
	"encoding/json"
	"strings"
)

// View selects which native or configured status a board renders.
type View string

const (
	// ViewReady is the synthetic first tab: claimable work (open, no active
	// blockers, not deferred). It is bd's own `bd list --ready` semantics, so
	// large stores land on actionable rows instead of every open issue.
	ViewReady      View = "ready"
	ViewOpen       View = "open"
	ViewInProgress View = "in_progress"
	ViewBlocked    View = "blocked"
	ViewClosed     View = "closed"
	ViewDeferred   View = "deferred"
)

var defaultViews = [...]View{ViewReady, ViewOpen, ViewInProgress, ViewBlocked, ViewClosed, ViewDeferred}

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
// fields (including description, parent, and the issue's inline dependency
// edges); `bd show ID --json` returns the same shape for one bead.
type Issue struct {
	ID              string    `json:"id"`
	Title           string    `json:"title"`
	Description     string    `json:"description"`
	Notes           string    `json:"notes"`
	Status          string    `json:"status"`
	Priority        int       `json:"priority"`
	IssueType       string    `json:"issue_type"`
	ParentID        string    `json:"parent_id"`
	Assignee        string    `json:"assignee"`
	Owner           string    `json:"owner"`
	Repeat          string    `json:"repeat"`
	RecurrenceStart string    `json:"recurrence_start"`
	RecurrenceEnd   string    `json:"recurrence_end"`
	RecurrenceTZ    string    `json:"recurrence_tz"`
	URL             string    `json:"url"`
	Labels          []string  `json:"labels"`
	DeferUntil      string    `json:"defer_until"`
	CreatedAt       string    `json:"created_at"`
	CreatedBy       string    `json:"created_by"`
	UpdatedAt       string    `json:"updated_at"`
	DependencyCount int       `json:"dependency_count"`
	DependentCount  int       `json:"dependent_count"`
	CommentCount    int       `json:"comment_count"`
	Dependencies    []DepEdge `json:"dependencies"`
}

// DepEdge is one inline dependency record embedded in `bd list/show --json`
// output. Every edge is owned by the dependent issue (IssueID), so a single
// `bd list --all --json` call carries the whole dependency graph.
type DepEdge struct {
	IssueID     string `json:"issue_id"`
	DependsOnID string `json:"depends_on_id"`
	Type        string `json:"type"`
}

// UnmarshalJSON accepts both parent spellings: bd's JSON emits `parent`
// (string or null), while the on-disk column is parent_id. The `parent` form
// wins when both are present.
func (i *Issue) UnmarshalJSON(data []byte) error {
	type issueAlias Issue
	var alias issueAlias
	if err := json.Unmarshal(data, &alias); err != nil {
		return err
	}
	*i = Issue(alias)
	var parent struct {
		Parent *string `json:"parent"`
	}
	if err := json.Unmarshal(data, &parent); err == nil && parent.Parent != nil {
		i.ParentID = strings.TrimSpace(*parent.Parent)
	}
	return nil
}

// IsRecurring reports whether the issue carries a repeat schedule.
func (i Issue) IsRecurring() bool { return strings.TrimSpace(i.Repeat) != "" }

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

// Comment is one entry in an issue's chronological comment thread.
// CreatedBy is retained as a compatibility fallback for bd versions that use
// that field name instead of author.
type Comment struct {
	ID        string `json:"id"`
	IssueID   string `json:"issue_id"`
	Author    string `json:"author"`
	CreatedBy string `json:"created_by"`
	Text      string `json:"text"`
	CreatedAt string `json:"created_at"`
}
