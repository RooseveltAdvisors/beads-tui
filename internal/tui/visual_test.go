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
	if !up && id == f.failID {
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

func TestReadyDependsSortPutsMostBlockingFirst(t *testing.T) {
	issues := []bd.Issue{
		{ID: "new", CreatedAt: "2026-09-03T00:00:00Z", DependentCount: 1},
		{ID: "root", CreatedAt: "2026-09-01T00:00:00Z", DependentCount: 7},
		{ID: "tie", CreatedAt: "2026-09-02T00:00:00Z", DependentCount: 1},
	}
	got := SortIssues(issues, SortDependents)
	if got[0].ID != "root" || got[1].ID != "new" || got[2].ID != "tie" {
		t.Fatalf("depends order = %v, want root/new/tie", []string{got[0].ID, got[1].ID, got[2].ID})
	}
}

func TestBoardDependsUsesReverseDependencyGraph(t *testing.T) {
	issues := []bd.Issue{
		{ID: "one", CreatedAt: "2026-09-03T00:00:00Z"},
		{ID: "seven", CreatedAt: "2026-09-01T00:00:00Z"},
	}
	down := map[string][]bd.DepRecord{
		"dependent-one": {{ID: "one"}},
	}
	issues = append(issues, bd.Issue{ID: "dependent-one"})
	for i := 0; i < 7; i++ {
		id := "dependent-seven-" + itoa(i)
		issues = append(issues, bd.Issue{ID: id})
		down[id] = []bd.DepRecord{{ID: "seven"}}
	}
	f := &fakeClient{
		issues:   map[bd.View][]bd.Issue{bd.ViewOpen: issues},
		downByID: down,
	}
	m := newTestModel(f)
	m.sortMode = SortDependents
	msg := m.loadBoardCmd()()
	board := msg.(boardMsg)
	m = applyMsg(t, m, board)
	m = applyMsg(t, m, m.loadGraphCmd(board.view, board.generation, board.issues)())
	if got := m.rows[0].ID; got != "seven" {
		t.Fatalf("depends order = %q, want seven", got)
	}
	if m.rows[0].DependentCount != 7 {
		t.Fatalf("reverse dependency count = %d, want 7", m.rows[0].DependentCount)
	}
}

func TestBoardGraphDerivesDependentsFromDownEdges(t *testing.T) {
	issues := []bd.Issue{
		{ID: "blocker", CreatedAt: "2026-09-01T00:00:00Z"},
		{ID: "dependent", CreatedAt: "2026-09-02T00:00:00Z"},
	}
	f := &fakeClient{
		issues: map[bd.View][]bd.Issue{bd.ViewClosed: issues},
		downByID: map[string][]bd.DepRecord{
			"dependent": {{ID: "blocker", DependencyType: "blocks"}},
			"blocker":   {},
		},
	}
	m := newTestModel(f)
	m.view = bd.ViewClosed
	msg := m.loadBoardCmd()()
	board := msg.(boardMsg)
	m = applyMsg(t, m, board)
	boardGraph := m.loadGraphCmd(board.view, board.generation, board.issues)().(graphMsg)
	for _, issue := range boardGraph.issues {
		if issue.ID == "blocker" && issue.DependentCount != 1 {
			t.Fatalf("derived dependent count = %d, want 1", issue.DependentCount)
		}
	}
	if len(boardGraph.deps["dependent"]) != 1 {
		t.Fatalf("down graph edges = %+v, want one dependent edge", boardGraph.deps)
	}
	depCalls := f.depCalls
	m = applyMsg(t, m, boardGraph)
	_ = m.loadGraphCmd(board.view, board.generation, board.issues)()
	if f.depCalls != depCalls {
		t.Fatalf("cached graph made %d new dependency calls", f.depCalls-depCalls)
	}
}

func TestBoardRowsApplyBeforeGraphEnrichment(t *testing.T) {
	f := &fakeClient{issues: map[bd.View][]bd.Issue{
		bd.ViewOpen: {{ID: "fast", Title: "Paint first", Status: "open"}},
	}}
	m := newTestModel(f)
	board := m.loadBoardCmd()().(boardMsg)
	updated, _ := m.Update(board)
	painted := updated.(Model)
	if len(painted.rows) != 1 || painted.rows[0].ID != "fast" {
		t.Fatalf("rows were not painted from list snapshot: %+v", painted.rows)
	}
	if f.depCalls != 0 {
		t.Fatalf("list load triggered dependency calls before first paint: %d", f.depCalls)
	}
}

func TestBoardGraphLoadsWithBoundedConcurrency(t *testing.T) {
	issues := make([]bd.Issue, boardGraphWorkers+2)
	for i := range issues {
		issues[i].ID = "issue-" + itoa(i)
	}
	f := &concurrentDepsClient{fakeClient: &fakeClient{
		issues: map[bd.View][]bd.Issue{bd.ViewClosed: issues},
	}}
	m := newTestModel(f.fakeClient)
	m.backend = f
	m.view = bd.ViewClosed
	msg := m.loadBoardCmd()()
	board := msg.(boardMsg)
	if got := board.err; got != nil {
		t.Fatalf("load board: %v", got)
	}
	_ = m.loadGraphCmd(board.view, board.generation, board.issues)()
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
			issues: map[bd.View][]bd.Issue{bd.ViewOpen: issues},
			down:   []bd.DepRecord{{ID: "blocker", Title: "Blocker", Status: "open"}},
		},
		failID:  "graph-timeout",
		failErr: errors.New("dependency timeout"),
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
	m = applyMsg(t, m, board)
	graph := m.loadGraphCmd(board.view, board.generation, board.issues)().(graphMsg)
	if len(graph.issues) != len(issues) || graph.issues[1].DependentCount != issues[1].DependentCount {
		t.Fatalf("board issues = %+v, want list rows and preserved fallback count", graph.issues)
	}
	if len(graph.deps["ready"]) != 1 || graph.deps["ready"][0].ID != "blocker" {
		t.Fatalf("successful dependency enrichment was discarded: %+v", graph.deps)
	}
	m = applyMsg(t, m, graph)
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
