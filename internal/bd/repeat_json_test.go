package bd

import (
	"encoding/json"
	"testing"
)

func TestUnmarshalRepeatPatternAlias(t *testing.T) {
	raw := []byte(`{"id":"x","title":"t","status":"open","repeat_pattern":"*/30 * * * *","due_at":"2026-09-13T16:00:00Z"}`)
	var issue Issue
	if err := json.Unmarshal(raw, &issue); err != nil {
		t.Fatal(err)
	}
	if issue.Repeat != "*/30 * * * *" {
		t.Fatalf("Repeat = %q, want cron from repeat_pattern", issue.Repeat)
	}
	if !issue.IsRecurring() {
		t.Fatal("IsRecurring false")
	}
}

func TestUnmarshalRepeatStartEnd(t *testing.T) {
	raw := []byte(`{"id":"x","title":"t","status":"open","repeat_pattern":"0 9 * * *","repeat_start":"2026-09-21T15:18:15Z","repeat_end":"2026-12-31T23:59:59Z"}`)
	var issue Issue
	if err := json.Unmarshal(raw, &issue); err != nil {
		t.Fatal(err)
	}
	if issue.Repeat != "0 9 * * *" {
		t.Fatalf("Repeat = %q, want cron from repeat_pattern", issue.Repeat)
	}
	if issue.RecurrenceStart != "2026-09-21T15:18:15Z" {
		t.Fatalf("RecurrenceStart = %q, want repeat_start", issue.RecurrenceStart)
	}
	if issue.RecurrenceEnd != "2026-12-31T23:59:59Z" {
		t.Fatalf("RecurrenceEnd = %q, want repeat_end", issue.RecurrenceEnd)
	}
	if !issue.IsRecurring() {
		t.Fatal("IsRecurring false")
	}
}

