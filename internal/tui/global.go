package tui

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/RooseveltAdvisors/beads-tui/internal/bd"
	"github.com/RooseveltAdvisors/beads-tui/internal/dbindex"
	tea "github.com/charmbracelet/bubbletea"
)

const globalStateKey = "global"
const globalWorkers = 3

// One adapter owns immutable directory clients and a per-store snapshot cache.
// All routes require an explicit source key: there is no ambient-store fallback.
type aggregateBackend struct {
	clients map[string]Backend
	paths   []string
	mu      sync.RWMutex
	stores  map[string]aggregateStore
}
type aggregateStore struct {
	view      bd.View
	rows, all []bd.Issue
	statuses  []bd.StatusInfo
	err       error
}

func newAggregate(entries []dbindex.Entry, factory func(string) Backend, previous *aggregateBackend) *aggregateBackend {
	a := &aggregateBackend{clients: map[string]Backend{}, stores: map[string]aggregateStore{}}
	for _, e := range entries {
		if _, exists := a.clients[e.Path]; exists {
			continue
		}
		a.paths = append(a.paths, e.Path)
		a.clients[e.Path] = factory(e.Path)
		if previous != nil {
			previous.mu.RLock()
			cached, ok := previous.stores[e.Path]
			previous.mu.RUnlock()
			if ok {
				a.stores[e.Path] = cached
			}
		}
	}
	return a
}
func globalID(path, id string) string {
	if id == "" {
		return ""
	}
	return path + "\x1f" + id
}
func sourcedIssues(path string, rows []bd.Issue) []bd.Issue {
	rows = cloneIssues(rows)
	for i := range rows {
		r := &rows[i]
		r.SourcePath = path
		r.ID = globalID(path, r.ID)
		r.ParentID = globalID(path, r.ParentID)
		r.Dependencies = append([]bd.DepEdge(nil), r.Dependencies...)
		for j := range r.Dependencies {
			r.Dependencies[j].IssueID = globalID(path, r.Dependencies[j].IssueID)
			r.Dependencies[j].DependsOnID = globalID(path, r.Dependencies[j].DependsOnID)
		}
	}
	return rows
}
func (a *aggregateBackend) route(key string) (Backend, string, error) {
	path, id, ok := strings.Cut(key, "\x1f")
	client := a.clients[path]
	if !ok || id == "" || client == nil {
		return nil, "", fmt.Errorf("global row has no indexed source database")
	}
	return client, id, nil
}
func (a *aggregateBackend) List(_ context.Context, view bd.View) ([]bd.Issue, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	var rows []bd.Issue
	for _, path := range a.paths {
		s := a.stores[path]
		if s.view == view {
			rows = append(rows, s.rows...)
		} else if view != bd.ViewReady {
			for _, r := range s.all {
				if r.Status == string(view) {
					rows = append(rows, r)
				}
			}
		}
	}
	return cloneIssues(rows), nil
}
func (a *aggregateBackend) ListAll(context.Context) ([]bd.Issue, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	var rows []bd.Issue
	for _, path := range a.paths {
		rows = append(rows, a.stores[path].all...)
	}
	return cloneIssues(rows), nil
}
func (a *aggregateBackend) Statuses(context.Context) ([]bd.StatusInfo, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	seen := map[string]bool{}
	var statuses []bd.StatusInfo
	for _, path := range a.paths {
		for _, s := range a.stores[path].statuses {
			if !seen[s.Name] {
				statuses = append(statuses, s)
				seen[s.Name] = true
			}
		}
	}
	return statuses, nil
}
func (a *aggregateBackend) Show(ctx context.Context, key string) (*bd.Issue, error) {
	b, id, err := a.route(key)
	if err != nil {
		return nil, err
	}
	issue, err := b.Show(ctx, id)
	if issue == nil || err != nil {
		return nil, err
	}
	path, _, _ := strings.Cut(key, "\x1f")
	rows := sourcedIssues(path, []bd.Issue{*issue})
	return &rows[0], nil
}
func (a *aggregateBackend) Deps(ctx context.Context, key string, up bool) ([]bd.DepRecord, error) {
	b, id, err := a.route(key)
	if err != nil {
		return nil, err
	}
	records, err := b.Deps(ctx, id, up)
	records = append([]bd.DepRecord(nil), records...)
	path, _, _ := strings.Cut(key, "\x1f")
	for i := range records {
		records[i].ID = globalID(path, records[i].ID)
	}
	return records, err
}

// Every global snapshot comes from ListAll, which includes inline edges. Empty
// graphs need no per-bead subprocesses, even when there are thousands of rows.
func (a *aggregateBackend) DepsBatch(ctx context.Context, _ []string, up bool) (map[string][]bd.DepRecord, error) {
	all, _ := a.ListAll(ctx)
	down, reverse := normalizeGraphEdges(all, inlineDepRecords(all))
	if up {
		return reverse, nil
	}
	return down, nil
}
func (a *aggregateBackend) Comments(ctx context.Context, key string) ([]bd.Comment, error) {
	b, id, err := a.route(key)
	if err != nil {
		return nil, err
	}
	return b.Comments(ctx, id)
}
func (a *aggregateBackend) AddComment(ctx context.Context, key, text string) error {
	b, id, err := a.route(key)
	if err != nil {
		return err
	}
	return b.AddComment(ctx, id, text)
}

type globalLoadMsg struct {
	view       bd.View
	generation uint64
}
type globalStoreMsg struct {
	backend    *aggregateBackend
	view       bd.View
	generation uint64
	path       string
	store      aggregateStore
	rest       []string
}

// Three command chains provide incremental delivery and a bounded worker pool.
// A fresh board generation cancels the previous chains before starting new work.
func (a *aggregateBackend) storeCmd(parent context.Context, paths []string, view bd.View, gen uint64) tea.Cmd {
	if len(paths) == 0 {
		return nil
	}
	path := paths[0]
	rest := append([]string(nil), paths[1:]...)
	return func() tea.Msg {
		if parent.Err() != nil {
			return nil
		}
		ctx, cancel := context.WithTimeout(parent, boardRetryTimeout)
		defer cancel()
		b := a.clients[path]
		rows, err := b.List(ctx, view)
		s := aggregateStore{view: view, err: err}
		if err == nil {
			s.rows = sourcedIssues(path, rows)
			s.all = s.rows
			if graph, ok := b.(graphBackend); ok {
				all, graphErr := graph.ListAll(ctx)
				if graphErr != nil {
					log.Printf("global graph unavailable (path=%s): %v", path, graphErr)
					s.err = graphErr
					s.rows, s.all = nil, nil
				} else {
					s.all = sourcedIssues(path, all)
					s.rows = swapWithGraphRows(s.rows, s.all)
				}
			}
			statuses, statusErr := b.Statuses(ctx)
			if statusErr != nil {
				log.Printf("global statuses unavailable (path=%s): %v", path, statusErr)
			} else {
				s.statuses = statuses
			}
		} else {
			log.Printf("global database skipped (path=%s): %v", path, err)
		}
		return globalStoreMsg{backend: a, path: path, store: s, rest: rest, view: view, generation: gen}
	}
}
func (m *Model) enterGlobal() tea.Cmd {
	if m.globalMode {
		m.databasePicker = false
		return nil
	}
	m.saveState()
	a := newAggregate(m.databaseCache.Entries, m.databaseFactory, m.aggregate)
	fresh := newModel(a, globalStateKey)
	fresh.databaseState = m.databaseState
	fresh.globalMode = true
	fresh.aggregate = a
	fresh.backend = a
	fresh.databaseEpoch++
	fresh.databaseSwitching = ""
	fresh.databasePicker = false
	fresh.width, fresh.height = m.width, m.height
	fresh.liveSessions = m.liveSessions
	// Do not restore a disk snapshot for deleted or excluded workspaces.
	fresh.allRows, fresh.rows, fresh.treeRows, fresh.lastSnapshot = nil, nil, nil, nil
	*m = fresh
	m.saveState()
	return m.loadBoardCmd()
}
func (m *Model) beginGlobalLoad(msg globalLoadMsg) tea.Cmd {
	if !m.globalMode || msg.generation != m.boardGen || msg.view != m.view {
		return nil
	}
	if m.globalCancel != nil {
		m.globalCancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.globalContext = ctx
	m.globalCancel = cancel
	m.globalDone, m.globalTotal, m.globalFailed = 0, len(m.aggregate.paths), 0
	rows, _ := m.aggregate.List(ctx, m.view)
	initial := m.applyGlobalSnapshot(rows)
	var cmds []tea.Cmd
	cmds = append(cmds, initial)
	for worker := 0; worker < globalWorkers; worker++ {
		var paths []string
		for i := worker; i < len(m.aggregate.paths); i += globalWorkers {
			paths = append(paths, m.aggregate.paths[i])
		}
		cmds = append(cmds, m.aggregate.storeCmd(ctx, paths, m.view, m.boardGen))
	}
	return tea.Batch(cmds...)
}
func (m *Model) applyGlobalSnapshot(rows []bd.Issue) tea.Cmd {
	prev := m.selectedID()
	m.allRows = rows
	m.graphRows, _ = m.aggregate.ListAll(context.Background())
	m.deps, m.reverseDeps = normalizeGraphEdges(m.graphRows, inlineDepRecords(m.graphRows))
	m.allRows = enrichIssues(m.allRows, m.deps, m.reverseDeps)
	m.graphRows = enrichIssues(m.graphRows, m.deps, m.reverseDeps)
	m.graphEdges = countGraphEdges(m.deps)
	m.graphReady = true
	m.loading = false
	m.boardErr = ""
	m.graphCache = &graphCache{complete: true}
	m.boardLoadedAt = time.Now()
	return m.rebuildRows(prev)
}
func (m Model) globalNotice() string {
	if !m.globalMode {
		return ""
	}
	var parts []string
	if m.globalDone < m.globalTotal {
		parts = append(parts, fmt.Sprintf("loading %d/%d workspaces", m.globalDone, m.globalTotal))
	}
	if m.globalFailed > 0 {
		parts = append(parts, fmt.Sprintf("%d workspace(s) skipped · r retry", m.globalFailed))
	}
	if m.globalTotal == 0 {
		parts = append(parts, "No indexed workspaces · ctrl+o then R to rescan")
	}
	return strings.Join(parts, " · ")
}

// Existing row mutations retain the same explicit source routing as comments.
func (a *aggregateBackend) UpdateIssue(ctx context.Context, key string, fields map[string]string) error {
	b, id, err := a.route(key)
	if err != nil {
		return err
	}
	u, ok := b.(interface {
		UpdateIssue(context.Context, string, map[string]string) error
	})
	if !ok {
		return fmt.Errorf("source backend cannot update issues")
	}
	return u.UpdateIssue(ctx, id, fields)
}
func (a *aggregateBackend) CloseIssue(ctx context.Context, key, reason string) error {
	b, id, err := a.route(key)
	if err != nil {
		return err
	}
	u, ok := b.(interface {
		CloseIssue(context.Context, string, string) error
	})
	if !ok {
		return fmt.Errorf("source backend cannot close issues")
	}
	return u.CloseIssue(ctx, id, reason)
}
func (a *aggregateBackend) DeleteIssue(ctx context.Context, key string) error {
	b, id, err := a.route(key)
	if err != nil {
		return err
	}
	u, ok := b.(interface {
		DeleteIssue(context.Context, string) error
	})
	if !ok {
		return fmt.Errorf("source backend cannot delete issues")
	}
	return u.DeleteIssue(ctx, id)
}
func (a *aggregateBackend) SetLabels(ctx context.Context, key string, labels []string) error {
	b, id, err := a.route(key)
	if err != nil {
		return err
	}
	u, ok := b.(interface {
		SetLabels(context.Context, string, []string) error
	})
	if !ok {
		return fmt.Errorf("source backend cannot set labels")
	}
	return u.SetLabels(ctx, id, labels)
}
func (a *aggregateBackend) CreateIssue(context.Context, string, string) (*bd.Issue, error) {
	return nil, fmt.Errorf("select a workspace with ctrl+o before creating an issue")
}
