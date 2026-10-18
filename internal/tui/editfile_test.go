package tui

import (
	"os"
	"path/filepath"
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

func TestEditorOpensDottedChildBeadWithFixedTempName(t *testing.T) {
	child := bd.Issue{ID: "fm-abc.1", Title: "Child", Status: "open"}
	f := &fakeClient{
		issues:    map[bd.View][]bd.Issue{bd.ViewOpen: {child}},
		issueByID: map[string]*bd.Issue{"fm-abc.1": &child},
	}
	m := drive(t, f)
	msg, ok := m.openEditorEdit()().(editorReadyMsg)
	if !ok {
		t.Fatalf("editor did not open for dotted child bead")
	}
	defer os.RemoveAll(filepath.Dir(msg.path))
	if filepath.Base(msg.path) != "bead.txt" || msg.id != "fm-abc.1" || f.lastShowID != "fm-abc.1" {
		t.Fatalf("path=%q id=%q show=%q", msg.path, msg.id, f.lastShowID)
	}
	body, err := os.ReadFile(msg.path)
	if err != nil || !strings.Contains(string(body), "id: fm-abc.1\n") {
		t.Fatalf("edit document: %q %v", body, err)
	}
}
