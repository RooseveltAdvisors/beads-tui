package tui

import (
	"fmt"
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

func TestBuildDependencyTreeUsesActiveSortWithinHierarchy(t *testing.T) {
	issues := []bd.Issue{
		{ID: "zulu", Title: "Zulu", Priority: 0},
		{ID: "alpha", Title: "Alpha", Priority: 2},
		{ID: "root", Title: "Root", Priority: 1},
		{ID: "child-z", Title: "Zulu child", Priority: 0, ParentID: "root"},
		{ID: "child-a", Title: "Alpha child", Priority: 2, ParentID: "root"},
	}
	roots := BuildDependencyTree(issues, nil, SortAlphabetical)
	if got := strings.Join(treeIDs(roots), ","); got != "alpha,root,zulu" {
		t.Fatalf("alphabetical roots = %s", got)
	}
	if got := strings.Join(treeIDs(roots[1].Children), ","); got != "child-a,child-z" {
		t.Fatalf("alphabetical children = %s", got)
	}
}

func TestBuildDependencyTreeUsesDependsSortWithinHierarchy(t *testing.T) {
	issues := []bd.Issue{
		{ID: "root-low", CreatedAt: "2026-08-03T00:00:00Z", DependentCount: 1},
		{ID: "root-high", CreatedAt: "2026-08-01T00:00:00Z", DependentCount: 4},
		{ID: "parent", CreatedAt: "2026-08-02T00:00:00Z", DependentCount: 2},
		{ID: "child-old", ParentID: "parent", CreatedAt: "2026-08-01T00:00:00Z", DependentCount: 3},
		{ID: "child-new", ParentID: "parent", CreatedAt: "2026-08-03T00:00:00Z", DependentCount: 3},
	}
	roots := BuildDependencyTree(issues, nil, SortDependents)
	if got := strings.Join(treeIDs(roots), ","); got != "root-high,parent,root-low" {
		t.Fatalf("depends roots = %s", got)
	}
	if got := strings.Join(treeIDs(roots[1].Children), ","); got != "child-new,child-old" {
		t.Fatalf("depends children = %s", got)
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

func TestFlattenDependencyTreeCapsDepthWithDeeperMarker(t *testing.T) {
	issues := make([]bd.Issue, 8)
	for i := range issues {
		issues[i] = bd.Issue{ID: fmt.Sprintf("lvl%d", i), Title: "Level", Status: "open"}
		if i > 0 {
			issues[i].ParentID = fmt.Sprintf("lvl%d", i-1)
		}
	}
	roots := BuildDependencyTree(issues, nil)
	rows := FlattenDependencyTree(roots, map[string]bool{})
	if len(rows) != maxTreeDepth {
		t.Fatalf("rows = %d, want depth cap %d", len(rows), maxTreeDepth)
	}
	last := rows[len(rows)-1]
	if last.Issue.ID != "lvl4" {
		t.Fatalf("last row = %s, want lvl4", last.Issue.ID)
	}
	if last.Deeper != 3 {
		t.Errorf("deeper = %d, want 3 hidden descendants", last.Deeper)
	}
	if last.HasChildren || !last.Expanded {
		t.Errorf("capped row should not advertise folding: %+v", last)
	}
	rendered := stripANSI(NewVocab(nil).TreeRow(last, 80, false))
	if !strings.Contains(rendered, "+3 deeper") {
		t.Errorf("rendered row missing depth marker: %s", rendered)
	}
	if strings.Contains(rendered, "lvl5") {
		t.Errorf("capped row leaked deeper level: %s", rendered)
	}
}

func TestFlattenDependencyTreeCapRespectsSharedNodes(t *testing.T) {
	issues := []bd.Issue{
		{ID: "root"},
		{ID: "c1", ParentID: "root"},
		{ID: "c2", ParentID: "c1"},
		{ID: "c3", ParentID: "c2"},
		{ID: "c4", ParentID: "c3"},
		{ID: "c5", ParentID: "c4"},
	}
	rows := FlattenDependencyTree(BuildDependencyTree(issues, nil), map[string]bool{"c1": false})
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want folded root and child", len(rows))
	}
	if rows[1].Deeper != 0 {
		t.Errorf("folded node should not carry a deeper count: %+v", rows[1])
	}
	rows = FlattenDependencyTree(BuildDependencyTree(issues, nil), map[string]bool{})
	if rows[len(rows)-1].Deeper != 1 {
		t.Errorf("deeper = %d, want 1 (c5 under cap)", rows[len(rows)-1].Deeper)
	}
}
