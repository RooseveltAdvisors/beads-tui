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
