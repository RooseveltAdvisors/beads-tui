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

// A bead with both a real parent and dependency-edge parents (blocked-by,
// blocks) nests under its real parent only: incidental dep edges must not
// rewrite the hierarchy.
func TestParentEdgeWinsOverDependencyEdge(t *testing.T) {
	issues := []bd.Issue{
		{ID: "root", Title: "Root", Status: "open"},
		{ID: "blocker", Title: "Blocker", Status: "open"},
		{ID: "child", Title: "Child", Status: "open", ParentID: "root"},
	}
	deps := map[string][]bd.DepRecord{
		"child": {{ID: "blocker", Title: "Blocker", Status: "open", DependencyType: "blocks"}},
	}
	roots := BuildDependencyTree(issues, deps)
	rows := FlattenDependencyTree(roots, map[string]bool{})
	depths := map[string]int{}
	for _, row := range rows {
		depths[row.Issue.ID] = row.Depth
	}
	if depths["root"] != 1 || depths["child"] != 2 {
		t.Fatalf("parent chain broken: root=%d child=%d (rows=%+v)", depths["root"], depths["child"], rows)
	}
	if depths["blocker"] != 1 {
		t.Fatalf("blocker nested under child via dep edge: depth=%d, want root-level 1", depths["blocker"])
	}
}

// Beads hidden below the five-level depth cap stay navigable in the flat list.
func TestDeepChainReachableThroughFlatView(t *testing.T) {
	issues := make([]bd.Issue, 0, 8)
	var parent string
	for level := 1; level <= 8; level++ {
		id := fmt.Sprintf("bd-%d", level)
		issues = append(issues, bd.Issue{ID: id, Title: "Level", Status: "open", ParentID: parent})
		parent = id
	}
	f := &fakeClient{issues: map[bd.View][]bd.Issue{bd.ViewOpen: issues}}
	m := drive(t, f)
	if len(m.rows) != maxTreeDepth {
		t.Fatalf("tree rows = %d, want the depth cap %d", len(m.rows), maxTreeDepth)
	}
	m = sendKey(t, m, "v")
	if len(m.rows) != 8 {
		t.Fatalf("flat rows = %d, want all 8 levels", len(m.rows))
	}
	if !strings.Contains(stripANSI(m.View()), "bd-8") {
		t.Fatal("flat view must render the deepest bead")
	}
}

// A parent that moved to another status view (say, a closed epic) must still
// anchor its open children: the tree pulls the ancestor chain in from the
// graph snapshot instead of orphaning every child whose parent moved on.
func TestTreePullsAncestorsFromOutsideTheView(t *testing.T) {
	f := &fakeClient{issues: map[bd.View][]bd.Issue{
		bd.ViewOpen:   {{ID: "child", Title: "Open child", Status: "open", ParentID: "epic"}},
		bd.ViewClosed: {{ID: "epic", Title: "Closed epic", Status: "closed"}},
	}}
	m := drive(t, f)
	if !m.treeMode {
		t.Fatal("board should render the hierarchy tree by default")
	}
	ids := make([]string, 0, len(m.treeRows))
	for _, row := range m.treeRows {
		ids = append(ids, row.Issue.ID)
	}
	want := []string{"epic", "child"}
	if len(ids) != len(want) || ids[0] != want[0] || ids[1] != want[1] {
		t.Fatalf("tree rows = %v, want the closed epic anchoring its open child", ids)
	}
	if m.treeRows[1].Depth != 2 {
		t.Fatalf("child depth = %d, want nested at 2", m.treeRows[1].Depth)
	}
}
