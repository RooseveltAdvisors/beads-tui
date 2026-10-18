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
)

func databaseTestModel(t *testing.T) (Model, string, string, *fakeClient, *fakeClient) {
	t.Helper()
	t.Setenv("BEADS_TUI_CONFIG_DIR", t.TempDir())
	root := t.TempDir()
	a := filepath.Join(root, "alpha", ".beads")
	b := filepath.Join(root, "beta", ".beads")
	for _, path := range []string{a, b} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	fa := &fakeClient{issues: map[bd.View][]bd.Issue{bd.ViewOpen: {{ID: "same-id", Title: "Alpha row", Status: "open"}}}}
	fb := &fakeClient{issues: map[bd.View][]bd.Issue{bd.ViewOpen: {{ID: "same-id", Title: "Beta row", Status: "open"}}}}
	m := newModel(fa, a)
	m.multiDatabase = true
	m.detailDebounce = 0
	m.smear = false
	m.databaseFactory = func(path string) Backend {
		if path == a {
			return fa
		}
		return fb
	}
	m.databaseCache = dbindex.Cache{Scanned: time.Now(), Entries: []dbindex.Entry{{Path: a}, {Path: b}}}
	m = applyMsg(t, m, boardMsg{view: m.view, generation: m.boardGen, issues: fa.issues[bd.ViewOpen]})
	return m, a, b, fa, fb
}
func switchTestDatabase(t *testing.T, m Model, path string) Model {
	t.Helper()
	cmd := m.beginDatabaseSwitch(path)
	if cmd == nil {
		t.Fatal("no switch probe")
	}
	// Execute the probe and feed its result through the production Update path.
	updated, _ := m.Update(cmd())
	return updated.(Model)
}
func TestDatabaseSwitchReloadAndStateIsolation(t *testing.T) {
	m, a, b, _, _ := databaseTestModel(t)
	m.sortMode = SortAlphabetical
	m.layout = LayoutStacked
	m.expanded["alpha-only"] = true
	m.saveState()
	// Deliberately collide IDs and timestamps to prove cache identity is scoped.
	m.detailCache[detailCacheKey("same-id", "")] = cachedDetail{issue: bd.Issue{ID: "same-id", Title: "stale alpha detail"}}
	oldEpoch := m.databaseEpoch
	stale := m.scopeDatabaseCmd(func() tea.Msg {
		return prefetchMsg{hits: []prefetchHit{{key: "late", detail: cachedDetail{issue: bd.Issue{ID: "same-id", Title: "late alpha"}}}}}
	})()
	m = switchTestDatabase(t, m, b)
	if m.database != b || m.backend == nil || len(m.rows) != 1 || m.rows[0].Title != "Beta row" || m.databaseEpoch == oldEpoch {
		t.Fatalf("switch failed: db=%s rows=%+v", m.database, m.rows)
	}
	if len(m.detailCache) != 0 || m.detail != nil || m.expanded["alpha-only"] || m.sortMode != SortCreated {
		t.Fatal("alpha state leaked into beta")
	}
	if !strings.Contains(stripANSI(m.View()), "ready board · beta") {
		t.Fatal("active database missing from title")
	}
	m = applyMsg(t, m, stale)
	if _, ok := m.detailCache["late"]; ok {
		t.Fatal("late prefetch crossed database boundary")
	}
	m = applyMsg(t, m, databaseScopedMsg{epoch: oldEpoch, msg: boardMsg{view: m.view, generation: m.boardGen, issues: []bd.Issue{{ID: "wrong", Title: "wrong database"}}}})
	if m.rows[0].Title != "Beta row" {
		t.Fatal("late board replaced beta")
	}
	m.sortMode = SortPriority
	m.saveState()
	m = switchTestDatabase(t, m, a)
	if m.sortMode != SortAlphabetical || m.layout != LayoutStacked || !m.expanded["alpha-only"] || m.rows[0].Title != "Alpha row" {
		t.Fatalf("alpha state not restored: %+v", m.rows)
	}
	reloaded := newModel(m.backend, b)
	if reloaded.sortMode != SortPriority || len(reloaded.rows) != 1 || reloaded.rows[0].Title != "Beta row" {
		t.Fatalf("beta persistence failed: %+v", reloaded.rows)
	}
	root, _ := loadState()
	if root.ActiveDatabase != a || root.Snapshot != nil {
		t.Fatal("active choice or scoped snapshot contract failed")
	}
}
func TestDatabaseFailedSwitchRetainsPreviousBoardAndClient(t *testing.T) {
	m, a, b, fa, fb := databaseTestModel(t)
	fb.failList = errors.New("store busy or locked")
	m.databasePicker = true
	m = switchTestDatabase(t, m, b)
	if m.database != a || m.backend != fa || len(m.rows) != 1 || m.rows[0].Title != "Alpha row" {
		t.Fatal("failed probe replaced original board")
	}
	if !strings.Contains(stripANSI(m.View()), "Could not switch to beta") {
		t.Fatal("switch error not visible")
	}
	m = sendKey(t, m, "esc")
	if !strings.Contains(stripANSI(m.View()), "alpha") || m.statusFlash == "" {
		t.Fatal("previous board or visible notice missing")
	}
}
func TestPickerKeyboardCachedOpenAndNoDatabaseView(t *testing.T) {
	m, a, _, _, _ := databaseTestModel(t)
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	m = updated.(Model)
	if !m.databasePicker || cmd != nil || !strings.Contains(stripANSI(m.View()), "alpha") {
		t.Fatal("picker did not open immediately from cache")
	}
	if m.databaseCache.Entries[0].Path != a {
		t.Fatal("active store is not first")
	}
	m = sendKey(t, m, "esc")
	m.database = ""
	m.databaseCache.Entries = nil
	m.databasePicker = true
	if !strings.Contains(stripANSI(m.View()), "No Beads databases found") {
		t.Fatal("no-database path rendered blank")
	}
	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil || updated.(Model).databaseSwitching != "" {
		t.Fatal("empty picker initiated a switch")
	}
}
func TestExplicitDatabaseWinsOverSavedSelection(t *testing.T) {
	t.Setenv("BEADS_TUI_CONFIG_DIR", t.TempDir())
	root := t.TempDir()
	saved := filepath.Join(root, "saved", ".beads")
	explicit := filepath.Join(root, "explicit", ".beads")
	for _, path := range []string{saved, explicit} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	m := newModel(&fakeClient{}, saved)
	m.multiDatabase = true
	m.saveState()
	t.Setenv("BEADS_DIR", explicit)
	got := NewMultiDatabase(bd.New())
	if got.database != explicit || got.databaseCache.Entries[0].Path != explicit {
		t.Fatalf("explicit target lost: %s", got.database)
	}
	t.Setenv("BEADS_DIR", "")
	got = NewMultiDatabase(bd.New())
	if got.database != saved {
		t.Fatalf("saved selection not restored: %s", got.database)
	}
}
func TestLegacySnapshotNeverAppearsUnderDatabaseName(t *testing.T) {
	t.Setenv("BEADS_TUI_CONFIG_DIR", t.TempDir())
	legacy := New(&fakeClient{})
	legacy.lastSnapshot = &boardSnapshot{View: bd.ViewReady, Issues: []bd.Issue{{ID: "legacy", Title: "unscoped"}}}
	legacy.saveState()
	m := newModel(&fakeClient{}, filepath.Join(t.TempDir(), "new", ".beads"))
	if len(m.rows) != 0 || m.lastSnapshot != nil {
		t.Fatal("legacy rows leaked into named database")
	}
}

func TestPickerEnterProbesThenCommitsDatabaseSwitch(t *testing.T) {
	m, _, b, _, _ := databaseTestModel(t)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	m = updated.(Model)
	m = sendKey(t, m, "down")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if cmd == nil || m.databaseSwitching != b {
		t.Fatal("Enter lost the pending target")
	}
	updated, _ = m.Update(cmd())
	m = updated.(Model)
	if m.database != b || m.databasePicker || m.rows[0].Title != "Beta row" {
		t.Fatalf("Enter did not switch: %s %+v", m.database, m.rows)
	}
}
func TestPickerRescanMarksGenerationBeforeDispatch(t *testing.T) {
	m, _, _, _, _ := databaseTestModel(t)
	m.databasePicker = true
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'R'}})
	m = updated.(Model)
	if cmd == nil || !m.databaseScanning || m.databaseScanGeneration == 0 {
		t.Fatal("R lost its scan state")
	}
	updated, _ = m.Update(databaseScanMsg{generation: m.databaseScanGeneration - 1})
	if !updated.(Model).databaseScanning {
		t.Fatal("obsolete scan replaced active scan")
	}
}
func TestNoDatabaseBackendRejectsReloadAndCommentWrites(t *testing.T) {
	backend := &unavailableDatabase{}
	m := newModel(backend, "no-database")
	m.multiDatabase = true
	m.database = ""
	msg := m.loadBoardCmd()().(boardMsg)
	if msg.err == nil || !strings.Contains(msg.err.Error(), "No Beads databases found") || len(msg.issues) != 0 {
		t.Fatal("no-database reload did not fail visibly")
	}
	if err := backend.AddComment(context.Background(), "same-id", "text"); err == nil {
		t.Fatal("no-database write accepted")
	}
}
