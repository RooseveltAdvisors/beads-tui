package bd

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
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
		// Keep the bounded retry schedule fast under test.
		waitOverride: time.Millisecond,
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

const commentsFixture = `[
  {"id":"c-old","issue_id":"fm-ju3","author":"Jon","text":"First note","created_at":"2026-09-07T12:00:00Z"},
  {"id":"c-new","issue_id":"fm-ju3","author":"Ada","text":"Second note","created_at":"2026-09-07T13:00:00Z"}
]`

func TestCommentsBuildsRightArgsAndParsesFixture(t *testing.T) {
	var gotArgs []string
	c := stubClient(t, func(args []string) (string, string, error) {
		gotArgs = args
		return commentsFixture, "", nil
	})
	comments, err := c.Comments(context.Background(), "fm-ju3")
	if err != nil {
		t.Fatalf("Comments: %v", err)
	}
	if strings.Join(gotArgs, " ") != "comments fm-ju3 --json" {
		t.Fatalf("args = %q", gotArgs)
	}
	if len(comments) != 2 || comments[1].Author != "Ada" || comments[1].Text != "Second note" {
		t.Fatalf("comments = %+v", comments)
	}
}

func TestAddCommentUsesStdinAdapter(t *testing.T) {
	var gotArgs []string
	var gotInput string
	c := &Client{
		lookPath: func(string) (string, error) { return "/fake/bd", nil },
		runInput: func(_ context.Context, _ string, input string, args ...string) (string, string, error) {
			gotInput, gotArgs = input, args
			return "", "", nil
		},
	}
	if err := c.AddComment(context.Background(), "fm-ju3", "comment from TUI"); err != nil {
		t.Fatalf("AddComment: %v", err)
	}
	if strings.Join(gotArgs, " ") != "comment fm-ju3 --stdin" || gotInput != "comment from TUI" {
		t.Fatalf("args=%q input=%q", gotArgs, gotInput)
	}
}

func TestAddCommentRetriesBusyStore(t *testing.T) {
	attempts := 0
	c := &Client{
		lookPath: func(string) (string, error) { return "/fake/bd", nil },
		runInput: func(context.Context, string, string, ...string) (string, string, error) {
			attempts++
			return "", "database is locked", errors.New("exit status 1")
		},
		waitOverride: time.Millisecond,
	}
	err := c.AddComment(context.Background(), "fm-ju3", "retry me")
	if err == nil || !strings.Contains(err.Error(), "busy or locked") {
		t.Fatalf("err = %v, want sanitized busy error", err)
	}
	if attempts != callAttempts {
		t.Fatalf("attempts = %d, want %d", attempts, callAttempts)
	}
	if strings.Contains(err.Error(), "database is locked") {
		t.Fatalf("raw lock diagnostic leaked: %v", err)
	}
}

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

func TestListStatusVariants(t *testing.T) {
	for _, tc := range []struct {
		view View
		want string
	}{
		{ViewOpen, "list --status open --json -n 0"},
		{ViewInProgress, "list --status in_progress --json -n 0"},
		{ViewBlocked, "list --status blocked --json -n 0"},
		{ViewClosed, "list --status closed --json -n 0"},
		{ViewDeferred, "list --status deferred --json -n 0"},
		{View("awaiting_review"), "list --status awaiting_review --json -n 0"},
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

func TestDefaultViewsAreStableAndDistinct(t *testing.T) {
	want := []View{ViewOpen, ViewInProgress, ViewBlocked, ViewClosed, ViewDeferred}
	views := DefaultViews()
	if len(views) != len(want) {
		t.Fatalf("views = %v, want %v", views, want)
	}
	for i, view := range views {
		if view != want[i] {
			t.Fatalf("view %d = %q, want %q", i, view, want[i])
		}
		if !view.Valid() || view.Label() == "" {
			t.Fatalf("invalid status view: %q", view)
		}
	}
}

func TestViewsFromStatusesIncludesCustomStatuses(t *testing.T) {
	views := ViewsFromStatuses([]StatusInfo{
		{Name: "open"},
		{Name: "awaiting_review"},
		{Name: " AWAITING_REVIEW "},
	})
	if len(views) != 6 || views[5] != View("awaiting_review") {
		t.Fatalf("views = %v, want built-ins plus awaiting_review", views)
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
	var callsMu sync.Mutex
	c := stubClient(t, func(args []string) (string, string, error) {
		callsMu.Lock()
		calls = append(calls, strings.Join(args, " "))
		callsMu.Unlock()
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
	callsMu.Lock()
	downCalls := append([]string(nil), calls...)
	callsMu.Unlock()
	if len(downCalls) != 2 {
		t.Fatalf("down calls = %q, want two calls", downCalls)
	}
	for _, want := range []string{"dep list fm-a --json", "dep list fm-c --json"} {
		found := false
		for _, call := range downCalls {
			if call == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("down args = %q, missing %q", downCalls, want)
		}
	}
	if len(down["fm-a"]) != 1 || down["fm-a"][0].ID != "fm-b" {
		t.Errorf("down edges = %+v", down)
	}

	up, err := c.DepsBatch(context.Background(), []string{"fm-b"}, true)
	if err != nil {
		t.Fatalf("DepsBatch(up): %v", err)
	}
	callsMu.Lock()
	lastCall := calls[len(calls)-1]
	callsMu.Unlock()
	if lastCall != "dep list fm-b --json --direction up" {
		t.Errorf("up args = %q", lastCall)
	}
	if len(up["fm-b"]) != 2 || up["fm-b"][0].ID != "fm-a" || up["fm-b"][1].DependencyType != "tracks" {
		t.Errorf("up edges = %+v", up)
	}
}

func TestDepsBatchRunsCallsConcurrently(t *testing.T) {
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	c := stubClient(t, func([]string) (string, string, error) {
		started <- struct{}{}
		<-release
		return `[]`, "", nil
	})
	done := make(chan error, 1)
	go func() {
		_, err := c.DepsBatch(context.Background(), []string{"fm-a", "fm-b"}, false)
		done <- err
	}()
	for range 2 {
		select {
		case <-started:
		case <-time.After(time.Second):
			close(release)
			<-done
			t.Fatal("DepsBatch serialized single-ID calls")
		}
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("DepsBatch: %v", err)
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
		return "SECRET-STDOUT garbage {{", "bd: exploded badly", errors.New("exit status 1")
	})
	_, err := c.List(context.Background(), ViewOpen)
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "SECRET-STDOUT") {
		t.Errorf("raw stdout leaked into error: %q", err)
	}
	if !strings.Contains(err.Error(), "exploded badly") {
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

func TestTimeoutIsNamedPlainly(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	client := &Client{
		lookPath: func(string) (string, error) { return "/bin/echo", nil },
		run: func(context.Context, string, ...string) (string, string, error) {
			// What Go's os/exec reports after a context kill.
			return "", "", errors.New("signal: killed")
		},
	}
	_, err := client.ListStatus(ctx, "open")
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v, want a plain timeout message", err)
	}
	if strings.Contains(err.Error(), "signal: killed") {
		t.Fatalf("err leaks the raw kill signal: %v", err)
	}
}

// busyTimeoutStub returns a client whose every bd attempt hangs until the
// attempt deadline kills it (the SIGKILL path), plus the attempt count.
func busyTimeoutStub(t *testing.T) (*Client, *int) {
	t.Helper()
	attempts := 0
	c := &Client{
		lookPath: func(string) (string, error) { return "/fake/bd", nil },
		run: func(context.Context, string, ...string) (string, string, error) {
			// What the production run reports when the attempt deadline
			// kills a bd wedged on the store lock.
			return "", "", context.DeadlineExceeded
		},
		waitOverride: time.Millisecond,
	}
	realRun := c.run
	c.run = func(ctx context.Context, path string, args ...string) (string, string, error) {
		attempts++
		return realRun(ctx, path, args...)
	}
	return c, &attempts
}

// A locked store must be retried, not dropped: the first busy attempt
// recovers on a later one.
func TestBusyStoreIsRetriedUntilSuccess(t *testing.T) {
	c, attempts := busyTimeoutStub(t)
	failures := 2
	c.run = func(ctx context.Context, path string, args ...string) (string, string, error) {
		*attempts++
		if failures > 0 {
			failures--
			return "", "", context.DeadlineExceeded
		}
		return readyFixture, "", nil
	}
	issues, err := c.List(context.Background(), ViewOpen)
	if err != nil {
		t.Fatalf("transient lock must recover, got %v", err)
	}
	if len(issues) != 1 || issues[0].ID != "fm-2fw" {
		t.Errorf("unexpected issues after retry: %+v", issues)
	}
	if *attempts != 3 {
		t.Errorf("attempts = %d, want 3 (two busy retries then success)", *attempts)
	}
}

// Bounded retries: once the schedule is exhausted the single sanitized
// busy error is surfaced, never the raw bd output.
func TestBusyStoreSurfacesBusyMessageAfterBoundedRetries(t *testing.T) {
	c, attempts := busyTimeoutStub(t)
	_, err := c.List(context.Background(), ViewOpen)
	if err == nil {
		t.Fatal("expected busy error after exhausting retries")
	}
	if !strings.Contains(err.Error(), "timed out; the beads store is busy or locked") {
		t.Errorf("err = %v, want the plain busy message", err)
	}
	if *attempts != callAttempts {
		t.Errorf("attempts = %d, want %d", *attempts, callAttempts)
	}
}

// A lock diagnostic on bd's own stderr is the same transient condition:
// retry, then surface the busy message.
func TestLockDiagnosticOnStderrIsRetried(t *testing.T) {
	c := stubClient(t, func(args []string) (string, string, error) {
		return "", "bd: database is locked", errors.New("exit status 1")
	})
	_, err := c.List(context.Background(), ViewOpen)
	if err == nil || !strings.Contains(err.Error(), "timed out; the beads store is busy or locked") {
		t.Fatalf("err = %v, want the plain busy message", err)
	}
}

// A non-busy failure is not retried and keeps bd's actionable stderr.
func TestNonBusyFailureIsNotRetried(t *testing.T) {
	calls := 0
	c := stubClient(t, func(args []string) (string, string, error) {
		calls++
		return "", "bd: no workspace found", errors.New("exit status 1")
	})
	_, err := c.List(context.Background(), ViewOpen)
	if err == nil || !strings.Contains(err.Error(), "no workspace found") {
		t.Fatalf("err = %v, want the stderr hint", err)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1 (no retry for non-busy errors)", calls)
	}
}
