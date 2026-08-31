package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RooseveltAdvisors/beads-tui/internal/bd"
	tea "github.com/charmbracelet/bubbletea"
)

// fakeClient serves canned data without touching a real Beads store.
type fakeClient struct {
	issues   map[bd.View][]bd.Issue
	issue    *bd.Issue
	down     []bd.DepRecord
	up       []bd.DepRecord
	statuses []bd.StatusInfo

	failList   error
	failShow   error
	listCalls  int
	showCalls  int
	lastShowID string
}

func (f *fakeClient) List(_ context.Context, view bd.View) ([]bd.Issue, error) {
	f.listCalls++
	return f.issues[view], f.failList
}

func (f *fakeClient) Show(_ context.Context, id string) (*bd.Issue, error) {
	f.showCalls++
	f.lastShowID = id
	if f.failShow != nil {
		return nil, f.failShow
	}
	return f.issue, nil
}

func (f *fakeClient) Deps(_ context.Context, _ string, up bool) ([]bd.DepRecord, error) {
	if up {
		return f.up, nil
	}
	return f.down, nil
}

func (f *fakeClient) Statuses(context.Context) ([]bd.StatusInfo, error) {
	return f.statuses, nil
}

func testIssues() []bd.Issue {
	return []bd.Issue{
		{ID: "fm-aaa", Title: "Alpha task", Status: "open", Priority: 1, IssueType: "task"},
		{ID: "fm-bbb", Title: "Beta blocked task", Status: "blocked", Priority: 2, IssueType: "bug", DependencyCount: 1},
		{ID: "fm-ccc", Title: "Gamma done task", Status: "closed", Priority: 0, IssueType: "task"},
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
		f.issues = map[bd.View][]bd.Issue{bd.ViewReady: testIssues()}
	}
	if f.issue == nil {
		f.issue = testDetail()
	}
	m := New(f)
	m.width, m.height = 100, 30
	return m
}

// drive loads the initial board snapshot like the real Init flow.
func drive(t *testing.T, f *fakeClient) Model {
	t.Helper()
	if f == nil {
		f = &fakeClient{}
	}
	m := newTestModel(f)
	updated, cmd := m.Update(boardMsg{view: bd.ViewReady, issues: f.issues[bd.ViewReady], err: nil})
	m = updated.(Model)
	if cmd != nil {
		if msg := cmd(); msg != nil {
			m = applyMsg(t, m, msg)
		}
	}
	return m
}

// teaKeyMsg builds a KeyMsg whose String() matches the given key name.
func teaKeyMsg(s string) tea.KeyMsg {
	k := tea.Key{Type: tea.KeyRunes, Runes: []rune(s)}
	switch s {
	case "up":
		k.Type = tea.KeyUp
	case "down":
		k.Type = tea.KeyDown
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
	}
	return tea.KeyMsg(k)
}

func sendKey(t *testing.T, m Model, key string) Model {
	t.Helper()
	return applyMsg(t, m, teaKeyMsg(key))
}

// step applies a keypress and then runs any command Update returned, so
// async loads (board/detail fetches) land like they do in the real program.
func step(t *testing.T, m Model, key string) Model {
	t.Helper()
	updated, cmd := m.Update(teaKeyMsg(key))
	nm := updated.(Model)
	if cmd != nil {
		if msg := cmd(); msg != nil {
			nm = applyMsg(t, nm, msg)
		}
	}
	return nm
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
		"beads-tui", "Ready board", "fm-aaa", "Alpha task", "fm-bbb",
		"Beta blocked task", "fm-ccc", "Gamma done task", "[1]Ready", "[2]Open",
		"[3]All", "q quit",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q", want)
		}
	}
	if got := len(strings.Split(m.View(), "\n")); got != 30 {
		t.Errorf("view height = %d lines, want 30", got)
	}
}

func TestSelectionMovesAndLoadsDetail(t *testing.T) {
	f := &fakeClient{down: []bd.DepRecord{{ID: "fm-aaa", Title: "Alpha task", Status: "open", DependencyType: "blocks"}}}
	m := drive(t, f)
	m = step(t, m, "j")
	if m.selected != 1 {
		t.Fatalf("selected = %d, want 1", m.selected)
	}
	if f.lastShowID != "fm-bbb" {
		t.Errorf("j should load detail for fm-bbb, got %q", f.lastShowID)
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
	m = applyMsg(t, m, detailMsg{id: "fm-aaa", issue: testDetailOf("fm-aaa"), err: nil})
	m = sendKey(t, m, "j") // now on fm-bbb; its request is in flight
	if m.rows[m.selected].ID != "fm-bbb" {
		t.Fatalf("selection = %q, want fm-bbb", m.rows[m.selected].ID)
	}
	// A response for a bead that is not the current selection must be dropped.
	nm := applyMsg(t, m, detailMsg{id: "fm-ccc", issue: testDetailOf("fm-ccc"), err: nil})
	if nm.detail != nil && nm.detail.ID == "fm-ccc" {
		t.Errorf("stale detail (fm-ccc) applied while selection is fm-bbb")
	}
	// The real response lands normally.
	nm = applyMsg(t, nm, detailMsg{id: "fm-bbb", issue: testDetail(), err: nil})
	if nm.detail == nil || nm.detail.ID != "fm-bbb" {
		t.Errorf("current selection's detail not applied: %+v", nm.detail)
	}
	if nm.checking {
		t.Error("checking should clear once the detail response lands")
	}
}

func TestFocusEnterAndEsc(t *testing.T) {
	m := drive(t, nil)
	m = applyMsg(t, m, detailMsg{id: "fm-aaa", issue: testDetail(), err: nil})
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

func TestDetailScrollBounds(t *testing.T) {
	long := testDetail()
	long.Description = strings.Repeat("word ", 200)
	f := &fakeClient{issue: long}
	m := drive(t, f)
	m = applyMsg(t, m, detailMsg{id: "fm-aaa", issue: long, err: nil})
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
			bd.ViewReady: testIssues(),
			bd.ViewAll:   append(testIssues(), bd.Issue{ID: "fm-ddd", Title: "Delta", Status: "closed", Priority: 2}),
		},
	}
	m := newTestModel(f)
	m = applyMsg(t, m, boardMsg{view: bd.ViewReady, issues: f.issues[bd.ViewReady], err: nil})
	m = sendKey(t, m, "3")
	if m.view != bd.ViewAll {
		t.Fatalf("view = %v, want all", m.view)
	}
	m = applyMsg(t, m, boardMsg{view: bd.ViewAll, issues: f.issues[bd.ViewAll], err: nil})
	if len(m.rows) != 4 {
		t.Fatalf("rows = %d, want 4 after switch", len(m.rows))
	}
	view := stripANSI(m.View())
	if !strings.Contains(view, "All board") || !strings.Contains(view, "fm-ddd") {
		t.Errorf("switched view missing content:\n%s", view)
	}
}

func TestSelectionSurvivesRefreshByID(t *testing.T) {
	m := drive(t, nil)
	m = sendKey(t, m, "j")
	if m.rows[m.selected].ID != "fm-bbb" {
		t.Fatalf("pre-refresh selection = %q", m.rows[m.selected].ID)
	}
	reordered := []bd.Issue{testIssues()[2], testIssues()[0], testIssues()[1]}
	m = applyMsg(t, m, boardMsg{view: bd.ViewReady, issues: reordered, err: nil})
	if m.rows[m.selected].ID != "fm-bbb" {
		t.Errorf("selection lost after refresh: %q", m.rows[m.selected].ID)
	}
}

func TestBoardErrorRendersAndKeepsLife(t *testing.T) {
	f := &fakeClient{failList: errors.New("deadline exceeded")}
	m := newTestModel(f)
	m = applyMsg(t, m, boardMsg{view: bd.ViewReady, issues: nil, err: f.failList})
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

func TestHelpToggle(t *testing.T) {
	m := drive(t, nil)
	m = sendKey(t, m, "?")
	if !m.help {
		t.Fatal("? should open help")
	}
	view := stripANSI(m.View())
	for _, want := range []string{"1 Ready", "2 Open", "3 All", "Read-only", "⇣", "⇡"} {
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
	f := &fakeClient{issues: map[bd.View][]bd.Issue{bd.ViewReady: nil}, issue: testDetail()}
	m := newTestModel(f)
	m = applyMsg(t, m, boardMsg{view: bd.ViewReady, issues: nil, err: nil})
	view := stripANSI(m.View())
	for _, want := range []string{"No ready work", "Select a bead for details"} {
		if !strings.Contains(view, want) {
			t.Errorf("empty board missing %q", want)
		}
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

func TestSortFilterAndStatusBarInteractions(t *testing.T) {
	m := drive(t, nil)
	if m.sortMode != SortAlphabetical {
		t.Fatalf("default sort = %v, want alphabetical presentation", m.sortMode)
	}
	m = sendKey(t, m, "s")
	if m.sortMode != SortDependencies {
		t.Fatalf("after s sort = %v, want dependencies", m.sortMode)
	}
	m = sendKey(t, m, "f")
	if !m.filtering {
		t.Fatal("f should open filter prompt")
	}
	m = sendKey(t, m, "status:blocked")
	m = sendKey(t, m, "enter")
	if m.filtering || m.filter.Status != "blocked" || len(m.rows) != 1 {
		t.Fatalf("filter state = filtering:%v filter:%+v rows:%d", m.filtering, m.filter, len(m.rows))
	}
	plain := stripANSI(m.View())
	for _, want := range []string{"sort:dependencies", "filter:status:blocked", "fm-bbb", "1 total", "100%"} {
		if !strings.Contains(plain, want) {
			t.Errorf("status bar missing %q: %s", want, plain)
		}
	}
	m = sendKey(t, m, "f")
	m = sendKey(t, m, "esc")
	if m.filter.Active() || len(m.rows) != 3 {
		t.Errorf("esc did not clear filter: %+v rows=%d", m.filter, len(m.rows))
	}
}

func TestGraphToggleAndTreeCollapse(t *testing.T) {
	f := &fakeClient{down: []bd.DepRecord{{ID: "fm-aaa", Title: "Alpha task", Status: "open", DependencyType: "blocks"}}}
	m := drive(t, f)
	m = step(t, m, "v")
	if !m.tree || m.graphLoading {
		t.Fatalf("tree toggle state = tree:%v loading:%v", m.tree, m.graphLoading)
	}
	plain := stripANSI(m.View())
	if !strings.Contains(plain, "Graph") || !strings.Contains(plain, "└─") && !strings.Contains(plain, "├─") {
		t.Errorf("tree render lacks hierarchy: %s", plain)
	}
	before := len(m.displayItems())
	m = sendKey(t, m, "enter")
	if len(m.displayItems()) > before || m.expanded["fm-bbb"] {
		t.Errorf("enter did not collapse tree node: before=%d after=%d expanded=%v", before, len(m.displayItems()), m.expanded)
	}
}

func TestHalfPageAndTabNavigation(t *testing.T) {
	m := drive(t, nil)
	m.height = 20
	m = sendKey(t, m, "ctrl+d")
	if m.selected == 0 {
		t.Error("ctrl+d should move list by half page")
	}
	m = sendKey(t, m, "tab")
	if m.view != bd.ViewOpen {
		t.Errorf("tab view = %v, want open", m.view)
	}
	m = sendKey(t, m, "shift+tab")
	if m.view != bd.ViewReady {
		t.Errorf("shift-tab view = %v, want ready", m.view)
	}
}

func TestDetailRendersMarkdownFieldsAndNamedEdges(t *testing.T) {
	issue := testDetail()
	issue.Description = "# Heading\n\n**Important** description"
	issue.Owner = "owner@example.com"
	issue.CreatedAt, issue.UpdatedAt = "created", "updated"
	issue.Labels = []string{"urgent", "frontend"}
	lines := BuildDetail(NewVocab(nil), issue, []bd.DepRecord{{ID: "fm-dep", Title: "Dependency name", Status: "open", DependencyType: "blocks"}}, nil, 50)
	plain := stripANSI(strings.Join(lines, "\n"))
	golden, err := os.ReadFile(filepath.Join("testdata", "detail.golden"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := normalizeGolden(plain), strings.TrimSpace(string(golden)); got != want {
		t.Errorf("detail golden mismatch\n got:\n%s\nwant:\n%s", got, want)
	}
	for _, want := range []string{"Heading", "Important", "Owner: owner@example.com", "Created: created", "Tags: urgent, frontend", "Dependency name", "[blocks]"} {
		if !strings.Contains(plain, want) {
			t.Errorf("detail missing %q:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "fm-dep") {
		t.Error("named dependency should not render raw id")
	}
}

func normalizeGolden(value string) string {
	lines := strings.Split(value, "\n")
	for i := range lines {
		lines[i] = strings.TrimSpace(lines[i])
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}
