package tui

import (
	"strings"
	"testing"

	"github.com/RooseveltAdvisors/beads-tui/internal/bd"
)

func TestVisualModeToggleAndRangeSelection(t *testing.T) {
	issues := []bd.Issue{
		{ID: "fm-1", Title: "Issue 1", Status: "open"},
		{ID: "fm-2", Title: "Issue 2", Status: "open"},
		{ID: "fm-3", Title: "Issue 3", Status: "open"},
		{ID: "fm-4", Title: "Issue 4", Status: "open"},
	}
	f := &fakeClient{issues: map[bd.View][]bd.Issue{bd.ViewOpen: issues}}
	m := drive(t, f)
	if m.visualMode {
		t.Fatal("visual mode should initially be false")
	}

	// Move to index 1 ("fm-2")
	m = sendKey(t, m, "j")
	if m.selectedID() != "fm-2" {
		t.Fatalf("selected = %q, want fm-2", m.selectedID())
	}

	// Press v to enter visual mode
	m = sendKey(t, m, "v")
	if !m.visualMode {
		t.Fatal("v did not enter visual mode")
	}
	if m.visualAnchor != 1 {
		t.Fatalf("visualAnchor = %d, want 1", m.visualAnchor)
	}

	// Select range down to index 3 ("fm-4")
	m = sendKey(t, m, "j")
	m = sendKey(t, m, "j")
	if m.selectedID() != "fm-4" {
		t.Fatalf("selected = %q, want fm-4", m.selectedID())
	}
	ids := m.visualSelectedIDs()
	if len(ids) != 3 || ids[0] != "fm-2" || ids[1] != "fm-3" || ids[2] != "fm-4" {
		t.Fatalf("visualSelectedIDs = %v, want [fm-2, fm-3, fm-4]", ids)
	}

	// Move backwards up to index 0 ("fm-1")
	m = sendKey(t, m, "k")
	m = sendKey(t, m, "k")
	m = sendKey(t, m, "k")
	if m.selectedID() != "fm-1" {
		t.Fatalf("selected = %q, want fm-1", m.selectedID())
	}
	// Range is now anchor 1 down to selected 0 -> [fm-1, fm-2]
	ids = m.visualSelectedIDs()
	if len(ids) != 2 || ids[0] != "fm-1" || ids[1] != "fm-2" {
		t.Fatalf("visualSelectedIDs = %v, want [fm-1, fm-2]", ids)
	}

	// Press Esc to cancel visual mode
	m = sendKey(t, m, "esc")
	if m.visualMode {
		t.Fatal("esc did not exit visual mode")
	}

	// Press V (visual line mode) to re-enter
	m = sendKey(t, m, "V")
	if !m.visualMode {
		t.Fatal("V did not enter visual mode")
	}
	// Press V again to toggle off
	m = sendKey(t, m, "V")
	if m.visualMode {
		t.Fatal("V again did not toggle off visual mode")
	}
}

func TestVisualModeBatchDelete(t *testing.T) {
	issues := []bd.Issue{
		{ID: "fm-1", Title: "Keep 1", Status: "open"},
		{ID: "fm-2", Title: "Delete 2", Status: "open"},
		{ID: "fm-3", Title: "Delete 3", Status: "open"},
		{ID: "fm-4", Title: "Keep 4", Status: "open"},
	}
	f := &fakeClient{issues: map[bd.View][]bd.Issue{bd.ViewOpen: issues}}
	m := drive(t, f)

	// Move to fm-2
	m = sendKey(t, m, "j")
	// Enter visual mode
	m = sendKey(t, m, "v")
	// Move to fm-3
	m = sendKey(t, m, "j")

	// Trigger delete with 'd'
	m = sendKey(t, m, "d")
	if m.crudMode != crudDeleteConfirm {
		t.Fatalf("crudMode = %v, want crudDeleteConfirm", m.crudMode)
	}
	if len(m.crudDeleteIDs) != 2 || m.crudDeleteIDs[0] != "fm-2" || m.crudDeleteIDs[1] != "fm-3" {
		t.Fatalf("crudDeleteIDs = %v, want [fm-2, fm-3]", m.crudDeleteIDs)
	}

	crudView := stripANSI(m.renderCrud())
	if !strings.Contains(crudView, "DELETE forever?") || !strings.Contains(crudView, "2 beads selected") {
		t.Fatalf("renderCrud missing multi-select delete prompt: %s", crudView)
	}

	// Confirm with 'y' and drain commands
	m = step(t, m, "y")

	if len(f.deletedIDs) != 2 || f.deletedIDs[0] != "fm-2" || f.deletedIDs[1] != "fm-3" {
		t.Fatalf("f.deletedIDs = %v, want [fm-2, fm-3]", f.deletedIDs)
	}
	if m.visualMode {
		t.Fatal("visualMode should be reset after delete")
	}
}

func TestVisualModeBatchDeleteOnSearchResults(t *testing.T) {
	allIssues := []bd.Issue{
		{ID: "fm-other", Title: "Unrelated task", Status: "open"},
		{ID: "fm-dup1", Title: "Babysit 4358 dup 1", Status: "closed", CloseReason: "duplicate babysit bead for PR 4358"},
		{ID: "fm-dup2", Title: "Babysit 4358 dup 2", Status: "closed", CloseReason: "duplicate babysit bead for PR 4358"},
		{ID: "fm-dup3", Title: "Babysit 4358 dup 3", Status: "closed", CloseReason: "duplicate babysit bead for PR 4358"},
	}
	f := &fakeClient{issues: map[bd.View][]bd.Issue{
		bd.ViewOpen:   {allIssues[0]},
		bd.ViewClosed: {allIssues[1], allIssues[2], allIssues[3]},
	}}
	m := drive(t, f)
	m.graphRows = allIssues

	// Perform slash search for "4358"
	m.filter = ParseSearchFilter("4358")
	m.projectRows("")

	if len(m.rows) != 3 {
		t.Fatalf("expected 3 search result rows for 4358, got %d: %+v", len(m.rows), m.rows)
	}

	// Enter visual mode on the search results
	m = sendKey(t, m, "v")
	// Select all 3 items (press G to go to end)
	m = sendKey(t, m, "G")
	ids := m.visualSelectedIDs()
	if len(ids) != 3 {
		t.Fatalf("expected 3 selected IDs in search results, got %d: %v", len(ids), ids)
	}

	// Press 'd' to delete the batch of search results
	m = sendKey(t, m, "d")
	if m.crudMode != crudDeleteConfirm {
		t.Fatalf("crudMode = %v, want crudDeleteConfirm", m.crudMode)
	}
	if len(m.crudDeleteIDs) != 3 {
		t.Fatalf("crudDeleteIDs = %v, want 3 IDs", m.crudDeleteIDs)
	}

	// Confirm and drain commands
	m = step(t, m, "y")

	if len(f.deletedIDs) != 3 {
		t.Fatalf("deleted IDs count = %d, want 3: %v", len(f.deletedIDs), f.deletedIDs)
	}
}

func TestFullTextSearchCoversAllFields(t *testing.T) {
	issueNotes := bd.Issue{
		ID:    "fm-notes",
		Title: "General Title",
		Notes: "Specific note mentioning 4358 reference",
	}
	issueClose := bd.Issue{
		ID:          "fm-close",
		Title:       "General Title",
		CloseReason: "closed as duplicate of 4358",
	}
	issueLabels := bd.Issue{
		ID:     "fm-labels",
		Title:  "General Title",
		Labels: []string{"backend", "pr-4358-fix"},
	}
	issueComments := bd.Issue{
		ID:    "fm-comments",
		Title: "General Title",
		Comments: []bd.Comment{
			{Author: "worker", Text: "Please verify against 4358 check"},
		},
	}
	issueUnrelated := bd.Issue{
		ID:    "fm-unrelated",
		Title: "Completely unrelated",
	}

	search := ParseSearchFilter("4358")
	if !search.Matches(issueNotes) {
		t.Error("FTS should match issue with 4358 in Notes")
	}
	if !search.Matches(issueClose) {
		t.Error("FTS should match issue with 4358 in CloseReason")
	}
	if !search.Matches(issueLabels) {
		t.Error("FTS should match issue with 4358 in Labels")
	}
	if !search.Matches(issueComments) {
		t.Error("FTS should match issue with 4358 in Comments")
	}
	if search.Matches(issueUnrelated) {
		t.Error("FTS should not match unrelated issue")
	}

	// TextFilter also covers notes, close reason, labels, comments
	textFilter := ParseFilter("text:4358")
	if !textFilter.Matches(issueNotes) {
		t.Error("text filter should match Notes")
	}
	if !textFilter.Matches(issueClose) {
		t.Error("text filter should match CloseReason")
	}
	if !textFilter.Matches(issueLabels) {
		t.Error("text filter should match Labels")
	}
	if !textFilter.Matches(issueComments) {
		t.Error("text filter should match Comments")
	}
}
