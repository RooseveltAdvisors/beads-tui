package tui

import (
	"strings"
	"testing"

	"github.com/RooseveltAdvisors/beads-tui/internal/bd"
)

func boardFixtures() []bd.Issue {
	return []bd.Issue{
		{ID: "c", Title: "Gamma deploy", Description: "ship the api", Status: "closed", Priority: 2, DependencyCount: 1, CreatedAt: "2026-08-03T00:00:00Z", UpdatedAt: "2026-08-05T00:00:00Z"},
		{ID: "a", Title: "Alpha plan", Description: "write the api plan", Status: "open", Priority: 0, DependencyCount: 3, CreatedAt: "2026-08-01T00:00:00Z", UpdatedAt: "2026-08-04T00:00:00Z"},
		{ID: "b", Title: "Beta build", Description: "build the web client", Status: "in_progress", Priority: 1, DependencyCount: 0, CreatedAt: "2026-08-02T00:00:00Z", UpdatedAt: "2026-08-06T00:00:00Z"},
	}
}

func ids(rows []bd.Issue) string {
	parts := make([]string, len(rows))
	for i, row := range rows {
		parts[i] = row.ID
	}
	return strings.Join(parts, ",")
}

func TestApplyBoardSortModes(t *testing.T) {
	rows := boardFixtures()
	for _, tc := range []struct {
		mode SortMode
		want string
	}{
		{SortPriority, "a,b,c"}, {SortCreated, "c,b,a"}, {SortUpdated, "b,c,a"},
		{SortAlphabetical, "a,b,c"}, {SortDependencies, "a,c,b"},
	} {
		if got := ids(ApplyBoard(rows, Filter{}, tc.mode)); got != tc.want {
			t.Errorf("%s = %s, want %s", tc.mode, got, tc.want)
		}
	}
}

func TestFilterCombinesStatusPriorityAndText(t *testing.T) {
	f := ParseFilter("status:OPEN priority:p0 api plan")
	if f.Status != "open" || f.Priority == nil || *f.Priority != 0 || f.Text != "api plan" {
		t.Fatalf("parsed filter = %+v", f)
	}
	rows := ApplyBoard(boardFixtures(), f, SortPriority)
	if got := ids(rows); got != "a" {
		t.Fatalf("filtered ids = %q, want a", got)
	}
	if MatchesFilter(boardFixtures()[1], ParseFilter("status:closed")) {
		t.Error("status filter matched wrong status")
	}
}

func TestBuildTreeExpandsAndCollapsesWithoutCycles(t *testing.T) {
	rows := boardFixtures()
	edges := map[string][]bd.DepRecord{
		"a": {{ID: "b", Title: "Beta build"}},
		"b": {{ID: "c", Title: "Gamma deploy"}},
		"c": {{ID: "a", Title: "Alpha plan"}},
	}
	expanded := map[string]bool{"a": true, "b": true, "c": true}
	items := BuildTree(rows, edges, expanded)
	if len(items) != len(rows) {
		t.Fatalf("cycle tree has %d items, want %d", len(items), len(rows))
	}
	if !strings.Contains(items[1].Prefix, "─") {
		t.Errorf("child lacks tree branch: %+v", items)
	}
	expanded[items[0].Issue.ID] = false
	if got := len(BuildTree(rows, edges, expanded)); got != 1 {
		t.Errorf("collapsed tree has %d items, want 1", got)
	}
}
