package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/RooseveltAdvisors/beads-tui/internal/bd"
	"github.com/RooseveltAdvisors/beads-tui/internal/dbindex"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/muesli/termenv"
)

func globalTestModel(t *testing.T) (Model, string, string, *fakeClient, *fakeClient) {
	t.Helper()
	m, a, b, fa, fb := databaseTestModel(t)
	m.width, m.height = 160, 40
	for _, f := range []*fakeClient{fa, fb} {
		f.commentsByID = map[string][]bd.Comment{}
		f.issueByID = map[string]*bd.Issue{"same-id": &f.issues[bd.ViewOpen][0]}
	}
	cmd := m.enterGlobal()
	m.detailDebounce = 0
	m.smear = false
	m = runCmd(t, m, cmd)
	return m, a, b, fa, fb
}

func TestGlobalSearchCollisionsTabsAndSingleScope(t *testing.T) {
	m, a, b, fa, fb := globalTestModel(t)
	if !m.globalMode || len(m.rows) != 2 || m.rows[0].ID == m.rows[1].ID {
		t.Fatalf("colliding local IDs lost: %+v", m.rows)
	}
	if !strings.Contains(stripANSI(m.View()), "ready board · global") {
		t.Fatal("global scope absent from title")
	}
	beforeA, beforeB := fa.listCalls, fb.listCalls
	m = step(t, m, "/")
	m = step(t, m, "same-id")
	if len(m.rows) != 2 {
		t.Fatalf("global search lost a workspace: %+v", m.rows)
	}
	paths := map[string]bool{}
	for _, row := range m.rows {
		paths[row.SourcePath] = true
	}
	if !paths[a] || !paths[b] {
		t.Fatalf("search sources: %v", paths)
	}
	if fa.listCalls != beforeA || fb.listCalls != beforeB {
		t.Fatal("search reloaded databases")
	}
	m = step(t, m, "esc")
	m = step(t, m, "2") // Open across both stores.
	if len(m.rows) != 2 {
		t.Fatal("status tab did not aggregate")
	}
	m = step(t, m, "5") // Closed across both stores.
	if len(m.rows) != 0 {
		t.Fatal("unfiltered status tab leaked other statuses")
	}
	m = step(t, m, "/")
	m = step(t, m, "same-id")
	if len(m.rows) != 2 {
		t.Fatal("cross-status global search lost graph matches")
	}
	m = step(t, m, "esc")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlE})
	m = runCmd(t, updated.(Model), cmd)
	if m.globalMode || m.database != a || len(m.rows) != 1 || m.rows[0].SourcePath != "" || m.rows[0].ID != "same-id" {
		t.Fatalf("single database changed: db=%s rows=%+v", m.database, m.rows)
	}
}

func TestGlobalPillPlacementAndDetailSource(t *testing.T) {
	withColorProfile(t, termenv.TrueColor)
	issue := sourcedIssues("/work/wiseman/.beads", []bd.Issue{{ID: "fm-xyz", Title: "A task", Priority: 1, Status: "open", Assignee: "jon"}})[0]
	v := NewVocab(nil)
	row := v.ListRow(issue, 120, false)
	plain := stripANSI(row)
	p, workspace, assignee := strings.Index(plain, "P1"), strings.Index(plain, "wiseman"), strings.Index(plain, "@jon")
	if p < 0 || workspace <= p || assignee <= workspace {
		t.Fatalf("pill order: %q", plain)
	}
	if strings.Contains(plain, "/work/") || !strings.Contains(plain, "fm-xyz") {
		t.Fatalf("internal key exposed: %q", plain)
	}
	if !strings.Contains(row, colorSeq(hudCyanHex, false)) {
		t.Fatal("workspace pill missing HUD cyan")
	}
	detail := strings.Join(buildDetail(v, &issue, nil, nil, nil, nil, 120, nil), "\n")
	if !strings.Contains(stripANSI(detail), "Workspace: /work/wiseman/.beads") {
		t.Fatal("detail source missing")
	}
	issue.SourcePath = ""
	issue.ID = "fm-xyz"
	if strings.Contains(stripANSI(v.ListRow(issue, 120, false)), "wiseman") {
		t.Fatal("single DB gained a workspace pill")
	}
}

func TestGlobalDetailAndCommentsRouteToSourceWithSameID(t *testing.T) {
	m, a, b, fa, fb := globalTestModel(t)
	for _, path := range []string{a, b} {
		key := globalID(path, "same-id")
		issue, err := m.backend.Show(context.Background(), key)
		if err != nil || issue.SourcePath != path {
			t.Fatalf("source detail: %+v %v", issue, err)
		}
		m.commentsID = key
		msg := m.submitCommentCmd(key, "source comment")().(commentSubmitMsg)
		if msg.err != nil {
			t.Fatal(msg.err)
		}
		comments := m.loadCommentsCmd(key, 1)().(commentsMsg)
		if comments.err != nil || len(comments.comments) != 1 {
			t.Fatalf("source thread: %+v", comments)
		}
	}
	if len(fa.commentAdds) != 1 || len(fb.commentAdds) != 1 || fa.commentAdds[0] != "same-id:source comment" || fb.commentAdds[0] != fa.commentAdds[0] {
		t.Fatalf("comment crossed a source: %v %v", fa.commentAdds, fb.commentAdds)
	}
	if fa.lastShowID != "same-id" || fb.lastShowID != "same-id" {
		t.Fatal("namespaced ID reached a source client")
	}
	if err := m.backend.AddComment(context.Background(), "same-id", "bad route"); err == nil {
		t.Fatal("unscoped global write accepted")
	}
	if err := m.backend.AddComment(context.Background(), globalID("/unindexed/.beads", "same-id"), "bad route"); err == nil {
		t.Fatal("unknown source accepted")
	}
}

func TestGlobalCommentUsesDirectoryClientNotAmbientDatabase(t *testing.T) {
	root := t.TempDir()
	a, b := filepath.Join(root, "alpha", ".beads"), filepath.Join(root, "beta", ".beads")
	for _, path := range []string{a, b} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	script := "#!/bin/sh\nprintf '%s' \"$*\" > \"$BEADS_DIR/args\"\ncat > \"$BEADS_DIR/comment\"\n"
	if err := os.WriteFile(filepath.Join(root, "bd"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("BEADS_DIR", a)
	client := bd.New()
	client.Actor = "test"
	aggregate := newAggregate([]dbindex.Entry{{Path: a}, {Path: b}}, func(path string) Backend { return client.ForDirectory(path) }, nil)
	m := newModel(aggregate, globalStateKey)
	key := globalID(b, "same-id")
	msg := m.submitCommentCmd(key, "only beta")().(commentSubmitMsg)
	if msg.err != nil {
		t.Fatal(msg.err)
	}
	data, err := os.ReadFile(filepath.Join(b, "comment"))
	if err != nil || string(data) != "only beta" {
		t.Fatalf("write did not reach beta: %q %v", data, err)
	}
	args, err := os.ReadFile(filepath.Join(b, "args"))
	if err != nil || !strings.Contains(string(args), "comment same-id --stdin") {
		t.Fatalf("wrong source args: %q %v", args, err)
	}
	if _, err := os.Stat(filepath.Join(a, "comment")); !os.IsNotExist(err) {
		t.Fatal("ambient database was written")
	}
	if os.Getenv("BEADS_DIR") != a {
		t.Fatal("process environment changed")
	}
}

func TestGlobalPartialResultsFailuresBoundedChainsAndStaleDelivery(t *testing.T) {
	m, a, b, _, fb := databaseTestModel(t)
	fb.failList = errors.New("database unavailable")
	// More stores than workers ensure the remainder stays in command chains.
	for i := 0; i < 3; i++ {
		m.databaseCache.Entries = append(m.databaseCache.Entries, dbindex.Entry{Path: filepath.Join(t.TempDir(), ".beads")})
	}
	start := m.enterGlobal()
	updated, load := m.Update(start())
	m = updated.(Model)
	batch := load().(tea.BatchMsg)
	workers := 0
	var first tea.Cmd
	for _, cmd := range batch {
		if cmd != nil {
			workers++
			if first == nil {
				first = cmd
			}
		}
	}
	if workers != globalWorkers {
		t.Fatalf("worker chains=%d", workers)
	}
	result := first()
	updated, _ = m.Update(result)
	m = updated.(Model)
	if len(m.rows) != 1 || m.rows[0].SourcePath != a || !strings.Contains(stripANSI(m.View()), "loading 1/5") {
		t.Fatal("first workspace did not paint partial results")
	}
	// A failed store is skipped, while the previously delivered board remains.
	result = m.scopeDatabaseCmd(m.aggregate.storeCmd(context.Background(), []string{b}, m.view, m.boardGen))()
	updated, _ = m.Update(result)
	m = updated.(Model)
	if len(m.rows) != 1 || m.boardErr != "" || !strings.Contains(stripANSI(m.View()), "1 workspace(s) skipped") {
		t.Fatal("one failed store broke aggregate board")
	}
	old := result
	m.boardGen++
	updated, _ = m.Update(old)
	if updated.(Model).globalDone != m.globalDone {
		t.Fatal("outdated generation changed progress")
	}
}

func TestGlobalModePersistsLastSingleWorkspaceAndPickerEntry(t *testing.T) {
	m, a, _, _, _ := globalTestModel(t)
	m.sortMode = SortPriority
	m.saveState()
	root, _ := loadState()
	if !root.GlobalMode || root.ActiveDatabase != a || root.Databases[globalStateKey].SortMode != SortPriority {
		t.Fatalf("scope state lost: %+v", root)
	}
	t.Setenv("BEADS_DIR", "")
	restored := NewMultiDatabase(bd.New())
	if !restored.globalMode || restored.database != a || restored.sortMode != SortPriority {
		t.Fatal("scope not restored")
	}
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	m = updated.(Model)
	if cmd != nil || !m.databasePicker || !strings.Contains(stripANSI(m.View()), "Global - all workspaces") {
		t.Fatal("global picker entry missing")
	}
}

func TestGlobalGraphNamespacesEdgesWithoutMutatingSource(t *testing.T) {
	native := []bd.Issue{{ID: "child", ParentID: "parent", Dependencies: []bd.DepEdge{{IssueID: "child", DependsOnID: "parent", Type: "blocks"}}}, {ID: "parent"}}
	a := sourcedIssues("/alpha/.beads", native)
	b := sourcedIssues("/beta/.beads", native)
	all := append(a, b...)
	down, _ := normalizeGraphEdges(all, inlineDepRecords(all))
	for _, path := range []string{"/alpha/.beads", "/beta/.beads"} {
		deps := down[globalID(path, "child")]
		if len(deps) != 1 || deps[0].ID != globalID(path, "parent") {
			t.Fatalf("cross-source edge: %+v", deps)
		}
	}
	if native[0].Dependencies[0].IssueID != "child" || native[0].ID != "child" {
		t.Fatal("source snapshot mutated")
	}
}

func TestGlobalDiscoveryUsesExistingExclusions(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"wiseman", ".treehouse/slot/repo", ".no-mistakes/gate", "actions-runner-3/_work/repo"} {
		if err := os.MkdirAll(filepath.Join(root, name, ".beads"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	entries := dbindex.Discover([]string{root}, 5)
	aggregate := newAggregate(entries, func(string) Backend { return &fakeClient{} }, nil)
	if len(aggregate.paths) != 1 || aggregate.paths[0] != filepath.Join(root, "wiseman", ".beads") {
		t.Fatalf("excluded database indexed globally: %v", aggregate.paths)
	}
}

func TestGlobalDeleteConfirmShowsReadableIDsWithWorkspace(t *testing.T) {
	m, a, b, _, _ := globalTestModel(t)
	single := sendKey(t, m, "D")
	view := stripANSI(single.renderCrud())
	if strings.Contains(view, "\x1f") || !strings.Contains(view, "same-id  · "+single.rows[0].SourcePath) {
		t.Fatalf("single delete confirm: %q", view)
	}
	multi := sendKey(t, m, "v")
	multi = sendKey(t, multi, "j")
	multi = sendKey(t, multi, "d")
	view = stripANSI(multi.renderCrud())
	if strings.Contains(view, "\x1f") || !strings.Contains(view, "same-id  · "+a) || !strings.Contains(view, "same-id  · "+b) {
		t.Fatalf("multi delete confirm: %q", view)
	}
	multi.applyCrudResult(crudResultMsg{kind: "delete", id: globalID(a, "same-id")})
	if multi.statusFlash != "delete ok same-id" {
		t.Fatalf("flash = %q", multi.statusFlash)
	}
}

func TestGlobalFooterKeepsSortMode(t *testing.T) {
	m, a, b, _, _ := databaseTestModel(t)
	m.databaseCache = dbindex.Cache{Scanned: time.Now(), Entries: []dbindex.Entry{{Path: a}, {Path: b}}}
	m.width, m.height = 140, 30
	m.sortMode = SortPriority
	m.globalMode = true
	m.globalDone, m.globalTotal, m.globalFailed = 2, 2, 1 // notice active (skipped)
	m.rows = []bd.Issue{{ID: "x", Title: "X", Status: "open", SourcePath: a}}
	plain := stripANSI(m.renderFooter(m.width))
	if !strings.Contains(plain, "sort:priority") && !strings.Contains(plain, "sort:pri") {
		t.Fatalf("global notice hid sort mode: %q", plain)
	}
	if !strings.Contains(plain, "skipped") {
		t.Fatalf("global notice missing: %q", plain)
	}
	// Cycling sort still reflects immediately.
	m.sortMode = m.sortMode.Next()
	plain = stripANSI(m.renderFooter(m.width))
	if !strings.Contains(plain, "sort:"+m.sortMode.String()) && !strings.Contains(plain, "sort:"+truncate(m.sortMode.String(), 3)) {
		t.Fatalf("sort cycle not visible in global footer: mode=%s footer=%q", m.sortMode.String(), plain)
	}
}
