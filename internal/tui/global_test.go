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
	// Real stores carry metadata.json; the structural gate in
	// partitionGlobalEntries treats its absence as death.
	for _, p := range []string{a, b} {
		if err := os.WriteFile(filepath.Join(p, "metadata.json"), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
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
	for _, p := range []string{a, b} {
		if err := os.WriteFile(filepath.Join(p, "metadata.json"), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// More stores than workers ensure the remainder stays in command chains.
	for i := 0; i < 3; i++ {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, ".beads"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".beads", "metadata.json"), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		m.databaseCache.Entries = append(m.databaseCache.Entries, dbindex.Entry{Path: filepath.Join(dir, ".beads")})
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
	// The footer hint renders the active database path; pin a short, realistic
	// one so the assertion does not depend on the host's tmpdir length.
	m.database = "/opt/ra/firstmate/.beads"
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

func TestGlobalNegativeCacheSkipsDeadStoresWithoutReprobing(t *testing.T) {
	// Build from the single-scope fixture: globalTestModel already entered
	// global once, and enterGlobal is a no-op while already global.
	m, a, b, fa, fb := databaseTestModel(t)
	m.width, m.height = 120, 30
	probes := map[string]int{}
	base := m.databaseFactory
	m.databaseFactory = func(path string) Backend {
		probes[path]++
		return base(path)
	}
	_ = fa
	fb.failList = errors.New("dead store")
	// Mark beta dead inside the TTL.
	failed := time.Now().UTC().Add(-time.Minute)
	for i := range m.databaseCache.Entries {
		if m.databaseCache.Entries[i].Path == b {
			m.databaseCache.Entries[i].FailedAt, m.databaseCache.Entries[i].Error = failed, "interrupted create"
		}
	}
	m = applyMsg(t, m, m.enterGlobal())
	if len(m.aggregate.paths) != 1 || m.aggregate.paths[0] != a {
		t.Fatalf("dead store probed anyway: paths=%v", m.aggregate.paths)
	}
	if probes[b] != 0 {
		t.Fatalf("factory constructed dead backend: probes=%v", probes)
	}
	if len(m.globalPreSkipped) != 1 || m.globalPreSkipped[0] != b {
		t.Fatalf("pre-skipped not recorded: %v", m.globalPreSkipped)
	}
	// Drive the load chain so the footer counters include the pre-skip.
	updated, cmd := m.Update(globalLoadMsg{view: m.view, generation: m.boardGen})
	m = updated.(Model)
	if cmd != nil {
		m = applyMsg(t, m, cmd)
	}
	if m.globalFailed != 1 || m.globalTotal != 2 {
		t.Fatalf("skip counters wrong: failed=%d total=%d", m.globalFailed, m.globalTotal)
	}
	if !strings.Contains(stripANSI(m.View()), "skipped") {
		t.Fatal("footer missing skip notice")
	}
}

func TestGlobalNegativeCacheNeverFiltersActiveOrExplicit(t *testing.T) {
	m, a, b, _, _ := globalTestModel(t)
	failed := time.Now().UTC()
	m.database, m.explicitDatabase = a, b
	m.explicitDatabase = b
	for i := range m.databaseCache.Entries {
		if m.databaseCache.Entries[i].Path == a || m.databaseCache.Entries[i].Path == b {
			m.databaseCache.Entries[i].FailedAt, m.databaseCache.Entries[i].Error = failed, "x"
		}
	}
	probe, skipped := m.partitionGlobalEntries()
	if len(skipped) != 0 || len(probe) != 2 {
		t.Fatalf("active/explicit filtered: probe=%v skipped=%v", probe, skipped)
	}
}

func TestGlobalStoreResultMarksAndClearsFailure(t *testing.T) {
	m, _, b, _, fb := globalTestModel(t)
	fb.failList = errors.New("store busy or locked")
	m = applyMsg(t, m, m.enterGlobal())
	// Drive the store result through the production path.
	st := m.aggregate
	msg := st.storeCmd(context.Background(), []string{b}, m.view, m.boardGen)
	updated, _ := m.Update(msg())
	mm := updated.(Model)
	var marked dbindex.Entry
	for _, e := range mm.databaseCache.Entries {
		if e.Path == b {
			marked = e
		}
	}
	if !marked.Unavailable() || marked.Error == "" {
		t.Fatalf("failure not marked: %+v", marked)
	}
	// A later clean store result clears the mark through the same handler.
	updated2, _ := mm.Update(globalStoreMsg{backend: st, path: b, store: aggregateStore{view: m.view}, view: m.view, generation: m.boardGen})
	mm = updated2.(Model)
	for _, e := range mm.databaseCache.Entries {
		if e.Path == b && (e.FailedAt != (time.Time{}) || e.Error != "") {
			t.Fatalf("success did not clear mark: %+v", e)
		}
	}
}

func TestEmojiPresentationAndFlagWidths(t *testing.T) {
	cases := map[string]int{
		"❤️ heart":        8,  // base 1 + VS16 widen = 2 cells + " heart"=6
		"🇺🇸 flag":        7,  // RI pair = 2 cells + " flag"=5
		"T1 ❤️ vs16":      10,
		"café 中文 🚀":      12, // cafe(5, é combining-safe) + space + CJK 4 + space + rocket 2
		"plain ascii":     11,
	}
	for s, want := range cases {
		if got := displayWidth(s); got != want {
			t.Errorf("displayWidth(%q) = %d, want %d", s, got, want)
		}
	}
}

func TestRescanResetsPreSkippedAndRRepartitions(t *testing.T) {
	m, a, b, _, fb := databaseTestModel(t)
	m.width, m.height = 120, 30
	fb.failList = errors.New("store busy or locked")
	// Enter global with beta healthy; then mark beta dead and rescan.
	m = applyMsg(t, m, m.enterGlobal())
	for i := range m.databaseCache.Entries {
		if m.databaseCache.Entries[i].Path == b {
			m.databaseCache.Entries[i].FailedAt = time.Now().UTC()
			m.databaseCache.Entries[i].FailureClass = dbindex.FailureStructural
		}
	}
	m.globalPreSkipped = []string{b}
	// databaseScanMsg re-evaluates the partition instead of trusting the
	// stale list: with the mark cleared and beta structurally valid, the
	// store returns to the probe set and the pre-skip list empties.
	for _, p := range []string{a, b} {
		if err := os.WriteFile(filepath.Join(p, "metadata.json"), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	updated, _ := m.Update(databaseScanMsg{entries: m.databaseCache.Entries, generation: m.databaseScanGeneration})
	m = updated.(Model)
	if len(m.globalPreSkipped) != 0 {
		t.Fatalf("scan left stale pre-skip: %v", m.globalPreSkipped)
	}
	// r is an explicit retry: a marked but structurally valid store must
	// return to the probe set (marks cleared, not trusted).
	for i := range m.databaseCache.Entries {
		if m.databaseCache.Entries[i].Path == b {
			m.databaseCache.Entries[i].FailedAt = time.Now().UTC().Add(-time.Minute)
			m.databaseCache.Entries[i].FailureClass = dbindex.FailureStructural
		}
	}
	m.repartitionGlobal()
	found := false
	for _, p := range m.aggregate.paths {
		if p == b {
			found = true
		}
	}
	if !found {
		t.Fatal("r retry kept the marked store out of the probe set")
	}
	_ = a
}

func TestTransientFailureExpiresFast(t *testing.T) {
	e := dbindex.Entry{FailedAt: time.Now().UTC().Add(-2 * time.Minute), FailureClass: dbindex.FailureTransient}
	if e.Unavailable() {
		t.Fatal("transient failure still cached after 2 minutes")
	}
	s := dbindex.Entry{FailedAt: time.Now().UTC().Add(-2 * time.Minute), FailureClass: dbindex.FailureStructural}
	if !s.Unavailable() {
		t.Fatal("structural failure expired too early")
	}
	if dbindex.ClassifyFailure("bd list: timed out; the beads store is busy or locked") != dbindex.FailureTransient {
		t.Fatal("busy/locked misclassified as structural")
	}
	if dbindex.ClassifyFailure("Error 1105: incomplete database directory from an interrupted create") != dbindex.FailureStructural {
		t.Fatal("interrupted create misclassified as transient")
	}
}

func TestStructurallyInvalidStoreMarkedWithoutBd(t *testing.T) {
	m, a, b, _, _ := databaseTestModel(t)
	_ = a
	bad := filepath.Join(t.TempDir(), "dead", ".beads")
	if err := os.MkdirAll(bad, 0o700); err != nil {
		t.Fatal(err)
	}
	calls := 0
	base := m.databaseFactory
	m.databaseFactory = func(path string) Backend {
		calls++
		return base(path)
	}
	m.databaseCache.Entries = append(m.databaseCache.Entries, dbindex.Entry{Path: bad})
	cmd := m.databaseMetadataCmd(context.Background(), []dbindex.Entry{{Path: bad}}, 1)
	var got dbindex.Entry
	switch msg := cmd().(type) {
	case databaseMetadataMsg:
		got = msg.entry
	case tea.BatchMsg:
		for _, c := range msg {
			if m2, ok := c().(databaseMetadataMsg); ok && m2.entry.Path == bad {
				got = m2.entry
			}
		}
	}
	if got.Path != bad || !got.Unavailable() || got.FailureClass != dbindex.FailureStructural {
		t.Fatalf("invalid store not marked: %+v", got)
	}
	if calls != 0 {
		t.Fatalf("bd invoked for invalid store: %d calls", calls)
	}
	_ = b
}

func TestPartitionMarksInvalidStoresWithoutProbing(t *testing.T) {
	m, a, b, _, _ := databaseTestModel(t)
	_ = a
	bad := filepath.Join(t.TempDir(), "dead", ".beads")
	if err := os.MkdirAll(bad, 0o700); err != nil {
		t.Fatal(err)
	}
	calls := 0
	base := m.databaseFactory
	m.databaseFactory = func(path string) Backend {
		calls++
		return base(path)
	}
	// databaseTestModel's fixture dirs also lack metadata.json; give the
	// healthy pair real markers so only `bad` is invalid.
	for _, p := range []string{a, b} {
		if err := os.WriteFile(filepath.Join(p, "metadata.json"), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	m.databaseCache.Entries = append(m.databaseCache.Entries, dbindex.Entry{Path: bad})
	probe, skipped := m.partitionGlobalEntries()
	for _, p := range probe {
		if p.Path == bad {
			t.Fatal("invalid store in probe set")
		}
	}
	if len(skipped) != 1 || skipped[0] != bad {
		t.Fatalf("skipped=%v", skipped)
	}
	if calls != 0 {
		t.Fatalf("factory called during partition: %d", calls)
	}
	var marked dbindex.Entry
	for _, e := range m.databaseCache.Entries {
		if e.Path == bad {
			marked = e
		}
	}
	if !marked.Unavailable() || marked.FailureClass != dbindex.FailureStructural {
		t.Fatalf("invalid store not marked: %+v", marked)
	}
}

func TestClassifierCoversBd131StructuralMessages(t *testing.T) {
	structural := []string{
		"bd list --status open --json -n 0: Error: no beads database found",
		"Error: failed to open database: embeddeddolt: init schema: embeddeddolt: creating_database: Error 1105: incomplete database directory from an interrupted create",
		"Error: failed to open database: database \"bt\" not found on Dolt server at 127.0.0.1:3308",
	}
	for _, s := range structural {
		if dbindex.ClassifyFailure(s) != dbindex.FailureStructural {
			t.Errorf("misclassified as transient: %s", s)
		}
	}
	transient := []string{
		"bd list --ready --json -n 0: timed out; the beads store is busy or locked",
		"context deadline exceeded",
	}
	for _, s := range transient {
		if dbindex.ClassifyFailure(s) != dbindex.FailureTransient {
			t.Errorf("misclassified as structural: %s", s)
		}
	}
}

func TestGlobalRetryClearsMarksAndReprobes(t *testing.T) {
	m, a, b, _, fb := databaseTestModel(t)
	_ = a
	m.width, m.height = 120, 30
	if err := os.WriteFile(filepath.Join(a, "metadata.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(b, "metadata.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	fb.failList = errors.New("store busy or locked")
	// A live probe failure marks beta; a manual retry must clear and re-probe.
	m.markDatabaseFailure(b, errors.New("Error: no beads database found"))
	m.repartitionGlobal()
	found := false
	for _, p := range m.aggregate.paths {
		if p == b {
			found = true
		}
	}
	if !found {
		t.Fatal("r retry kept the marked store filtered out of the probe set")
	}
	for _, e := range m.databaseCache.Entries {
		if e.Path == b && e.FailedAt != (time.Time{}) {
			t.Fatal("r retry did not clear failure marks")
		}
	}
}

func TestScanWhileGlobalRepartitionsInsteadOfProbingInvalid(t *testing.T) {
	m, a, b, _, _ := databaseTestModel(t)
	_ = b
	m.width, m.height = 120, 30
	if err := os.WriteFile(filepath.Join(a, "metadata.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(t.TempDir(), "dead", ".beads")
	if err := os.MkdirAll(bad, 0o700); err != nil {
		t.Fatal(err)
	}
	calls := 0
	base := m.databaseFactory
	m.databaseFactory = func(path string) Backend {
		calls++
		return base(path)
	}
	m.databaseCache.Entries = append(m.databaseCache.Entries, dbindex.Entry{Path: bad})
	m = applyMsg(t, m, m.enterGlobal())
	// R while global: the scan handler must repartition, so the invalid
	// store is re-marked instead of probed.
	m.beginDatabaseScan()
	updated, _ := m.Update(databaseScanMsg{entries: append([]dbindex.Entry(nil), m.databaseCache.Entries...), generation: m.databaseScanGeneration})
	m = updated.(Model)
	for _, p := range m.aggregate.paths {
		if p == bad {
			t.Fatal("scan-while-global probed the invalid store")
		}
	}
	var marked dbindex.Entry
	for _, e := range m.databaseCache.Entries {
		if e.Path == bad {
			marked = e
		}
	}
	if !marked.Unavailable() || marked.FailureClass != dbindex.FailureStructural {
		t.Fatalf("invalid store not re-marked after scan: %+v", marked)
	}
}
