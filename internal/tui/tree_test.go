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

func TestBuildDependencyTreeSortsUnrelatedRoots(t *testing.T) {
	issues := []bd.Issue{
		{ID: "later", Priority: 3},
		{ID: "urgent", Priority: 0},
	}

	if got := treeIDs(BuildDependencyTree(issues, nil)); strings.Join(got, ",") != "urgent,later" {
		t.Fatalf("roots = %v, want priority order", got)
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

func TestFlattenDependencyTreeEmitsSharedNodesOnce(t *testing.T) {
	shared := &TreeNode{Issue: bd.Issue{ID: "shared"}}
	a := &TreeNode{Issue: bd.Issue{ID: "a"}, Children: []*TreeNode{shared}}
	unique := &TreeNode{Issue: bd.Issue{ID: "unique"}}
	b := &TreeNode{Issue: bd.Issue{ID: "b"}, Children: []*TreeNode{unique, shared}}

	rows := FlattenDependencyTree([]*TreeNode{a, b}, nil)
	got := make([]string, len(rows))
	for i, row := range rows {
		got[i] = row.Issue.ID
	}
	if strings.Join(got, ",") != "a,shared,b,unique" {
		t.Fatalf("rows = %v, want shared node only at first location", got)
	}
	if !rows[2].HasChildren || rows[3].Prefix != "└── " {
		t.Fatalf("filtered child metadata = %+v, want one final visible child", rows[2:])
	}

	rows = FlattenDependencyTree([]*TreeNode{a, {Issue: bd.Issue{ID: "empty"}, Children: []*TreeNode{shared}}}, nil)
	if rows[2].HasChildren {
		t.Fatalf("duplicate-only parent = %+v, want no visible children", rows[2])
	}
}

func TestFlattenDependencyTreeUsesActualVisibleSiblings(t *testing.T) {
	shared := &TreeNode{Issue: bd.Issue{ID: "shared"}}
	child := &TreeNode{Issue: bd.Issue{ID: "child"}, Children: []*TreeNode{shared}}
	root := &TreeNode{Issue: bd.Issue{ID: "root"}, Children: []*TreeNode{child, shared}}

	rows := FlattenDependencyTree([]*TreeNode{root}, nil)
	if len(rows) != 3 || rows[1].Prefix != "└── " || rows[2].Prefix != "    └── " {
		t.Fatalf("rows = %+v, want connectors for one actual child branch", rows)
	}
}

func treeIDs(nodes []*TreeNode) []string {
	ids := make([]string, len(nodes))
	for i, node := range nodes {
		ids[i] = node.Issue.ID
	}
	return ids
}
