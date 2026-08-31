package tui

import (
	"strings"
	"testing"

	"github.com/RooseveltAdvisors/beads-tui/internal/bd"
)

func TestBuildDependencyTreeSortsSiblingGroups(t *testing.T) {
	issues := []bd.Issue{
		{ID: "root", Title: "Root", Priority: 2},
		{ID: "child-high", Title: "High", Priority: 2, ParentID: "root"},
		{ID: "child-low", Title: "Low", Priority: 0, ParentID: "root"},
		{ID: "blocked", Title: "Blocked", Priority: 1},
	}
	deps := map[string][]bd.DepRecord{
		"blocked": {{ID: "root", DependencyType: "blocks"}},
	}

	roots := BuildDependencyTree(issues, deps)
	if len(roots) != 1 || roots[0].Issue.ID != "root" {
		t.Fatalf("roots = %v, want root", treeIDs(roots))
	}
	if got := treeIDs(roots[0].Children); strings.Join(got, ",") != "child-low,blocked,child-high" {
		t.Fatalf("children = %v, want priority order", got)
	}
}

func TestFlattenDependencyTreeDrawsEdgesAndCollapses(t *testing.T) {
	issues := []bd.Issue{
		{ID: "root", Title: "Root"},
		{ID: "middle", Title: "Middle", ParentID: "root"},
		{ID: "leaf", Title: "Leaf", ParentID: "middle"},
	}
	roots := BuildDependencyTree(issues, nil)
	rows := FlattenDependencyTree(roots, map[string]bool{})
	if len(rows) != 3 {
		t.Fatalf("expanded rows = %d, want 3", len(rows))
	}
	if rows[1].Prefix != "└── " || rows[2].Prefix != "    └── " {
		t.Errorf("prefixes = %q, %q; want nested box-drawing edges", rows[1].Prefix, rows[2].Prefix)
	}
	rows = FlattenDependencyTree(roots, map[string]bool{"middle": false})
	if len(rows) != 2 || rows[1].Issue.ID != "middle" || rows[1].Expanded {
		t.Fatalf("collapsed rows = %+v, want root and collapsed middle", rows)
	}
}

func TestFlattenDependencyTreeGuardsCycles(t *testing.T) {
	a := &TreeNode{Issue: bd.Issue{ID: "a"}}
	b := &TreeNode{Issue: bd.Issue{ID: "b"}}
	a.Children = []*TreeNode{b}
	b.Children = []*TreeNode{a}
	rows := FlattenDependencyTree([]*TreeNode{a}, nil)
	if len(rows) != 2 || rows[0].Issue.ID != "a" || rows[1].Issue.ID != "b" {
		t.Fatalf("cycle rows = %+v, want each node once along the path", rows)
	}
}

func treeIDs(nodes []*TreeNode) []string {
	ids := make([]string, len(nodes))
	for i, node := range nodes {
		ids[i] = node.Issue.ID
	}
	return ids
}
