package tui

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RooseveltAdvisors/beads-tui/internal/bd"
)

type concurrentDepsClient struct {
	*fakeClient
	mu     sync.Mutex
	active int
	max    int
}

type failingGraphClient struct {
	*fakeClient
	failID  string
	failErr error
}

func (f *failingGraphClient) Deps(ctx context.Context, id string, up bool) ([]bd.DepRecord, error) {
	if up && id == f.failID {
		return nil, f.failErr
	}
	return f.fakeClient.Deps(ctx, id, up)
}

func (f *concurrentDepsClient) Deps(ctx context.Context, id string, up bool) ([]bd.DepRecord, error) {
	f.mu.Lock()
	f.active++
	if f.active > f.max {
		f.max = f.active
	}
	f.mu.Unlock()
	time.Sleep(10 * time.Millisecond)
	f.mu.Lock()
	f.active--
	f.mu.Unlock()
	return f.fakeClient.Deps(ctx, id, up)
}

func TestReadyLeverageSortPutsLargestUnblockFirst(t *testing.T) {
	issues := []bd.Issue{
		{ID: "new", CreatedAt: "2026-09-03T00:00:00Z", DependentCount: 1},
		{ID: "root", CreatedAt: "2026-09-01T00:00:00Z", DependentCount: 7},
		{ID: "tie", CreatedAt: "2026-09-02T00:00:00Z", DependentCount: 1},
	}
	got := SortIssues(issues, SortLeverage)
	if got[0].ID != "root" || got[1].ID != "new" || got[2].ID != "tie" {
		t.Fatalf("leverage order = %v, want root/new/tie", []string{got[0].ID, got[1].ID, got[2].ID})
	}
}

func TestBoardLeverageUsesReverseDependencyGraph(t *testing.T) {
	issues := []bd.Issue{
		{ID: "one", CreatedAt: "2026-09-03T00:00:00Z"},
		{ID: "seven", CreatedAt: "2026-09-01T00:00:00Z"},
	}
	dependents := make([]bd.DepRecord, 7)
	for i := range dependents {
		dependents[i].ID = "dependent-" + itoa(i)
	}
	f := &fakeClient{
		issues: map[bd.View][]bd.Issue{bd.ViewReady: issues},
		upByID: map[string][]bd.DepRecord{
			"one":   {{ID: "dependent-one"}},
			"seven": dependents,
		},
	}
	m := newTestModel(f)
	m.sortMode = SortLeverage
	msg := m.loadBoardCmd()()
	m = applyMsg(t, m, msg)
	if got := m.rows[0].ID; got != "seven" {
		t.Fatalf("leverage order = %q, want seven", got)
	}
	if m.rows[0].DependentCount != 7 {
		t.Fatalf("reverse dependency count = %d, want 7", m.rows[0].DependentCount)
	}
}

func TestBoardGraphLoadsWithBoundedConcurrency(t *testing.T) {
	issues := make([]bd.Issue, boardGraphWorkers+2)
	for i := range issues {
		issues[i].ID = "issue-" + itoa(i)
	}
	f := &concurrentDepsClient{fakeClient: &fakeClient{
		issues: map[bd.View][]bd.Issue{bd.ViewAll: issues},
	}}
	m := newTestModel(f.fakeClient)
	m.backend = f
	m.view = bd.ViewAll
	msg := m.loadBoardCmd()()
	if got := msg.(boardMsg).err; got != nil {
		t.Fatalf("load board: %v", got)
	}
	if f.max <= 1 || f.max > boardGraphWorkers {
		t.Fatalf("peak dependency calls = %d, want 2..%d", f.max, boardGraphWorkers)
	}
}

func TestBoardGraphFailureKeepsListRows(t *testing.T) {
	issues := []bd.Issue{
		{ID: "ready", Title: "Ready row", Status: "open", DependentCount: 3},
		{ID: "graph-timeout", Title: "Graph timeout row", Status: "open", DependencyCount: 1, DependentCount: 4},
	}
	f := &failingGraphClient{
		fakeClient: &fakeClient{
			issues: map[bd.View][]bd.Issue{bd.ViewReady: issues},
			down:   []bd.DepRecord{{ID: "blocker", Title: "Blocker", Status: "open"}},
		},
		failID:    "graph-timeout",
		failErr:   errors.New("dependency timeout"),
	}
	m := newTestModel(f.fakeClient)
	m.backend = f
	msg := m.loadBoardCmd()()
	board, ok := msg.(boardMsg)
	if !ok {
		t.Fatalf("load board returned %T, want boardMsg", msg)
	}
	if board.err != nil {
		t.Fatalf("graph enrichment error hid board rows: %v", board.err)
	}
	if len(board.issues) != len(issues) || board.issues[1].DependentCount != issues[1].DependentCount {
		t.Fatalf("board issues = %+v, want list rows and preserved fallback count", board.issues)
	}
	if len(board.deps["graph-timeout"]) != 1 || board.deps["graph-timeout"][0].ID != "blocker" {
		t.Fatalf("successful dependency enrichment was discarded: %+v", board.deps)
	}
	m = applyMsg(t, m, board)
	if len(m.rows) != len(issues) {
		t.Fatalf("visible rows = %d, want %d", len(m.rows), len(issues))
	}
}

func TestDependencyChipsNameBothDirections(t *testing.T) {
	row := stripANSI(NewVocab(nil).ListRow(bd.Issue{ID: "root", Status: "open", DependencyCount: 2, DependentCount: 5}, 80, false))
	for _, want := range []string{"⇣2 blocked-by", "⇡5 blocks"} {
		if !strings.Contains(row, want) {
			t.Errorf("row missing dependency chip %q: %q", want, row)
		}
	}
}

func TestGraphLinesShowsTwoHopsAndCycleCallout(t *testing.T) {
	issues := []bd.Issue{
		{ID: "root", Title: "Root", Status: "open"},
		{ID: "middle", Title: "Middle", Status: "blocked"},
		{ID: "leaf", Title: "Leaf", Status: "open"},
	}
	deps := map[string][]bd.DepRecord{
		"middle": {{ID: "root", Title: "Root", Status: "open"}},
		"leaf":   {{ID: "middle", Title: "Middle", Status: "blocked"}},
	}
	lines, hasCycle := graphLines(issues, issues, deps, "leaf", NewVocab(nil))
	plain := stripANSI(strings.Join(lines, "\n"))
	for _, want := range []string{"leaf · Leaf", "blocked-by: middle · Middle", "blocked-by: root · Root"} {
		if !strings.Contains(plain, want) {
			t.Errorf("graph missing %q: %q", want, plain)
		}
	}
	if hasCycle {
		t.Fatalf("acyclic dependency chain reported a cycle: %q", plain)
	}
	_, hasCycle = graphLines(issues, issues, map[string][]bd.DepRecord{
		"middle": {{ID: "root", Title: "Root"}},
		"root":   {{ID: "middle", Title: "Middle"}},
	}, "root", NewVocab(nil))
	if !hasCycle {
		t.Fatal("graph did not report dependency cycle")
	}
}

func TestGraphCycleCalloutRemainsVisibleInShortModal(t *testing.T) {
	m := New(nil)
	m.width, m.height = 50, 4
	m.rows = []bd.Issue{{ID: "root", Title: "Root"}, {ID: "middle", Title: "Middle"}}
	m.allRows = m.rows
	m.deps = map[string][]bd.DepRecord{
		"root":   {{ID: "middle"}},
		"middle": {{ID: "root"}},
	}
	m.selected = 0
	view := stripANSI(m.renderGraph())
	if !strings.Contains(strings.Split(view, "\n")[0], "⚠ CYCLE") {
		t.Fatalf("graph modal header missing cycle callout: %q", view)
	}
}

func TestGraphShortcutOpensForDependencyNeighborhood(t *testing.T) {
	m := New(nil)
	m.rows = []bd.Issue{{ID: "root", Status: "open"}, {ID: "leaf", Status: "open"}}
	m.allRows = m.rows
	m.deps = map[string][]bd.DepRecord{"leaf": {{ID: "root"}}}
	m.selected = 1
	m = sendKey(t, m, "G")
	if !m.graph {
		t.Fatal("G did not open graph view")
	}
	m = sendKey(t, m, "esc")
	if m.graph {
		t.Fatal("esc did not close graph view")
	}
}
