package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/RooseveltAdvisors/beads-tui/internal/bd"
)

func TestFormatDueDeltaUnits(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Second, "<1m"},
		{5 * time.Minute, "5m"},
		{90 * time.Minute, "1h"},
		{5 * time.Hour, "5h"},
		{30 * time.Hour, "30h"},
		{3 * 24 * time.Hour, "3d"},
	}
	for _, tc := range cases {
		if got := formatDueDelta(tc.d); got != tc.want {
			t.Errorf("formatDueDelta(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

func TestDueTextNearDeadlineUsesMinutesOrHours(t *testing.T) {
	soon := time.Now().Add(25 * time.Minute).UTC().Format(time.RFC3339)
	got := dueText(bd.Issue{Status: "open", DueAt: soon})
	if !strings.Contains(got, "m left") && !strings.Contains(got, "h left") {
		t.Fatalf("near due = %q, want minutes/hours left", got)
	}
	// same calendar day but many hours out still hours not 0d
	later := time.Now().Add(10 * time.Hour).UTC().Format(time.RFC3339)
	got = dueText(bd.Issue{Status: "open", DueAt: later})
	if strings.Contains(got, "0d") || !strings.Contains(got, "h left") {
		t.Fatalf("same-day due = %q, want Nh left", got)
	}
}

func TestDetailShowsDueRepeatAssignee(t *testing.T) {
	due := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
	issue := &bd.Issue{
		ID: "fm-x", Title: "Tick", Status: "open", Priority: 1,
		Assignee: "wiseman", DueAt: due, Repeat: "*/30 * * * *",
		CreatedAt: "2026-09-13T00:00:00Z", UpdatedAt: "2026-09-13T01:00:00Z",
		CreatedBy: "Jon", IssueType: "task",
	}
	plain := stripANSI(strings.Join(BuildDetail(NewVocab(nil), issue, nil, nil, 80), "\n"))
	for _, want := range []string{"Due:", due, "left", "Repeat: */30", "Assignee: wiseman", "Created:", "Updated:", "Created by: Jon"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("detail missing %q:\n%s", want, plain)
		}
	}
}
