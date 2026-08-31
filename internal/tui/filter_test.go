package tui

import (
	"strings"
	"testing"

	"github.com/RooseveltAdvisors/beads-tui/internal/bd"
)

func TestSortIssuesModes(t *testing.T) {
	issues := []bd.Issue{
		{ID: "c", Title: "Charlie", Priority: 2, CreatedAt: "2026-08-01T00:00:00Z", UpdatedAt: "2026-08-03T00:00:00Z"},
		{ID: "a", Title: "alpha", Priority: 0, CreatedAt: "2026-08-03T00:00:00Z", UpdatedAt: "2026-08-01T00:00:00Z"},
		{ID: "b", Title: "Bravo", Priority: 1, CreatedAt: "2026-08-02T00:00:00Z", UpdatedAt: "2026-08-04T00:00:00Z"},
	}
	for _, tc := range []struct {
		name string
		mode SortMode
		want []string
	}{
		{"priority", SortPriority, []string{"a", "b", "c"}},
		{"created newest first", SortCreated, []string{"a", "b", "c"}},
		{"updated newest first", SortUpdated, []string{"b", "c", "a"}},
		{"alphabetical", SortAlphabetical, []string{"a", "b", "c"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := SortIssues(issues, tc.mode)
			for i, want := range tc.want {
				if got[i].ID != want {
					t.Fatalf("position %d = %q, want %q", i, got[i].ID, want)
				}
			}
		})
	}
	if issues[0].ID != "c" {
		t.Fatal("SortIssues mutated the backend snapshot")
	}
}

func TestFilterMatching(t *testing.T) {
	issues := []bd.Issue{
		{ID: "open", Title: "Write API docs", Description: "Explain the filter syntax", Status: "open", Priority: 1, Labels: []string{"docs", "ux"}},
		{ID: "blocked", Title: "Fix runner", Description: "Needs credentials", Status: "blocked", Priority: 2, Labels: []string{"infra"}},
		{ID: "closed", Title: "Ship it", Status: "closed", Priority: 0},
	}
	tests := []struct {
		input string
		want  []string
	}{
		{"status:blocked", []string{"blocked"}},
		{"priority:P1", []string{"open"}},
		{"label:UX", []string{"open"}},
		{"credentials", []string{"blocked"}},
		{"status:open", []string{"open"}},
	}
	for _, tc := range tests {
		got := FilterIssues(issues, ParseFilter(tc.input))
		if len(got) != len(tc.want) {
			t.Fatalf("%q matched %d rows, want %d", tc.input, len(got), len(tc.want))
		}
		for i, want := range tc.want {
			if got[i].ID != want {
				t.Errorf("%q match %d = %q, want %q", tc.input, i, got[i].ID, want)
			}
		}
	}
}

func TestNewKeyDispatchAndStatusBar(t *testing.T) {
	f := &fakeClient{issues: map[bd.View][]bd.Issue{bd.ViewReady: {
		{ID: "fm-a", Title: "Alpha", Status: "open", Priority: 2, Labels: []string{"frontend"}},
		{ID: "fm-b", Title: "Beta", Status: "blocked", Priority: 1, Labels: []string{"backend"}},
	}}}
	m := drive(t, f)
	if m.sortMode != SortAlphabetical {
		t.Fatalf("initial sort = %s, want alphabetical", m.sortMode)
	}
	m = sendKey(t, m, "s")
	if m.sortMode != SortPriority || m.rows[0].ID != "fm-b" {
		t.Fatalf("s sort = %s, first row %q; want priority/fm-b", m.sortMode, m.rows[0].ID)
	}
	m = sendKey(t, m, "f")
	if !m.filtering {
		t.Fatal("f did not open the filter prompt")
	}
	m = sendKey(t, m, "status:blocked")
	m = sendKey(t, m, "enter")
	if m.filter.Kind != FilterStatus || len(m.rows) != 1 || m.rows[0].ID != "fm-b" {
		t.Fatalf("filter = %+v, rows = %+v", m.filter, m.rows)
	}
	m = sendKey(t, m, "esc")
	if m.filter.Active() || len(m.rows) != 2 {
		t.Fatalf("esc did not clear filter: %+v rows=%d", m.filter, len(m.rows))
	}
	m = sendKey(t, m, "t")
	if m.filter.Kind != FilterLabel || m.filter.Query != "backend" || len(m.rows) != 1 {
		t.Fatalf("t filter = %+v, rows=%d", m.filter, len(m.rows))
	}
	view := stripANSI(m.View())
	for _, want := range []string{"sort:priority", "filter:label:backend", "sel:fm-b", "total:1", "s sort", "f filter", "t tag", "? help", "q quit"} {
		if !strings.Contains(view, want) {
			t.Errorf("status bar missing %q: %s", want, view)
		}
	}
}

func TestListRowsRenderColoredLabels(t *testing.T) {
	row := NewVocab(nil).ListRow(bd.Issue{ID: "fm-x", Title: "T", Status: "open", Labels: []string{"frontend", "urgent"}}, 50, false)
	plain := stripANSI(row)
	if !strings.Contains(plain, "[frontend] [urgent]") {
		t.Fatalf("labels missing from row: %q", plain)
	}
}

func TestRequiredFooterFieldsSurviveNarrowWidths(t *testing.T) {
	m := New(nil)
	m.rows = []bd.Issue{{ID: "fm-long-selected-id"}}
	m.filter = ParseFilter("label:extraordinarily-long-label")
	for _, width := range []int{80, 48} {
		footer := stripANSI(m.renderFooter(width))
		if displayWidth(footer) > width {
			t.Fatalf("width %d footer overflowed: %q", width, footer)
		}
		for _, field := range []string{"view:", "sort:", "filter:", "sel:", "total:"} {
			if !strings.Contains(footer, field) {
				t.Errorf("width %d footer missing %q: %q", width, field, footer)
			}
		}
	}
}

func TestFilteredEmptyStateNamesActiveFilter(t *testing.T) {
	m := New(nil)
	m.allRows = []bd.Issue{{ID: "fm-a", Status: "open"}}
	m.filter = ParseFilter("status:closed")
	m.rebuildRows("")
	got := stripANSI(strings.Join(m.renderListPane(60, 8), "\n"))
	if !strings.Contains(got, "No matches for filter: status:closed") {
		t.Fatalf("filtered empty state = %q", got)
	}
}

func TestTruncatedRowKeepsTagStyleAndSpace(t *testing.T) {
	row := NewVocab(nil).ListRow(bd.Issue{
		ID: "fm-x", Title: strings.Repeat("long title ", 8), Status: "open", Labels: []string{"frontend"},
	}, 32, false)
	if displayWidth(row) > 32 || !strings.Contains(stripANSI(row), "[f") {
		t.Fatalf("truncated row lost reserved tag: %q", stripANSI(row))
	}
	tag := strings.Index(row, "[f")
	if tag < 0 || !strings.Contains(row[:tag], "\x1b[") {
		t.Fatalf("truncated tag lost ANSI style: %q", row)
	}
}
