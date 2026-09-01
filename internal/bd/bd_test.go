package bd

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// stubClient wires a Client to a canned exec: it records the arguments given
// and satisfies each test with the handler's stdout/stderr/error.
func stubClient(t *testing.T, handler func(args []string) (stdout, stderr string, err error)) *Client {
	t.Helper()
	return &Client{
		lookPath: func(string) (string, error) { return "/fake/bd", nil },
		run: func(_ context.Context, _ string, args ...string) (string, string, error) {
			return handler(args)
		},
	}
}

const readyFixture = `[
  {
    "id": "fm-2fw",
    "title": "Doug: schedule the special board meeting",
    "status": "open",
    "priority": 1,
    "issue_type": "task",
    "parent_id": "fm-parent",
    "owner": "rooseveltadvisors@gmail.com",
    "created_at": "2026-08-29T10:40:30Z",
    "created_by": "Jon Roosevelt",
    "updated_at": "2026-08-29T10:40:30Z",
    "labels": ["active", "captain-ask"],
    "dependency_count": 0,
    "dependent_count": 0,
    "comment_count": 0
  }
]`

const allFixture = `[
  {
    "id": "fm-rbc",
    "title": "Update treehouse + fix pool-entry wedge",
    "description": "New spawns timed out twice.",
    "status": "closed",
    "priority": 0,
    "issue_type": "task",
    "assignee": "Jon Roosevelt",
    "created_at": "2026-08-30T01:08:20Z",
    "updated_at": "2026-08-30T02:39:22Z",
    "dependency_count": 2,
    "dependent_count": 1,
    "comment_count": 3
  }
]`

const showFixture = `[
  {
    "id": "fm-ju3",
    "title": "ORDER: beads-tui public repo + prefix+H Herdr mapping",
    "description": "RooseveltAdvisors/beads-tui created (public).",
    "notes": "Repo exists; dispatching TUI v0 build worker now.",
    "status": "open",
    "priority": 1,
    "issue_type": "task",
    "owner": "rooseveltadvisors@gmail.com",
    "created_at": "2026-08-30T00:00:00Z",
    "updated_at": "2026-08-30T00:05:00Z",
    "dependency_count": 0,
    "dependent_count": 0,
    "comment_count": 0
  }
]`

const depsDownFixture = `[
  {
    "id": "fm-5l0",
    "title": "Needs the other thing done first",
    "status": "open",
    "priority": 2,
    "issue_type": "task",
    "dependency_type": "blocks"
  }
]`

const depsUpFixture = `[
  {
    "id": "fm-4dt",
    "title": "Waiting on A",
    "status": "in_progress",
    "priority": 2,
    "issue_type": "task",
    "dependency_type": "blocks"
  }
]`

const statusesFixture = `{
  "built_in_statuses": [
    {"category": "active", "name": "open", "icon": "○"},
    {"category": "wip", "name": "in_progress", "icon": "◐"},
    {"category": "wip", "name": "blocked", "icon": "●"},
    {"category": "frozen", "name": "deferred", "icon": "❄"},
    {"category": "done", "name": "closed", "icon": "✓"},
    {"category": "frozen", "name": "pinned", "icon": "📌"},
    {"category": "wip", "name": "hooked", "icon": "◇"}
  ],
  "custom_statuses": [
    {"name": "awaiting_review", "category": "active"}
  ],
  "schema_version": 1
}`

func TestListBuildsRightArgs(t *testing.T) {
	var gotArgs []string
	c := stubClient(t, func(args []string) (string, string, error) {
		gotArgs = args
		return readyFixture, "", nil
	})
	issues, err := c.List(context.Background(), ViewOpen)
	if err != nil {
		t.Fatalf("List(open): %v", err)
	}
	want := []string{"list", "--status", "open", "--json", "-n", "0"}
	if strings.Join(gotArgs, " ") != strings.Join(want, " ") {
		t.Errorf("args = %q, want %q", gotArgs, want)
	}
	if len(issues) != 1 {
		t.Fatalf("got %d issues, want 1", len(issues))
	}
	i := issues[0]
	if i.ID != "fm-2fw" || i.Title != "Doug: schedule the special board meeting" {
		t.Errorf("unexpected issue: %+v", i)
	}
	if i.Status != "open" || i.Priority != 1 || i.IssueType != "task" {
		t.Errorf("unexpected fields: status=%q priority=%d type=%q", i.Status, i.Priority, i.IssueType)
	}
	if i.ParentID != "fm-parent" {
		t.Errorf("parent_id = %q, want fm-parent", i.ParentID)
	}
	if len(i.Labels) != 2 || i.Labels[1] != "captain-ask" {
		t.Errorf("labels = %v, want [active captain-ask]", i.Labels)
	}
}

func TestListViewVariants(t *testing.T) {
	for _, tc := range []struct {
		view View
		want string
	}{
		{ViewReady, "list --ready --json -n 0"},
		{ViewOpen, "list --status open --json -n 0"},
		{ViewAll, "list --all --json -n 0"},
	} {
		var gotArgs []string
		c := stubClient(t, func(args []string) (string, string, error) {
			gotArgs = args
			return allFixture, "", nil
		})
		issues, err := c.List(context.Background(), tc.view)
		if err != nil {
			t.Fatalf("List(%s): %v", tc.view, err)
		}
		if strings.Join(gotArgs, " ") != tc.want {
			t.Errorf("List(%s) args = %q, want %q", tc.view, gotArgs, tc.want)
		}
		if issues[0].ID != "fm-rbc" {
			t.Errorf("List(%s) id = %q, want fm-rbc", tc.view, issues[0].ID)
		}
		if issues[0].Description != "New spawns timed out twice." {
			t.Errorf("description = %q, want the fixture body", issues[0].Description)
		}
	}
}

func TestListAllBuildsRightArgs(t *testing.T) {
	var gotArgs []string
	c := stubClient(t, func(args []string) (string, string, error) {
		gotArgs = args
		return allFixture, "", nil
	})
	issues, err := c.ListAll(context.Background())
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if strings.Join(gotArgs, " ") != "list --all --json -n 0" {
		t.Errorf("ListAll args = %q", gotArgs)
	}
	if len(issues) != 1 || issues[0].ID != "fm-rbc" {
		t.Errorf("ListAll issues = %+v", issues)
	}
}

func TestListInvalidView(t *testing.T) {
	c := stubClient(t, func(args []string) (string, string, error) {
		t.Error("run must not be called for an invalid view")
		return "", "", nil
	})
	if _, err := c.List(context.Background(), View("")); err == nil {
		t.Fatal("expected error for invalid view")
	}
}

func TestAllViewsAreStableAndDistinct(t *testing.T) {
	if len(AllViews) != 3 || AllViews[0] != ViewReady || AllViews[1] != ViewOpen || AllViews[2] != ViewAll {
		t.Fatalf("views = %v, want ready/open/all", AllViews)
	}
	for _, view := range AllViews {
		if !view.Valid() || view.Label() == "" {
			t.Fatalf("invalid board view: %q", view)
		}
	}
}

func TestShow(t *testing.T) {
	c := stubClient(t, func(args []string) (string, string, error) {
		want := "show fm-ju3 --json"
		if strings.Join(args, " ") != want {
			t.Errorf("args = %q, want %q", args, want)
		}
		return showFixture, "", nil
	})
	issue, err := c.Show(context.Background(), "fm-ju3")
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if issue.ID != "fm-ju3" || issue.Notes != "Repo exists; dispatching TUI v0 build worker now." {
		t.Errorf("unexpected issue: %+v", issue)
	}
	if issue.Description != "RooseveltAdvisors/beads-tui created (public)." {
		t.Errorf("description = %q", issue.Description)
	}
}

func TestShowEmptyResult(t *testing.T) {
	c := stubClient(t, func(args []string) (string, string, error) { return "[]", "", nil })
	if _, err := c.Show(context.Background(), "fm-nope"); err == nil {
		t.Fatal("expected error for empty show result")
	}
}

func TestDepsDirections(t *testing.T) {
	var gotArgs []string
	c := stubClient(t, func(args []string) (string, string, error) {
		gotArgs = args
		if strings.Contains(strings.Join(args, " "), "direction") {
			return depsUpFixture, "", nil
		}
		return depsDownFixture, "", nil
	})
	down, err := c.Deps(context.Background(), "fm-4dt-x", false)
	if err != nil {
		t.Fatalf("Deps(down): %v", err)
	}
	if strings.Join(gotArgs, " ") != "dep list fm-4dt-x --json" {
		t.Errorf("down args = %q", gotArgs)
	}
	if len(down) != 1 || down[0].ID != "fm-5l0" || down[0].DependencyType != "blocks" {
		t.Errorf("unexpected down deps: %+v", down)
	}
	up, err := c.Deps(context.Background(), "fm-4dt-x", true)
	if err != nil {
		t.Fatalf("Deps(up): %v", err)
	}
	if strings.Join(gotArgs, " ") != "dep list fm-4dt-x --json --direction up" {
		t.Errorf("up args = %q", gotArgs)
	}
	if len(up) != 1 || up[0].ID != "fm-4dt" || up[0].Status != "in_progress" {
		t.Errorf("unexpected up deps: %+v", up)
	}
}

func TestDepsBatchGroupsEdgesByAnchor(t *testing.T) {
	var calls []string
	c := stubClient(t, func(args []string) (string, string, error) {
		calls = append(calls, strings.Join(args, " "))
		switch args[2] {
		case "fm-a":
			return `[{"id":"fm-b","dependency_type":"blocks"}]`, "", nil
		case "fm-c":
			return `[{"id":"fm-b","dependency_type":"tracks"}]`, "", nil
		case "fm-b":
			return `[{"id":"fm-a","dependency_type":"blocks"},{"id":"fm-c","dependency_type":"tracks"}]`, "", nil
		default:
			return `[]`, "", nil
		}
	})
	down, err := c.DepsBatch(context.Background(), []string{"fm-a", "fm-c"}, false)
	if err != nil {
		t.Fatalf("DepsBatch(down): %v", err)
	}
	if strings.Join(calls, " | ") != "dep list fm-a --json | dep list fm-c --json" {
		t.Errorf("down args = %q", calls)
	}
	if len(down["fm-a"]) != 1 || down["fm-a"][0].ID != "fm-b" {
		t.Errorf("down edges = %+v", down)
	}

	up, err := c.DepsBatch(context.Background(), []string{"fm-b"}, true)
	if err != nil {
		t.Fatalf("DepsBatch(up): %v", err)
	}
	if calls[len(calls)-1] != "dep list fm-b --json --direction up" {
		t.Errorf("up args = %q", calls[len(calls)-1])
	}
	if len(up["fm-b"]) != 2 || up["fm-b"][0].ID != "fm-a" || up["fm-b"][1].DependencyType != "tracks" {
		t.Errorf("up edges = %+v", up)
	}
}

func TestStatuses(t *testing.T) {
	c := stubClient(t, func(args []string) (string, string, error) {
		return statusesFixture, "", nil
	})
	statuses, err := c.Statuses(context.Background())
	if err != nil {
		t.Fatalf("Statuses: %v", err)
	}
	if len(statuses) != 8 {
		t.Fatalf("got %d statuses, want 8", len(statuses))
	}
	if statuses[2].Name != "blocked" || statuses[2].Icon != "●" || statuses[2].Category != "wip" {
		t.Errorf("unexpected blocked status: %+v", statuses[2])
	}
	if statuses[7].Name != "awaiting_review" || statuses[7].Category != "active" {
		t.Errorf("unexpected custom status: %+v", statuses[7])
	}
}

func TestBdMissingFromPath(t *testing.T) {
	c := &Client{
		lookPath: func(string) (string, error) { return "", errors.New("executable file not found") },
		run: func(context.Context, string, ...string) (string, string, error) {
			t.Error("run must not be called when bd is missing")
			return "", "", nil
		},
	}
	_, err := c.List(context.Background(), ViewOpen)
	if err == nil || !strings.Contains(err.Error(), "bd not found in PATH") {
		t.Fatalf("expected bd-not-found error, got %v", err)
	}
}

func TestBdFailureCarriesStderr(t *testing.T) {
	c := stubClient(t, func(args []string) (string, string, error) {
		return "", "No active beads workspace found.\nHint: check BEADS_DIR/worktree setup", errors.New("exit status 1")
	})
	_, err := c.List(context.Background(), ViewOpen)
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"bd list --status open --json -n 0", "No active beads workspace found", "BEADS_DIR"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
}

func TestNonJSONOutputIsTranslated(t *testing.T) {
	c := stubClient(t, func(args []string) (string, string, error) {
		return "HTTPServer listening on :8080\n", "", nil
	})
	_, err := c.Statuses(context.Background())
	if err == nil {
		t.Fatal("expected error for non-JSON stdout")
	}
	if !strings.Contains(err.Error(), "not JSON") {
		t.Errorf("error should mention non-JSON output, got %q", err)
	}
}

func TestJsonCallNeverLeaksRawOutput(t *testing.T) {
	c := stubClient(t, func(args []string) (string, string, error) {
		// A hostile/garbage stdout: the error must come from stderr or the
		// wrapper, never from dumping raw stdout.
		return "SECRET-STDOUT garbage {{", "bd: workspace is locked", errors.New("exit status 1")
	})
	_, err := c.List(context.Background(), ViewOpen)
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "SECRET-STDOUT") {
		t.Errorf("raw stdout leaked into error: %q", err)
	}
	if !strings.Contains(err.Error(), "workspace is locked") {
		t.Errorf("stderr hint missing from error: %q", err)
	}
}

func TestEmptyListIsNotAnError(t *testing.T) {
	c := stubClient(t, func(args []string) (string, string, error) { return "[]", "", nil })
	issues, err := c.List(context.Background(), ViewOpen)
	if err != nil {
		t.Fatalf("empty list must not error: %v", err)
	}
	if len(issues) != 0 {
		t.Errorf("got %d issues, want 0", len(issues))
	}
}
