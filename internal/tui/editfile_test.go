package tui

import (
	"strings"
	"testing"

	"github.com/RooseveltAdvisors/beads-tui/internal/bd"
)

func TestBeadEditRoundTrip(t *testing.T) {
	iss := &bd.Issue{
		ID: "fm-1", Title: "Hello", Status: "open", Assignee: "wiseman",
		Priority: 1, DueAt: "2026-09-14T15:00:00Z", IssueType: "task",
		Labels: []string{"a", "b"}, Description: "line1\nline2", Notes: "n1",
	}
	doc := beadEditDocument(iss)
	if !strings.Contains(doc, "due: ") || !strings.Contains(doc, "--- description ---") {
		t.Fatalf("doc=%s", doc)
	}
	p, err := parseBeadEditDocument(doc)
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "fm-1" || p.Title != "Hello" || p.Assignee != "wiseman" {
		t.Fatalf("%+v", p)
	}
	if p.Description != "line1\nline2" {
		t.Fatalf("desc=%q", p.Description)
	}
	fields := diffBeadFields(iss, p)
	if len(fields) != 0 {
		t.Fatalf("expected no diff, got %v", fields)
	}
	p.Title = "Hello2"
	p.Due = "2026-09-20 12:00"
	fields = diffBeadFields(iss, p)
	if fields["title"] != "Hello2" {
		t.Fatalf("%v", fields)
	}
}
