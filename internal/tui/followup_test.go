package tui

import (
	"testing"

	"github.com/RooseveltAdvisors/beads-tui/internal/bd"
)

func TestSlashSearchMatchesIncrementallyAndCommits(t *testing.T) {
	issues := []bd.Issue{
		{ID: "fm-known", Title: "Unrelated", Description: "needle in the notes", Status: "open"},
		{ID: "fm-other", Title: "Another task", Description: "No match", Status: "open"},
	}
	f := &fakeClient{issues: map[bd.View][]bd.Issue{bd.ViewReady: issues}}
	m := newTestModel(f)
	m.treeMode = false
	m = applyMsg(t, m, boardMsg{view: bd.ViewReady, issues: issues})

	m = sendKey(t, m, "/")
	if !m.filtering || !m.searching {
		t.Fatal("/ did not open search prompt")
	}
	m = sendKey(t, m, "needle")
	if len(m.rows) != 1 || m.rows[0].ID != "fm-known" {
		t.Fatalf("incremental search rows = %+v", m.rows)
	}
	if m.filter.Kind != FilterSearch {
		t.Fatalf("search filter kind = %v, want FilterSearch", m.filter.Kind)
	}
	m = sendKey(t, m, "enter")
	if m.filtering || m.searching || m.filter.Query != "needle" {
		t.Fatalf("search did not commit: filtering=%v searching=%v filter=%+v", m.filtering, m.searching, m.filter)
	}

	// IDs are part of slash search even though the f filter is title/description only.
	m = sendKey(t, m, "/")
	m = sendKey(t, m, "other")
	if len(m.rows) != 1 || m.rows[0].ID != "fm-other" {
		t.Fatalf("id search rows = %+v", m.rows)
	}
	m = sendKey(t, m, "esc")
	if m.filter.Query != "needle" || len(m.rows) != 1 || m.rows[0].ID != "fm-known" {
		t.Fatalf("search cancel did not restore prior filter: %+v rows=%+v", m.filter, m.rows)
	}
}

func TestSlashSearchNavigationUsesMatches(t *testing.T) {
	issues := []bd.Issue{
		{ID: "a", Title: "Task alpha", Status: "open"},
		{ID: "b", Title: "Task beta", Status: "open"},
		{ID: "c", Title: "Task gamma", Status: "open"},
	}
	f := &fakeClient{issues: map[bd.View][]bd.Issue{bd.ViewReady: issues}}
	m := newTestModel(f)
	m.treeMode = false
	m = applyMsg(t, m, boardMsg{view: bd.ViewReady, issues: issues})
	m = sendKey(t, m, "/")
	m = sendKey(t, m, "task")
	m = sendKey(t, m, "enter")
	m = sendKey(t, m, "j")
	if m.selected != 1 || m.rows[m.selected].ID != "b" {
		t.Fatalf("j did not move through search matches: selected=%d rows=%+v", m.selected, m.rows)
	}
}

func TestSlashSearchFromDetailFocusesListAndCancelRestoresContext(t *testing.T) {
	issues := []bd.Issue{
		{ID: "a", Title: "Task alpha", Status: "open"},
		{ID: "b", Title: "Task beta", Status: "open"},
		{ID: "c", Title: "Task gamma", Status: "open"},
	}
	f := &fakeClient{issues: map[bd.View][]bd.Issue{bd.ViewReady: issues}}
	m := newTestModel(f)
	m.treeMode = false
	m = applyMsg(t, m, boardMsg{view: bd.ViewReady, issues: issues})
	m.selected = 1
	m.focus = FocusDetail
	m.dOffset = 7
	m.detail = testDetailOf("b")
	m.down = []bd.DepRecord{{ID: "saved-down"}}
	m.up = []bd.DepRecord{{ID: "saved-up"}}

	m = sendKey(t, m, "/")
	if m.focus != FocusList {
		t.Fatalf("search focus = %v, want list", m.focus)
	}
	m = sendKey(t, m, "task")
	m = sendKey(t, m, "enter")
	m = sendKey(t, m, "j")
	if m.focus != FocusList || m.rows[m.selected].ID != "c" {
		t.Fatalf("search navigation focus=%v selected=%s", m.focus, m.rows[m.selected].ID)
	}

	m.focus = FocusDetail
	m.dOffset = 7
	m.selected = 1
	m = sendKey(t, m, "/")
	m = sendKey(t, m, "alpha")
	m = applyMsg(t, m, detailMsg{
		id: "a", generation: m.detailGen, issue: testDetailOf("a"),
		down: []bd.DepRecord{{ID: "search-down"}}, up: []bd.DepRecord{{ID: "search-up"}},
	})
	m = sendKey(t, m, "esc")
	if m.focus != FocusDetail || m.rows[m.selected].ID != "b" || m.dOffset != 7 {
		t.Fatalf("cancel restored focus=%v selected=%s offset=%d", m.focus, m.rows[m.selected].ID, m.dOffset)
	}
	if m.detail == nil || m.detail.ID != "b" || m.down[0].ID != "saved-down" || m.up[0].ID != "saved-up" {
		t.Fatalf("cancel restored detail=%+v down=%+v up=%+v", m.detail, m.down, m.up)
	}

	m.focus = FocusDetail
	m.dOffset = 7
	m.selected = 1
	_ = m.loadDetailCmd("b")
	staleGeneration := m.detailGen
	m = sendKey(t, m, "/")
	m = sendKey(t, m, "esc")
	m = applyMsg(t, m, detailMsg{
		id: "b", generation: staleGeneration, issue: testDetailOf("b"),
		down: []bd.DepRecord{{ID: "late-down"}}, up: []bd.DepRecord{{ID: "late-up"}},
	})
	if m.dOffset != 7 || m.down[0].ID != "saved-down" || m.up[0].ID != "saved-up" {
		t.Fatalf("late response overwrote restored context: offset=%d down=%+v up=%+v", m.dOffset, m.down, m.up)
	}
}

func TestSlashSearchFlattensMatchesAndRevealsSelectedTreePath(t *testing.T) {
	issues := []bd.Issue{
		{ID: "parent", Title: "Matching parent", Status: "open"},
		{ID: "child", Title: "Matching child", Status: "open", ParentID: "parent"},
	}
	f := &fakeClient{issues: map[bd.View][]bd.Issue{bd.ViewReady: issues}}
	m := newTestModel(f)
	m = applyMsg(t, m, boardMsg{view: bd.ViewReady, issues: issues})
	m.expanded["parent"] = false
	m.rebuildRows("parent")

	m = sendKey(t, m, "/")
	m = sendKey(t, m, "matching")
	m = sendKey(t, m, "enter")
	if len(m.rows) != 2 || len(m.treeRows) != 0 {
		t.Fatalf("search results were not flat: rows=%+v treeRows=%+v", m.rows, m.treeRows)
	}
	m = sendKey(t, m, "k")
	if m.rows[m.selected].ID != "child" || !m.expanded["parent"] {
		t.Fatalf("child navigation did not reveal ancestor path: selected=%s expanded=%v", m.rows[m.selected].ID, m.expanded)
	}
	m = sendKey(t, m, "esc")
	if len(m.rows) != 2 || m.rows[m.selected].ID != "child" {
		t.Fatalf("cleared search hid selected child: selected=%d rows=%+v", m.selected, m.rows)
	}
}

func TestSearchAncestorExpansionGuardsDependencyCycles(t *testing.T) {
	m := New(nil)
	m.allRows = []bd.Issue{
		{ID: "a", Status: "open"},
		{ID: "b", Status: "open"},
		{ID: "c", Status: "open"},
		{ID: "root", Status: "open"},
	}
	m.deps = map[string][]bd.DepRecord{
		"a": {{ID: "root"}, {ID: "b"}},
		"b": {{ID: "a"}},
	}
	m.expandAncestors("c")
	if len(m.expanded) != 0 {
		t.Fatalf("unrelated cycle changed expansion state: %v", m.expanded)
	}
}
