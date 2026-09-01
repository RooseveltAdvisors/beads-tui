package tui

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/RooseveltAdvisors/beads-tui/internal/bd"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

// bdTimeout caps every bd call so a wedged store (or lock contention) shows
// an error instead of freezing the UI.
const bdTimeout = 8 * time.Second

// Focus tracks which pane receives the navigation keys.
type Focus int

const (
	// FocusList navigates the board rows.
	FocusList Focus = iota
	// FocusDetail scrolls the detail pane.
	FocusDetail
)

// Backend is the read-only data source the board renders. *bd.Client is the
// production implementation; tests supply fakes.
type Backend interface {
	List(ctx context.Context, view bd.View) ([]bd.Issue, error)
	Show(ctx context.Context, id string) (*bd.Issue, error)
	Deps(ctx context.Context, id string, up bool) ([]bd.DepRecord, error)
	Statuses(ctx context.Context) ([]bd.StatusInfo, error)
}

type graphBackend interface {
	Backend
	ListAll(ctx context.Context) ([]bd.Issue, error)
	DepsBatch(ctx context.Context, ids []string, up bool) (map[string][]bd.DepRecord, error)
}

// Model is the bead board application state.
type Model struct {
	backend Backend

	view        bd.View
	views       []bd.View
	sortMode    SortMode
	filter      Filter
	allRows     []bd.Issue
	deps        map[string][]bd.DepRecord
	reverseDeps map[string][]bd.DepRecord
	graphRows   []bd.Issue
	rows        []bd.Issue
	treeRows    []TreeRow
	expanded    map[string]bool
	treeMode    bool
	vocab       Vocab
	boardErr    string
	loading     bool
	boardGen    uint64
	graphReady  bool
	graphEdges  int
	graphCache  *graphCache

	selected int
	focus    Focus
	dOffset  int

	detail    *bd.Issue
	down      []bd.DepRecord
	up        []bd.DepRecord
	detailErr string
	checking  bool
	detailGen uint64
	graph     bool

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

type yankItem struct {
	label string
	value string
}

type yankMsg struct {
	err error
}

type savedState struct {
	View     bd.View  `json:"view"`
	SortMode SortMode `json:"sort_mode"`
	Filter   Filter   `json:"filter"`
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
	state.View = bd.View(strings.ToLower(strings.TrimSpace(string(state.View))))
	if !state.View.Valid() || state.View == bd.View("ready") || state.View == bd.View("all") {
		state.View = bd.ViewOpen
	}
	if state.SortMode > SortDependents {
		state.SortMode = SortCreated
	}
	if state.Filter.Kind > FilterSearch || len(state.Filter.Query) > 120 {
		state.Filter = Filter{}
	}
	return state, true
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
	data, err := json.MarshalIndent(savedState{View: m.view, SortMode: m.sortMode, Filter: m.filter}, "", "  ")
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
	m := Model{
		backend:  backend,
		view:     bd.ViewOpen,
		views:    bd.DefaultViews(),
		loading:  true,
		boardGen: 1,
		// Created is newest-first by default; s cycles through the remaining modes.
		sortMode:    SortCreated,
		focus:       FocusList,
		vocab:       NewVocab(nil),
		filterInput: input,
		width:       80,
		height:      24,
		markdown:    &markdownRenderer{},
		expanded:    map[string]bool{},
		treeMode:    true,
	}
	if persistenceEnabled(backend) {
		if state, ok := loadState(); ok {
			m.view, m.sortMode, m.filter = state.View, state.SortMode, state.Filter
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
		return m.updateKey(msg)
	case boardMsg:
		return m, m.applyBoard(msg)
	case graphMsg:
		if msg.generation != m.boardGen || msg.view != m.view {
			return m, nil
		}
		prev := m.selectedID()
		m.allRows = append([]bd.Issue(nil), msg.issues...)
		m.graphRows = append([]bd.Issue(nil), msg.graphIssues...)
		m.deps = cloneDepMap(msg.deps)
		m.reverseDeps = cloneDepMap(msg.reverseDeps)
		m.graphEdges = msg.edgeCount
		m.graphCache = msg.cache
		m.graphReady = true
		if m.detail != nil {
			if msg.complete {
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
			m.detailGen++
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
		boardCmd := m.startBoardLoad()
		if len(m.rows) > 0 {
			m.checking = true
			return m, tea.Batch(boardCmd, m.loadDetailCmd(m.rows[m.selected].ID))
		}
		return m, boardCmd
	case "R":
		wasOpen := m.view == bd.ViewOpen
		m.view, m.sortMode, m.filter = bd.ViewOpen, SortCreated, Filter{}
		m.saveState()
		if !wasOpen {
			return m, m.startBoardLoad()
		}
		return m, m.rebuildRows(m.selectedID())
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
	m.searchID = m.selectedID()
	m.searchDOff = m.dOffset
	m.searchDetail = m.detail
	m.searchDown = m.down
	m.searchUp = m.up
	m.searchDErr = m.detailErr
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
// by bead id. It also requests detail if the selected row changed.
func (m *Model) rebuildRows(previousID string) tea.Cmd {
	projected := SortIssues(FilterIssues(m.allRows, m.filter), m.sortMode)
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
	if len(m.rows) == 0 {
		m.detail, m.down, m.up = nil, nil, nil
		m.detailErr = ""
		m.checking = false
		return nil
	}
	if m.detail == nil || m.detail.ID != m.rows[m.selected].ID {
		m.checking = true
		return m.loadDetailCmd(m.rows[m.selected].ID)
	}
	return nil
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
		if m.treeMode && m.selected < len(m.treeRows) && m.treeRows[m.selected].HasChildren {
			return m, m.toggleSelectedTreeRow()
		}
		if msg.String() == "tab" {
			return m, nil
		}
		m.focus = FocusDetail
		m.dOffset = 0
	case "h", "left":
		if m.treeMode && m.selected < len(m.treeRows) && m.treeRows[m.selected].HasChildren {
			m.expanded[m.treeRows[m.selected].Issue.ID] = false
			return m, m.rebuildRows(m.treeRows[m.selected].Issue.ID)
		}
		return m, nil
	case "l", "L", "right":
		m.focus = FocusDetail
		m.dOffset = 0
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
		m.checking = true
		return m, m.loadDetailCmd(m.rows[m.selected].ID)
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
	return buildDetail(m.vocab, m.detail, m.down, m.up, width, m.markdown)
}

func (m *Model) startBoardLoad() tea.Cmd {
	m.boardGen++
	m.loading = true
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
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), bdTimeout)
		issues, err := backend.List(ctx, view)
		cancel()
		if err != nil {
			return boardMsg{view: view, generation: generation, err: err}
		}
		return boardMsg{view: view, generation: generation, issues: issues}
	}
}

func (m *Model) invalidateDetail() {
	m.detailGen++
	m.detail = nil
	m.down = nil
	m.up = nil
	m.detailErr = ""
	m.checking = false
	m.dOffset = 0
	m.searchDetail = nil
	m.searchDown = nil
	m.searchUp = nil
	m.searchDErr = ""
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
			ctx, cancel := context.WithTimeout(context.Background(), bdTimeout)
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
		ctx, cancel := context.WithTimeout(context.Background(), bdTimeout)
		defer cancel()
		statuses, err := m.backend.Statuses(ctx)
		return statusMsg{statuses: statuses, err: err}
	}
}

// loadDetailCmd fetches issue detail plus both dependency directions. The
// first failure short-circuits the rest so a dead store yields one error.
func (m *Model) loadDetailCmd(id string) tea.Cmd {
	m.detailGen++
	generation := m.detailGen
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), bdTimeout)
		defer cancel()
		issue, err := m.backend.Show(ctx, id)
		var down, up []bd.DepRecord
		if err == nil {
			down, err = m.backend.Deps(ctx, id, false)
		}
		if err == nil {
			up, err = m.backend.Deps(ctx, id, true)
		}
		return detailMsg{id: id, generation: generation, issue: issue, down: down, up: up, err: err}
	}
}

// applyBoard installs a fresh board snapshot.
func (m *Model) applyBoard(msg boardMsg) tea.Cmd {
	if msg.generation != m.boardGen || msg.view != m.view {
		// A newer board superseded this one.
		return nil
	}
	m.invalidateDetail()
	m.loading = false
	if msg.err != nil {
		m.boardErr = msg.err.Error()
		return nil
	}
	m.boardErr = ""
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
	return tea.Batch(m.rebuildRows(prev), m.loadGraphCmd(msg.view, msg.generation, msg.issues))
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

// applyDetail installs a detail snapshot, discarding stale responses.
func (m *Model) applyDetail(msg detailMsg) tea.Cmd {
	if msg.generation != m.detailGen || m.selected >= len(m.rows) || msg.id != m.rows[m.selected].ID {
		return nil
	}
	m.checking = false
	if msg.err != nil {
		m.detailErr = msg.err.Error()
		if msg.issue != nil {
			issue := normalizeIssueCounts(*msg.issue, msg.down, msg.up)
			m.detail = &issue
			m.down, m.up = msg.down, msg.up
		} else {
			m.detail, m.down, m.up = nil, nil, nil
		}
		return nil
	}
	m.detailErr = ""
	if msg.issue != nil {
		issue := normalizeIssueCounts(*msg.issue, msg.down, msg.up)
		m.detail = &issue
	} else {
		m.detail = nil
	}
	m.down, m.up = msg.down, msg.up
	m.dOffset = 0
	return nil
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
	listW := listPaneWidth(w)
	detailW := w - 1 - listW
	if detailW < 12 {
		detailW = 12
		listW = w - 1 - detailW
	}
	listPane := m.renderListPane(listW, contentH)
	detailPane := m.renderDetailPane(detailW, contentH)
	var sb strings.Builder
	sb.WriteString(m.renderHeader(w))
	sb.WriteString("\n")
	for i := 0; i < contentH; i++ {
		sb.WriteString(listPane[i])
		sb.WriteString(" ")
		sb.WriteString(detailPane[i])
		sb.WriteString("\n")
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
		title = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("red")).Render("⚠ CYCLE") + " · " + title
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
	case m.boardErr != "":
		msg := "Could not load board."
		for _, l := range wrapText(msg, inner) {
			lines = append(lines, lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("red")).Render(l))
		}
		for _, l := range wrapText(m.boardErr, inner) {
			lines = append(lines, styleDim.Render(l))
		}
		lines = append(lines, styleDim.Render("Check bd is installed and a beads workspace is active (BEADS_DIR)."))
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
		lines = append(lines, lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("red")).Render("Could not load detail."))
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
	title := styleDim.Render("Detail")
	if m.focus == FocusDetail {
		title = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("cyan")).Render("Detail")
	}
	return pane(title, lines, w, h)
}

// renderFooter paints the hint/status bar.
func (m Model) renderFooter(w int) string {
	if w < 48 {
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
	hints := styleDim.Render("s sort / search t tag ? help q quit")
	var left string
	switch {
	case m.boardErr != "":
		left = status + " · " + lipgloss.NewStyle().Foreground(lipgloss.Color("red")).Render("board error")
		hints = styleDim.Render("q quit")
	case m.loading:
		left = status + " · " + styleDim.Render("loading…")
	case m.focus == FocusDetail:
		left = status
		hints = styleDim.Render("j·k/↑↓ scroll · ctrl-u/d half-page · space/page pg · g/G · esc back · q quit")
	default:
		left = status
	}
	if displayWidth(left)+1+displayWidth(hints) <= w {
		return fitLine(left, hints, w)
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
		"  Tree:          enter/tab toggle · h/l controls (h collapse, l detail) · v flat/tree (preserves selection) · siblings use active sort",
		"  Detail:        enter/l/→ open · h/← return · j/k or ↑/↓ scroll",
		"  Navigation:    esc close detail / clear search",
		viewHelp,
		"  Sort:          s cycle created · updated · alphabetical · dependencies (blocked-by/in) · depends (blocks/out) · priority",
		"  Search:        / prompt · Enter apply · status:open · priority:P1 · label:frontend · text · Esc cancel",
		"  Tags:          t search by the selected bead's labels",
		"  Refresh:       r · Reset: R · Help: ? (any key closes) · Quit: q/Ctrl+C",
		"",
		rowLegend,
		"Markers: ⇣N depends on N · ⇡N has N dependents",
		"",
		"Read-only: beads-tui never creates, edits or closes beads.",
	}
	inner := max(1, width-2)
	var lines []string
	for _, line := range raw {
		lines = append(lines, wrapText(line, inner)...)
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

// listPaneWidth splits the width between board and detail.
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
		w := total / 2
		if w > 48 {
			return 48
		}
		return w
	}
}

func (m Model) detailWidth() int {
	w := m.width
	if w <= 0 {
		w = 80
	}
	return w - 3 - listPaneWidth(w)
}

func (m Model) detailVisLines() int {
	if m.height <= 0 {
		return 10
	}
	return m.height - 4
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
