package tui

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/RooseveltAdvisors/beads-tui/internal/bd"
	"github.com/RooseveltAdvisors/beads-tui/internal/dbindex"
	tea "github.com/charmbracelet/bubbletea"
)

type databaseState struct {
	multiDatabase          bool
	database               string
	explicitDatabase       string
	databaseEpoch          uint64
	databaseFactory        func(string) Backend
	databaseCache          dbindex.Cache
	databasePicker         bool
	databaseIndex          int
	databaseScanning       bool
	databaseSwitching      string
	databaseError          string
	databaseScanGeneration uint64
	databaseMetadataCancel context.CancelFunc
}
type databaseScopedMsg struct {
	epoch uint64
	msg   tea.Msg
}
type databaseScanStartMsg struct{}
type databaseScanMsg struct {
	entries    []dbindex.Entry
	generation uint64
}
type databaseMetadataMsg struct {
	entry      dbindex.Entry
	generation uint64
}
type databaseSwitchMsg struct {
	path    string
	backend Backend
	view    bd.View
	issues  []bd.Issue
	err     error
}

// No discovered target must never fall through to bd's ambient store, even
// after reload or a write shortcut is pressed on the empty board.
type unavailableDatabase struct{}

var errNoDatabase = errors.New("No Beads databases found. Press ctrl+o, then R to rescan local workspaces.")

func (*unavailableDatabase) List(context.Context, bd.View) ([]bd.Issue, error) {
	return nil, errNoDatabase
}
func (*unavailableDatabase) Show(context.Context, string) (*bd.Issue, error) {
	return nil, errNoDatabase
}
func (*unavailableDatabase) Deps(context.Context, string, bool) ([]bd.DepRecord, error) {
	return nil, errNoDatabase
}
func (*unavailableDatabase) Statuses(context.Context) ([]bd.StatusInfo, error) {
	return nil, errNoDatabase
}
func (*unavailableDatabase) Comments(context.Context, string) ([]bd.Comment, error) {
	return nil, errNoDatabase
}
func (*unavailableDatabase) AddComment(context.Context, string, string) error { return errNoDatabase }

func databaseSavedState(root savedState, path string) savedState {
	if state, ok := root.Databases[path]; ok {
		return normalizeState(state)
	}
	root.ActiveDatabase, root.Databases, root.Snapshot = "", nil, nil
	return normalizeState(root)
}

// NewMultiDatabase is the interactive entry point. CLI list/show retain bd's
// ambient store contract. Each model and each in-flight command owns its client.
func NewMultiDatabase(client *bd.Client) Model {
	cache := dbindex.Read(databaseIndexPath())
	root, _ := loadState()
	explicit := strings.TrimSpace(os.Getenv("BEADS_DIR"))
	if explicit != "" {
		explicit = dbindex.Canonical(explicit)
	}
	selected := dbindex.Select(explicit, root.ActiveDatabase, dbindex.DefaultDirectory, cache.Entries)
	factory := func(path string) Backend { return client.ForDirectory(path) }
	var backend Backend = &unavailableDatabase{}
	if selected != "" {
		backend = factory(selected)
	}
	stateKey := selected
	if stateKey == "" {
		stateKey = "no-database"
	}
	m := newModel(backend, stateKey)
	m.database = selected
	m.multiDatabase = true
	m.explicitDatabase = explicit
	m.databaseFactory = factory
	m.databaseCache = cache
	m.touchDatabase()
	if selected == "" {
		m.loading = false
		m.boardErr = "No Beads databases found. Press ctrl+o, then R to rescan local workspaces."
	}
	return m
}

func databaseIndexPath() string {
	path, err := statePath()
	if err != nil {
		log.Printf("database index path: %v", err)
		return ""
	}
	return filepath.Join(filepath.Dir(path), "database-index.json")
}
func (m Model) databaseLabel() string {
	if !m.multiDatabase {
		return ""
	}
	if m.database == "" {
		return " · no database"
	}
	return " · " + (dbindex.Entry{Path: m.database}).Name()
}
func (m *Model) sortDatabases() {
	dbindex.Sort(m.databaseCache.Entries, m.database, m.explicitDatabase)
}
func (m *Model) touchDatabase() {
	if m.database == "" {
		return
	}
	found := false
	for i := range m.databaseCache.Entries {
		if m.databaseCache.Entries[i].Path == m.database {
			m.databaseCache.Entries[i].Used = time.Now().UTC()
			found = true
		}
	}
	if !found {
		m.databaseCache.Entries = append(m.databaseCache.Entries, dbindex.Entry{Path: m.database, Used: time.Now().UTC()})
	}
	m.sortDatabases()
	dbindex.Write(databaseIndexPath(), m.databaseCache)
}

// Scope only data messages: Bubbletea's own exec/quit/input messages must reach
// its runtime unchanged. Batch children carry the original database epoch too.
func (m Model) scopeDatabaseCmd(cmd tea.Cmd) tea.Cmd {
	if cmd == nil || !m.multiDatabase {
		return cmd
	}
	epoch := m.databaseEpoch
	return func() tea.Msg {
		msg := cmd()
		if batch, ok := msg.(tea.BatchMsg); ok {
			wrapped := make(tea.BatchMsg, len(batch))
			for i, c := range batch {
				wrapped[i] = m.scopeDatabaseCmd(c)
			}
			return wrapped
		}
		switch msg.(type) {
		case boardMsg, graphMsg, statusMsg, detailMsg, prefetchMsg, commentsMsg, commentSubmitMsg,
			crudResultMsg, editorReadyMsg, editorDoneMsg, boardRetryMsg, detailDebounceMsg:
			return databaseScopedMsg{epoch: epoch, msg: msg}
		}
		return msg
	}
}

func (m Model) updateDatabase(msg tea.Msg) (Model, tea.Cmd, bool) {
	if !m.multiDatabase {
		return m, nil, false
	}
	switch msg := msg.(type) {
	case databaseScanStartMsg:
		if m.databaseCache.Fresh(time.Now()) {
			return m, nil, true
		}
		cmd := m.beginDatabaseScan()
		return m, cmd, true
	case databaseScanMsg:
		if msg.generation != m.databaseScanGeneration {
			return m, nil, true
		}
		m.databaseScanning = false
		// Keep MRU and known metadata until fresh metadata arrives.
		old := map[string]dbindex.Entry{}
		for _, e := range m.databaseCache.Entries {
			old[e.Path] = e
		}
		for i, e := range msg.entries {
			if prev, ok := old[e.Path]; ok {
				msg.entries[i].Used = prev.Used
				msg.entries[i].Count = prev.Count
				msg.entries[i].Prefix = prev.Prefix
			}
		}
		m.databaseCache = dbindex.Cache{Scanned: time.Now().UTC(), Entries: msg.entries}
		// An explicit target stays pinned even when outside the curated roots.
		for _, path := range []string{m.explicitDatabase, m.database} {
			if path == "" {
				continue
			}
			found := false
			for _, e := range m.databaseCache.Entries {
				if e.Path == path {
					found = true
				}
			}
			if !found {
				m.databaseCache.Entries = append(m.databaseCache.Entries, dbindex.Entry{Path: path})
			}
		}
		m.sortDatabases()
		m.databaseIndex = 0
		dbindex.Write(databaseIndexPath(), m.databaseCache)
		var commands []tea.Cmd
		metadataContext, cancel := context.WithCancel(context.Background())
		m.databaseMetadataCancel = cancel
		// Three chains bound background subprocesses while keeping one locked
		// store from delaying every other project's metadata.
		for worker := 0; worker < 3; worker++ {
			var queue []dbindex.Entry
			for i := worker; i < len(m.databaseCache.Entries); i += 3 {
				queue = append(queue, m.databaseCache.Entries[i])
			}
			commands = append(commands, m.databaseMetadataCmd(metadataContext, queue, msg.generation))
		}
		if m.database == "" && len(m.databaseCache.Entries) > 0 {
			commands = append(commands, m.beginDatabaseSwitch(m.databaseCache.Entries[0].Path))
		}
		return m, tea.Batch(commands...), true
	case databaseMetadataMsg:
		if msg.generation != m.databaseScanGeneration {
			return m, nil, true
		}
		for i, e := range m.databaseCache.Entries {
			if e.Path == msg.entry.Path {
				msg.entry.Used = e.Used
				m.databaseCache.Entries[i] = msg.entry
				break
			}
		}
		dbindex.Write(databaseIndexPath(), m.databaseCache)
		return m, nil, true
	case databaseSwitchMsg:
		if m.databaseSwitching != msg.path {
			return m, nil, true
		}
		m.databaseSwitching = ""
		if msg.err != nil {
			log.Printf("database switch failed (path=%s): %v", msg.path, msg.err)
			m.databaseError = "Could not switch to " + (dbindex.Entry{Path: msg.path}).Name() + ": " + msg.err.Error()
			m.statusFlash = m.databaseError
			// The original model/backend/rows/name are still intact.
			return m, nil, true
		}
		m.saveState()
		fresh := newModel(msg.backend, msg.path)
		fresh.databaseState = m.databaseState
		fresh.database = msg.path
		fresh.databaseEpoch++
		fresh.databasePicker = false
		fresh.databaseError = ""
		fresh.width, fresh.height = m.width, m.height
		fresh.liveSessions = m.liveSessions
		fresh.touchDatabase()
		cmd := fresh.applyBoard(boardMsg{view: msg.view, generation: fresh.boardGen, issues: msg.issues})
		fresh.saveState()
		return fresh, tea.Batch(cmd, fresh.loadStatusesCmd()), true
	}
	return m, nil, false
}

func (m *Model) beginDatabaseScan() tea.Cmd {
	if m.databaseScanning {
		return nil
	}
	if m.databaseMetadataCancel != nil {
		m.databaseMetadataCancel()
		m.databaseMetadataCancel = nil
	}
	m.databaseScanning = true
	m.databaseScanGeneration++
	generation := m.databaseScanGeneration
	return func() tea.Msg {
		home, err := os.UserHomeDir()
		if err != nil {
			log.Printf("database discovery home: %v", err)
		}
		roots := []string{"/opt/ra", "/srv", "/arcs"}
		if home != "" {
			roots = append([]string{home}, roots...)
		}
		return databaseScanMsg{entries: dbindex.Discover(roots, 5), generation: generation}
	}
}

// Each chain processes one store at a time and stops when a rescan cancels it.
func (m Model) databaseMetadataCmd(parent context.Context, entries []dbindex.Entry, generation uint64) tea.Cmd {
	if len(entries) == 0 {
		return nil
	}
	entry := entries[0]
	rest := append([]dbindex.Entry(nil), entries[1:]...)
	return func() tea.Msg {
		if parent.Err() != nil {
			return nil
		}
		ctx, cancel := context.WithTimeout(parent, 5*time.Second)
		defer cancel()
		backend := m.databaseFactory(entry.Path)
		if all, ok := backend.(interface {
			ListAll(context.Context) ([]bd.Issue, error)
		}); ok {
			issues, err := all.ListAll(ctx)
			if err != nil {
				log.Printf("database metadata unavailable (path=%s): %v", entry.Path, err)
			} else {
				count := len(issues)
				entry.Count = &count
				for _, issue := range issues {
					if updated, err := time.Parse(time.RFC3339Nano, issue.UpdatedAt); err == nil && updated.After(entry.Touched) {
						entry.Touched = updated.UTC()
					}
				}
			}
		}
		if config, ok := backend.(interface {
			Prefix(context.Context) (string, error)
		}); ok {
			prefix, err := config.Prefix(ctx)
			if err != nil {
				log.Printf("database prefix unavailable (path=%s): %v", entry.Path, err)
			} else {
				entry.Prefix = prefix
			}
		}
		// Batch schedules the next entry without blocking delivery of this one.
		return tea.Batch(func() tea.Msg { return databaseMetadataMsg{entry: entry, generation: generation} }, m.databaseMetadataCmd(parent, rest, generation))()
	}
}

func (m *Model) beginDatabaseSwitch(path string) tea.Cmd {
	if path == m.database || m.databaseSwitching != "" {
		m.databasePicker = false
		return nil
	}
	m.databaseSwitching = path
	m.databaseError = ""
	backend := m.databaseFactory(path)
	state, _ := loadState()
	view := databaseSavedState(state, path).View
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), boardRetryTimeout)
		defer cancel()
		issues, err := backend.List(ctx, view)
		return databaseSwitchMsg{path: path, backend: backend, view: view, issues: issues, err: err}
	}
}
func (m Model) databaseKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "ctrl+c":
		m.quitting = true
		m.saveState()
		return m, tea.Quit
	case "esc", "q", "ctrl+o":
		m.databasePicker = false
		m.databaseSwitching = ""
		m.databaseError = ""
		return m, nil
	}
	if m.databaseSwitching != "" {
		return m, nil
	}
	switch key.String() {
	case "j", "down", "ctrl+n":
		m.databaseIndex = min(m.databaseIndex+1, max(0, len(m.databaseCache.Entries)-1))
	case "k", "up", "ctrl+p":
		m.databaseIndex = max(0, m.databaseIndex-1)
	case "g", "home":
		m.databaseIndex = 0
	case "G", "end":
		m.databaseIndex = max(0, len(m.databaseCache.Entries)-1)
	case "R":
		cmd := m.beginDatabaseScan()
		return m, cmd
	case "enter":
		if m.databaseIndex < len(m.databaseCache.Entries) {
			cmd := m.beginDatabaseSwitch(m.databaseCache.Entries[m.databaseIndex].Path)
			return m, cmd
		}
	}
	return m, nil
}
func (m Model) renderDatabases() string {
	home, err := os.UserHomeDir()
	if err != nil {
		log.Printf("database picker home: %v", err)
	}
	lines := []string{"j/k select · Enter switch · R rescan · esc close", ""}
	if m.databaseScanning {
		lines = append(lines, "Scanning local workspaces…")
	}
	if m.databaseSwitching != "" {
		lines = append(lines, "Opening "+m.databaseSwitching+"…")
	}
	if m.databaseError != "" {
		lines = append(lines, styleError.Render(m.databaseError))
	}
	entries := m.databaseCache.Entries
	if len(entries) == 0 {
		lines = append(lines, "No Beads databases found. R rescans local workspaces.")
	}
	// Each entry uses two lines; keep the selected entry in the viewport.
	visible := max(1, (m.height-len(lines)-3)/2)
	start := max(0, m.databaseIndex-visible+1)
	for i := start; i < len(entries) && i < start+visible; i++ {
		e := entries[i]
		mark := "  "
		if i == m.databaseIndex {
			mark = "▎ "
		}
		active := ""
		if e.Path == m.database {
			active = " · active"
		}
		if e.Path == m.explicitDatabase {
			active += " · BEADS_DIR"
		}
		count := "?"
		if e.Count != nil {
			count = fmt.Sprint(*e.Count)
		}
		touched := "unknown"
		if !e.Touched.IsZero() {
			touched = e.Touched.Local().Format("Jan 02 15:04")
		}
		lines = append(lines, fmt.Sprintf("%s%s%s · %s issues · prefix %s · %s", mark, e.Name(), active, count, orDash(e.Prefix), touched), "  "+e.DisplayPath(home))
	}
	title := "Databases" + m.databaseLabel()
	return strings.Join(pane(title, lines, max(20, m.width), max(6, m.height)), "\n")
}
