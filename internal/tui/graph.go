package tui

import (
	"fmt"
	"sort"

	"github.com/RooseveltAdvisors/beads-tui/internal/bd"
)

type graphEdge struct {
	to   string
	kind string
}

// graphLines renders a compact, deterministic two-hop neighborhood for the
// focused bead. Edges retain their direction so the graph is useful even when
// the dependency tree is collapsed.
func graphLines(rows, all []bd.Issue, deps map[string][]bd.DepRecord, focus string, vocab Vocab, reverse ...map[string][]bd.DepRecord) ([]string, bool) {
	issues := make(map[string]bd.Issue, len(all)+len(rows))
	for _, issue := range all {
		issues[issue.ID] = issue
	}
	for _, issue := range rows {
		issues[issue.ID] = issue
	}
	if focus == "" {
		return []string{"No focused bead."}, false
	}
	adj := make(map[string][]graphEdge)
	directed := make(map[string][]string)
	addAdj := func(from, to, kind string) {
		if from == "" || to == "" || from == to {
			return
		}
		for _, edge := range adj[from] {
			if edge.to == to && edge.kind == kind {
				return
			}
		}
		adj[from] = append(adj[from], graphEdge{to: to, kind: kind})
	}
	addDirected := func(from, to string) {
		if from == "" || to == "" || from == to {
			return
		}
		for _, next := range directed[from] {
			if next == to {
				return
			}
		}
		directed[from] = append(directed[from], to)
	}
	for _, issue := range all {
		addAdj(issue.ID, issue.ParentID, "child-of")
		if issue.ParentID != "" {
			addAdj(issue.ParentID, issue.ID, "parent-of")
			addDirected(issue.ID, issue.ParentID)
		}
		for _, dep := range deps[issue.ID] {
			if _, exists := issues[dep.ID]; !exists {
				issues[dep.ID] = bd.Issue{ID: dep.ID, Title: dep.Title, Status: dep.Status, Priority: dep.Priority}
			}
			addAdj(issue.ID, dep.ID, "blocked-by")
			addAdj(dep.ID, issue.ID, "blocks")
			addDirected(issue.ID, dep.ID)
		}
		if len(reverse) > 0 {
			for _, dependent := range reverse[0][issue.ID] {
				if _, exists := issues[dependent.ID]; !exists {
					issues[dependent.ID] = bd.Issue{ID: dependent.ID, Title: dependent.Title, Status: dependent.Status, Priority: dependent.Priority}
				}
				addAdj(issue.ID, dependent.ID, "blocks")
				addDirected(dependent.ID, issue.ID)
			}
		}
	}
	for id := range adj {
		sort.SliceStable(adj[id], func(i, j int) bool {
			if adj[id][i].kind != adj[id][j].kind {
				return adj[id][i].kind < adj[id][j].kind
			}
			return adj[id][i].to < adj[id][j].to
		})
	}
	distance := map[string]int{focus: 0}
	queue := []string{focus}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if distance[id] >= 2 {
			continue
		}
		for _, edge := range adj[id] {
			if _, seen := distance[edge.to]; !seen {
				distance[edge.to] = distance[id] + 1
				queue = append(queue, edge.to)
			}
		}
	}
	label := func(id string) string {
		issue := issues[id]
		name := issue.Title
		if name == "" {
			name = "(untitled)"
		}
		return vocab.statusStyle(issue.Status).Render(id + " · " + name)
	}
	lines := []string{"Focused: " + label(focus), "", "2-hop dependency neighborhood"}
	for _, edge := range adj[focus] {
		if distance[edge.to] > 1 {
			continue
		}
		lines = append(lines, fmt.Sprintf("├─ %s: %s", edge.kind, label(edge.to)))
		for _, next := range adj[edge.to] {
			if distance[next.to] == 2 {
				lines = append(lines, fmt.Sprintf("│  └─ %s: %s", next.kind, label(next.to)))
			}
		}
	}
	if len(lines) == 3 {
		lines = append(lines, styleDim.Render("No dependency edges."))
	}
	return lines, hasDirectedCycle(directed, distance)
}

func hasDirectedCycle(graph map[string][]string, included map[string]int) bool {
	state := make(map[string]uint8)
	var visit func(string) bool
	visit = func(id string) bool {
		depth, ok := included[id]
		if !ok || depth > 2 {
			return false
		}
		if state[id] == 1 {
			return true
		}
		if state[id] == 2 {
			return false
		}
		state[id] = 1
		for _, next := range graph[id] {
			if nextDepth, ok := included[next]; ok && nextDepth <= 2 && visit(next) {
				return true
			}
		}
		state[id] = 2
		return false
	}
	for id := range included {
		if visit(id) {
			return true
		}
	}
	return false
}
