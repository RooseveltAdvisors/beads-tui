package tui

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RooseveltAdvisors/beads-tui/internal/bd"
	tea "github.com/charmbracelet/bubbletea"
)

// fakeClient serves canned data without touching a real Beads store.
type fakeClient struct {
	mu             sync.Mutex
	issues         map[bd.View][]bd.Issue
	issue          *bd.Issue
	issueByID      map[string]*bd.Issue
	down           []bd.DepRecord
	downByID       map[string][]bd.DepRecord
	up             []bd.DepRecord
	upByID         map[string][]bd.DepRecord
	statuses       []bd.StatusInfo
	commentsByID   map[string][]bd.Comment
	commentAdds    []string
	failComments   error
	failCommentAdd error

	failList    error
	failShow    error
	listCalls   int
	depCalls    int
	showCalls   int
	showLog     []string
	lastShowID  string
	queuedLists [][]bd.Issue
	batchCalls  int
}

func (f *fakeClient) List(_ context.Context, view bd.View) ([]bd.Issue, error) {
	f.listCalls++
	if len(f.queuedLists) > 0 {
		issues := f.queuedLists[0]
		f.queuedLists = f.queuedLists[1:]
		return issues, f.failList
	}
	if view == bd.ViewReady {
		if issues, ok := f.issues[bd.ViewReady]; ok {
			return issues, f.failList
		}
		// Default fake semantics: ready work is the open board minus blocked
		// rows, mirroring bd's claimable-work definition.
		var ready []bd.Issue
		for _, issue := range f.issues[bd.ViewOpen] {
			if issue.Status != "blocked" && issue.Status != "deferred" {
				ready = append(ready, issue)
			}
		}
		return ready, f.failList
	}
	return f.issues[view], f.failList
}

func (f *fakeClient) ListAll(context.Context) ([]bd.Issue, error) {
	seen := make(map[string]struct{})
	var issues []bd.Issue
	for _, viewIssues := range f.issues {
		for _, issue := range viewIssues {
			if issue.ID == "" {
				continue
			}
			if _, ok := seen[issue.ID]; ok {
				continue
			}
			seen[issue.ID] = struct{}{}
			issues = append(issues, issue)
		}
	}
	return issues, nil
}

func (f *fakeClient) Show(_ context.Context, id string) (*bd.Issue, error) {
	f.showCalls++
	f.showLog = append(f.showLog, id)
	f.lastShowID = id
	if f.failShow != nil {
		return nil, f.failShow
	}
	if f.issueByID != nil {
		issue, ok := f.issueByID[id]
		if !ok {
			return nil, fmt.Errorf("bd show %s: no issue returned", id)
		}
		return issue, nil
	}
	return f.issue, nil
}

func (f *fakeClient) Deps(_ context.Context, id string, up bool) ([]bd.DepRecord, error) {
	f.mu.Lock()
	f.depCalls++
	f.mu.Unlock()
	if up {
		if f.upByID != nil {
			return f.upByID[id], nil
		}
		return f.up, nil
	}
	if f.downByID != nil {
		return f.downByID[id], nil
	}
	return f.down, nil
}

func (f *fakeClient) DepsBatch(_ context.Context, ids []string, up bool) (map[string][]bd.DepRecord, error) {
	f.mu.Lock()
	f.batchCalls++
	f.mu.Unlock()
	result := make(map[string][]bd.DepRecord, len(ids))
	for _, id := range ids {
		if up {
			if f.upByID != nil {
				result[id] = f.upByID[id]
			} else {
				result[id] = f.up
			}
			continue
		}
		if f.downByID != nil {
			result[id] = f.downByID[id]
		} else {
			result[id] = f.down
		}
	}
	return result, nil
}

func (f *fakeClient) Statuses(context.Context) ([]bd.StatusInfo, error) {
	return f.statuses, nil
}

func (f *fakeClient) Comments(_ context.Context, id string) ([]bd.Comment, error) {
	if f.failComments != nil {
		return nil, f.failComments
	}
	return append([]bd.Comment(nil), f.commentsByID[id]...), nil
}

func (f *fakeClient) AddComment(_ context.Context, id, text string) error {
	if f.failCommentAdd != nil {
		return f.failCommentAdd
	}
	f.commentAdds = append(f.commentAdds, id+":"+text)
	f.commentsByID[id] = append(f.commentsByID[id], bd.Comment{
		IssueID: id, Author: "tester", Text: text, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	})
	return nil
}

func testIssues() []bd.Issue {
	return []bd.Issue{
		{ID: "fm-aaa", Title: "Alpha task", Status: "open", Priority: 0, IssueType: "task"},
		{ID: "fm-bbb", Title: "Beta blocked task", Status: "blocked", Priority: 1, IssueType: "bug", DependencyCount: 1},
		{ID: "fm-ccc", Title: "Gamma done task", Status: "closed", Priority: 2, IssueType: "task"},
	}
}

func testDetailOf(id string) *bd.Issue {
	return &bd.Issue{
		ID:          id,
		Title:       "Beta blocked task",
		Description: "Needs the alpha milestone before it can start. Second sentence here.",
		Notes:       "Assigned last sprint.",
		Status:      "blocked",
		Priority:    2,
		IssueType:   "bug",
		Assignee:    "Jane",
	}
}

func testDetail() *bd.Issue { return testDetailOf("fm-bbb") }

// newTestModel builds a model with sane defaults over a fake backend.
func newTestModel(f *fakeClient) Model {
	if f == nil {
		f = &fakeClient{}
	}
	if f.issues == nil {
		f.issues = map[bd.View][]bd.Issue{bd.ViewOpen: testIssues()}
	}
	if f.issue == nil {
		f.issue = testDetail()
	}
	m := New(f)
	m.width, m.height = 160, 40
	m.detailDebounce = 0 // fire debounce ticks immediately in tests
	return m
}

// runCmd executes a command and applies every message it produces, including
// nested batches and the commands Update returns for those messages, so async
// loads land like they do in the real program.
func runCmd(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	if cmd == nil {
		return m
	}
	msg := cmd()
	if msg == nil {
		return m
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			m = runCmd(t, m, c)
		}
		return m
	}
	updated, next := m.Update(msg)
	nm, ok := updated.(Model)
	if !ok {
		t.Fatalf("update returned %T, want Model", updated)
	}
	return runCmd(t, nm, next)
}

// drive loads the initial board snapshot like the real Init flow.
func drive(t *testing.T, f *fakeClient) Model {
	t.Helper()
	if f == nil {
		f = &fakeClient{}
	}
	m := newTestModel(f)
	updated, cmd := m.Update(boardMsg{view: m.view, generation: m.boardGen, issues: f.issues[bd.ViewOpen], err: nil})
	m = updated.(Model)
	return runCmd(t, m, cmd)
}

// teaKeyMsg builds a KeyMsg whose String() matches the given key name.
func teaKeyMsg(s string) tea.KeyMsg {
	k := tea.Key{Type: tea.KeyRunes, Runes: []rune(s)}
	switch s {
	case "up":
		k.Type = tea.KeyUp
	case "down":
		k.Type = tea.KeyDown
	case "left":
		k.Type = tea.KeyLeft
	case "right":
		k.Type = tea.KeyRight
	case "enter":
		k.Type = tea.KeyEnter
	case "esc":
		k.Type = tea.KeyEsc
	case " ":
		k.Type = tea.KeySpace
	case "pgup":
		k.Type = tea.KeyPgUp
	case "pgdown":
		k.Type = tea.KeyPgDown
	case "ctrl+c":
		k.Type = tea.KeyCtrlC
	case "ctrl+f":
		k.Type = tea.KeyCtrlF
	case "ctrl+b":
		k.Type = tea.KeyCtrlB
	case "ctrl+d":
		k.Type = tea.KeyCtrlD
	case "ctrl+u":
		k.Type = tea.KeyCtrlU
	}
	return tea.KeyMsg(k)
}

func sendKey(t *testing.T, m Model, key string) Model {
	t.Helper()
	return applyMsg(t, m, teaKeyMsg(key))
}

// step applies a keypress and then drains every command Update returned, so
// async loads (board/detail fetches) land like they do in the real program.
func step(t *testing.T, m Model, key string) Model {
	t.Helper()
	updated, cmd := m.Update(teaKeyMsg(key))
	nm := updated.(Model)
	return runCmd(t, nm, cmd)
}

// applyMsg drives one message through Update, asserting the model type.
func applyMsg(t *testing.T, m Model, msg tea.Msg) Model {
	t.Helper()
	updated, _ := m.Update(msg)
	nm, ok := updated.(Model)
	if !ok {
		t.Fatalf("update returned %T, want Model", updated)
	}
	return nm
}

func TestBoardLoadAndRender(t *testing.T) {
	m := drive(t, nil)
	if len(m.rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(m.rows))
	}
	view := stripANSI(m.View())
	for _, want := range []string{
		"beads-tui", "ready board", "fm-aaa", "Alpha task", "fm-bbb",
		"Beta blocked task", "fm-ccc", "Gamma done task", "[1]ready", "[2]open",
		"[3]in_progress", "[4]blocked", "[5]closed", "[6]deferred",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q", want)
		}
	}
	if got := len(strings.Split(m.View(), "\n")); got != 40 {
		t.Errorf("view height = %d lines, want 40", got)
	}
}

func TestInitialBoardShowsLoading(t *testing.T) {
	m := newTestModel(&fakeClient{})
	view := stripANSI(m.View())
	if !strings.Contains(view, "Loading board") {
		t.Fatalf("initial board should show loading state:\n%s", view)
	}
	if strings.Contains(view, "No open issues") {
		t.Fatalf("initial board must not claim open work is empty:\n%s", view)
	}
}

func TestSelectionMovesAndLoadsDetail(t *testing.T) {
	f := &fakeClient{down: []bd.DepRecord{{ID: "fm-aaa", Title: "Alpha task", Status: "open", DependencyType: "blocks"}}}
	m := drive(t, f)
	m = step(t, m, "j")
	if m.selected != 1 {
		t.Fatalf("selected = %d, want 1", m.selected)
	}
	// The board row already holds every detail field, so loading detail for
	// a bead on the board must not round-trip `bd show` at all.
	if f.showCalls != 0 {
		t.Errorf("detail load made %d show calls, want 0 (board data covers it)", f.showCalls)
	}
	if m.detail == nil || m.detail.ID != "fm-bbb" {
		t.Fatalf("detail not applied: %+v", m.detail)
	}
	if len(m.down) != 1 || m.down[0].ID != "fm-aaa" {
		t.Errorf("down deps not applied: %+v", m.down)
	}
	view := stripANSI(m.View())
	if !strings.Contains(view, "Depends on (1)") || !strings.Contains(view, "Alpha task") {
		t.Errorf("detail pane missing dependency edges:\n%s", view)
	}
}

func TestTreeExpandCollapseAndFlatToggle(t *testing.T) {
	f := &fakeClient{issue: testDetail()}
	m := newTestModel(f)
	issues := []bd.Issue{
		{ID: "root", Title: "Root", Status: "open", Priority: 1},
		{ID: "child", Title: "Child", Status: "open", Priority: 2, ParentID: "root"},
	}
	m = applyMsg(t, m, boardMsg{view: m.view, generation: m.boardGen, issues: issues, deps: map[string][]bd.DepRecord{}})
	if !m.treeMode || len(m.rows) != 2 {
		t.Fatalf("initial tree = %v rows, want expanded tree", m.rows)
	}
	m = sendKey(t, m, "enter")
	if len(m.rows) != 1 || m.expanded["root"] {
		t.Fatalf("after collapse rows=%d expanded=%v", len(m.rows), m.expanded)
	}
	m = sendKey(t, m, "l")
	if m.focus == FocusDetail || len(m.rows) != 2 || !m.expanded["root"] {
		t.Fatalf("l should unfold a folded node: focus=%v rows=%d expanded=%v", m.focus, len(m.rows), m.expanded)
	}
	m = sendKey(t, m, "h")
	if len(m.rows) != 1 || m.expanded["root"] {
		t.Fatalf("h should refold: rows=%d expanded=%v", len(m.rows), m.expanded)
	}
	m = sendKey(t, m, "L")
	if m.focus != FocusDetail || len(m.rows) != 1 {
		t.Fatalf("L should focus detail without expanding: focus=%v rows=%d", m.focus, len(m.rows))
	}
	m = sendKey(t, m, "esc")
	m = sendKey(t, m, "l")
	if m.focus == FocusDetail || len(m.rows) != 2 {
		t.Fatalf("l should unfold again: focus=%v rows=%d", m.focus, len(m.rows))
	}
	plain := stripANSI(m.View())
	if !strings.Contains(plain, "└──") {
		t.Errorf("tree view missing connector:\n%s", plain)
	}
	m = sendKey(t, m, "v")
	if m.treeMode || len(m.treeRows) != 0 || len(m.rows) != 2 {
		t.Fatalf("v should switch to flat view: tree=%v treeRows=%d rows=%d", m.treeMode, len(m.treeRows), len(m.rows))
	}
}

func TestTreeEnterOpensLeafDetail(t *testing.T) {
	m := newTestModel(nil)
	m = applyMsg(t, m, boardMsg{view: m.view, generation: m.boardGen, issues: []bd.Issue{{ID: "leaf", Status: "open"}}})
	m = sendKey(t, m, "enter")
	if m.focus != FocusDetail {
		t.Fatalf("leaf enter focus = %v, want detail", m.focus)
	}
}

func TestSelectionClampedAtEdges(t *testing.T) {
	m := drive(t, nil)
	m = sendKey(t, m, "G")
	if m.selected != 2 {
		t.Fatalf("G selected = %d, want 2", m.selected)
	}
	m = sendKey(t, m, "j")
	if m.selected != 2 {
		t.Errorf("j past bottom: selected = %d, want 2", m.selected)
	}
	m = sendKey(t, m, "g")
	if m.selected != 0 {
		t.Errorf("g selected = %d, want 0", m.selected)
	}
	m = sendKey(t, m, "k")
	if m.selected != 0 {
		t.Errorf("k past top: selected = %d, want 0", m.selected)
	}
}

func TestStaleDetailIsDiscarded(t *testing.T) {
	m := drive(t, nil)
	m = applyMsg(t, m, detailMsg{id: "fm-aaa", generation: m.detailGen, issue: testDetailOf("fm-aaa"), err: nil})
	m = sendKey(t, m, "j") // now on fm-bbb; its request is in flight
	if m.rows[m.selected].ID != "fm-bbb" {
		t.Fatalf("selection = %q, want fm-bbb", m.rows[m.selected].ID)
	}
	// A response for a bead that is not the current selection must be dropped.
	nm := applyMsg(t, m, detailMsg{id: "fm-ccc", generation: m.detailGen, issue: testDetailOf("fm-ccc"), err: nil})
	if nm.detail != nil && nm.detail.ID == "fm-ccc" {
		t.Errorf("stale detail (fm-ccc) applied while selection is fm-bbb")
	}
	// The real response lands normally.
	nm = applyMsg(t, nm, detailMsg{id: "fm-bbb", generation: nm.detailGen, issue: testDetail(), err: nil})
	if nm.detail == nil || nm.detail.ID != "fm-bbb" {
		t.Errorf("current selection's detail not applied: %+v", nm.detail)
	}
	if nm.checking {
		t.Error("checking should clear once the detail response lands")
	}
}

// showLogContains reports whether the fake served Show for id.
func showLogContains(f *fakeClient, id string) bool {
	for _, shown := range f.showLog {
		if shown == id {
			return true
		}
	}
	return false
}

// Regression (fm-isv6): a bead claimed into another status (open ->
// in_progress three minutes after creation) vanished from the captain's
// persisted open tab, and searching its ID there reported "No matches".
// The graph snapshot already holds every issue, so a search must find beads
// outside the active status tab.
func TestSearchFindsBeadOnOtherStatusTab(t *testing.T) {
	claimed := bd.Issue{ID: "fm-0nli", Title: "Update Zeta distribution for new keybindings", Status: "in_progress", Priority: 1, IssueType: "task", Assignee: "fork-converge"}
	f := &fakeClient{issues: map[bd.View][]bd.Issue{
		bd.ViewOpen:       {{ID: "fm-aaa", Title: "Alpha task", Status: "open", Priority: 0, IssueType: "task"}},
		bd.ViewInProgress: {claimed},
	}}
	m := drive(t, f)
	if len(m.graphRows) == 0 {
		t.Fatal("graph snapshot did not load; the cross-tab search path is untested")
	}

	// Without a filter the tab stays a strict status slice.
	if m.rowByID("fm-0nli") {
		t.Fatal("unfiltered open/ready tab leaked an in_progress bead")
	}

	// Searching the claimed bead's ID on the default tab finds it.
	m.filter = ParseSearchFilter("fm-0nli")
	m.projectRows("")
	if !m.rowByID("fm-0nli") {
		t.Fatalf("search missed the bead on another status tab: rows=%+v", issueIDsOf(m.rows))
	}

	// Title search reaches it too, and existing rows are not duplicated.
	m.filter = ParseSearchFilter("zeta")
	m.projectRows("")
	if !m.rowByID("fm-0nli") {
		t.Fatal("title search missed the cross-tab bead")
	}
	seen := map[string]int{}
	for _, row := range m.rows {
		seen[row.ID]++
	}
	for id, n := range seen {
		if n != 1 {
			t.Fatalf("row %s appeared %d times after graph-wide search", id, n)
		}
	}

	// Clearing the search restores the strict tab view.
	m.filter = Filter{}
	m.projectRows("")
	if m.rowByID("fm-0nli") {
		t.Fatal("clearing the search kept cross-status rows on the tab")
	}
}

func rowByID(rows []bd.Issue, id string) bool {
	for _, row := range rows {
		if row.ID == id {
			return true
		}
	}
	return false
}

func (m Model) rowByID(id string) bool { return rowByID(m.rows, id) }

func issueIDsOf(rows []bd.Issue) []string {
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	return ids
}

func TestLowercaseRReloadsKeepingViewSortAndFilter(t *testing.T) {
	// Keep every fixture row claimable so the selection survives the reload.
	f := &fakeClient{issues: map[bd.View][]bd.Issue{
		bd.ViewOpen:  testIssues(),
		bd.ViewReady: testIssues(),
	}}
	m := drive(t, f)
	m.sortMode = SortUpdated
	m.filter = ParseSearchFilter("task")
	m = sendKey(t, m, "j") // select fm-bbb so selection preservation is observable
	updated, cmd := m.Update(teaKeyMsg("r"))
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("r should reload the board")
	}
	if f.listCalls != 0 {
		t.Fatalf("r reload started bd before the command ran (%d calls)", f.listCalls)
	}
	m = runCmd(t, m, cmd)
	if f.listCalls != 1 {
		t.Fatalf("r reload ran bd list %d times, want 1", f.listCalls)
	}
	if m.view != bd.ViewReady || m.sortMode != SortUpdated || !m.filter.Active() {
		t.Fatalf("r changed view/sort/filter: view %q sort %q filter %+v", m.view, m.sortMode, m.filter)
	}
	if len(m.rows) != 3 || m.rows[m.selected].ID != "fm-bbb" {
		t.Fatalf("reload lost rows/selection: rows=%+v selected=%d", m.rows, m.selected)
	}
	if m.boardErr != "" || m.reloadNotice != "" || m.reloadAttempts != 0 {
		t.Fatalf("successful reload left failure state: err=%q notice=%q attempts=%d", m.boardErr, m.reloadNotice, m.reloadAttempts)
	}
}

func TestReloadTimeoutKeepsRowsAndNotifies(t *testing.T) {
	m := newTestModel(nil)
	m = applyMsg(t, m, boardMsg{view: m.view, generation: m.boardGen, issues: testIssues(), timeout: bdTimeout})
	if len(m.rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(m.rows))
	}
	failed := applyMsg(t, m, boardMsg{
		view:       m.view,
		generation: m.boardGen,
		err:        errors.New("bd list --status open --json -n 0: context deadline exceeded"),
		timeout:    bdTimeout,
	})
	if len(failed.rows) != 3 {
		t.Fatalf("failed reload discarded rows: %d", len(failed.rows))
	}
	if !strings.Contains(failed.reloadNotice, "reload timed out after 8s") ||
		!strings.Contains(failed.reloadNotice, "still showing board from") ||
		!strings.Contains(failed.reloadNotice, "retrying") {
		t.Fatalf("reload notice = %q", failed.reloadNotice)
	}
	view := stripANSI(failed.View())
	for _, want := range []string{"Alpha task", "reload timed out after 8s"} {
		if !strings.Contains(view, want) {
			t.Errorf("view after failed reload missing %q:\n%s", want, view)
		}
	}
}

func TestBoardRetryBackoffAndAdaptiveTimeout(t *testing.T) {
	for _, tc := range []struct {
		attempt int
		want    time.Duration
	}{
		{1, 2 * time.Second},
		{2, 5 * time.Second},
		{3, 15 * time.Second},
		{9, 15 * time.Second},
	} {
		if got := boardRetryBackoff(tc.attempt); got != tc.want {
			t.Errorf("boardRetryBackoff(%d) = %v, want %v", tc.attempt, got, tc.want)
		}
	}
	// bd retries busy stores internally, so every board load gets the same
	// generous cap instead of a short first-attempt deadline.
	for _, attempt := range []int{0, 1, 2} {
		if got := boardLoadTimeout(attempt); got != boardRetryTimeout {
			t.Errorf("boardLoadTimeout(%d) = %v, want %v", attempt, got, boardRetryTimeout)
		}
	}
}

func TestBoardRetrySucceedsAndClearsFailureState(t *testing.T) {
	f := &fakeClient{}
	m := newTestModel(f)
	m = applyMsg(t, m, boardMsg{view: m.view, generation: m.boardGen, issues: testIssues(), timeout: bdTimeout})
	f.issues[bd.ViewOpen] = []bd.Issue{{ID: "fresh", Title: "Fresh board", Status: "open"}}
	m = applyMsg(t, m, boardMsg{view: m.view, generation: m.boardGen, err: errors.New("boom"), timeout: bdTimeout})
	if m.reloadAttempts != 1 {
		t.Fatalf("reloadAttempts = %d, want 1", m.reloadAttempts)
	}
	updated, cmd := m.Update(boardRetryMsg{})
	m = updated.(Model)
	if !m.loading || cmd == nil {
		t.Fatalf("retry did not restart the load: loading=%v cmd=%v", m.loading, cmd)
	}
	m = runCmd(t, m, cmd)
	if m.loading || m.reloadAttempts != 0 || m.reloadNotice != "" || m.boardErr != "" {
		t.Fatalf("retry left failure state: loading=%v attempts=%d notice=%q err=%q", m.loading, m.reloadAttempts, m.reloadNotice, m.boardErr)
	}
	if len(m.rows) != 1 || m.rows[0].ID != "fresh" {
		t.Fatalf("retry rows = %+v, want fresh board", m.rows)
	}
	if !m.boardLoadedAt.After(time.Time{}) {
		t.Error("retry success did not stamp boardLoadedAt")
	}
}

func TestDetailCacheHitRendersWithoutBdCall(t *testing.T) {
	// Board rows carry every detail field (description included), so the
	// detail pane is served entirely from board data and the cache.
	issues := testIssues()
	for i := range issues {
		issues[i].Description = "Needs the alpha milestone before it can start."
		issues[i].Notes = "Assigned last sprint."
	}
	f := &fakeClient{issues: map[bd.View][]bd.Issue{bd.ViewOpen: issues}}
	m := drive(t, f) // row 0 detail loaded from board data, cache warm
	// Board rows carry every detail field, so no `bd show` runs at all.
	if f.showCalls != 0 {
		t.Fatalf("drive made %d show calls, want 0 (board data covers detail)", f.showCalls)
	}
	updated, _ := m.Update(teaKeyMsg("j"))
	m = updated.(Model)
	if f.showCalls != 0 {
		t.Fatalf("cached selection change made %d new bd calls", f.showCalls)
	}
	if m.detail == nil || m.detail.ID != "fm-bbb" {
		t.Fatalf("cached detail not installed: %+v", m.detail)
	}
	view := stripANSI(m.View())
	if !strings.Contains(view, "Needs the alpha milestone") {
		t.Errorf("cached detail not rendered instantly:\n%s", view)
	}
	// The debounced background refresh marks itself, then lands.
	updated, cmd := m.Update(detailDebounceMsg{seq: m.detailDebounceSeq})
	m = updated.(Model)
	if !m.detailRefreshing {
		t.Fatal("cache hit did not schedule a background refresh")
	}
	if !strings.Contains(stripANSI(m.View()), "refreshing") {
		t.Errorf("refreshing marker missing while refresh runs:\n%s", stripANSI(m.View()))
	}
	m = runCmd(t, m, cmd)
	if m.detailRefreshing {
		t.Error("refreshing marker stuck after refresh landed")
	}
	// The refresh also runs from board data: still zero bd calls.
	if f.showCalls != 0 {
		t.Fatalf("background refresh made %d show calls, want 0", f.showCalls)
	}
}

func TestDebounceCoalescesRapidMoves(t *testing.T) {
	f := &fakeClient{issueByID: map[string]*bd.Issue{
		"fm-aaa": testDetailOf("fm-aaa"),
		"fm-bbb": testDetailOf("fm-bbb"),
		"fm-ccc": testDetailOf("fm-ccc"),
	}}
	m := newTestModel(f)
	m.treeMode = false
	m = applyMsg(t, m, boardMsg{view: m.view, generation: m.boardGen, issues: testIssues()})
	updated, firstCmd := m.Update(teaKeyMsg("j")) // row 1, superseded
	m = updated.(Model)
	updated, secondCmd := m.Update(teaKeyMsg("j")) // row 2, final
	m = updated.(Model)
	m = runCmd(t, m, firstCmd) // the abandoned tick fires late
	if f.showCalls != 0 {
		t.Fatalf("superseded debounce tick fetched detail: %v", f.showLog)
	}
	m = runCmd(t, m, secondCmd)
	// Board data covers the detail fields, so `bd show` never runs. Without
	// a loaded graph the dep edges still come from bd: two directions for
	// the final selection plus two each for the two prefetched neighbours.
	if f.showCalls != 0 {
		t.Fatalf("final detail fetch made %d show calls, want 0", f.showCalls)
	}
	if f.depCalls != 6 {
		t.Fatalf("final detail fetch made %d dep calls, want 6 (final + 2 prefetch, both directions)", f.depCalls)
	}
	if m.detail == nil || m.detail.ID != "fm-ccc" {
		t.Fatalf("detail = %+v, want fm-ccc", m.detail)
	}
}

func TestUppercaseRResetsWithoutReloadingDefaultBoard(t *testing.T) {
	f := &fakeClient{}
	m := drive(t, f)
	m.sortMode = SortPriority
	m.filter = ParseFilter("status:closed")
	m.detail = nil
	m.detailPendingID = ""
	updated, cmd := m.Update(teaKeyMsg("R"))
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("reset should prepare detail for the selected row")
	}
	if f.listCalls != 0 {
		t.Fatalf("reset on the default board reloaded bd %d times", f.listCalls)
	}
	if m.view != bd.ViewReady || m.sortMode != SortCreated || m.filter.Active() {
		t.Fatalf("reset state = view %q sort %q filter %+v", m.view, m.sortMode, m.filter)
	}
	if len(m.rows) != len(m.allRows) {
		t.Fatalf("reset rows = %d, want %d", len(m.rows), len(m.allRows))
	}
	// The detail request for the selected row is debounced, not started yet.
	m = runCmd(t, m, cmd)
	if f.listCalls != 0 {
		t.Fatalf("reset's debounced detail reloaded the board %d times", f.listCalls)
	}
	// Board data covers the detail fields, so the debounced load runs
	// without any bd round-trip yet still installs the selected detail.
	if f.showCalls != 0 {
		t.Fatalf("reset's debounced detail made %d show calls, want 0", f.showCalls)
	}
	if m.detail == nil || m.detail.ID != m.selectedID() {
		t.Fatalf("reset did not install selected detail: %+v, want %q", m.detail, m.selectedID())
	}
}

func TestUppercaseRPreservesPendingDetailRequest(t *testing.T) {
	f := &fakeClient{issueByID: map[string]*bd.Issue{
		"a": testDetailOf("a"),
		"b": testDetailOf("b"),
	}}
	m := newTestModel(f)
	m.treeMode = false
	m.allRows = []bd.Issue{{ID: "a", Title: "A"}, {ID: "b", Title: "B"}}
	m.rows = append([]bd.Issue(nil), m.allRows...)
	m.detail = &bd.Issue{ID: "a"}

	updated, tickCmd := m.Update(teaKeyMsg("j"))
	m = updated.(Model)
	if tickCmd == nil {
		t.Fatal("selection did not schedule a debounced detail load")
	}
	updated, fetchCmd := m.Update(tickCmd())
	m = updated.(Model)
	if fetchCmd == nil || m.detailPendingID != "b" {
		t.Fatalf("selection did not start pending detail: cmd=%v pending=%q", fetchCmd != nil, m.detailPendingID)
	}
	pendingGeneration := m.detailGen

	updated, resetCmd := m.Update(teaKeyMsg("R"))
	m = updated.(Model)
	if resetCmd != nil {
		t.Fatal("reset should not reload the default board")
	}
	if m.detailGen != pendingGeneration || m.detailPendingID != "b" || !m.checking {
		t.Fatalf("reset stranded pending detail: generation=%d pending=%q checking=%v", m.detailGen, m.detailPendingID, m.checking)
	}

	m = runCmd(t, m, fetchCmd)
	if m.detail == nil || m.detail.ID != "b" || m.detailPendingID != "" || m.checking {
		t.Fatalf("pending detail did not apply after reset: detail=%+v pending=%q checking=%v", m.detail, m.detailPendingID, m.checking)
	}
}

func TestGraphCompletionDoesNotReplacePendingDetail(t *testing.T) {
	f := &fakeClient{issueByID: map[string]*bd.Issue{
		"a": testDetailOf("a"),
		"b": testDetailOf("b"),
	}}
	m := newTestModel(f)
	m.treeMode = false
	m.allRows = []bd.Issue{{ID: "a", Title: "A"}, {ID: "b", Title: "B"}}
	m.rows = append([]bd.Issue(nil), m.allRows...)
	m.detail = &bd.Issue{ID: "a"}
	m.down = []bd.DepRecord{{ID: "old-down"}}
	m.up = []bd.DepRecord{{ID: "old-up"}}

	updated, tickCmd := m.Update(teaKeyMsg("j"))
	m = updated.(Model)
	updated, detailCmd := m.Update(tickCmd())
	m = updated.(Model)
	if detailCmd == nil || m.detailPendingID != "b" {
		t.Fatalf("selection did not start the b detail request: cmd=%v pending=%q", detailCmd != nil, m.detailPendingID)
	}
	if m.detail != nil {
		t.Fatalf("stale detail remained selected: %+v", m.detail)
	}
	pendingGeneration := m.detailGen
	updated, graphCmd := m.Update(graphMsg{
		view:        m.view,
		generation:  m.boardGen,
		issues:      m.allRows,
		graphIssues: m.allRows,
		deps:        map[string][]bd.DepRecord{"a": {{ID: "graph-down"}}},
		reverseDeps: map[string][]bd.DepRecord{"a": {{ID: "graph-up"}}},
		complete:    true,
	})
	m = updated.(Model)
	if graphCmd != nil || m.detailGen != pendingGeneration || m.detailPendingID != "b" {
		t.Fatalf("graph completion disturbed pending detail: cmd=%v generation=%d pending=%q", graphCmd != nil, m.detailGen, m.detailPendingID)
	}
}

func TestCompleteGraphSnapshotUpdatesDetailEdges(t *testing.T) {
	m := newTestModel(nil)
	m.rows = []bd.Issue{{ID: "root", Title: "Root"}}
	m.selected = 0
	m.detail = &bd.Issue{ID: "root"}
	m.down = []bd.DepRecord{{ID: "old-down"}}
	m.up = []bd.DepRecord{{ID: "old-up"}}
	updated, _ := m.Update(graphMsg{
		view:        m.view,
		generation:  m.boardGen,
		issues:      m.rows,
		graphIssues: m.rows,
		deps:        map[string][]bd.DepRecord{"root": {{ID: "new-down"}}},
		reverseDeps: map[string][]bd.DepRecord{"root": {{ID: "new-up"}}},
		complete:    true,
	})
	m = updated.(Model)
	if len(m.down) != 1 || m.down[0].ID != "new-down" || len(m.up) != 1 || m.up[0].ID != "new-up" {
		t.Fatalf("detail edges = down:%+v up:%+v, want graph snapshot", m.down, m.up)
	}
}

func TestPartialGraphSnapshotLeavesDetailCoherent(t *testing.T) {
	m := newTestModel(nil)
	m.rows = []bd.Issue{{ID: "root", Title: "Root"}}
	m.selected = 0
	m.detail = &bd.Issue{ID: "root"}
	m.down = []bd.DepRecord{{ID: "old-down"}}
	m.up = []bd.DepRecord{{ID: "old-up"}}
	updated, _ := m.Update(graphMsg{
		view:        m.view,
		generation:  m.boardGen,
		issues:      m.rows,
		graphIssues: m.rows,
		deps:        map[string][]bd.DepRecord{"root": {{ID: "partial-down-1"}, {ID: "partial-down-2"}}},
		reverseDeps: map[string][]bd.DepRecord{"root": {{ID: "partial-up-1"}, {ID: "partial-up-2"}}},
		complete:    false,
	})
	m = updated.(Model)
	if m.detail == nil || m.detail.DependencyCount != 0 || m.detail.DependentCount != 0 {
		t.Fatalf("partial graph changed detail counts: %+v", m.detail)
	}
	if len(m.down) != 1 || m.down[0].ID != "old-down" || len(m.up) != 1 || m.up[0].ID != "old-up" {
		t.Fatalf("partial graph changed detail edges: down:%+v up:%+v", m.down, m.up)
	}
}

func TestFocusEnterAndEsc(t *testing.T) {
	m := drive(t, nil)
	m = applyMsg(t, m, detailMsg{id: "fm-aaa", generation: m.detailGen, issue: testDetail(), err: nil})
	if m.focus != FocusList {
		t.Fatalf("initial focus = %v, want list", m.focus)
	}
	m = sendKey(t, m, "enter")
	if m.focus != FocusDetail {
		t.Fatalf("enter focus = %v, want detail", m.focus)
	}
	m = sendKey(t, m, "j")
	if m.selected != 0 {
		t.Errorf("j in detail should not move the selection (selected=%d)", m.selected)
	}
	m = sendKey(t, m, "esc")
	if m.focus != FocusList {
		t.Fatalf("esc focus = %v, want list", m.focus)
	}
}

func TestVimPaneFocusKeys(t *testing.T) {
	m := drive(t, nil)
	for _, key := range []string{"l", "L", "right"} {
		m = sendKey(t, m, key)
		if m.focus != FocusDetail {
			t.Errorf("%s focus = %v, want detail", key, m.focus)
		}
		m = sendKey(t, m, "h")
	}
	for _, key := range []string{"h", "H", "left"} {
		m = sendKey(t, m, "l")
		m = sendKey(t, m, key)
		if m.focus != FocusList {
			t.Errorf("%s focus = %v, want list", key, m.focus)
		}
	}
}

func TestHalfPageScrollingInListAndDetail(t *testing.T) {
	issues := make([]bd.Issue, 40)
	for i := range issues {
		issues[i] = bd.Issue{ID: "fm-" + strings.Repeat("x", i+1), Title: "Task", Status: "open"}
	}
	f := &fakeClient{issues: map[bd.View][]bd.Issue{bd.ViewOpen: issues}}
	m := newTestModel(f)
	m = applyMsg(t, m, boardMsg{view: m.view, generation: m.boardGen, issues: issues, err: nil})
	half := m.halfPageStep()
	m = sendKey(t, m, "ctrl+d")
	if m.selected != half {
		t.Fatalf("ctrl+d list selection = %d, want %d", m.selected, half)
	}
	m = sendKey(t, m, "ctrl+u")
	if m.selected != 0 {
		t.Fatalf("ctrl+u list selection = %d, want 0", m.selected)
	}

	long := testDetailOf(issues[0].ID)
	long.Description = strings.Repeat("word ", 600)
	m = applyMsg(t, m, detailMsg{id: issues[0].ID, generation: m.detailGen, issue: long, err: nil})
	m = sendKey(t, m, "enter")
	m = sendKey(t, m, "ctrl+d")
	if m.dOffset != half {
		t.Fatalf("ctrl+d detail offset = %d, want %d", m.dOffset, half)
	}
	m = sendKey(t, m, "ctrl+u")
	if m.dOffset != 0 {
		t.Fatalf("ctrl+u detail offset = %d, want 0", m.dOffset)
	}
}

func TestDescriptionRendersMarkdown(t *testing.T) {
	d := testDetail()
	d.Description = "# Heading\n\n**bold** and *italic*\n\n- first\n- second\n\n```go\nfmt.Println(\"code\")\n```"
	plain := stripANSI(strings.Join(BuildDetail(NewVocab(nil), d, nil, nil, 60), "\n"))
	for _, want := range []string{"Heading", "bold", "italic", "first", "second", "fmt.Println(\"code\")"} {
		if !strings.Contains(plain, want) {
			t.Errorf("rendered description missing %q:\n%s", want, plain)
		}
	}
	for _, sourceSyntax := range []string{"# Heading", "**bold**", "*italic*", "```"} {
		if strings.Contains(plain, sourceSyntax) {
			t.Errorf("markdown syntax %q was not rendered:\n%s", sourceSyntax, plain)
		}
	}
}

func TestDetailScrollBounds(t *testing.T) {
	long := testDetail()
	long.Description = strings.Repeat("word ", 200)
	f := &fakeClient{issue: long}
	m := drive(t, f)
	m = applyMsg(t, m, detailMsg{id: "fm-aaa", generation: m.detailGen, issue: long, err: nil})
	m = sendKey(t, m, "enter")

	lines := len(BuildDetail(m.vocab, long, nil, nil, m.detailWidth()))
	vis := m.detailVisLines()
	contentVis := vis
	if m.detailErr != "" {
		contentVis -= 2
	}
	if lines > vis {
		contentVis--
	}
	if contentVis < 0 {
		contentVis = 0
	}
	maxOffset := lines - contentVis
	if maxOffset < 0 {
		maxOffset = 0
	}
	m = sendKey(t, m, "G")
	if m.dOffset != maxOffset {
		t.Errorf("G offset = %d, want max %d", m.dOffset, maxOffset)
	}
	m = sendKey(t, m, "j")
	if m.dOffset > maxOffset {
		t.Errorf("offset overflow: %d > %d", m.dOffset, maxOffset)
	}
	m = sendKey(t, m, "g")
	if m.dOffset != 0 {
		t.Errorf("g offset = %d, want 0", m.dOffset)
	}
	m = sendKey(t, m, "k")
	if m.dOffset < 0 {
		t.Errorf("offset underflow: %d", m.dOffset)
	}
}

func TestViewSwitching(t *testing.T) {
	f := &fakeClient{
		issue: testDetail(),
		issues: map[bd.View][]bd.Issue{
			bd.ViewOpen:   testIssues(),
			bd.ViewClosed: append(testIssues(), bd.Issue{ID: "fm-ddd", Title: "Delta", Status: "blocked", Priority: 2}),
		},
	}
	m := newTestModel(f)
	m = applyMsg(t, m, boardMsg{view: m.view, generation: m.boardGen, issues: f.issues[bd.ViewOpen], err: nil})
	m = sendKey(t, m, "5")
	if m.view != bd.ViewClosed {
		t.Fatalf("view = %v, want closed", m.view)
	}
	m = applyMsg(t, m, boardMsg{view: bd.ViewClosed, generation: m.boardGen, issues: f.issues[bd.ViewClosed], err: nil})
	if len(m.rows) != 4 {
		t.Fatalf("rows = %d, want 4 after switch", len(m.rows))
	}
	view := stripANSI(m.View())
	if !strings.Contains(view, "closed") || !strings.Contains(view, "fm-ddd") {
		t.Errorf("switched view missing content:\n%s", view)
	}
}

func TestNumericKeysSelectNativeAndCustomStatusViews(t *testing.T) {
	m := newTestModel(nil)
	m.views = bd.ViewsFromStatuses([]bd.StatusInfo{{Name: "awaiting_review"}})
	for i, key := range []string{"1", "2", "3", "4", "5", "6"} {
		updated, _ := m.Update(teaKeyMsg(key))
		m = updated.(Model)
		if m.view != m.views[i] {
			t.Fatalf("key %s selected %q, want %q", key, m.view, m.views[i])
		}
	}
}

func TestStatusMessagePopulatesNativeAndCustomTabs(t *testing.T) {
	m := newTestModel(nil)
	updated, cmd := m.Update(statusMsg{statuses: []bd.StatusInfo{
		{Name: "open"},
		{Name: "in_progress"},
		{Name: "blocked"},
		{Name: "closed"},
		{Name: "deferred"},
		{Name: "awaiting_review"},
	}})
	m = updated.(Model)
	if cmd != nil {
		t.Fatal("status refresh unexpectedly reloaded the current view")
	}
	want := []bd.View{
		bd.ViewReady,
		bd.ViewOpen,
		bd.ViewInProgress,
		bd.ViewBlocked,
		bd.ViewClosed,
		bd.ViewDeferred,
		bd.View("awaiting_review"),
	}
	if len(m.views) != len(want) {
		t.Fatalf("status tabs = %v, want %v", m.views, want)
	}
	for i, view := range want {
		if m.views[i] != view {
			t.Fatalf("status tab %d = %q, want %q", i, m.views[i], view)
		}
	}
}

func TestYankIndexClampsAfterRowsChange(t *testing.T) {
	m := newTestModel(nil)
	m.rows = []bd.Issue{{ID: "with-url", Title: "Title", URL: "https://example.test"}}
	m.selected = 0
	m.yank = true
	m.yankIndex = 2
	m.allRows = []bd.Issue{{ID: "without-url", Title: "Replacement"}}
	m.rebuildRows("")
	if m.yankIndex != 1 {
		t.Fatalf("yank index = %d, want 1 after URL disappeared", m.yankIndex)
	}
	updated, cmd := m.Update(teaKeyMsg("enter"))
	if cmd == nil {
		t.Fatal("enter did not copy the clamped yank item")
	}
	if updated.(Model).yankIndex != 1 {
		t.Fatalf("enter changed yank index to %d", updated.(Model).yankIndex)
	}
}

func TestSelectionSurvivesRefreshByID(t *testing.T) {
	m := drive(t, nil)
	m = sendKey(t, m, "j")
	if m.rows[m.selected].ID != "fm-bbb" {
		t.Fatalf("pre-refresh selection = %q", m.rows[m.selected].ID)
	}
	reordered := []bd.Issue{testIssues()[2], testIssues()[0], testIssues()[1]}
	m = applyMsg(t, m, boardMsg{view: m.view, generation: m.boardGen, issues: reordered, err: nil})
	if m.rows[m.selected].ID != "fm-bbb" {
		t.Errorf("selection lost after refresh: %q", m.rows[m.selected].ID)
	}
}

func TestBoardErrorRendersAndKeepsLife(t *testing.T) {
	f := &fakeClient{failList: errors.New("deadline exceeded")}
	m := newTestModel(f)
	m = applyMsg(t, m, boardMsg{view: m.view, generation: m.boardGen, issues: nil, err: f.failList})
	view := stripANSI(m.View())
	for _, want := range []string{"Could not load board", "deadline exceeded", "q quit"} {
		if !strings.Contains(view, want) {
			t.Errorf("error view missing %q", want)
		}
	}
	nm := applyMsg(t, m, teaKeyMsg("q"))
	if !nm.quitting {
		t.Error("q after board error should quit")
	}
}

func TestStaleBoardResponseDoesNotEndCurrentLoad(t *testing.T) {
	m := newTestModel(&fakeClient{})
	m.view = bd.ViewOpen
	m.loading = true
	m.boardErr = ""
	m = applyMsg(t, m, boardMsg{view: bd.ViewClosed, generation: m.boardGen, issues: testIssues()})
	if !m.loading {
		t.Fatal("stale board response ended the active load")
	}
	if len(m.allRows) != 0 {
		t.Fatalf("stale board response replaced current rows: %d", len(m.allRows))
	}
	if m.boardErr != "" {
		t.Fatalf("stale board response set board error: %q", m.boardErr)
	}
	if view := stripANSI(m.View()); !strings.Contains(view, "Loading board") {
		t.Fatalf("stale board response hid loading state:\n%s", view)
	}
}

func TestSameViewStaleBoardResponseDoesNotOverwriteCurrentLoad(t *testing.T) {
	f := &fakeClient{
		queuedLists: [][]bd.Issue{
			{{ID: "old", Title: "Old board", Status: "open"}},
			{{ID: "new", Title: "New board", Status: "open"}},
		},
	}
	m := newTestModel(f)
	firstCmd := m.startBoardLoad()
	secondCmd := m.startBoardLoad()
	if m.boardGen != 3 {
		t.Fatalf("board generation = %d, want 3 after two refreshes", m.boardGen)
	}
	m = applyMsg(t, m, firstCmd())
	if !m.loading || len(m.allRows) != 0 {
		t.Fatalf("stale same-view response changed state: loading=%v rows=%d", m.loading, len(m.allRows))
	}
	m = applyMsg(t, m, secondCmd())
	if m.loading || len(m.rows) != 1 || m.rows[0].ID != "new" {
		t.Fatalf("current same-view response not applied: loading=%v rows=%+v", m.loading, m.rows)
	}
}

func TestViewSwitchLoadUsesReturnedModelGeneration(t *testing.T) {
	f := &fakeClient{
		issues: map[bd.View][]bd.Issue{
			bd.ViewOpen:       {{ID: "ready", Title: "Ready board", Status: "open"}},
			bd.ViewInProgress: {{ID: "open", Title: "In progress board", Status: "in_progress"}},
		},
	}
	m := newTestModel(f)
	m = applyMsg(t, m, boardMsg{
		view:       bd.ViewOpen,
		generation: m.boardGen,
		issues:     f.issues[bd.ViewOpen],
	})
	updated, cmd := m.Update(teaKeyMsg("3"))
	m = updated.(Model)
	if !m.loading {
		t.Fatal("view switch did not mark the board as loading")
	}
	if cmd == nil {
		t.Fatal("view switch did not return a board load command")
	}
	m = applyMsg(t, m, cmd())
	if m.loading || m.view != bd.ViewInProgress || len(m.rows) != 1 || m.rows[0].ID != "open" {
		t.Fatalf("view switch load was discarded: loading=%v view=%s rows=%+v generation=%d", m.loading, m.view, m.rows, m.boardGen)
	}
}

func TestHelpToggle(t *testing.T) {
	m := drive(t, nil)
	m = sendKey(t, m, "?")
	if !m.help {
		t.Fatal("? should open help")
	}
	view := stripANSI(m.View())
	for _, want := range []string{"1 ready", "2 open", "3 in_progress", "4 blocked", "5 closed", "6", "deferred", "ctrl-u/d", "h collapse", "l unfold", "expand all", "Read-only", "⇣", "⇡"} {
		if !strings.Contains(view, want) {
			t.Errorf("help missing %q", want)
		}
	}
	m = sendKey(t, m, "x")
	if m.help {
		t.Error("any key should close help")
	}
}

func TestQuitKeys(t *testing.T) {
	for _, key := range []string{"q", "ctrl+c"} {
		m := drive(t, nil)
		nm := applyMsg(t, m, teaKeyMsg(key))
		if !nm.quitting {
			t.Errorf("%s should set quitting", key)
		}
	}
}

func TestEmptyBoardStates(t *testing.T) {
	f := &fakeClient{issues: map[bd.View][]bd.Issue{bd.ViewOpen: nil}, issue: testDetail()}
	m := newTestModel(f)
	m = applyMsg(t, m, boardMsg{view: m.view, generation: m.boardGen, issues: nil, err: nil})
	view := stripANSI(m.View())
	for _, want := range []string{"No ready issues", "Select a bead for details"} {
		if !strings.Contains(view, want) {
			t.Errorf("empty board missing %q", want)
		}
	}
}

func TestCommentsViewLoadsScrollsAndAddsWithoutBoardReload(t *testing.T) {
	f := &fakeClient{
		issues: map[bd.View][]bd.Issue{bd.ViewOpen: {{ID: "fm-comments", Title: "Commentable task", Status: "open"}}},
		issue:  &bd.Issue{ID: "fm-comments", Title: "Commentable task", Status: "open"},
		commentsByID: map[string][]bd.Comment{
			"fm-comments": {
				{ID: "old", Author: "Ada", Text: "Old note", CreatedAt: "2026-09-07T12:00:00Z"},
			},
		},
	}
	m := drive(t, f)
	listCalls := f.listCalls
	m = step(t, m, "c")
	if !m.commentsOpen || m.commentsID != "fm-comments" || m.commentsLoading {
		t.Fatalf("comments view state = open:%v id:%q loading:%v", m.commentsOpen, m.commentsID, m.commentsLoading)
	}
	view := stripANSI(m.View())
	for _, want := range []string{"Commentable task", "ID fm-comments", "Ada", "Old note"} {
		if !strings.Contains(view, want) {
			t.Errorf("comments view missing %q:\n%s", want, view)
		}
	}
	m = step(t, m, "a")
	if !m.commentsInputActive {
		t.Fatal("a did not focus the inline comment input")
	}
	m = step(t, m, "Added from the comments view")
	m = step(t, m, "enter")
	if m.commentsInputActive || m.commentsLoading || len(f.commentAdds) != 1 {
		t.Fatalf("comment submit state: active=%v loading=%v adds=%v", m.commentsInputActive, m.commentsLoading, f.commentAdds)
	}
	if !strings.Contains(stripANSI(m.View()), "Added from the comments view") {
		t.Fatalf("new comment missing from reloaded thread:\n%s", stripANSI(m.View()))
	}
	if m.rows[0].CommentCount != 2 || m.allRows[0].CommentCount != 2 {
		t.Fatalf("comment count not updated in place: rows=%d all=%d", m.rows[0].CommentCount, m.allRows[0].CommentCount)
	}
	graph := graphMsg{
		view: m.view, generation: m.boardGen,
		issues:      []bd.Issue{{ID: "fm-comments", Title: "Commentable task", Status: "open"}},
		graphIssues: []bd.Issue{{ID: "fm-comments", Title: "Commentable task", Status: "open"}},
		complete:    true,
	}
	m = applyMsg(t, m, graph)
	if m.rows[0].CommentCount != 2 {
		t.Fatalf("graph enrichment dropped in-memory comment count: %d", m.rows[0].CommentCount)
	}
	if f.listCalls != listCalls {
		t.Fatalf("adding a comment reloaded board: before=%d after=%d", listCalls, f.listCalls)
	}
	m = step(t, m, "esc")
	if m.commentsOpen {
		t.Fatal("esc did not leave comments view")
	}
	inline := stripANSI(m.View())
	if !strings.Contains(inline, "Comments (2)") || !strings.Contains(inline, "Added from the comments view") {
		t.Fatalf("new comment missing from inline detail section:\n%s", inline)
	}
}

func TestCommentsNewestFirstStableWithoutMutatingRecords(t *testing.T) {
	sameTime := "2026-09-07T13:00:00Z"
	stored := []bd.Comment{
		{ID: "old", Author: "Ada", Text: "Old note", CreatedAt: "2026-09-07T12:00:00Z"},
		{ID: "new-a", Author: "Grace", Text: "First same-time note", CreatedAt: sameTime},
		{ID: "new-b", Author: "Linus", Text: "Second same-time note", CreatedAt: sameTime},
	}
	wantStored := append([]bd.Comment(nil), stored...)

	ordered := sortComments(stored)
	if got := []string{ordered[0].ID, ordered[1].ID, ordered[2].ID}; !reflect.DeepEqual(got, []string{"new-a", "new-b", "old"}) {
		t.Fatalf("comment order = %v, want stable newest-first order", got)
	}
	if !reflect.DeepEqual(stored, wantStored) {
		t.Fatalf("stored comments mutated: got %+v, want %+v", stored, wantStored)
	}
}

func TestBoardCOpensCommentsWithInput(t *testing.T) {
	f := &fakeClient{issues: map[bd.View][]bd.Issue{bd.ViewOpen: {{ID: "fm-comments", Title: "Commentable task", Status: "open"}}}}
	m := drive(t, f)
	m = step(t, m, "C")
	if !m.commentsOpen || !m.commentsInputActive {
		t.Fatalf("C state = open:%v input:%v", m.commentsOpen, m.commentsInputActive)
	}
}

func TestCommentsErrorsStayInStatusAndDoNotCrash(t *testing.T) {
	f := &fakeClient{
		issues:       map[bd.View][]bd.Issue{bd.ViewOpen: {{ID: "fm-comments", Title: "Commentable task", Status: "open"}}},
		failComments: errors.New("bd comments fm-comments: timed out; the beads store is busy or locked"),
	}
	m := drive(t, f)
	m = step(t, m, "c")
	view := stripANSI(m.View())
	if !strings.Contains(view, "Could not load comments") || !strings.Contains(view, "busy or locked") {
		t.Fatalf("comment error missing from rendered view:\n%s", view)
	}
}

func TestNarrowTerminalLaysOut(t *testing.T) {
	m := drive(t, nil)
	m.width, m.height = 60, 15
	for i, line := range strings.Split(m.View(), "\n") {
		if displayWidth(line) > 60 {
			t.Errorf("line %d wider than terminal (%d): %q", i, displayWidth(line), stripANSI(line))
		}
	}
}

func TestListRowsCarryMarks(t *testing.T) {
	v := NewVocab(nil)
	row := v.ListRow(bd.Issue{ID: "fm-x", Title: "T", Status: "open", Priority: 1, DependencyCount: 2, DependentCount: 1}, 40, false)
	plain := stripANSI(row)
	if !strings.Contains(plain, "⇣2") || !strings.Contains(plain, "⇡1") {
		t.Errorf("row missing dep marks: %q", plain)
	}
	if strings.Contains(plain, "fm-xT") {
		t.Errorf("row fields jammed: %q", plain)
	}
}

func TestListRowsRenderCommentBadgesAtResponsiveWidths(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	v := NewVocab(nil)
	for _, count := range []int{0, 1, 12, 120} {
		for _, width := range []int{24, 80} {
			row := v.ListRow(bd.Issue{
				ID: "fm-comments", Title: "Commentable task", Status: "open", CommentCount: count,
			}, width, false)
			plain := stripANSI(row)
			if displayWidth(row) > width {
				t.Errorf("count %d at width %d overflowed: %q", count, width, plain)
			}
			badge := commentBadgeText(count)
			if count == 0 {
				if strings.Contains(plain, "💬") || strings.Contains(plain, "C0") {
					t.Errorf("zero-count row rendered a comment badge: %q", plain)
				}
			} else if !strings.Contains(plain, badge) {
				t.Errorf("count %d at width %d missing badge %q: %q", count, width, badge, plain)
			}
		}
	}
}

func TestListRowsUseASCIICommentBadgeForDumbTerminal(t *testing.T) {
	t.Setenv("TERM", "dumb")
	row := NewVocab(nil).ListRow(bd.Issue{ID: "fm-comments", Status: "open", CommentCount: 12}, 24, false)
	plain := stripANSI(row)
	if !strings.Contains(plain, "C12") || strings.Contains(plain, "💬") {
		t.Fatalf("dumb terminal badge = %q, want ASCII C12", plain)
	}
}

func TestCommentHeadersPutTruncatedAuthorFirst(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	comment := bd.Comment{
		Author:    "Jon Roosevelt with a very long display name",
		Text:      "A note",
		CreatedAt: "2026-09-07T12:00:00Z",
	}
	headers := []string{commentHeader(comment, 32)}
	inline := inlineCommentLines([]bd.Comment{comment}, false, "", 1, 32, 10)
	headers = append(headers, inline[1])
	for _, header := range headers {
		plain := stripANSI(header)
		if displayWidth(header) > 32 {
			t.Errorf("comment header overflowed: %q", plain)
		}
		if !strings.HasPrefix(plain, "Jon Roosevelt") {
			t.Errorf("comment author was not first: %q", plain)
		}
		if !strings.Contains(plain, "·") || !strings.Contains(plain, "ago") {
			t.Errorf("comment header missing relative time: %q", plain)
		}
	}
}

func TestWrapText(t *testing.T) {
	for _, tc := range []struct {
		text  string
		width int
		frag  string
	}{
		{"one two three four five six", 10, "one two"},
		{"supercalifragilisticexpialidocious", 8, "supercal"},
		{"", 10, ""},
	} {
		lines := wrapText(tc.text, tc.width)
		joined := strings.Join(lines, "\n")
		if !strings.Contains(joined, tc.frag) {
			t.Errorf("wrapText(%q, %d) = %q, missing %q", tc.text, tc.width, joined, tc.frag)
		}
		for _, l := range lines {
			if displayWidth(l) > tc.width {
				t.Errorf("wrapText produced line wider than %d: %q", tc.width, stripANSI(l))
			}
		}
	}
}

func TestRuneBurstMovesSelectionPerRune(t *testing.T) {
	f := &fakeClient{issueByID: map[string]*bd.Issue{
		"fm-aaa": testDetailOf("fm-aaa"),
		"fm-bbb": testDetailOf("fm-bbb"),
		"fm-ccc": testDetailOf("fm-ccc"),
	}}
	m := newTestModel(f)
	m.treeMode = false
	m = applyMsg(t, m, boardMsg{view: m.view, generation: m.boardGen, issues: testIssues()})
	burst := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("jjj")}
	updated, _ := m.Update(burst)
	m = updated.(Model)
	if m.selected != 3-1 {
		t.Fatalf("burst of three j moved selection to %d, want 2", m.selected)
	}
	if m.rows[m.selected].ID != "fm-ccc" {
		t.Fatalf("burst landed on %q, want fm-ccc", m.rows[m.selected].ID)
	}
}

func TestDetailRefreshFailureKeepsShownDetail(t *testing.T) {
	f := &fakeClient{issueByID: map[string]*bd.Issue{
		"fm-aaa": testDetailOf("fm-aaa"),
		"fm-bbb": testDetailOf("fm-bbb"),
		"fm-ccc": testDetailOf("fm-ccc"),
	}}
	m := drive(t, f) // detail for fm-aaa fetched, cached and shown
	updated, cmd := m.Update(detailDebounceMsg{seq: m.detailDebounceSeq})
	m = updated.(Model)
	if !m.detailRefreshing {
		t.Fatal("refresh did not start")
	}
	m = applyMsg(t, m, detailMsg{
		id:         "fm-aaa",
		generation: m.detailGen,
		err:        errors.New("bd show fm-aaa --json: context deadline exceeded"),
	})
	if m.detail == nil || m.detail.ID != "fm-aaa" {
		t.Fatalf("failed refresh wiped the shown detail: %+v", m.detail)
	}
	if m.detailRefreshing || m.detailPendingID != "" {
		t.Fatalf("refresh state not cleared: refreshing=%v pending=%q", m.detailRefreshing, m.detailPendingID)
	}
	if m.detailErr == "" {
		t.Fatal("refresh error was not surfaced")
	}
	view := stripANSI(m.View())
	if !strings.Contains(view, "Alpha task") || strings.Contains(view, "Could not load detail") {
		t.Errorf("detail pane missing kept content or showed error screen:\n%s", view)
	}
	_ = cmd
}

func TestStarExpandsAllFolds(t *testing.T) {
	issues := []bd.Issue{
		{ID: "root", Status: "open"},
		{ID: "mid", Status: "open", ParentID: "root"},
		{ID: "leaf", Status: "open", ParentID: "mid"},
	}
	m := newTestModel(nil)
	m = applyMsg(t, m, boardMsg{view: m.view, generation: m.boardGen, issues: issues, deps: map[string][]bd.DepRecord{}})
	m = sendKey(t, m, "h") // fold root
	if len(m.rows) != 1 {
		t.Fatalf("root should fold: rows=%d", len(m.rows))
	}
	m = sendKey(t, m, "*")
	if len(m.rows) != 3 {
		t.Fatalf("star should expand all: rows=%d expanded=%v", len(m.rows), m.expanded)
	}
	for _, id := range []string{"root", "mid", "leaf"} {
		if !m.expanded[id] {
			t.Errorf("expanded[%s] = false, want true", id)
		}
	}
}

func TestSavedStatePersistsFolds(t *testing.T) {
	t.Setenv("BEADS_TUI_CONFIG_DIR", t.TempDir())
	issues := []bd.Issue{
		{ID: "root", Status: "open"},
		{ID: "child", Status: "open", ParentID: "root"},
	}
	m := newTestModel(nil)
	m = applyMsg(t, m, boardMsg{view: m.view, generation: m.boardGen, issues: issues, deps: map[string][]bd.DepRecord{}})
	m = sendKey(t, m, "h")
	m.saveState()
	reloaded := newTestModel(nil)
	if reloaded.expanded["root"] {
		t.Fatalf("folded root should persist: expanded=%v", reloaded.expanded)
	}
	reloaded = applyMsg(t, reloaded, boardMsg{view: m.view, generation: reloaded.boardGen, issues: issues, deps: map[string][]bd.DepRecord{}})
	if len(reloaded.rows) != 1 || reloaded.rows[0].ID != "root" {
		t.Fatalf("reloaded board should honor the saved fold: rows=%v", reloaded.rows)
	}
}

func TestDetailShowsParentBreadcrumbAndChildren(t *testing.T) {
	issues := []bd.Issue{
		{ID: "gp", Title: "Grandparent", Status: "open"},
		{ID: "p", Title: "Parent", Status: "in_progress", ParentID: "gp"},
		{ID: "me", Title: "Me", Status: "open", ParentID: "p"},
		{ID: "kid1", Title: "Kid one", Status: "open", ParentID: "me"},
		{ID: "kid2", Title: "Kid two", Status: "closed", ParentID: "me"},
	}
	m := newTestModel(nil)
	m.allRows = issues
	me := issues[2]
	m.detail = &me
	m.down, m.up = nil, nil
	view := stripANSI(strings.Join(m.buildDetail(60), "\n"))
	if !strings.Contains(view, "Path: gp › p") {
		t.Errorf("detail missing parent breadcrumb:\n%s", view)
	}
	if !strings.Contains(view, "Children (2)") {
		t.Errorf("detail missing children section:\n%s", view)
	}
	for _, want := range []string{"kid1", "Kid one", "kid2", "Kid two"} {
		if !strings.Contains(view, want) {
			t.Errorf("detail children missing %q:\n%s", want, view)
		}
	}
	// Child rows render the live status glyph from the vocabulary.
	if !strings.Contains(view, m.vocab.Icon("closed")) {
		t.Errorf("children rows missing status glyphs:\n%s", view)
	}
}

func TestDetailHierarchyDerivedWithoutExtraCalls(t *testing.T) {
	issues := []bd.Issue{
		{ID: "root", Status: "open"},
		{ID: "child", Status: "open", ParentID: "root"},
	}
	f := &fakeClient{issue: testDetail()}
	m := newTestModel(f)
	m.allRows = issues
	child := issues[1]
	m.detail = &child
	m.buildDetail(60)
	if f.listCalls != 0 || f.showCalls != 0 || f.depCalls != 0 {
		t.Fatalf("hierarchy must come from loaded rows: list=%d show=%d dep=%d", f.listCalls, f.showCalls, f.depCalls)
	}
}

func TestFilterKeepsAncestorsVisible(t *testing.T) {
	issues := []bd.Issue{
		{ID: "root", Title: "Root task", Status: "open"},
		{ID: "mid", Title: "Middle", Status: "open", ParentID: "root"},
		{ID: "hit", Title: "findme needle", Status: "open", ParentID: "mid"},
	}
	m := newTestModel(nil)
	m.allRows = issues
	m.filter = ParseFilter("needle")
	m.projectRows("")
	got := make([]string, 0, len(m.rows))
	for _, row := range m.rows {
		got = append(got, row.ID)
	}
	if strings.Join(got, ",") != "root,mid,hit" {
		t.Fatalf("filtered tree rows = %v, want ancestors kept", got)
	}
}

func TestInlineCommentsRenderCountsOrderTruncationAndWrapping(t *testing.T) {
	m := New(nil)
	m.width, m.height, m.layout = 160, 80, LayoutSide
	m.detail = &bd.Issue{ID: "inline", Title: "Inline comments", Status: "open"}
	m.commentsID = "inline"

	for _, count := range []int{0, 1, 5, 7} {
		comments := make([]bd.Comment, count)
		for i := range comments {
			comments[i] = bd.Comment{
				Author:    "author-" + itoa(i),
				Text:      "comment-" + itoa(i),
				CreatedAt: fmt.Sprintf("2026-09-07T12:%02d:00Z", i),
			}
		}
		m.detail.CommentCount = count
		m.comments = sortComments(comments)
		m.commentsLoading = false
		plain := stripANSI(strings.Join(m.buildDetail(40), "\n"))
		if !strings.Contains(plain, "Comments ("+itoa(count)+")") {
			t.Errorf("count %d missing comments heading:\n%s", count, plain)
		}
		switch count {
		case 0:
			if !strings.Contains(plain, "No comments - a to add") {
				t.Errorf("empty inline thread missing add hint:\n%s", plain)
			}
		case 1:
			if !strings.Contains(plain, "author-0") || !strings.Contains(plain, "comment-0") {
				t.Errorf("single inline comment missing:\n%s", plain)
			}
		case 5:
			if !strings.Contains(plain, "author-4") || !strings.Contains(plain, "author-0") || strings.Contains(plain, "c for all") {
				t.Errorf("five inline comments rendered incorrectly:\n%s", plain)
			}
		case 7:
			if !strings.Contains(plain, "… c for all 7") {
				t.Errorf("long inline thread missing truncation line:\n%s", plain)
			}
			if strings.Index(plain, "author-6") > strings.Index(plain, "author-5") {
				t.Errorf("inline comments are not newest first:\n%s", plain)
			}
		}
	}

	m.comments = []bd.Comment{{Author: "wrap", Text: "one two three four five", CreatedAt: "2026-09-07T12:00:00Z"}}
	plain := stripANSI(strings.Join(m.buildDetail(20), "\n"))
	if !strings.Contains(plain, "  one two three four\n  five") {
		t.Fatalf("inline comment text did not wrap to pane width:\n%s", plain)
	}
}
