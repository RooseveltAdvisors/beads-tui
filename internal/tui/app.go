package tui

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/RooseveltAdvisors/beads-tui/internal/bd"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

// bdTimeout is the legacy single-call cap. bd retries busy/locked calls
// internally now (see internal/bd), so no TUI-side call should assume one
// attempt per bd invocation.
const bdTimeout = 8 * time.Second

// boardRetryTimeout caps every board/graph/status bd round-trip. It must
// comfortably exceed bd's worst-case internal retry schedule (3 attempts x
// 8s + waits) so a transiently locked store recovers here instead of
// dropping the graph or failing the detail load.
const boardRetryTimeout = 60 * time.Second

// detailDebounceDelay coalesces rapid list navigation so holding j/k does not
// spawn a bd process per row.
const detailDebounceDelay = 120 * time.Millisecond

// prefetchSpan is how far on either side of the selection the detail loader
// prefetches at idle.
const prefetchSpan = 3

// boardRetryBackoff returns the wait before the nth automatic board reload
// retry (n starts at 1), capped so retries keep coming without hammering bd.
func boardRetryBackoff(attempt int) time.Duration {
	switch {
	case attempt <= 1:
		return 2 * time.Second
	case attempt == 2:
		return 5 * time.Second
	default:
		return 15 * time.Second
	}
}

// boardLoadTimeout picks the deadline for a board load. bd retries busy
// stores internally, so every load gets the same generous cap.
func boardLoadTimeout(reloadAttempts int) time.Duration {
	return boardRetryTimeout
}

// isTimeoutErr reports whether err is (or carries) a context deadline.
func isTimeoutErr(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, context.DeadlineExceeded) ||
		strings.Contains(err.Error(), "context deadline exceeded") ||
		strings.Contains(err.Error(), "deadline exceeded")
}

// Focus tracks which pane receives the navigation keys.
type Focus int

const (
	// FocusList navigates the board rows.
	FocusList Focus = iota
	// FocusDetail scrolls the detail pane.
	FocusDetail
)

// LayoutMode selects how the list and detail panes are arranged.
type LayoutMode string

const (
	// LayoutAuto picks the arrangement from the terminal width: side-by-side
	// on wide terminals, stacked below the threshold.
	LayoutAuto LayoutMode = ""
	// LayoutSide forces the side-by-side list|detail split.
	LayoutSide LayoutMode = "side"
	// LayoutStacked forces the stacked list-over-detail split.
	LayoutStacked LayoutMode = "stacked"
)

// wideLayoutColumns is the width at which auto layout switches to a
// side-by-side split (gh-dash style).
const wideLayoutColumns = 140

// Backend is the data source and comment adapter the board renders. *bd.Client
// is the production implementation; tests supply fakes.
type Backend interface {
	List(ctx context.Context, view bd.View) ([]bd.Issue, error)
	Show(ctx context.Context, id string) (*bd.Issue, error)
	Deps(ctx context.Context, id string, up bool) ([]bd.DepRecord, error)
	Statuses(ctx context.Context) ([]bd.StatusInfo, error)
	Comments(ctx context.Context, id string) ([]bd.Comment, error)
	AddComment(ctx context.Context, id, text string) error
}

type graphBackend interface {
	Backend
	ListAll(ctx context.Context) ([]bd.Issue, error)
	DepsBatch(ctx context.Context, ids []string, up bool) (map[string][]bd.DepRecord, error)
}

// Model is the bead board application state.
type Model struct {
	backend Backend

	view           bd.View
	views          []bd.View
	sortMode       SortMode
	filter         Filter
	allRows        []bd.Issue
	deps           map[string][]bd.DepRecord
	reverseDeps    map[string][]bd.DepRecord
	graphRows      []bd.Issue
	rows           []bd.Issue
	treeRows       []TreeRow
	expanded       map[string]bool
	treeMode       bool
	vocab          Vocab
	boardErr       string
	loading        bool
	boardGen       uint64
	graphReady     bool
	graphEdges     int
	graphCache     *graphCache
	lastSnapshot   *boardSnapshot
	reloadAttempts int
	reloadNotice   string
	boardLoadedAt  time.Time

	selected int
	focus    Focus
	dOffset  int

	detail            *bd.Issue
	down              []bd.DepRecord
	up                []bd.DepRecord
	detailErr         string
	checking          bool
	detailGen         uint64
	detailPendingID   string
	detailCache       map[string]cachedDetail
	detailRefreshing  bool
	detailDebounceSeq uint64
	detailDebounce    time.Duration
	graph             bool
	layout            LayoutMode

	help         bool
	helpOffset   int
	filtering    bool
	searching    bool
	searchBase   Filter
	searchFocus  Focus
	searchID     string
	searchDOff   int
	searchDetail *bd.Issue
	searchDown   []bd.DepRecord
	searchUp     []bd.DepRecord
	searchDErr   string
	yank         bool
	yankIndex    int
	yankErr      string
	filterInput  textinput.Model
	quitting     bool
	markdown     *markdownRenderer

	commentsOpen        bool
	commentsID          string
	comments            []bd.Comment
	commentsErr         string
	commentsLoading     bool
	commentsPendingID   string
	commentsOffset      int
	commentsGeneration  uint64
	commentsInput       textinput.Model
	commentsInputActive bool
	commentsSubmitting  bool

	width  int
	height int
}

// boardMsg carries the result of a board load.
type boardMsg struct {
	view       bd.View
	generation uint64
	issues     []bd.Issue
	deps       map[string][]bd.DepRecord
	err        error
	timeout    time.Duration
}

// cachedDetail is one bead's fetched detail snapshot, cached per id and
// updated_at so a row whose content changed is treated as a cache miss.
type cachedDetail struct {
	issue bd.Issue
	down  []bd.DepRecord
	up    []bd.DepRecord
}

// prefetchHit pairs a cache key with the detail snapshot fetched at idle.
type prefetchHit struct {
	key    string
	detail cachedDetail
}

// boardRetryMsg asks the model to re-run the board load after a backoff wait.
type boardRetryMsg struct{}

// detailDebounceMsg fires after the selection has been stable for the debounce
// delay; stale ticks (newer selection changes bumped the sequence) are dropped.
type detailDebounceMsg struct {
	seq uint64
}

// prefetchMsg carries detail snapshots for neighbours of the selection.
type prefetchMsg struct {
	hits []prefetchHit
}

// graphMsg carries best-effort dependency enrichment separately from the list
// snapshot so the first rows paint without waiting on graph subprocesses.
type graphMsg struct {
	view        bd.View
	generation  uint64
	issues      []bd.Issue
	graphIssues []bd.Issue
	deps        map[string][]bd.DepRecord
	reverseDeps map[string][]bd.DepRecord
	edgeCount   int
	complete    bool
	cache       *graphCache
}

type graphCache struct {
	view        bd.View
	generation  uint64
	currentIDs  []string
	issues      []bd.Issue
	graphIssues []bd.Issue
	deps        map[string][]bd.DepRecord
	reverseDeps map[string][]bd.DepRecord
	edgeCount   int
	complete    bool
}

type boardSnapshot struct {
	View   bd.View    `json:"view"`
	Issues []bd.Issue `json:"issues"`
}

// statusMsg carries the status vocabulary.
type statusMsg struct {
	statuses []bd.StatusInfo
	err      error
}

// detailMsg carries the detail snapshot for one bead: the issue plus both
// dependency directions.
type detailMsg struct {
	id         string
	generation uint64
	issue      *bd.Issue
	down       []bd.DepRecord
	up         []bd.DepRecord
	err        error
}

type commentsMsg struct {
	id         string
	generation uint64
	comments   []bd.Comment
	err        error
}

type commentSubmitMsg struct {
	id   string
	text string
	err  error
}

type yankItem struct {
	label string
	value string
}

type yankMsg struct {
	err error
}

type savedState struct {
	View     bd.View         `json:"view"`
	SortMode SortMode        `json:"sort_mode"`
	Filter   Filter          `json:"filter"`
	Expanded map[string]bool `json:"expanded,omitempty"`
	Layout   LayoutMode      `json:"layout,omitempty"`
	Snapshot *boardSnapshot  `json:"board_snapshot,omitempty"`
}

func statePath() (string, error) {
	if dir := strings.TrimSpace(os.Getenv("BEADS_TUI_CONFIG_DIR")); dir != "" {
		return filepath.Join(dir, "state.json"), nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "beads-tui", "state.json"), nil
}

func loadState() (savedState, bool) {
	path, err := statePath()
	if err != nil {
		log.Printf("beads-tui: locate state: %v", err)
		return savedState{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("beads-tui: read state: %v", err)
		}
		return savedState{}, false
	}
	var state savedState
	if err := json.Unmarshal(data, &state); err != nil {
		log.Printf("beads-tui: parse state: %v", err)
		return savedState{}, false
	}
	state.View = normalizeStateView(state.View)
	if state.View == "" {
		state.View = bd.ViewOpen
	}
	if state.Expanded == nil {
		state.Expanded = map[string]bool{}
	}
	if state.Snapshot != nil {
		state.Snapshot.View = normalizeStateView(state.Snapshot.View)
		if state.Snapshot.View == "" {
			state.Snapshot = nil
		} else {
			state.Snapshot.Issues = cloneIssues(state.Snapshot.Issues)
		}
	}
	if state.SortMode > SortDependents {
		state.SortMode = SortCreated
	}
	switch state.Layout {
	case LayoutSide, LayoutStacked:
	default:
		state.Layout = LayoutAuto
	}
	if state.Filter.Kind > FilterSearch || len(state.Filter.Query) > 120 {
		state.Filter = Filter{}
	}
	return state, true
}

func normalizeStateView(view bd.View) bd.View {
	view = bd.View(strings.ToLower(strings.TrimSpace(string(view))))
	if !view.Valid() || view == bd.View("ready") || view == bd.View("all") {
		return ""
	}
	return view
}

func persistenceEnabled(backend Backend) bool {
	if backend == nil {
		return false
	}
	_, configured := os.LookupEnv("BEADS_TUI_CONFIG_DIR")
	_, production := backend.(*bd.Client)
	return configured || production
}

func (m Model) saveState() {
	if !persistenceEnabled(m.backend) {
		return
	}
	path, err := statePath()
	if err != nil {
		log.Printf("beads-tui: locate state: %v", err)
		return
	}
	data, err := json.MarshalIndent(savedState{
		View:     m.view,
		SortMode: m.sortMode,
		Filter:   m.filter,
		Expanded: cloneExpanded(m.expanded),
		Layout:   m.layout,
		Snapshot: cloneBoardSnapshot(m.lastSnapshot),
	}, "", "  ")
	if err != nil {
		log.Printf("beads-tui: encode state: %v", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		log.Printf("beads-tui: create state directory: %v", err)
		return
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		log.Printf("beads-tui: write state: %v", err)
	}
}

// New builds the board model backed by the given read-only data source.
func New(backend Backend) Model {
	input := textinput.New()
	input.Prompt = "Search / › "
	input.Placeholder = "status:open  priority:P1  label:frontend  text"
	input.CharLimit = 120
	input.Width = 60
	commentInput := textinput.New()
	commentInput.Prompt = "Comment › "
	commentInput.Placeholder = "Write a comment"
	commentInput.CharLimit = 4000
	commentInput.Width = 80
	m := Model{
		backend:  backend,
		view:     bd.ViewOpen,
		views:    bd.DefaultViews(),
		loading:  true,
		boardGen: 1,
		// Created is newest-first by default; s cycles through the remaining modes.
		sortMode:       SortCreated,
		focus:          FocusList,
		vocab:          NewVocab(nil),
		filterInput:    input,
		width:          80,
		height:         24,
		markdown:       &markdownRenderer{},
		expanded:       map[string]bool{},
		treeMode:       true,
		detailCache:    map[string]cachedDetail{},
		detailDebounce: detailDebounceDelay,
		commentsInput:  commentInput,
	}
	if persistenceEnabled(backend) {
		if state, ok := loadState(); ok {
			m.view, m.sortMode, m.filter = state.View, state.SortMode, state.Filter
			m.expanded = state.Expanded
			m.layout = state.Layout
			m.lastSnapshot = cloneBoardSnapshot(state.Snapshot)
			if m.lastSnapshot != nil && m.lastSnapshot.View == m.view {
				m.allRows = cloneIssues(m.lastSnapshot.Issues)
				m.projectRows("")
			}
		}
	}
	return m
}

// Init loads the board and the status vocabulary.
func (m Model) Init() tea.Cmd {
	return tea.Batch(m.loadBoardCmd(), m.loadStatusesCmd())
}

// Update drives the application.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		if m.detail != nil {
			lines := len(m.buildDetail(m.detailWidth()))
			_, maxOffset := m.detailContentBudget(lines)
			if m.dOffset > maxOffset {
				m.dOffset = maxOffset
			}
			if m.dOffset < 0 {
				m.dOffset = 0
			}
		}
	case tea.KeyMsg:
		// Terminals and tmux can deliver several runes in one read (fast
		// typing, key repeat, paste); handle each rune so a burst like
		// "jjjj" moves four rows instead of being silently dropped.
		if msg.Type == tea.KeyRunes && len(msg.Runes) > 1 {
			var last tea.Cmd
			for _, r := range msg.Runes {
				updated, cmd := m.updateKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}, Alt: msg.Alt})
				nm, ok := updated.(Model)
				if !ok {
					return m, cmd
				}
				m = nm
				last = cmd
			}
			return m, last
		}
		return m.updateKey(msg)
	case boardMsg:
		return m, m.applyBoard(msg)
	case boardRetryMsg:
		if m.loading || m.reloadAttempts == 0 {
			return m, nil
		}
		m.boardGen++
		m.loading = true
		return m, m.loadBoardCmd()
	case detailDebounceMsg:
		if msg.seq != m.detailDebounceSeq {
			return m, nil
		}
		return m, m.startDetailLoad()
	case prefetchMsg:
		for _, hit := range msg.hits {
			m.detailCache[hit.key] = hit.detail
		}
	case graphMsg:
		if msg.generation != m.boardGen || msg.view != m.view {
			return m, nil
		}
		prev := m.selectedID()
		commentCounts := make(map[string]int, len(m.allRows))
		for _, issue := range m.allRows {
			if issue.CommentCount > 0 {
				commentCounts[issue.ID] = issue.CommentCount
			}
		}
		for i := range msg.issues {
			if count := commentCounts[msg.issues[i].ID]; count > msg.issues[i].CommentCount {
				msg.issues[i].CommentCount = count
			}
		}
		for i := range msg.graphIssues {
			if count := commentCounts[msg.graphIssues[i].ID]; count > msg.graphIssues[i].CommentCount {
				msg.graphIssues[i].CommentCount = count
			}
		}
		m.allRows = append([]bd.Issue(nil), msg.issues...)
		m.graphRows = append([]bd.Issue(nil), msg.graphIssues...)
		m.deps = cloneDepMap(msg.deps)
		m.reverseDeps = cloneDepMap(msg.reverseDeps)
		m.graphEdges = msg.edgeCount
		m.graphCache = msg.cache
		m.graphReady = true
		if msg.complete && m.detailPendingID == "" && m.detail != nil && m.detail.ID == prev {
			for _, issue := range msg.issues {
				if issue.ID == m.detail.ID {
					detail := *m.detail
					detail = normalizeIssueCounts(detail, m.deps[detail.ID], m.reverseDeps[detail.ID])
					m.detail = &detail
					m.down = append([]bd.DepRecord(nil), m.deps[detail.ID]...)
					m.up = append([]bd.DepRecord(nil), m.reverseDeps[detail.ID]...)
					break
				}
			}
		}
		return m, m.rebuildRows(prev)
	case statusMsg:
		if msg.err == nil {
			m.vocab = NewVocab(msg.statuses)
			m.views = bd.ViewsFromStatuses(msg.statuses)
			if !m.hasView(m.view) {
				m.view = m.views[0]
				m.saveState()
				return m, m.startBoardLoad()
			}
		}
	case detailMsg:
		return m, m.applyDetail(msg)
	case commentsMsg:
		return m, m.applyComments(msg)
	case commentSubmitMsg:
		return m, m.applyCommentSubmit(msg)
	case yankMsg:
		if msg.err != nil {
			m.yankErr = msg.err.Error()
		} else {
			m.yankErr = "copied"
		}
	}
	return m, nil
}

func (m Model) updateKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.quitting {
		return m, nil
	}
	if m.yank {
		switch msg.String() {
		case "esc", "y":
			m.yank = false
			m.yankErr = ""
		case "j", "down":
			if m.yankIndex < len(m.yankItems())-1 {
				m.yankIndex++
			}
		case "k", "up":
			if m.yankIndex > 0 {
				m.yankIndex--
			}
		case "enter":
			items := m.yankItems()
			if len(items) == 0 {
				return m, nil
			}
			m.yankIndex = min(max(m.yankIndex, 0), len(items)-1)
			return m, copyToClipboard(items[m.yankIndex].value)
		}
		return m, nil
	}
	if m.commentsOpen {
		if msg.String() == "ctrl+c" {
			m.quitting = true
			m.saveState()
			return m, tea.Quit
		}
		if msg.String() == "?" && !m.commentsInputActive {
			m.help = true
			m.helpOffset = 0
			return m, nil
		}
		return m.commentsKey(msg)
	}
	if m.graph {
		switch msg.String() {
		case "esc", "G":
			m.graph = false
		}
		return m, nil
	}
	if msg.String() == "ctrl+c" {
		m.quitting = true
		m.saveState()
		return m, tea.Quit
	}
	if m.help {
		switch msg.String() {
		case "j", "down":
			m.helpOffset = min(m.helpOffset+1, m.helpMaxOffset())
			return m, nil
		case "k", "up":
			m.helpOffset = max(m.helpOffset-1, 0)
			return m, nil
		}
		m.help = false
		m.helpOffset = 0
		return m, nil
	}
	if m.filtering {
		switch msg.String() {
		case "esc":
			m.filtering = false
			m.filterInput.Blur()
			m.filter = m.searchBase
			m.detail = m.searchDetail
			m.down = m.searchDown
			m.up = m.searchUp
			m.detailErr = m.searchDErr
			m.checking = false
			m.detailRefreshing = false
			m.detailGen++
			m.detailPendingID = ""
			cmd := m.rebuildRows(m.searchID)
			m.focus = m.searchFocus
			m.dOffset = m.searchDOff
			m.saveState()
			m.searching = false
			return m, cmd
		case "enter":
			m.filtering = false
			m.filterInput.Blur()
			m.filter = ParseSearchFilter(m.filterInput.Value())
			m.saveState()
			m.searching = false
			return m, m.rebuildRows(m.selectedID())
		}
		var cmd tea.Cmd
		m.filterInput, cmd = m.filterInput.Update(msg)
		previousID := m.selectedID()
		m.filter = ParseSearchFilter(m.filterInput.Value())
		filterCmd := m.rebuildRows(previousID)
		if cmd != nil && filterCmd != nil {
			return m, tea.Batch(cmd, filterCmd)
		}
		if filterCmd != nil {
			return m, filterCmd
		}
		return m, cmd
	}
	key := msg.String()
	if len(key) == 1 && key[0] >= '1' && key[0] <= '9' {
		return m.switchView(m.viewAt(int(key[0] - '1')))
	}
	switch key {
	case "?":
		m.help = true
		m.helpOffset = 0
		return m, nil
	case "q":
		m.quitting = true
		m.saveState()
		return m, tea.Quit
	case "c":
		return m, m.openComments(false)
	case "C":
		if m.focus == FocusList {
			return m, m.openComments(true)
		}
		return m, nil
	case "y":
		if len(m.yankItems()) > 0 {
			m.yank = true
			m.yankIndex = 0
			m.yankErr = ""
		}
		return m, nil
	case "esc":
		if m.filter.Active() {
			m.filter = Filter{}
			m.saveState()
			return m, m.rebuildRows(m.selectedID())
		}
		if m.focus == FocusDetail {
			m.focus = FocusList
			m.dOffset = 0
		}
		return m, nil
	case "s":
		m.sortMode = m.sortMode.Next()
		m.saveState()
		return m, m.rebuildRows(m.selectedID())
	case "V":
		switch m.layout {
		case LayoutSide:
			m.layout = LayoutStacked
		case LayoutStacked:
			m.layout = LayoutAuto
		default:
			m.layout = LayoutSide
		}
		lines := len(m.buildDetail(m.detailWidth()))
		_, maxOffset := m.detailContentBudget(lines)
		if m.dOffset > maxOffset {
			m.dOffset = maxOffset
		}
		if m.dOffset < 0 {
			m.dOffset = 0
		}
		m.saveState()
		return m, nil
	case "/":
		m.searchBase = m.filter
		return m, m.openPrompt()
	case "t":
		if id := m.selectedID(); id != "" {
			for _, issue := range m.rows {
				if issue.ID == id && len(issue.Labels) > 0 {
					labels := make([]string, 0, len(issue.Labels))
					for _, label := range issue.Labels {
						if label = strings.TrimSpace(label); label != "" {
							labels = append(labels, strings.ToLower(label))
						}
					}
					if len(labels) == 0 {
						return m, nil
					}
					m.filter = Filter{Kind: FilterLabel, Query: strings.Join(labels, ",")}
					m.saveState()
					return m, m.rebuildRows(id)
				}
			}
		}
	case "r":
		// Reload the current view/sort/filter in place. A failed or slow
		// reload never discards the board that is already on screen.
		return m, m.startBoardLoad()
	case "R":
		wasOpen := m.view == bd.ViewOpen
		previousID := m.selectedID()
		m.view, m.sortMode, m.filter = bd.ViewOpen, SortCreated, Filter{}
		m.saveState()
		if !wasOpen {
			return m, m.startBoardLoad()
		}
		return m, m.rebuildRows(previousID)
	}
	if m.focus == FocusList {
		return m.listKey(msg)
	}
	return m.detailKey(msg)
}

func (m *Model) openPrompt() tea.Cmd {
	m.filtering = true
	m.searching = true
	m.searchFocus = m.focus
	id := m.selectedID()
	// Keep the last visible detail while the same bead's replacement request is
	// pending; cancelling search should restore the context the user left.
	if m.detail != nil || m.searchID != id {
		m.searchDOff = m.dOffset
		m.searchDetail = m.detail
		m.searchDown = m.down
		m.searchUp = m.up
		m.searchDErr = m.detailErr
	}
	m.searchID = id
	m.focus = FocusList
	m.filterInput.Prompt = "Search / › "
	m.filterInput.SetValue("")
	m.filterInput.Focus()
	return textinput.Blink
}

// switchView changes the board view, keeping the selection stable by id when
// the same bead is still present.
func (m Model) switchView(view bd.View) (tea.Model, tea.Cmd) {
	if view == m.view {
		return m, nil
	}
	m.view = view
	m.saveState()
	return m, m.startBoardLoad()
}

func (m Model) viewAt(index int) bd.View {
	if index >= 0 && index < len(m.views) {
		return m.views[index]
	}
	return m.view
}

func (m Model) hasView(view bd.View) bool {
	for _, candidate := range m.views {
		if candidate == view {
			return true
		}
	}
	return false
}

func (m Model) selectedID() string {
	if m.selected >= 0 && m.selected < len(m.rows) {
		return m.rows[m.selected].ID
	}
	return ""
}

func (m Model) yankItems() []yankItem {
	if m.selected < 0 || m.selected >= len(m.rows) {
		return nil
	}
	issue := m.rows[m.selected]
	items := []yankItem{{label: "ID", value: issue.ID}, {label: "Title", value: issue.Title}}
	if strings.TrimSpace(issue.URL) != "" {
		items = append(items, yankItem{label: "URL", value: issue.URL})
	}
	return items
}

func copyToClipboard(value string) tea.Cmd {
	return func() tea.Msg {
		if path, err := exec.LookPath("clipboard-copy"); err == nil {
			cmd := exec.Command(path)
			cmd.Stdin = strings.NewReader(value)
			if err := cmd.Run(); err == nil {
				return yankMsg{}
			} else {
				log.Printf("beads-tui: clipboard-copy failed: %v", err)
			}
		} else {
			log.Printf("beads-tui: clipboard-copy unavailable: %v", err)
		}
		encoded := base64.StdEncoding.EncodeToString([]byte(value))
		if _, err := fmt.Fprint(os.Stdout, "\x1b]52;c;"+encoded+"\a"); err != nil {
			return yankMsg{err: err}
		}
		return yankMsg{}
	}
}

// rebuildRows applies the current filter and sort while preserving selection
// by bead id. It also prepares detail for the selected row (cache-first) and
// schedules the debounced (re)load.
func (m *Model) rebuildRows(previousID string) tea.Cmd {
	m.projectRows(previousID)
	if len(m.rows) == 0 {
		m.detailGen++
		m.detailPendingID = ""
		m.detail, m.down, m.up = nil, nil, nil
		m.detailErr = ""
		m.checking = false
		m.detailRefreshing = false
		return nil
	}
	id := m.rows[m.selected].ID
	if m.detailPendingID == id {
		m.checking = true
		return nil
	}
	if m.detail != nil && m.detail.ID == id {
		m.checking = false
		return nil
	}
	return m.prepareDetailForSelection()
}

// detailCacheKey namespaces a cached detail by bead id and its updated_at so
// refreshed rows invalidate their own entries.
func detailCacheKey(id, updatedAt string) string {
	return id + "\x00" + updatedAt
}

// detailCacheKeyFor builds the cache key for id from the already-loaded list
// rows; no bd call is needed to decide cache validity.
func (m Model) detailCacheKeyFor(id string) string {
	for i := range m.rows {
		if m.rows[i].ID == id {
			return detailCacheKey(id, m.rows[i].UpdatedAt)
		}
	}
	for i := range m.allRows {
		if m.allRows[i].ID == id {
			return detailCacheKey(id, m.allRows[i].UpdatedAt)
		}
	}
	return detailCacheKey(id, "")
}

// prepareDetailForSelection switches the detail pane to the selected row
// without touching bd: cached detail installs instantly, a miss clears the
// pane. It always schedules the debounced (re)load.
func (m *Model) prepareDetailForSelection() tea.Cmd {
	id := m.selectedID()
	if id == "" {
		return nil
	}
	m.prepareCommentsForSelection(id)
	if m.detailPendingID != "" && m.detailPendingID != id {
		m.detailGen++
		m.detailPendingID = ""
	}
	if m.detail != nil && m.detail.ID == id {
		m.checking = false
		return m.scheduleDetailDebounce()
	}
	if entry, ok := m.detailCache[m.detailCacheKeyFor(id)]; ok {
		m.installCachedDetail(entry)
		return m.scheduleDetailDebounce()
	}
	m.detail, m.down, m.up = nil, nil, nil
	m.detailErr = ""
	m.checking = true
	m.detailRefreshing = false
	m.dOffset = 0
	return m.scheduleDetailDebounce()
}

// installCachedDetail paints a cached snapshot as the current detail.
func (m *Model) installCachedDetail(entry cachedDetail) {
	issue := entry.issue
	issue = normalizeIssueCounts(issue, entry.down, entry.up)
	m.detail = &issue
	m.down = append([]bd.DepRecord(nil), entry.down...)
	m.up = append([]bd.DepRecord(nil), entry.up...)
	m.detailErr = ""
	m.checking = false
	m.detailRefreshing = false
	m.dOffset = 0
}

// scheduleDetailDebounce arms the coalescing timer; every selection change
// bumps the sequence so superseded ticks are dropped when they fire.
func (m *Model) scheduleDetailDebounce() tea.Cmd {
	m.detailDebounceSeq++
	seq := m.detailDebounceSeq
	delay := m.detailDebounce
	if delay < 0 {
		delay = 0
	}
	return tea.Tick(delay, func(time.Time) tea.Msg {
		return detailDebounceMsg{seq: seq}
	})
}

// startDetailLoad runs after the debounce settles: it fetches the selected
// bead's detail (refreshing in place when a cached copy is already shown) and
// prefetches the neighbours at idle.
func (m *Model) startDetailLoad() tea.Cmd {
	if m.selected < 0 || m.selected >= len(m.rows) {
		return nil
	}
	id := m.rows[m.selected].ID
	if m.detailPendingID == id {
		return nil
	}
	fetch := m.beginDetailFetch(id, true)
	return tea.Batch(fetch, m.prefetchNeighboursCmd(id), m.startPendingCommentsLoad(id))
}

// beginDetailFetch marks a detail fetch for id as in flight and returns the
// fetch command. keepStale keeps an already-shown snapshot for the same bead
// visible (with a refreshing marker) instead of blanking the pane.
func (m *Model) beginDetailFetch(id string, keepStale bool) tea.Cmd {
	m.detailGen++
	generation := m.detailGen
	m.detailPendingID = id
	if keepStale && m.detail != nil && m.detail.ID == id {
		m.checking = false
		m.detailRefreshing = true
	} else {
		m.checking = true
		m.detailRefreshing = false
		m.detail, m.down, m.up = nil, nil, nil
		m.detailErr = ""
		m.dOffset = 0
	}
	return m.fetchDetailCmd(id, generation)
}

func (m *Model) projectRows(previousID string) {
	filtered := FilterIssues(m.allRows, m.filter)
	if m.filter.Active() {
		// A matching child keeps its parent chain visible.
		filtered = withAncestors(filtered, m.allRows)
	}
	projected := SortIssues(filtered, m.sortMode)
	if m.expanded == nil {
		m.expanded = map[string]bool{}
	}
	if m.treeMode && m.filter.Kind != FilterSearch {
		roots := BuildDependencyTree(projected, m.deps, m.sortMode)
		m.treeRows = FlattenDependencyTree(roots, m.expanded)
		m.rows = make([]bd.Issue, len(m.treeRows))
		for i, row := range m.treeRows {
			m.rows[i] = row.Issue
		}
	} else {
		m.treeRows = nil
		m.rows = projected
	}
	m.selected = 0
	if previousID != "" {
		for i := range m.rows {
			if m.rows[i].ID == previousID {
				m.selected = i
				break
			}
		}
	}
	m.clampYankIndex()
	if m.filter.Kind == FilterSearch && len(m.rows) > 0 {
		m.expandAncestors(m.rows[m.selected].ID)
	}
}

func (m *Model) clampYankIndex() {
	items := m.yankItems()
	if len(items) == 0 {
		m.yankIndex = 0
		return
	}
	m.yankIndex = min(max(m.yankIndex, 0), len(items)-1)
}

func (m *Model) expandAncestors(id string) {
	var visit func(*TreeNode, []string, map[string]bool) bool
	visit = func(node *TreeNode, path []string, inPath map[string]bool) bool {
		if node == nil || inPath[node.Issue.ID] {
			return false
		}
		if node.Issue.ID == id {
			for _, ancestor := range path {
				m.expanded[ancestor] = true
			}
			return true
		}
		inPath[node.Issue.ID] = true
		path = append(path, node.Issue.ID)
		for _, child := range node.Children {
			if visit(child, path, inPath) {
				delete(inPath, node.Issue.ID)
				return true
			}
		}
		delete(inPath, node.Issue.ID)
		return false
	}
	for _, root := range BuildDependencyTree(m.allRows, m.deps, m.sortMode) {
		if visit(root, nil, map[string]bool{}) {
			return
		}
	}
}

// navIndexes returns the wrapped step for list navigation (no-op on empty).
func (m Model) listKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	n := len(m.rows)
	if n == 0 {
		return m, nil
	}
	before := m.selected
	page := m.pageStep()
	switch msg.String() {
	case "j", "down":
		m.selected++
	case "k", "up":
		m.selected--
	case "g":
		m.selected = 0
	case "G":
		if m.hasGraphEdges() {
			m.graph = true
			return m, nil
		}
		m.selected = n - 1
	case "v":
		m.treeMode = !m.treeMode
		return m, m.rebuildRows(m.rows[m.selected].ID)
	case "enter", "tab":
		if row, ok := m.selectedTreeRow(); ok && row.HasChildren {
			return m, m.toggleSelectedTreeRow()
		}
		if msg.String() == "tab" {
			return m, nil
		}
		m.focus = FocusDetail
		m.dOffset = 0
	case "h", "left":
		if row, ok := m.selectedTreeRow(); ok && row.HasChildren && row.Expanded {
			return m, m.foldRow(row.Issue.ID)
		}
		return m, nil
	case "l", "right":
		if row, ok := m.selectedTreeRow(); ok && row.HasChildren && !row.Expanded {
			return m, m.unfoldRow(row.Issue.ID)
		}
		m.focus = FocusDetail
		m.dOffset = 0
	case "L":
		m.focus = FocusDetail
		m.dOffset = 0
	case "*":
		return m, m.expandAll()
	case " ", "pgdown", "ctrl+f":
		m.selected += page
	case "b", "pgup", "ctrl+b":
		m.selected -= page
	case "ctrl+d":
		m.selected += m.halfPageStep()
	case "ctrl+u":
		m.selected -= m.halfPageStep()
	default:
		return m, nil
	}
	m = m.clampSelection()
	if m.selected != before {
		if m.filter.Kind == FilterSearch {
			m.expandAncestors(m.rows[m.selected].ID)
		}
		return m, m.prepareDetailForSelection()
	}
	return m, nil
}

func (m Model) detailKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	lines := len(m.buildDetail(m.detailWidth()))
	_, maxOffset := m.detailContentBudget(lines)
	page := m.pageStep()
	switch msg.String() {
	case "j", "down":
		m.dOffset++
	case "k", "up":
		m.dOffset--
	case "g":
		m.dOffset = 0
	case "G":
		if m.hasGraphEdges() {
			m.graph = true
			return m, nil
		}
		m.dOffset = maxOffset
	case " ", "pgdown", "ctrl+f":
		m.dOffset += page
	case "b", "pgup", "ctrl+b":
		m.dOffset -= page
	case "ctrl+d":
		m.dOffset += m.halfPageStep()
	case "ctrl+u":
		m.dOffset -= m.halfPageStep()
	case "h", "H", "left":
		m.focus = FocusList
		m.dOffset = 0
	case "l", "L", "right":
		m.focus = FocusDetail
	default:
		return m, nil
	}
	if m.dOffset < 0 {
		m.dOffset = 0
	}
	if m.dOffset > maxOffset {
		m.dOffset = maxOffset
	}
	return m, nil
}

// clampSelection returns m with the selection clamped into the row range.
func (m Model) clampSelection() Model {
	n := len(m.rows)
	if n == 0 {
		m.selected = 0
		return m
	}
	if m.selected < 0 {
		m.selected = 0
	}
	if m.selected >= n {
		m.selected = n - 1
	}
	return m
}

// pageStep is a rough page size used for paging keys.
func (m Model) pageStep() int {
	if s := m.height - 4; s > 1 {
		return s
	}
	return 10
}

// halfPageStep is the distance used by ctrl-u/ctrl-d, matching the terminal's
// usual half-page navigation convention.
func (m Model) halfPageStep() int {
	step := m.pageStep() / 2
	if step < 1 {
		return 1
	}
	return step
}

func (m Model) buildDetail(width int) []string {
	if m.markdown == nil {
		m.markdown = &markdownRenderer{}
	}
	chain, children := m.hierarchyFor(m.detail)
	commentCount := len(m.comments)
	if m.detail != nil && m.detail.CommentCount > commentCount {
		commentCount = m.detail.CommentCount
	}
	return buildDetailWithComments(
		m.vocab, m.detail, m.down, m.up, chain, children, width, m.markdown,
		m.comments, m.commentsLoading, m.commentsErr, commentCount, m.inlineCommentsMaxLines(),
	)
}

// hierarchyFor derives the parent chain and direct children of d from the
// rows already loaded, so the detail pane never waits on another bd call.
func (m Model) hierarchyFor(d *bd.Issue) (chain, children []bd.Issue) {
	if d == nil {
		return nil, nil
	}
	byID := make(map[string]bd.Issue, len(m.allRows)+len(m.graphRows))
	for _, issue := range m.allRows {
		if issue.ID != "" {
			byID[issue.ID] = issue
		}
	}
	for _, issue := range m.graphRows {
		if issue.ID != "" {
			if _, ok := byID[issue.ID]; !ok {
				byID[issue.ID] = issue
			}
		}
	}
	seen := map[string]bool{d.ID: true}
	parentID := strings.TrimSpace(d.ParentID)
	for parentID != "" && !seen[parentID] {
		parent, ok := byID[parentID]
		if !ok {
			break
		}
		seen[parentID] = true
		chain = append(chain, parent)
		parentID = strings.TrimSpace(parent.ParentID)
	}
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	for _, issue := range byID {
		if issue.ParentID == d.ID && issue.ID != d.ID {
			children = append(children, issue)
		}
	}
	children = SortIssues(children, m.sortMode)
	return chain, children
}

func (m *Model) startBoardLoad() tea.Cmd {
	m.boardGen++
	m.loading = true
	m.reloadAttempts = 0
	m.reloadNotice = ""
	m.invalidateDetail()
	m.graphReady = false
	m.graphEdges = 0
	m.graphCache = nil
	m.deps = nil
	m.reverseDeps = nil
	m.graphRows = nil
	return m.loadBoardCmd()
}

func (m Model) loadBoardCmd() tea.Cmd {
	view := m.view
	generation := m.boardGen
	backend := m.backend
	timeout := boardLoadTimeout(m.reloadAttempts)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		issues, err := backend.List(ctx, view)
		cancel()
		if err != nil {
			return boardMsg{view: view, generation: generation, err: err, timeout: timeout}
		}
		return boardMsg{view: view, generation: generation, issues: issues, timeout: timeout}
	}
}

// loadGraphCmd enriches a painted list with one bounded, cached dependency
// pass. The board never waits for this best-effort metadata before first paint.
func (m Model) loadGraphCmd(view bd.View, generation uint64, issues []bd.Issue) tea.Cmd {
	backend, supportsGraph := m.backend.(graphBackend)
	cache := m.graphCache
	currentIDs := issueIDs(issues)
	return func() tea.Msg {
		if graphCacheMatches(cache, view, generation, currentIDs) {
			return graphMsgFromCache(cache)
		}
		current := cloneIssues(issues)
		graphIssues := cloneIssues(issues)
		deps := map[string][]bd.DepRecord{}
		reverseDeps := map[string][]bd.DepRecord{}
		graphComplete := false
		if supportsGraph {
			ctx, cancel := context.WithTimeout(context.Background(), boardRetryTimeout)
			all, err := backend.ListAll(ctx)
			allLoaded := err == nil
			if err != nil {
				log.Printf("beads-tui: graph issue snapshot skipped: %v", err)
			} else {
				graphIssues = mergeIssueSnapshots(all, issues)
			}
			graphIDs := issueIDs(graphIssues)
			raw, err := backend.DepsBatch(ctx, graphIDs, false)
			cancel()
			if err != nil {
				log.Printf("beads-tui: graph dependencies skipped: %v", err)
			}
			graphComplete = allLoaded && err == nil
			deps, reverseDeps = normalizeGraphEdges(graphIssues, raw)
		} else {
			log.Printf("beads-tui: backend does not support batched graph loading")
		}
		current = enrichIssues(current, deps, reverseDeps)
		cache = &graphCache{
			view:        view,
			generation:  generation,
			currentIDs:  append([]string(nil), currentIDs...),
			issues:      cloneIssues(current),
			graphIssues: cloneIssues(graphIssues),
			deps:        cloneDepMap(deps),
			reverseDeps: cloneDepMap(reverseDeps),
			edgeCount:   countGraphEdges(deps),
			complete:    graphComplete,
		}
		return graphMsgFromCache(cache)
	}
}

func graphCacheMatches(cache *graphCache, view bd.View, generation uint64, currentIDs []string) bool {
	return cache != nil && cache.view == view && cache.generation == generation && sameIDs(cache.currentIDs, currentIDs)
}

func graphMsgFromCache(cache *graphCache) graphMsg {
	return graphMsg{
		view:        cache.view,
		generation:  cache.generation,
		issues:      cloneIssues(cache.issues),
		graphIssues: cloneIssues(cache.graphIssues),
		deps:        cloneDepMap(cache.deps),
		reverseDeps: cloneDepMap(cache.reverseDeps),
		edgeCount:   cache.edgeCount,
		complete:    cache.complete,
		cache:       cache,
	}
}

func countGraphEdges(deps map[string][]bd.DepRecord) int {
	count := 0
	for _, records := range deps {
		count += len(records)
	}
	return count
}

func issueIDs(issues []bd.Issue) []string {
	seen := make(map[string]struct{}, len(issues))
	ids := make([]string, 0, len(issues))
	for _, issue := range issues {
		if issue.ID == "" {
			continue
		}
		if _, ok := seen[issue.ID]; ok {
			continue
		}
		seen[issue.ID] = struct{}{}
		ids = append(ids, issue.ID)
	}
	return ids
}

func sameIDs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]struct{}, len(a))
	for _, id := range a {
		seen[id] = struct{}{}
	}
	for _, id := range b {
		if _, ok := seen[id]; !ok {
			return false
		}
	}
	return true
}

func cloneIssues(issues []bd.Issue) []bd.Issue {
	cloned := append([]bd.Issue(nil), issues...)
	for i := range cloned {
		cloned[i].Labels = append([]string(nil), cloned[i].Labels...)
	}
	return cloned
}

func cloneBoardSnapshot(snapshot *boardSnapshot) *boardSnapshot {
	if snapshot == nil {
		return nil
	}
	return &boardSnapshot{View: snapshot.View, Issues: cloneIssues(snapshot.Issues)}
}

func cloneExpanded(expanded map[string]bool) map[string]bool {
	cloned := make(map[string]bool, len(expanded))
	for id, value := range expanded {
		cloned[id] = value
	}
	return cloned
}

func cloneDepMap(source map[string][]bd.DepRecord) map[string][]bd.DepRecord {
	if source == nil {
		return nil
	}
	cloned := make(map[string][]bd.DepRecord, len(source))
	for id, records := range source {
		cloned[id] = append([]bd.DepRecord(nil), records...)
	}
	return cloned
}

func mergeIssueSnapshots(all, current []bd.Issue) []bd.Issue {
	merged := cloneIssues(all)
	seen := make(map[string]struct{}, len(merged))
	for _, issue := range merged {
		if issue.ID != "" {
			seen[issue.ID] = struct{}{}
		}
	}
	for _, issue := range current {
		if issue.ID == "" {
			continue
		}
		if _, ok := seen[issue.ID]; ok {
			continue
		}
		merged = append(merged, issue)
		seen[issue.ID] = struct{}{}
	}
	return merged
}

func normalizeGraphEdges(issues []bd.Issue, raw map[string][]bd.DepRecord) (map[string][]bd.DepRecord, map[string][]bd.DepRecord) {
	issueByID := make(map[string]bd.Issue, len(issues))
	for _, issue := range issues {
		if issue.ID != "" {
			issueByID[issue.ID] = issue
		}
	}
	deps := make(map[string][]bd.DepRecord)
	reverseDeps := make(map[string][]bd.DepRecord)
	for sourceID, records := range raw {
		if sourceID == "" {
			continue
		}
		source := issueByID[sourceID]
		for _, record := range records {
			if record.ID == "" || record.ID == sourceID {
				continue
			}
			target := issueByID[record.ID]
			if record.Title == "" {
				record.Title = target.Title
			}
			if record.Status == "" {
				record.Status = target.Status
			}
			if record.Priority == 0 {
				record.Priority = target.Priority
			}
			if record.IssueType == "" {
				record.IssueType = target.IssueType
			}
			deps[sourceID] = append(deps[sourceID], record)
			reverseDeps[record.ID] = append(reverseDeps[record.ID], bd.DepRecord{
				ID:             sourceID,
				Title:          source.Title,
				Status:         source.Status,
				Priority:       source.Priority,
				IssueType:      source.IssueType,
				DependencyType: record.DependencyType,
			})
		}
	}
	return deps, reverseDeps
}

func enrichIssues(issues []bd.Issue, deps, reverseDeps map[string][]bd.DepRecord) []bd.Issue {
	enriched := cloneIssues(issues)
	for i := range enriched {
		enriched[i] = normalizeIssueCounts(enriched[i], deps[enriched[i].ID], reverseDeps[enriched[i].ID])
	}
	return enriched
}

func normalizeIssueCounts(issue bd.Issue, deps, dependents []bd.DepRecord) bd.Issue {
	if len(deps) > issue.DependencyCount {
		issue.DependencyCount = len(deps)
	}
	if len(dependents) > issue.DependentCount {
		issue.DependentCount = len(dependents)
	}
	return issue
}

func (m Model) loadStatusesCmd() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), boardRetryTimeout)
		defer cancel()
		statuses, err := m.backend.Statuses(ctx)
		return statusMsg{statuses: statuses, err: err}
	}
}

func (m *Model) invalidateDetail() {
	m.detailGen++
	m.detailPendingID = ""
	m.detail = nil
	m.down = nil
	m.up = nil
	m.detailErr = ""
	m.checking = false
	m.detailRefreshing = false
	m.detailDebounceSeq++
	m.dOffset = 0
	m.searchDetail = nil
	m.searchDown = nil
	m.searchUp = nil
	m.searchDErr = ""
	m.commentsID = ""
	m.comments = nil
	m.commentsErr = ""
	m.commentsLoading = false
	m.commentsPendingID = ""
	m.commentsGeneration++
	m.commentsOffset = 0
}

// detailSeed carries what the board already knows about a bead so
// fetchDetail can skip bd round-trips for data it already has.
type detailSeed struct {
	issue *bd.Issue // board row for the id; nil means Show must run
	// deps are the graph edges for the id; haveDeps says the graph pass
	// loaded the full edge set, so the Deps round-trips can be skipped.
	haveDeps bool
	down, up []bd.DepRecord
}

// detailSeedFor collects the board row and (when the graph loaded
// completely) the dependency edges already in memory for id.
func (m Model) detailSeedFor(id string) detailSeed {
	var seed detailSeed
	for i := range m.allRows {
		if m.allRows[i].ID == id {
			row := m.allRows[i]
			seed.issue = &row
			break
		}
	}
	if m.graphCache != nil && m.graphCache.complete && m.deps != nil {
		seed.down = append([]bd.DepRecord(nil), m.deps[id]...)
		seed.up = append([]bd.DepRecord(nil), m.reverseDeps[id]...)
		seed.haveDeps = true
	}
	return seed
}

// fetchDetail builds one detail snapshot from the board seed, calling bd
// only for what the board does not already hold. The two dependency
// directions run concurrently so a slow dep list does not double the wait.
func fetchDetail(backend Backend, id string, seed detailSeed) (*bd.Issue, []bd.DepRecord, []bd.DepRecord, error) {
	issue, err := seed.issue, error(nil)
	if issue == nil {
		issue, err = backend.Show(context.Background(), id)
	}
	var down, up []bd.DepRecord
	if err == nil {
		if seed.haveDeps {
			down, up = seed.down, seed.up
		} else {
			var derr, uerr error
			var wg sync.WaitGroup
			wg.Add(2)
			go func() {
				defer wg.Done()
				down, derr = backend.Deps(context.Background(), id, false)
			}()
			go func() {
				defer wg.Done()
				up, uerr = backend.Deps(context.Background(), id, true)
			}()
			wg.Wait()
			if derr != nil {
				err = derr
			} else if uerr != nil {
				err = uerr
			}
		}
	}
	return issue, down, up, err
}

// fetchDetailCmd fetches one bead's detail plus both dependency directions,
// skipping the calls the board data already covers.
func (m Model) fetchDetailCmd(id string, generation uint64) tea.Cmd {
	backend := m.backend
	seed := m.detailSeedFor(id)
	return func() tea.Msg {
		issue, down, up, err := fetchDetail(backend, id, seed)
		return detailMsg{id: id, generation: generation, issue: issue, down: down, up: up, err: err}
	}
}

// neighbourIDs returns the ids of rows within span positions of the row for
// id, nearest first.
func (m Model) neighbourIDs(id string, span int) []string {
	idx := -1
	for i := range m.rows {
		if m.rows[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil
	}
	var ids []string
	for d := 1; d <= span; d++ {
		for _, i := range []int{idx - d, idx + d} {
			if i >= 0 && i < len(m.rows) && m.rows[i].ID != "" {
				ids = append(ids, m.rows[i].ID)
			}
		}
	}
	return ids
}

// prefetchNeighboursCmd fetches detail snapshots for the rows around the
// selection (selected±1..span) that are not cached yet. Best effort: failures
// simply leave the cache empty and the selection is never blocked on it.
func (m Model) prefetchNeighboursCmd(id string) tea.Cmd {
	seen := map[string]struct{}{}
	type target struct {
		id  string
		key string
	}
	var targets []target
	for _, nid := range m.neighbourIDs(id, prefetchSpan) {
		if _, dup := seen[nid]; dup {
			continue
		}
		seen[nid] = struct{}{}
		key := m.detailCacheKeyFor(nid)
		if _, ok := m.detailCache[key]; ok {
			continue
		}
		targets = append(targets, target{id: nid, key: key})
	}
	if len(targets) == 0 {
		return nil
	}
	backend := m.backend
	seeds := make(map[string]detailSeed, len(targets))
	for _, tgt := range targets {
		seeds[tgt.id] = m.detailSeedFor(tgt.id)
	}
	return func() tea.Msg {
		var hits []prefetchHit
		for _, tgt := range targets {
			issue, down, up, err := fetchDetail(backend, tgt.id, seeds[tgt.id])
			if err != nil || issue == nil {
				continue
			}
			hits = append(hits, prefetchHit{key: tgt.key, detail: cachedDetail{
				issue: *issue,
				down:  down,
				up:    up,
			}})
		}
		if len(hits) == 0 {
			return nil
		}
		return prefetchMsg{hits: hits}
	}
}

// pruneDetailCache drops entries for beads that are no longer on the board.
func (m *Model) pruneDetailCache() {
	if m.detailCache == nil {
		return
	}
	live := make(map[string]struct{}, len(m.allRows))
	for _, issue := range m.allRows {
		if issue.ID != "" {
			live[issue.ID] = struct{}{}
		}
	}
	for key := range m.detailCache {
		id, _, _ := strings.Cut(key, "\x00")
		if _, ok := live[id]; !ok {
			delete(m.detailCache, key)
		}
	}
}

// applyBoard installs a fresh board snapshot. A failed or timed-out reload
// never discards the board already on screen: it records a notice and
// schedules a backoff retry while the previous board stays interactive.
func (m *Model) applyBoard(msg boardMsg) tea.Cmd {
	if msg.generation != m.boardGen || msg.view != m.view {
		// A newer board superseded this one.
		return nil
	}
	m.loading = false
	if msg.err != nil {
		m.reloadAttempts++
		// The screen shows this, but the screen dies with the window; the log
		// is what survives to explain a board that never loaded.
		log.Printf("beads-tui: board load failed (view=%s attempt=%d): %v", msg.view, m.reloadAttempts, msg.err)
		m.boardErr = msg.err.Error()
		if len(m.allRows) > 0 {
			m.reloadNotice = reloadFailureNotice(msg.err, msg.timeout, m.boardLoadedAt)
			return m.scheduleBoardRetry()
		}
		m.reloadNotice = ""
		return m.scheduleBoardRetry()
	}
	m.boardErr = ""
	m.reloadAttempts = 0
	m.reloadNotice = ""
	m.boardLoadedAt = time.Now()
	m.invalidateDetail()
	// Capture the previous selection from the OLD board before rows are
	// replaced, then restore it by id if the bead is still present.
	prev := m.selectedID()
	m.graphReady = false
	m.graphEdges = 0
	m.graphCache = nil
	m.deps = nil
	m.reverseDeps = nil
	m.graphRows = nil
	m.allRows = append([]bd.Issue(nil), msg.issues...)
	m.deps = cloneDepMap(msg.deps)
	m.lastSnapshot = &boardSnapshot{View: msg.view, Issues: cloneIssues(msg.issues)}
	m.saveState()
	m.pruneDetailCache()
	return tea.Batch(m.rebuildRows(prev), m.loadGraphCmd(msg.view, msg.generation, msg.issues))
}

// reloadFailureNotice builds the transient status-line text shown when a
// reload fails but a previous board is still displayed.
func reloadFailureNotice(err error, timeout time.Duration, loadedAt time.Time) string {
	shown := "the previous board"
	if !loadedAt.IsZero() {
		shown = loadedAt.Format("15:04:05")
	}
	if isTimeoutErr(err) {
		return fmt.Sprintf("reload timed out after %ds - still showing board from %s; retrying",
			int(timeout.Seconds()), shown)
	}
	return fmt.Sprintf("reload failed - still showing board from %s; retrying", shown)
}

// scheduleBoardRetry arms the automatic reload timer with the next backoff.
func (m *Model) scheduleBoardRetry() tea.Cmd {
	delay := boardRetryBackoff(m.reloadAttempts)
	return tea.Tick(delay, func(time.Time) tea.Msg { return boardRetryMsg{} })
}

func (m Model) indexOfRow(id string) int {
	if id == "" {
		return 0
	}
	for i := range m.rows {
		if m.rows[i].ID == id {
			return i
		}
	}
	return 0
}

func (m Model) selectedTreeRow() (TreeRow, bool) {
	if m.treeMode && m.selected >= 0 && m.selected < len(m.treeRows) {
		return m.treeRows[m.selected], true
	}
	return TreeRow{}, false
}

// foldRow collapses one tree node, keeping the selection on it.
func (m *Model) foldRow(id string) tea.Cmd {
	m.expanded[id] = false
	return m.rebuildRows(id)
}

// unfoldRow expands one tree node, keeping the selection on it.
func (m *Model) unfoldRow(id string) tea.Cmd {
	m.expanded[id] = true
	return m.rebuildRows(id)
}

// expandAll clears every fold in the loaded graph so the whole hierarchy is
// visible. Hidden descendants are included, not just currently painted rows.
func (m *Model) expandAll() tea.Cmd {
	if !m.treeMode {
		return nil
	}
	var walk func(*TreeNode)
	walk = func(node *TreeNode) {
		if node == nil {
			return
		}
		m.expanded[node.Issue.ID] = true
		for _, child := range node.Children {
			walk(child)
		}
	}
	for _, root := range BuildDependencyTree(m.allRows, m.deps, m.sortMode) {
		walk(root)
	}
	return m.rebuildRows(m.selectedID())
}

func (m *Model) toggleSelectedTreeRow() tea.Cmd {
	if m.selected >= len(m.treeRows) {
		return nil
	}
	id := m.treeRows[m.selected].Issue.ID
	m.expanded[id] = !m.treeRows[m.selected].Expanded
	cmd := m.rebuildRows(id)
	m.selected = m.indexOfRow(id)
	return cmd
}

// applyDetail installs a detail snapshot, discarding stale responses, and
// caches it so returning to this bead renders instantly.
func (m *Model) applyDetail(msg detailMsg) tea.Cmd {
	if msg.generation != m.detailGen || m.selected >= len(m.rows) || msg.id != m.rows[m.selected].ID {
		return nil
	}
	m.detailPendingID = ""
	m.checking = false
	m.detailRefreshing = false
	if msg.err != nil {
		log.Printf("beads-tui: detail load failed (id=%s): %v", msg.id, msg.err)
		m.detailErr = msg.err.Error()
		if msg.issue != nil {
			issue := normalizeIssueCounts(*msg.issue, msg.down, msg.up)
			m.detail = &issue
			m.down, m.up = msg.down, msg.up
		} else if !(m.detail != nil && m.detail.ID == msg.id) {
			// A failed background refresh must not wipe the snapshot that is
			// already on screen for this bead; only clear when there is
			// nothing (or something stale) to show.
			m.detail, m.down, m.up = nil, nil, nil
		}
		return nil
	}
	m.detailErr = ""
	if msg.issue != nil {
		issue := normalizeIssueCounts(*msg.issue, msg.down, msg.up)
		m.detail = &issue
		m.detailCache[detailCacheKey(msg.id, msg.issue.UpdatedAt)] = cachedDetail{
			issue: cloneIssue(*msg.issue),
			down:  append([]bd.DepRecord(nil), msg.down...),
			up:    append([]bd.DepRecord(nil), msg.up...),
		}
	} else {
		m.detail = nil
	}
	m.down, m.up = msg.down, msg.up
	m.dOffset = 0
	return nil
}

// cloneIssue deep-copies the variable-length fields of one bead.
func cloneIssue(issue bd.Issue) bd.Issue {
	issue.Labels = append([]string(nil), issue.Labels...)
	return issue
}

// View renders the full frame.
func (m Model) View() string {
	if m.quitting {
		return ""
	}
	if m.yank {
		return m.renderYank()
	}
	if m.help {
		return m.renderHelp()
	}
	if m.commentsOpen {
		return m.renderComments()
	}
	if m.graph {
		return m.renderGraph()
	}
	w, h := m.width, m.height
	if w <= 0 {
		w = 80
	}
	if h <= 0 {
		h = 24
	}
	contentH := h - 2
	if m.filtering {
		contentH--
	}
	if contentH < 1 {
		contentH = 1
	}
	listPane, detailPane, paneH := m.splitPanes(w, contentH)
	var sb strings.Builder
	sb.WriteString(m.renderHeader(w))
	sb.WriteString("\n")
	if m.layoutSideBySide() {
		for i := 0; i < paneH; i++ {
			sb.WriteString(listPane[i])
			sb.WriteString(" ")
			sb.WriteString(detailPane[i])
			sb.WriteString("\n")
		}
	} else {
		for i := 0; i < len(listPane); i++ {
			sb.WriteString(listPane[i])
			sb.WriteString("\n")
		}
		for i := 0; i < len(detailPane); i++ {
			sb.WriteString(detailPane[i])
			sb.WriteString("\n")
		}
	}
	if m.filtering {
		sb.WriteString(m.renderFilterPrompt(w))
		sb.WriteString("\n")
	}
	sb.WriteString(m.renderFooter(w))
	return sb.String()
}

func (m Model) renderYank() string {
	items := m.yankItems()
	lines := []string{"Choose a value to copy:"}
	for i, item := range items {
		prefix := "  "
		if i == m.yankIndex {
			prefix = "▸ "
		}
		lines = append(lines, prefix+item.label+": "+item.value)
	}
	lines = append(lines, "", "j/k move · enter copy · esc close")
	if m.yankErr != "" {
		lines = append(lines, styleDim.Render(m.yankErr))
	}
	return strings.Join(pane("Yank", lines, m.width, m.height), "\n")
}

func (m Model) renderFilterPrompt(w int) string {
	input := m.filterInput.View()
	return truncatePhys(styleDim.Render("SEARCH")+"  "+input, w)
}

// renderHeader paints the title bar: view name and tabs.
func (m Model) renderHeader(w int) string {
	left := styleBold.Render("beads-tui") + "  " + styleDim.Render("·") + "  " + m.vocabViewLabel()
	return fitLine(left, m.renderTabs(), w)
}

func (m Model) hasGraphEdges() bool {
	if !m.graphReady {
		return false
	}
	if m.selected < 0 || m.selected >= len(m.rows) {
		return false
	}
	id := m.rows[m.selected].ID
	if id == "" {
		return false
	}
	if m.rows[m.selected].ParentID != "" || len(m.deps[id]) > 0 || len(m.reverseDeps[id]) > 0 {
		return true
	}
	all := m.graphRows
	if len(all) == 0 {
		all = m.allRows
	}
	for _, issue := range all {
		if issue.ParentID == id {
			return true
		}
	}
	for _, records := range m.deps {
		for _, dep := range records {
			if dep.ID == id {
				return true
			}
		}
	}
	return false
}

func (m Model) renderGraph() string {
	w, h := m.width, m.height
	if w <= 0 {
		w = 80
	}
	if h <= 0 {
		h = 24
	}
	all := m.graphRows
	if len(all) == 0 {
		all = m.allRows
	}
	lines, cycle := graphLines(m.rows, all, m.deps, m.selectedID(), m.vocab, m.reverseDeps)
	title := "Graph · G/esc close"
	if cycle {
		title = styleError.Render("⚠ CYCLE") + " · " + title
	}
	return strings.Join(pane(title, lines, w, h), "\n")
}

func (m Model) vocabViewLabel() string {
	return styleDim.Render(m.view.Label() + " board")
}

func (m Model) renderTabs() string {
	var parts []string
	for i, view := range m.views {
		label := fmt.Sprintf("[%d]%s", i+1, view.TabLabel())
		if view == m.view {
			parts = append(parts, viewStyle(view).Bold(true).Render(label))
		} else {
			parts = append(parts, viewStyle(view).Render(label))
		}
	}
	return strings.Join(parts, " ")
}

// renderListPane returns exactly h lines for the board pane.
func (m Model) renderListPane(w, h int) []string {
	inner := w - 2
	if inner < 1 {
		inner = 1
	}
	var lines []string
	switch {
	case m.boardErr != "" && len(m.rows) == 0 && len(m.allRows) == 0:
		msg := "✗ Could not load board."
		for _, l := range wrapText(msg, inner) {
			lines = append(lines, styleError.Render(l))
		}
		for _, l := range wrapText(m.boardErr, inner) {
			lines = append(lines, styleDim.Render(l))
		}
		lines = append(lines, styleDim.Render("Check bd is installed and a beads workspace is active (BEADS_DIR)."))
		lines = append(lines, styleDim.Render("Retrying automatically; r retries now."))
	case m.loading && len(m.rows) == 0 && len(m.allRows) == 0:
		lines = append(lines, styleDim.Render("Loading board…"))
	case len(m.rows) == 0:
		if m.filter.Active() {
			lines = append(lines, styleDim.Render("No matches for search: "+m.filter.String()))
		} else {
			lines = append(lines, styleDim.Render(m.emptyBoardText()))
		}
	default:
		vis := h - 2
		if m.loading {
			vis--
		}
		if vis > len(m.rows) {
			vis = len(m.rows)
		}
		if vis < 0 {
			vis = 0
		}
		top := m.scrollTop(vis)
		for i := top; i < top+vis; i++ {
			if m.treeMode && i < len(m.treeRows) {
				lines = append(lines, m.vocab.TreeRow(m.treeRows[i], inner, i == m.selected))
			} else {
				lines = append(lines, m.vocab.ListRow(m.rows[i], inner, i == m.selected))
			}
		}
		if m.loading {
			lines = append(lines, styleDim.Render("Refreshing…"))
		}
		if m.reloadNotice != "" {
			lines = append(lines, styleDim.Render(truncate(m.reloadNotice, inner)))
		}
	}
	title := styleDim.Render(m.view.Label())
	if m.focus == FocusList {
		title = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("cyan")).Render(m.view.Label())
	}
	return pane(title, lines, w, h)
}

func (m Model) emptyBoardText() string {
	switch m.view {
	default:
		return "No " + m.view.Label() + " issues."
	}
}

// scrollTop centers the selection in the visible window.
func (m Model) scrollTop(vis int) int {
	n := len(m.rows)
	if vis >= n {
		return 0
	}
	t := m.selected - (vis-1)/2
	maxTop := n - vis
	if t < 0 {
		t = 0
	}
	if t > maxTop {
		t = maxTop
	}
	return t
}

// renderDetailPane returns exactly h lines for the detail pane.
func (m Model) renderDetailPane(w, h int) []string {
	inner := w - 2
	if inner < 1 {
		inner = 1
	}
	var lines []string
	switch {
	case m.checking && m.detail == nil:
		lines = append(lines, styleDim.Render("Loading detail…"))
	case m.detailErr != "" && m.detail == nil:
		lines = append(lines, styleError.Render("✗ Could not load detail."))
		for _, l := range wrapText(m.detailErr, inner) {
			lines = append(lines, styleDim.Render(l))
		}
	case m.detail != nil:
		all := m.buildDetail(inner)
		offset := m.dOffset
		contentVis, maxOffset := m.detailContentBudget(len(all))
		if offset > maxOffset {
			offset = maxOffset
		}
		shown := 0
		for _, l := range all[offset:] {
			if shown >= contentVis {
				break
			}
			lines = append(lines, l)
			shown++
		}
		if m.detailErr != "" {
			lines = append(lines, "")
			lines = append(lines, styleDim.Render("ⓘ "+truncate(m.detailErr, inner)))
		}
		remaining := len(all) - offset - shown
		if remaining > 0 {
			lines = append(lines, styleDim.Render(truncate(
				fmt.Sprintf("↓ %d more lines", remaining), inner)))
		}
	default:
		lines = append(lines, styleDim.Render("Select a bead for details."))
	}
	title := "Detail"
	if m.detailRefreshing {
		title = "Detail · refreshing…"
	}
	if m.focus == FocusDetail {
		title = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("cyan")).Render(title)
	} else {
		title = styleDim.Render(title)
	}
	return pane(title, lines, w, h)
}

// renderFooter paints the hint/status bar.
func (m Model) renderFooter(w int) string {
	if w < 48 {
		if m.commentsOpen && m.commentsErr != "" {
			return truncatePhys(styleError.Render("✗ "+m.commentsErr), w)
		}
		query := "off"
		if m.filter.Active() {
			query = "on"
		}
		percent := 0
		if len(m.rows) == 1 {
			percent = 100
		} else if len(m.rows) > 1 {
			percent = m.selected * 100 / (len(m.rows) - 1)
		}
		return truncatePhys(styleDim.Render(fmt.Sprintf("%s  query:%s  %d%%", m.view.Label(), query, percent)), w)
	}
	selected := m.selectedID()
	if selected == "" {
		selected = "-"
	}
	scroll := 0
	if len(m.rows) > 1 {
		scroll = m.selected * 100 / (len(m.rows) - 1)
	}
	status := footerStatus(w, m.view.Label(), m.sortMode.String(), m.filter.String(), selected, len(m.rows), scroll, m.graphEdges)
	defaultHints := styleDim.Render("r reload R reset s sort / search t tag c comments C add comment ? help q quit")
	shortHints := styleDim.Render("c comments C add ? help q quit")
	hints := defaultHints
	var left string
	switch {
	case m.commentsOpen && m.commentsErr != "":
		left = styleError.Render("✗ " + truncate(m.commentsErr, max(1, w/2)))
		hints = styleDim.Render("r retry · a add · esc cancel/back · q back · ? help")
	case m.boardErr != "" && len(m.rows) == 0:
		left = styleError.Render("✗ board error")
		hints = styleDim.Render("r retry · q quit")
	case m.reloadNotice != "":
		left = styleDim.Render(m.reloadNotice)
		hints = styleDim.Render("r reload now · q quit")
	case m.loading:
		left = status + " · " + styleDim.Render("loading…")
	case m.commentsOpen:
		left = status
		hints = styleDim.Render("j·k scroll · a add · r reload · esc/q back · ? help")
	case m.focus == FocusDetail:
		left = status
		hints = styleDim.Render("j·k/↑↓ scroll · ctrl-u/d half-page · space/page pg · c comments · esc back · q quit")
	default:
		left = status
	}
	if displayWidth(left)+1+displayWidth(hints) <= w {
		return fitLine(left, hints, w)
	}
	if hints == defaultHints && displayWidth(left)+1+displayWidth(shortHints) <= w {
		return fitLine(left, shortHints, w)
	}
	return truncatePhys(left, w)
}

func footerStatus(w int, view, sortMode, filter, selected string, total, scroll, graphEdges int) string {
	if filter == "" {
		filter = "-"
	}
	if w < 64 {
		view = truncate(view, 3)
		sortMode = truncate(sortMode, 3)
	}
	fixed := fmt.Sprintf("view:%s sort:%s query: sel: total:%d graph:%d scroll:%d%%", view, sortMode, total, graphEdges, scroll)
	available := max(2, w-runewidth.StringWidth(fixed))
	filterWidth := max(1, available*2/3)
	selectedWidth := max(1, available-filterWidth)
	return strings.Join([]string{
		lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Render("view:" + view),
		lipgloss.NewStyle().Foreground(lipgloss.Color("141")).Render("sort:" + sortMode),
		lipgloss.NewStyle().Foreground(lipgloss.Color("208")).Render("query:" + truncate(filter, filterWidth)),
		lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Render("sel:" + truncate(selected, selectedWidth)),
		styleDim.Render(fmt.Sprintf("total:%d", total)),
		styleDim.Render(fmt.Sprintf("graph:%d edges", graphEdges)),
		lipgloss.NewStyle().Foreground(lipgloss.Color("81")).Render(fmt.Sprintf("scroll:%d%%", scroll)),
	}, " ")
}

// renderHelp overlays the key reference.
func (m Model) renderHelp() string {
	w, width, height := m.helpDimensions()
	lines := m.helpLines(width)
	vis := max(1, height-2)
	offset := min(m.helpOffset, max(0, len(lines)-vis))
	end := min(len(lines), offset+vis)
	title := fmt.Sprintf("Help · j/k scroll · %d/%d", offset+1, max(1, len(lines)-vis+1))
	box := pane(title, lines[offset:end], width, height)
	out := make([]string, 0, height)
	for _, line := range box {
		out = append(out, strings.Repeat(" ", max(0, (w-displayWidth(line))/2))+line)
	}
	return strings.Join(out, "\n")
}

func (m Model) helpDimensions() (w, width, height int) {
	w, height = m.width, m.height
	if w <= 0 {
		w = 80
	}
	if height <= 0 {
		height = 24
	}
	width = w - 4
	if width > 72 {
		width = 72
	}
	return w, max(3, width), max(3, height)
}

func (m Model) helpLines(width int) []string {
	rowLegend := fmt.Sprintf("Rows: %s open  %s in_progress  %s blocked  %s closed  %s deferred  %s hold",
		m.vocab.Icon("open"), m.vocab.Icon("in_progress"), m.vocab.Icon("blocked"),
		m.vocab.Icon("closed"), m.vocab.Icon("deferred"), m.vocab.Icon("hold"))
	viewHelp := "  Views:"
	for i, view := range m.views {
		if i == 9 {
			break
		}
		viewHelp += fmt.Sprintf(" %d %s ·", i+1, view.Label())
	}
	viewHelp = strings.TrimSuffix(viewHelp, " ·")
	raw := []string{
		"beads-tui - read-only board for Beads (bd)",
		"",
		"  Move/scroll:   j/k or ↑/↓ · g/G top/bottom · G graph when edges · space/PgDn/Ctrl+F forward · b/PgUp/Ctrl+B back",
		"  Half-page:     ctrl-u/d in list and detail",
		"  Tree:          enter/tab toggle · * expand all · h collapse · l unfold or detail · v flat/tree (preserves selection) · siblings use active sort",
		"  Layout:        V cycles side-by-side / stacked / auto (auto stacks below 140 columns)",
		"  Detail:        enter/l/→ open · h/← return · j/k or ↑/↓ scroll",
		"  Comments:      c view comments · a add from the thread · C add from the board · Esc/q back",
		"  Navigation:    esc close detail / clear search",
		viewHelp,
		"  Sort:          s cycle created · updated · alphabetical · dependencies (blocked-by/in) · depends (blocks/out) · priority",
		"  Search:        / prompt · Enter apply · status:open · priority:P1 · label:frontend · text · Esc cancel",
		"  Tags:          t search by the selected bead's labels",
		"  Reset:         R reloads the open board with defaults · r reloads keeping view/sort/search · Help: ? (any key closes) · Quit: q/Ctrl+C",
		"",
		rowLegend,
		"PRIORITY_COLORS",
		"STATUS_COLORS",
		"Markers: ⇣N depends on N · ⇡N has N dependents",
		"",
		"Read-only: board data never creates, edits or closes beads; comments are the one write action.",
	}
	inner := max(1, width-2)
	var lines []string
	for _, line := range raw {
		lines = append(lines, wrapText(line, inner)...)
	}
	for i, line := range lines {
		switch line {
		case "PRIORITY_COLORS":
			lines[i] = "Priority: " + strings.Join([]string{
				priorityStyle(0).Render("P0"), priorityStyle(1).Render("P1"),
				priorityStyle(2).Render("P2"), priorityStyle(3).Render("P3"),
				priorityStyle(4).Render("P4"),
			}, " ")
		case "STATUS_COLORS":
			lines[i] = "Status: " + strings.Join([]string{
				m.vocab.StatusPill("open"), m.vocab.StatusPill("in_progress"),
				m.vocab.StatusPill("blocked"), m.vocab.StatusPill("closed"),
				m.vocab.StatusPill("deferred"), m.vocab.StatusPill("hold"),
			}, " ")
		}
	}
	lines[0] = styleBold.Render(lines[0])
	for i := len(lines) - 1; i >= 0 && i >= len(lines)-4; i-- {
		if lines[i] != "" {
			lines[i] = styleDim.Render(lines[i])
		}
	}
	return lines
}

func (m Model) helpMaxOffset() int {
	_, width, height := m.helpDimensions()
	return max(0, len(m.helpLines(width))-max(1, height-2))
}

// splitPanes renders the list and detail panes for the current layout. In
// side-by-side mode the panes share every row; in stacked mode the list sits
// on top (~60% height) with the detail pane below.
func (m Model) splitPanes(w, contentH int) (listPane, detailPane []string, lines int) {
	if m.layoutSideBySide() {
		listW := listPaneWidth(w)
		detailW := w - 1 - listW
		if detailW < 12 {
			detailW = 12
			listW = w - 1 - detailW
		}
		return m.renderListPane(listW, contentH), m.renderDetailPane(detailW, contentH), contentH
	}
	listH := stackedListHeight(contentH)
	detailH := contentH - listH
	return m.renderListPane(w, listH), m.renderDetailPane(w, detailH), listH + detailH
}

// stackedListHeight splits stacked content height: ~60% list, rest detail,
// with at least one line left for the detail pane.
func stackedListHeight(contentH int) int {
	h := contentH * 3 / 5
	if h < 1 {
		h = 1
	}
	if h > contentH-1 {
		h = contentH - 1
	}
	return h
}

// layoutSideBySide reports whether the board uses the side-by-side
// list|detail split. LayoutAuto follows gh-dash: wide terminals split
// side-by-side with the list taking the majority, narrower terminals stack
// the detail pane below the full-width list.
func (m Model) layoutSideBySide() bool {
	switch m.layout {
	case LayoutSide:
		return true
	case LayoutStacked:
		return false
	default:
		return m.width >= wideLayoutColumns
	}
}

// listPaneWidth splits the width between board and detail for the
// side-by-side layout; the list gets the majority (~60%) so titles and
// descriptions stay readable on wide terminals.
func listPaneWidth(total int) int {
	switch {
	case total <= 0:
		return 40
	case total < 80:
		w := total * 3 / 5
		if w > 12 {
			return w
		}
		return 12
	default:
		w := total * 3 / 5
		if w < 12 {
			return 12
		}
		return w
	}
}

func (m Model) detailWidth() int {
	w := m.width
	if w <= 0 {
		w = 80
	}
	if !m.layoutSideBySide() {
		return w - 2
	}
	return w - 3 - listPaneWidth(w)
}

func (m Model) detailVisLines() int {
	h := m.height
	if h <= 0 {
		return 10
	}
	if m.filtering {
		h--
	}
	if !m.layoutSideBySide() {
		contentH := h - 2
		return contentH - stackedListHeight(contentH) - 2
	}
	return h - 4
}

func (m Model) detailContentBudget(lines int) (contentVis, maxOffset int) {
	vis := m.detailVisLines()
	contentVis = vis
	if m.detailErr != "" {
		contentVis -= 2
	}
	if lines > contentVis {
		contentVis--
	}
	if contentVis < 0 {
		contentVis = 0
	}
	maxOffset = lines - contentVis
	if maxOffset < 0 {
		maxOffset = 0
	}
	return
}

// inlineCommentsMaxLines keeps a long comment thread from crowding every
// other detail section out of the first viewport. The complete thread remains
// reachable through the detail scroll and the c view.
func (m Model) inlineCommentsMaxLines() int {
	maxLines := m.detailVisLines() * 2 / 5
	if maxLines < 4 {
		maxLines = 4
	}
	return maxLines
}

// pane frames content lines (which may carry ANSI) into a bordered pane of
// exactly h lines and w cells. Content gets w-2 cells; the border is drawn
// inside the same width budget.
func pane(title string, content []string, w, h int) []string {
	inner := w - 2
	if inner < 1 {
		inner = 1
	}
	topTitle := truncatePhys(title, inner)
	rest := inner - displayWidth(topTitle)
	if rest < 0 {
		rest = 0
	}
	out := make([]string, 0, h)
	out = append(out, "┌"+topTitle+strings.Repeat("─", rest)+"┐")
	vis := h - 2
	for i := 0; i < vis; i++ {
		var line string
		if i < len(content) {
			line = content[i]
		}
		out = append(out, "│"+padRight(truncatePhys(line, inner), inner)+"│")
	}
	out = append(out, "└"+strings.Repeat("─", inner)+"┘")
	return out
}

// padRight pads an ANSI string to exactly cells wide (ANSI-aware).
func padRight(s string, cells int) string {
	return s + strings.Repeat(" ", cells-displayWidth(s))
}

// truncatePhys truncates an ANSI string without discarding embedded styles.
func truncatePhys(s string, cells int) string {
	if cells <= 0 {
		return ""
	}
	plain := stripANSI(s)
	if runewidth.StringWidth(plain) <= cells {
		return s
	}
	limit := cells - 1
	var b strings.Builder
	width := 0
	for i := 0; i < len(s); {
		if s[i] == '\x1b' {
			end := strings.IndexByte(s[i:], 'm')
			if end < 0 {
				break
			}
			end += i + 1
			b.WriteString(s[i:end])
			i = end
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		rw := runewidth.RuneWidth(r)
		if width+rw > limit {
			break
		}
		b.WriteRune(r)
		width += rw
		i += size
	}
	b.WriteString("…\x1b[0m")
	return b.String()
}

// fitLine lays a left/right pair out on one line, preserving the right-side
// shortcuts and truncating the status text when the line cannot fit.
func fitLine(left, right string, w int) string {
	lw, rw := displayWidth(left), displayWidth(right)
	if lw+rw <= w {
		return left + strings.Repeat(" ", w-lw-rw) + right
	}
	if rw >= w {
		return truncatePhys(right, w)
	}
	return truncatePhys(left, w-rw-1) + " " + right
}

// displayWidth reports the cell width of ANSI-styled text.
func displayWidth(s string) int {
	return runewidth.StringWidth(stripANSI(s))
}
