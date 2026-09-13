package bd

import (
	"context"
	"strings"
	"testing"
)

func TestResolveActorPrefersBeadsActor(t *testing.T) {
	t.Setenv("BEADS_ACTOR", "firstmate")
	t.Setenv("BD_ACTOR", "ignored")
	if got := ResolveActor(); got != "firstmate" {
		t.Fatalf("ResolveActor=%q want firstmate", got)
	}
}

func TestFieldSnapshot(t *testing.T) {
	iss := &Issue{Title: "Hello", Status: "open", Assignee: "wiseman", Priority: 1, DueAt: "2026-01-01"}
	if got := fieldSnapshot(iss, "assignee"); got != "wiseman" {
		t.Fatalf("assignee=%q", got)
	}
	if got := fieldSnapshot(nil, "title"); got != "?" {
		t.Fatalf("nil=%q", got)
	}
}

func TestUpdateIssueLineageIncludesActor(t *testing.T) {
	var inputs []string
	c := &Client{
		Actor: "wiseman",
		lookPath: func(string) (string, error) { return "bd", nil },
		run: func(_ context.Context, _ string, args ...string) (string, string, error) {
			// show or update
			if len(args) > 0 && args[0] == "--actor" {
				// strip for switch
			}
			joined := strings.Join(args, " ")
			if strings.Contains(joined, "show") {
				return `[{"id":"fm-1","title":"Old","status":"open","assignee":"a","priority":2}]`, "", nil
			}
			return "", "", nil
		},
		runInput: func(_ context.Context, _ string, input string, args ...string) (string, string, error) {
			inputs = append(inputs, input)
			return "", "", nil
		},
		waitOverride: 1,
	}
	if err := c.UpdateIssue(context.Background(), "fm-1", map[string]string{
		"status":   "in_progress",
		"assignee": "wiseman",
	}); err != nil {
		t.Fatal(err)
	}
	if len(inputs) == 0 {
		t.Fatal("expected lineage comment")
	}
	got := inputs[len(inputs)-1]
	for _, want := range []string{
		"tui-lineage update",
		`actor="wiseman"`,
		"via=beads-tui",
		"status:open→in_progress",
		"assignee:a→wiseman",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("lineage missing %q in %q", want, got)
		}
	}
}

func TestClientActorOverridesEnv(t *testing.T) {
	t.Setenv("BEADS_ACTOR", "env-agent")
	c := &Client{Actor: "captain-jon"}
	if c.actor() != "captain-jon" {
		t.Fatalf("actor=%q", c.actor())
	}
}
