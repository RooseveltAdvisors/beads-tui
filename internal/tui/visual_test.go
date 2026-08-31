package tui

import (
	"strings"
	"testing"

	"github.com/RooseveltAdvisors/beads-tui/internal/bd"
)

func TestReadyLeverageSortPutsLargestUnblockFirst(t *testing.T) {
	issues := []bd.Issue{
		{ID: "new", CreatedAt: "2026-09-03T00:00:00Z", DependentCount: 1},
		{ID: "root", CreatedAt: "2026-09-01T00:00:00Z", DependentCount: 7},
		{ID: "tie", CreatedAt: "2026-09-02T00:00:00Z", DependentCount: 1},
	}
	got := SortReadyByLeverage(issues)
	if got[0].ID != "root" || got[1].ID != "new" || got[2].ID != "tie" {
		t.Fatalf("leverage order = %v, want root/new/tie", []string{got[0].ID, got[1].ID, got[2].ID})
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
	lines := graphLines(issues, issues, deps, "leaf", NewVocab(nil))
	plain := stripANSI(strings.Join(lines, "\n"))
	for _, want := range []string{"leaf · Leaf", "blocked-by: middle · Middle", "blocked-by: root · Root"} {
		if !strings.Contains(plain, want) {
			t.Errorf("graph missing %q: %q", want, plain)
		}
	}
	if strings.Contains(plain, "cycle detected") {
		t.Fatalf("acyclic dependency chain reported a cycle: %q", plain)
	}
	cycle := graphLines(issues, issues, map[string][]bd.DepRecord{
		"middle": {{ID: "root", Title: "Root"}},
		"root":   {{ID: "middle", Title: "Middle"}},
	}, "root", NewVocab(nil))
	if !strings.Contains(stripANSI(strings.Join(cycle, "\n")), "cycle detected") {
		t.Fatalf("graph missing cycle callout: %v", cycle)
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
