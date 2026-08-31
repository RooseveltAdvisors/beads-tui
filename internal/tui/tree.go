package tui

import (
	"sort"

	"github.com/RooseveltAdvisors/beads-tui/internal/bd"
)

// TreeNode is a bead and the graph relationships shown below it. Dependency
// records are keyed by the issue that depends on the record's ID, so a
// blocker is placed above the work it blocks. Explicit parent_id edges have
// the same parent-to-child orientation.
type TreeNode struct {
	Issue    bd.Issue
	Children []*TreeNode
}

// TreeRow is one visible row in a dependency tree. Prefix contains the
// box-drawing branch and ancestor continuation lines.
type TreeRow struct {
	Issue       bd.Issue
	Prefix      string
	HasChildren bool
	Expanded    bool
}

// BuildDependencyTree builds a deterministic forest from the issues and
// dependency edges. deps is keyed by dependent issue ID and contains the
// issues it depends on (the result of bd dep list ID --json).
//
// A bead may have both a parent and a blocker. The resulting graph is a DAG in
// normal Beads data; shared children are rendered at their first reachable
// location so the terminal view remains a navigable tree. Cycles are guarded
// against and disconnected/cyclic nodes are still returned as roots.
func BuildDependencyTree(issues []bd.Issue, deps map[string][]bd.DepRecord) []*TreeNode {
	nodes := make(map[string]*TreeNode, len(issues))
	order := make([]string, 0, len(issues))
	for _, issue := range issues {
		if issue.ID == "" {
			continue
		}
		if _, exists := nodes[issue.ID]; exists {
			continue
		}
		nodes[issue.ID] = &TreeNode{Issue: issue}
		order = append(order, issue.ID)
	}

	children := make(map[string]map[string]struct{}, len(nodes))
	addEdge := func(parent, child string) {
		if parent == "" || child == "" || parent == child {
			return
		}
		if _, ok := nodes[parent]; !ok {
			return
		}
		if _, ok := nodes[child]; !ok {
			return
		}
		if children[parent] == nil {
			children[parent] = make(map[string]struct{})
		}
		children[parent][child] = struct{}{}
	}

	for _, issue := range issues {
		if issue.ID == "" {
			continue
		}
		addEdge(issue.ParentID, issue.ID)
		for _, dep := range deps[issue.ID] {
			// dep.ID is the blocker; issue.ID is the dependent.
			addEdge(dep.ID, issue.ID)
		}
	}

	lessID := func(a, b string) bool {
		ia, ib := nodes[a].Issue, nodes[b].Issue
		if ia.Priority != ib.Priority {
			return ia.Priority < ib.Priority
		}
		return a < b
	}
	for parent, set := range children {
		ids := make([]string, 0, len(set))
		for child := range set {
			ids = append(ids, child)
		}
		sort.SliceStable(ids, func(i, j int) bool { return lessID(ids[i], ids[j]) })
		for _, child := range ids {
			nodes[parent].Children = append(nodes[parent].Children, nodes[child])
		}
	}

	hasParent := make(map[string]bool, len(nodes))
	for _, set := range children {
		for child := range set {
			hasParent[child] = true
		}
	}
	roots := make([]*TreeNode, 0, len(nodes))
	for _, id := range order {
		if !hasParent[id] {
			roots = append(roots, nodes[id])
		}
	}
	// A malformed graph can have no root (for example, a dependency cycle).
	// Add its nodes in stable order; flattening still prevents recursion loops.
	seen := make(map[string]bool, len(nodes))
	var mark func(*TreeNode, map[string]bool)
	mark = func(node *TreeNode, path map[string]bool) {
		if node == nil || path[node.Issue.ID] || seen[node.Issue.ID] {
			return
		}
		seen[node.Issue.ID] = true
		next := make(map[string]bool, len(path)+1)
		for id := range path {
			next[id] = true
		}
		next[node.Issue.ID] = true
		for _, child := range node.Children {
			mark(child, next)
		}
	}
	for _, root := range roots {
		mark(root, nil)
	}
	for _, id := range order {
		if !seen[id] {
			roots = append(roots, nodes[id])
			mark(nodes[id], nil)
		}
	}
	sort.SliceStable(roots, func(i, j int) bool {
		return lessID(roots[i].Issue.ID, roots[j].Issue.ID)
	})
	return roots
}

// FlattenDependencyTree returns the rows visible with the given expansion
// state. Missing expansion entries default to expanded, which makes newly
// loaded branches immediately visible.
func FlattenDependencyTree(roots []*TreeNode, expanded map[string]bool) []TreeRow {
	type visibleNode struct {
		node        *TreeNode
		children    []*visibleNode
		hasChildren bool
		expanded    bool
	}

	emitted := make(map[string]bool)
	var project func(*TreeNode, map[string]bool) *visibleNode
	project = func(node *TreeNode, path map[string]bool) *visibleNode {
		if node == nil || path[node.Issue.ID] || emitted[node.Issue.ID] {
			return nil
		}
		emitted[node.Issue.ID] = true
		isExpanded := true
		if value, ok := expanded[node.Issue.ID]; ok {
			isExpanded = value
		}
		visibleChildren := make([]*TreeNode, 0, len(node.Children))
		for _, child := range node.Children {
			if child != nil && !path[child.Issue.ID] && !emitted[child.Issue.ID] {
				visibleChildren = append(visibleChildren, child)
			}
		}
		visible := &visibleNode{node: node, hasChildren: len(visibleChildren) > 0, expanded: isExpanded}
		if !isExpanded {
			return visible
		}
		nextPath := make(map[string]bool, len(path)+1)
		for id := range path {
			nextPath[id] = true
		}
		nextPath[node.Issue.ID] = true
		for _, child := range visibleChildren {
			if projected := project(child, nextPath); projected != nil {
				visible.children = append(visible.children, projected)
			}
		}
		return visible
	}
	visibleRoots := make([]*visibleNode, 0, len(roots))
	for _, root := range roots {
		if projected := project(root, nil); projected != nil {
			visibleRoots = append(visibleRoots, projected)
		}
	}

	rows := make([]TreeRow, 0)
	var walk func(*visibleNode, string, []bool, bool)
	walk = func(visible *visibleNode, ancestorPrefix string, ancestorLast []bool, last bool) {
		prefix := ancestorPrefix
		if len(ancestorLast) > 0 {
			if last {
				prefix += "└── "
			} else {
				prefix += "├── "
			}
		}
		rows = append(rows, TreeRow{
			Issue:       visible.node.Issue,
			Prefix:      prefix,
			HasChildren: visible.hasChildren,
			Expanded:    visible.expanded,
		})
		for i, child := range visible.children {
			childPrefix := ancestorPrefix
			if len(ancestorLast) > 0 {
				if last {
					childPrefix += "    "
				} else {
					childPrefix += "│   "
				}
			}
			walk(child, childPrefix, append(ancestorLast, last), i == len(visible.children)-1)
		}
	}
	for i, root := range visibleRoots {
		walk(root, "", nil, i == len(visibleRoots)-1)
	}
	return rows
}
