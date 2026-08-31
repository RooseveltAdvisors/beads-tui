package tui

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/RooseveltAdvisors/beads-tui/internal/bd"
)

// SortMode is the ordering applied to the active board view.
type SortMode int

const (
	SortPriority SortMode = iota
	SortCreated
	SortUpdated
	SortAlphabetical
	SortDependencies
)

var sortModes = [...]SortMode{SortPriority, SortCreated, SortUpdated, SortAlphabetical, SortDependencies}

func (s SortMode) String() string {
	switch s {
	case SortCreated:
		return "created"
	case SortUpdated:
		return "updated"
	case SortAlphabetical:
		return "alphabetical"
	case SortDependencies:
		return "dependencies"
	default:
		return "priority"
	}
}

func (s SortMode) Next() SortMode {
	for i, mode := range sortModes {
		if mode == s {
			return sortModes[(i+1)%len(sortModes)]
		}
	}
	return SortPriority
}

// Filter is the AND-combined board filter. Text terms are each required.
type Filter struct {
	Status   string
	Priority *int
	Text     string
}

func (f Filter) Active() bool {
	return f.Status != "" || f.Priority != nil || strings.TrimSpace(f.Text) != ""
}

func (f Filter) String() string {
	var parts []string
	if f.Status != "" {
		parts = append(parts, "status:"+f.Status)
	}
	if f.Priority != nil {
		parts = append(parts, "priority:P"+strconv.Itoa(*f.Priority))
	}
	if strings.TrimSpace(f.Text) != "" {
		parts = append(parts, strings.TrimSpace(f.Text))
	}
	return strings.Join(parts, " ")
}

// ParseFilter accepts status:open, priority:P1, and free-text terms. Unknown
// field prefixes remain searchable text, which makes the prompt forgiving.
func ParseFilter(input string) Filter {
	var f Filter
	var text []string
	for _, token := range strings.Fields(input) {
		lower := strings.ToLower(token)
		switch {
		case strings.HasPrefix(lower, "status:") && len(token) > len("status:"):
			f.Status = strings.ToLower(token[len("status:"):])
		case strings.HasPrefix(lower, "priority:") && len(token) > len("priority:"):
			value := strings.TrimPrefix(strings.ToUpper(token[len("priority:"):]), "P")
			if p, err := strconv.Atoi(value); err == nil && p >= 0 && p <= 3 {
				f.Priority = &p
			} else {
				text = append(text, token)
			}
		default:
			text = append(text, token)
		}
	}
	f.Text = strings.Join(text, " ")
	return f
}

func MatchesFilter(issue bd.Issue, f Filter) bool {
	if f.Status != "" && strings.ToLower(issue.Status) != strings.ToLower(f.Status) {
		return false
	}
	if f.Priority != nil && issue.Priority != *f.Priority {
		return false
	}
	haystack := strings.ToLower(issue.Title + "\n" + issue.Description)
	for _, term := range strings.Fields(strings.ToLower(f.Text)) {
		if !strings.Contains(haystack, term) {
			return false
		}
	}
	return true
}

func ApplyBoard(issues []bd.Issue, f Filter, mode SortMode) []bd.Issue {
	rows := make([]bd.Issue, 0, len(issues))
	for _, issue := range issues {
		if MatchesFilter(issue, f) {
			rows = append(rows, issue)
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		left, right := rows[i], rows[j]
		var before bool
		switch mode {
		case SortCreated:
			before = timestamp(left.CreatedAt).After(timestamp(right.CreatedAt))
		case SortUpdated:
			before = timestamp(left.UpdatedAt).After(timestamp(right.UpdatedAt))
		case SortAlphabetical:
			before = strings.ToLower(left.Title) < strings.ToLower(right.Title)
		case SortDependencies:
			before = left.DependencyCount > right.DependencyCount
		default:
			before = left.Priority < right.Priority
		}
		if equalOrder(left, right, mode) {
			return strings.ToLower(left.ID) < strings.ToLower(right.ID)
		}
		return before
	})
	return rows
}

func equalOrder(a, b bd.Issue, mode SortMode) bool {
	switch mode {
	case SortCreated:
		return timestamp(a.CreatedAt).Equal(timestamp(b.CreatedAt))
	case SortUpdated:
		return timestamp(a.UpdatedAt).Equal(timestamp(b.UpdatedAt))
	case SortAlphabetical:
		return strings.EqualFold(a.Title, b.Title)
	case SortDependencies:
		return a.DependencyCount == b.DependencyCount
	default:
		return a.Priority == b.Priority
	}
}

func timestamp(value string) time.Time {
	parsed, _ := time.Parse(time.RFC3339Nano, value)
	return parsed
}

type treeItem struct {
	Issue  bd.Issue
	Prefix string
	Depth  int
}

// BuildTree projects dependency edges into a stable, cycle-safe indented
// tree. An issue's dependencies are its children; disconnected issues remain
// roots. expanded controls which subtrees are visible.
func BuildTree(rows []bd.Issue, edges map[string][]bd.DepRecord, expanded map[string]bool) []treeItem {
	byID := make(map[string]bd.Issue, len(rows))
	for _, row := range rows {
		byID[row.ID] = row
	}
	children := make(map[string][]string, len(rows))
	childIDs := make(map[string]bool)
	for _, row := range rows {
		for _, dep := range edges[row.ID] {
			if _, ok := byID[dep.ID]; ok && dep.ID != row.ID {
				children[row.ID] = append(children[row.ID], dep.ID)
				childIDs[dep.ID] = true
			}
		}
	}
	var roots []string
	for _, row := range rows {
		if !childIDs[row.ID] {
			roots = append(roots, row.ID)
		}
	}
	// A dependency cycle has no natural root. Pick its first board-order node;
	// the visit guard below then keeps the projection finite.
	if len(roots) == 0 && len(rows) > 0 {
		roots = append(roots, rows[0].ID)
	}
	var out []treeItem
	seen := map[string]bool{}
	var visit func(string, string, int, bool)
	visit = func(id, prefix string, depth int, last bool) {
		if seen[id] {
			return
		}
		seen[id] = true
		branch := ""
		if depth > 0 {
			branch = prefix
			if last {
				branch += "└─ "
			} else {
				branch += "├─ "
			}
		}
		out = append(out, treeItem{Issue: byID[id], Prefix: branch, Depth: depth})
		if !expanded[id] {
			return
		}
		kids := children[id]
		for i, kid := range kids {
			next := prefix
			if depth > 0 {
				if last {
					next += "   "
				} else {
					next += "│  "
				}
			}
			visit(kid, next, depth+1, i == len(kids)-1)
		}
	}
	for i, root := range roots {
		visit(root, "", 0, i == len(roots)-1)
	}
	return out
}
