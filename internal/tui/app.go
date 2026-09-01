package tui

import (
	"context"
	"fmt"
	"log"
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

// bdTimeout caps every bd call so a wedged store (or lock contention) shows
// an error instead of freezing the UI.
const bdTimeout = 8 * time.Second

const boardGraphWorkers = 8

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

// Model is the bead board application state.
type Model struct {
	backend Backend

	view     bd.View
	sortMode SortMode
	filter   Filter
	allRows  []bd.Issue
	deps     map[string][]bd.DepRecord
	rows     []bd.Issue
	treeRows []TreeRow
	expanded map[string]bool
	treeMode bool
	vocab    Vocab
	boardErr string
	loading  bool
	boardGen uint64

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

// New builds the board model backed by the given read-only data source.
func New(backend Backend) Model {
	input := textinput.New()
	input.Prompt = "Filter › "
	input.Placeholder = "status:open  priority:P1  label:frontend  text"
	input.CharLimit = 120
	input.Width = 60
	return Model{
		backend:  backend,
		view:     bd.ViewReady,
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
	case statusMsg:
		if msg.err == nil {
			m.vocab = NewVocab(msg.statuses)
		}
	case detailMsg:
		return m, m.applyDetail(msg)
	}
	return m, nil
}

func (m Model) updateKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.quitting {
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
			if m.searching {
				m.filter = m.searchBase
				m.searching = false
				m.detail = m.searchDetail
				m.down = m.searchDown
				m.up = m.searchUp
				m.detailErr = m.searchDErr
				m.checking = false
				m.detailGen++
				cmd := m.rebuildRows(m.searchID)
				m.focus = m.searchFocus
				m.dOffset = m.searchDOff
				return m, cmd
			} else {
				m.filter = Filter{}
			}
			m.searching = false
			return m, m.rebuildRows(m.selectedID())
		case "enter":
			m.filtering = false
			m.filterInput.Blur()
			if m.searching {
				m.filter = SearchFilter(m.filterInput.Value())
			} else {
				m.filter = ParseFilter(m.filterInput.Value())
			}
			m.searching = false
			return m, m.rebuildRows(m.selectedID())
		}
		var cmd tea.Cmd
		m.filterInput, cmd = m.filterInput.Update(msg)
		if m.searching {
			previousID := m.selectedID()
			m.filter = SearchFilter(m.filterInput.Value())
			filterCmd := m.rebuildRows(previousID)
			if cmd != nil && filterCmd != nil {
				return m, tea.Batch(cmd, filterCmd)
			}
			if filterCmd != nil {
				return m, filterCmd
			}
		}
		return m, cmd
	}
	switch msg.String() {
	case "?":
		m.help = true
		m.helpOffset = 0
		return m, nil
	case "q":
		m.quitting = true
		return m, tea.Quit
	case "esc":
		if m.filter.Active() {
			m.filter = Filter{}
			return m, m.rebuildRows(m.selectedID())
		}
		if m.focus == FocusDetail {
			m.focus = FocusList
			m.dOffset = 0
		}
		return m, nil
	case "1":
		return m.switchView(bd.ViewReady)
	case "2":
		return m.switchView(bd.ViewOpen)
	case "3":
		return m.switchView(bd.ViewAll)
	case "s":
		m.sortMode = m.sortMode.Next()
		return m, m.rebuildRows(m.selectedID())
	case "f":
		return m, m.openPrompt(false)
	case "/":
		m.searchBase = m.filter
		return m, m.openPrompt(true)
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
	}
	if m.focus == FocusList {
		return m.listKey(msg)
	}
	return m.detailKey(msg)
}

func (m *Model) openPrompt(search bool) tea.Cmd {
	m.filtering = true
	m.searching = search
	if search {
		m.searchFocus = m.focus
		m.searchID = m.selectedID()
		m.searchDOff = m.dOffset
		m.searchDetail = m.detail
		m.searchDown = m.down
		m.searchUp = m.up
		m.searchDErr = m.detailErr
		m.focus = FocusList
		m.filterInput.Prompt = "Search / › "
	} else {
		m.filterInput.Prompt = "Filter › "
	}
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
	return m, m.startBoardLoad()
}

func (m Model) selectedID() string {
	if m.selected >= 0 && m.selected < len(m.rows) {
		return m.rows[m.selected].ID
	}
	return ""
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
		deps := make(map[string][]bd.DepRecord)
		type graphResult struct {
			deps           []bd.DepRecord
			dependentCount int
			err            error
		}
		results := make([]graphResult, len(issues))
		jobs := make(chan int)
		var workers sync.WaitGroup
		workerCount := min(boardGraphWorkers, len(issues))
		workers.Add(workerCount)
		for range workerCount {
			go func() {
				defer workers.Done()
				for i := range jobs {
					issue := issues[i]
					if issue.ID == "" {
						continue
					}
					if issue.DependencyCount > 0 {
						callCtx, callCancel := context.WithTimeout(context.Background(), bdTimeout)
						results[i].deps, results[i].err = backend.Deps(callCtx, issue.ID, false)
						callCancel()
						if results[i].err != nil {
							continue
						}
					}
					callCtx, callCancel := context.WithTimeout(context.Background(), bdTimeout)
					dependents, depErr := backend.Deps(callCtx, issue.ID, true)
					callCancel()
					results[i].dependentCount = len(dependents)
					results[i].err = depErr
				}
			}()
		}
		for i := range issues {
			jobs <- i
		}
		close(jobs)
		workers.Wait()
		// Dependency metadata is best effort: a slow graph query must not hide
		// the list snapshot that the user can still browse.
		for i, result := range results {
			if result.err != nil {
				log.Printf("beads-tui: dependency enrichment for %s skipped: %v", issues[i].ID, result.err)
			}
			if len(result.deps) > 0 {
				deps[issues[i].ID] = result.deps
			}
			if result.err == nil {
				issues[i].DependentCount = result.dependentCount
			}
		}
		return boardMsg{view: view, generation: generation, issues: issues, deps: deps}
	}
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
	m.loading = false
	if msg.err != nil {
		m.boardErr = msg.err.Error()
		return nil
	}
	m.boardErr = ""
	// Capture the previous selection from the OLD board before rows are
	// replaced, then restore it by id if the bead is still present.
	prev := m.selectedID()
	m.allRows = append([]bd.Issue(nil), msg.issues...)
	m.deps = msg.deps
	return m.rebuildRows(prev)
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
			m.detail = msg.issue
			m.down, m.up = msg.down, msg.up
		} else {
			m.detail, m.down, m.up = nil, nil, nil
		}
		return nil
	}
	m.detailErr = ""
	m.detail = msg.issue
	m.down, m.up = msg.down, msg.up
	m.dOffset = 0
	return nil
}

// View renders the full frame.
func (m Model) View() string {
	if m.quitting {
		return ""
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

func (m Model) renderFilterPrompt(w int) string {
	label := "FILTER"
	if m.searching {
		label = "SEARCH"
	}
	input := m.filterInput.View()
	return truncatePhys(styleDim.Render(label)+"  "+input, w)
}

// renderHeader paints the title bar: view name and tabs.
func (m Model) renderHeader(w int) string {
	left := styleBold.Render("beads-tui") + "  " + styleDim.Render("·") + "  " + m.vocabViewLabel()
	return fitLine(left, m.renderTabs(), w)
}

func (m Model) hasGraphEdges() bool {
	if m.selected < 0 || m.selected >= len(m.rows) {
		return false
	}
	id := m.rows[m.selected].ID
	if id == "" {
		return false
	}
	if m.rows[m.selected].ParentID != "" || len(m.deps[id]) > 0 {
		return true
	}
	for _, issue := range m.allRows {
		if issue.ParentID == id {
			return true
		}
		for _, records := range m.deps {
			for _, dep := range records {
				if dep.ID == id {
					return true
				}
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
	lines, cycle := graphLines(m.rows, m.allRows, m.deps, m.selectedID(), m.vocab)
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
	for i, view := range bd.AllViews {
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
			lines = append(lines, styleDim.Render("No matches for filter: "+m.filter.String()))
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
				if m.view == bd.ViewReady {
					lines = append(lines, m.vocab.ReadyTreeRow(m.treeRows[i], inner, i == m.selected))
				} else {
					lines = append(lines, m.vocab.TreeRow(m.treeRows[i], inner, i == m.selected))
				}
			} else {
				if m.view == bd.ViewReady {
					lines = append(lines, m.vocab.ReadyRow(m.rows[i], inner, i == m.selected))
				} else {
					lines = append(lines, m.vocab.ListRow(m.rows[i], inner, i == m.selected))
				}
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
	case bd.ViewReady:
		return "No ready work. Everything unblocked is claimed or done."
	case bd.ViewOpen:
		return "No open issues."
	default:
		return "No issues in this graph."
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
		filter := "off"
		if m.filter.Active() {
			filter = "on"
		}
		percent := 0
		if len(m.rows) == 1 {
			percent = 100
		} else if len(m.rows) > 1 {
			percent = m.selected * 100 / (len(m.rows) - 1)
		}
		return truncatePhys(styleDim.Render(fmt.Sprintf("%s  filter:%s  %d%%", m.view.Label(), filter, percent)), w)
	}
	selected := m.selectedID()
	if selected == "" {
		selected = "-"
	}
	scroll := 0
	if len(m.rows) > 1 {
		scroll = m.selected * 100 / (len(m.rows) - 1)
	}
	status := footerStatus(w, m.view.Label(), m.sortMode.String(), m.filter.String(), selected, len(m.rows), scroll)
	hints := styleDim.Render("s sort f filter t tag ? help q quit")
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

func footerStatus(w int, view, sortMode, filter, selected string, total, scroll int) string {
	if filter == "" {
		filter = "-"
	}
	if w < 64 {
		view = truncate(view, 3)
		sortMode = truncate(sortMode, 3)
	}
	fixed := fmt.Sprintf("view:%s sort:%s filter: sel: total:%d scroll:%d%%", view, sortMode, total, scroll)
	available := max(2, w-runewidth.StringWidth(fixed))
	filterWidth := max(1, available*2/3)
	selectedWidth := max(1, available-filterWidth)
	return strings.Join([]string{
		lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Render("view:" + view),
		lipgloss.NewStyle().Foreground(lipgloss.Color("141")).Render("sort:" + sortMode),
		lipgloss.NewStyle().Foreground(lipgloss.Color("208")).Render("filter:" + truncate(filter, filterWidth)),
		lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Render("sel:" + truncate(selected, selectedWidth)),
		styleDim.Render(fmt.Sprintf("total:%d", total)),
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
	rowLegend := fmt.Sprintf("Rows: %s claimable  %s in_progress  %s blocked  %s closed  %s deferred  %s hold",
		m.vocab.Icon("open"), m.vocab.Icon("in_progress"), m.vocab.Icon("blocked"),
		m.vocab.Icon("closed"), m.vocab.Icon("deferred"), m.vocab.Icon("hold"))
	raw := []string{
		"beads-tui - read-only board for Beads (bd)",
		"",
		"  Move/scroll:   j/k or ↑/↓ · g/G top/bottom · G graph when edges · space/PgDn/Ctrl+F forward · b/PgUp/Ctrl+B back",
		"  Half-page:     ctrl-u/d in list and detail",
		"  Tree:          enter/tab toggle · h/l controls (h collapse, l detail) · v flat/tree (preserves selection) · siblings use active sort",
		"  Detail:        enter/l/→ open · h/← return · j/k or ↑/↓ scroll",
		"  Navigation:    esc close detail / clear filter",
		"  Views:         1 Ready · 2 Open · 3 All (work with no blockers / open / everything)",
		"  Sort:          s cycle priority · created · updated · alphabetical · leverage",
		"  Filter:        f prompt · Enter apply · status:open · priority:P1 · label:frontend · text",
		"  Search:        / incremental id/title/description · Enter commit · Esc cancel",
		"  Tags:          t filter by the selected bead's labels",
		"  Refresh:       r · Help: ? (any key closes) · Quit: q/Ctrl+C",
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
