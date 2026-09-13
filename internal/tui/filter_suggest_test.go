package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/RooseveltAdvisors/beads-tui/internal/bd"
)

func TestFilterSuggestionsEmptyLeadWithKeywords(t *testing.T) {
	sugs := FilterSuggestions("", 0, nil, nil, nil)
	if len(sugs) == 0 {
		t.Fatal("expected default suggestions")
	}
	if sugs[0].Insert != "overdue" {
		t.Fatalf("first suggestion = %q, want overdue", sugs[0].Insert)
	}
	joined := ""
	for _, s := range sugs {
		joined += s.Insert + " "
	}
	for _, want := range []string{"recurring", "status:", "priority:", "label:", "assignee:"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %q", want, joined)
		}
	}
}

func TestFilterSuggestionsPrefixAndValues(t *testing.T) {
	sugs := FilterSuggestions("over", 4, nil, nil, nil)
	if len(sugs) == 0 || sugs[0].Insert != "overdue" {
		t.Fatalf("over → %+v", sugs)
	}
	sugs = FilterSuggestions("status:in", 9, []string{"open", "in_progress"}, nil, nil)
	found := false
	for _, s := range sugs {
		if s.Insert == "status:in_progress" {
			found = true
		}
	}
	if !found {
		t.Fatalf("status:in → %+v", sugs)
	}
	sugs = FilterSuggestions("assignee:pi", 11, nil, []string{"pi", "wiseman"}, nil)
	if len(sugs) == 0 || sugs[0].Insert != "assignee:pi" {
		t.Fatalf("assignee:pi → %+v", sugs)
	}
	sugs = FilterSuggestions("label:ux", 8, nil, nil, []string{"ux", "ops"})
	if len(sugs) == 0 || sugs[0].Insert != "label:ux" {
		t.Fatalf("label:ux → %+v", sugs)
	}
}

func TestFilterSuggestionsNegation(t *testing.T) {
	sugs := FilterSuggestions("!rec", 4, nil, nil, nil)
	if len(sugs) == 0 || !strings.HasPrefix(sugs[0].Insert, "!") {
		t.Fatalf("!rec → %+v", sugs)
	}
}

func TestApplyFilterSuggestionReplacesToken(t *testing.T) {
	q, cur := ApplyFilterSuggestion("status:op assignee:pi", 9, FilterSuggestion{Insert: "status:open"})
	if q != "status:open assignee:pi" && q != "status:open  assignee:pi" {
		// trailing space after completed keyword is intentional
		if !strings.HasPrefix(q, "status:open ") {
			t.Fatalf("q=%q cur=%d", q, cur)
		}
	}
	q, cur = ApplyFilterSuggestion("foo ", 4, FilterSuggestion{Insert: "overdue"})
	if !strings.Contains(q, "overdue") || cur <= 4 {
		t.Fatalf("q=%q cur=%d", q, cur)
	}
	// bare prefix keeps cursor at end of "status:" with no forced space
	q, cur = ApplyFilterSuggestion("", 0, FilterSuggestion{Insert: "status:"})
	if q != "status:" || cur != len("status:") {
		t.Fatalf("prefix q=%q cur=%d", q, cur)
	}
}

func TestFilterTokenSpan(t *testing.T) {
	s, e := filterTokenSpan("a | status:op", 12)
	if got := "a | status:op"[s:e]; got != "status:op" {
		t.Fatalf("span=%q [%d,%d)", got, s, e)
	}
}

func TestListRowShowsOverdueAndRecurringIcons(t *testing.T) {
	vocab := NewVocab(nil)
	past := time.Now().Add(-48 * time.Hour).UTC().Format(time.RFC3339)
	row := stripANSI(vocab.ListRow(bd.Issue{
		ID: "late", Title: "Late work", Status: "open",
		DueAt: past, Repeat: "*/30 * * * *",
	}, 120, false))
	if !strings.Contains(row, overdueIcon) {
		t.Fatalf("overdue row missing %s: %q", overdueIcon, row)
	}
	if !strings.Contains(row, recurringIcon) {
		t.Fatalf("overdue+recurring row missing %s: %q", recurringIcon, row)
	}
	if !strings.Contains(row, "overdue") {
		t.Fatalf("overdue chip missing text: %q", row)
	}
	future := stripANSI(vocab.ListRow(bd.Issue{
		ID: "ok", Title: "On time", Status: "open", DueAt: "2099-01-02",
	}, 100, false))
	if strings.Contains(future, overdueIcon) {
		t.Fatalf("future due should not show overdue icon: %q", future)
	}
	if !strings.Contains(future, "Due: 2099-01-02") {
		t.Fatalf("future due chip missing: %q", future)
	}
}
