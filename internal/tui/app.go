package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/RooseveltAdvisors/beads-tui/internal/bd"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

const bdTimeout = 8 * time.Second

type Focus int

const (
	FocusList Focus = iota
	FocusDetail
)

type Backend interface {
	List(context.Context, bd.View) ([]bd.Issue, error)
	Show(context.Context, string) (*bd.Issue, error)
	Deps(context.Context, string, bool) ([]bd.DepRecord, error)
	Statuses(context.Context) ([]bd.StatusInfo, error)
}

type Model struct {
	backend  Backend
	view     bd.View
	allRows  []bd.Issue
	rows     []bd.Issue
	vocab    Vocab
	boardErr string
	loading  bool

	sortMode    SortMode
	filter      Filter
	filtering   bool
	filterInput string
	keys        KeyMap

	selected  int
	focus     Focus
	dOffset   int
	detail    *bd.Issue
	down      []bd.DepRecord
	up        []bd.DepRecord
	detailErr string
	checking  bool

	tree         bool
	graph        map[string][]bd.DepRecord
	graphLoading bool
	graphErr     string
	expanded     map[string]bool

	lastSync string
	help     bool
	quitting bool
	width    int
	height   int
}

type boardMsg struct {
	view   bd.View
	issues []bd.Issue
	err    error
}
type statusMsg struct {
	statuses []bd.StatusInfo
	err      error
}
type detailMsg struct {
	id       string
	issue    *bd.Issue
	down, up []bd.DepRecord
	err      error
}
type graphMsg struct {
	view  bd.View
	edges map[string][]bd.DepRecord
	err   error
}

func New(backend Backend) Model {
	return Model{backend: backend, view: bd.ViewReady, sortMode: SortAlphabetical, focus: FocusList, vocab: NewVocab(nil), keys: loadKeyMap(), graph: map[string][]bd.DepRecord{}, expanded: map[string]bool{}, width: 80, height: 24}
}

func (m Model) Init() tea.Cmd { return tea.Batch(m.loadBoardCmd(), m.loadStatusesCmd()) }

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.clampDetailOffset()
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
	case graphMsg:
		if msg.view == m.view {
			m.graphLoading = false
			m.graphErr = ""
			if msg.err == nil {
				m.graph = msg.edges
			} else {
				m.graphErr = msg.err.Error()
			}
		}
	}
	return m, nil
}

func (m Model) updateKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.quitting {
		return m, nil
	}
	key := msg.String()
	if key == "ctrl+c" {
		m.quitting = true
		return m, tea.Quit
	}
	if m.filtering {
		switch key {
		case "esc":
			m.filtering, m.filterInput, m.filter = false, "", Filter{}
			return m, m.rebuildRows()
		case "enter":
			m.filtering = false
			m.filter = ParseFilter(m.filterInput)
			return m, m.rebuildRows()
		case "backspace", "ctrl+h":
			runes := []rune(m.filterInput)
			if len(runes) > 0 {
				m.filterInput = string(runes[:len(runes)-1])
			}
		case "ctrl+u":
			m.filterInput = ""
		default:
			if msg.Type == tea.KeyRunes {
				m.filterInput += string(msg.Runes)
			}
		}
		return m, nil
	}
	if m.help {
		m.help = false
		return m, nil
	}
	switch {
	case m.keys.is("help", key):
		m.help = true
	case m.keys.is("quit", key) || key == "q":
		m.quitting = true
		return m, tea.Quit
	case key == "esc":
		if m.focus == FocusDetail {
			m.focus, m.dOffset = FocusList, 0
		}
	case key == "1":
		return m, m.switchView(bd.ViewReady)
	case key == "2":
		return m, m.switchView(bd.ViewOpen)
	case key == "3":
		return m, m.switchView(bd.ViewAll)
	case m.keys.is("next_view", key):
		return m, m.switchView(m.adjacentView(1))
	case m.keys.is("prev_view", key):
		return m, m.switchView(m.adjacentView(-1))
	case m.keys.is("filter", key):
		m.filtering, m.filterInput = true, m.filter.String()
	case m.keys.is("sort", key):
		m.sortMode = m.sortMode.Next()
		return m, m.rebuildRows()
	case m.keys.is("graph", key):
		m.tree = !m.tree
		if m.tree {
			for _, row := range m.rows {
				m.expanded[row.ID] = true
			}
			if len(m.graph) == 0 && len(m.rows) > 0 {
				m.graphLoading = true
				return m, m.loadGraphCmd()
			}
		}
	case m.keys.is("refresh", key):
		m.loading = true
		if selected := m.selectedIssue(); selected != nil {
			m.checking = true
			return m, tea.Batch(m.loadBoardCmd(), m.loadDetailCmd(selected.ID))
		}
		return m, m.loadBoardCmd()
	}
	if m.focus == FocusList {
		return m.listKey(msg)
	}
	return m.detailKey(msg)
}

func (m *Model) switchView(view bd.View) tea.Cmd {
	if view == m.view {
		return nil
	}
	m.view, m.loading = view, true
	m.graph = map[string][]bd.DepRecord{}
	m.graphErr = ""
	return m.loadBoardCmd()
}

func (m Model) adjacentView(step int) bd.View {
	idx := 0
	for i, view := range bd.AllViews {
		if view == m.view {
			idx = i
		}
	}
	idx = (idx + step + len(bd.AllViews)) % len(bd.AllViews)
	return bd.AllViews[idx]
}

func (m Model) listKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	items := m.displayItems()
	if len(items) == 0 {
		return m, nil
	}
	before, page := m.selected, m.pageStep()
	switch msg.String() {
	case "j", "down":
		m.selected++
	case "k", "up":
		m.selected--
	case "g":
		m.selected = 0
	case "G":
		m.selected = len(items) - 1
	case " ", "pgdown", "ctrl+f":
		m.selected += page
	case "b", "pgup", "ctrl+b", "ctrl+u":
		m.selected -= page / 2
	case "ctrl+d":
		m.selected += page / 2
	case "enter":
		if m.tree {
			item := items[m.selected]
			m.expanded[item.Issue.ID] = !m.expanded[item.Issue.ID]
			m.selected = clamp(m.selected, 0, len(m.displayItems())-1)
			return m, nil
		}
		m.focus, m.dOffset = FocusDetail, 0
	case "l", "right":
		m.focus, m.dOffset = FocusDetail, 0
	default:
		return m, nil
	}
	m.selected = clamp(m.selected, 0, len(m.displayItems())-1)
	if m.selected != before {
		if issue := m.selectedIssue(); issue != nil {
			m.checking = true
			return m, m.loadDetailCmd(issue.ID)
		}
	}
	return m, nil
}

func (m Model) detailKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	lines := len(BuildDetail(m.vocab, m.detail, m.down, m.up, m.detailWidth()))
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
		m.dOffset = maxOffset
	case " ", "pgdown", "ctrl+f", "ctrl+d":
		m.dOffset += page
	case "b", "pgup", "ctrl+b", "ctrl+u":
		m.dOffset -= page / 2
	case "h", "left":
		m.focus, m.dOffset = FocusList, 0
	default:
		return m, nil
	}
	m.dOffset = clamp(m.dOffset, 0, maxOffset)
	return m, nil
}

func (m *Model) rebuildRows() tea.Cmd {
	previous := m.selectedID()
	m.rows, m.selected = ApplyBoard(m.allRows, m.filter, m.sortMode), 0
	for i, row := range m.displayItems() {
		if row.Issue.ID == previous {
			m.selected = i
			break
		}
	}
	if len(m.rows) == 0 {
		m.detail, m.down, m.up = nil, nil, nil
		return nil
	} else if current := m.selectedIssue(); current == nil || m.detail == nil || m.detail.ID != current.ID {
		m.detail, m.down, m.up, m.detailErr = nil, nil, nil, ""
		if current != nil {
			m.checking = true
			return m.loadDetailCmd(current.ID)
		}
	}
	return nil
}

func (m Model) selectedID() string {
	if issue := m.selectedIssue(); issue != nil {
		return issue.ID
	}
	return ""
}

func (m Model) selectedIssue() *bd.Issue {
	items := m.displayItems()
	if m.selected < 0 || m.selected >= len(items) {
		return nil
	}
	return &items[m.selected].Issue
}

func (m Model) displayItems() []treeItem {
	if !m.tree {
		items := make([]treeItem, len(m.rows))
		for i, row := range m.rows {
			items[i] = treeItem{Issue: row}
		}
		return items
	}
	return BuildTree(m.rows, m.graph, m.expanded)
}

func (m Model) clampSelection() Model {
	m.selected = clamp(m.selected, 0, len(m.displayItems())-1)
	return m
}
func (m Model) pageStep() int {
	if s := m.height - 6; s > 1 {
		return s
	}
	return 10
}

func (m Model) loadBoardCmd() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), bdTimeout)
		defer cancel()
		issues, err := m.backend.List(ctx, m.view)
		return boardMsg{m.view, issues, err}
	}
}
func (m Model) loadStatusesCmd() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), bdTimeout)
		defer cancel()
		statuses, err := m.backend.Statuses(ctx)
		return statusMsg{statuses, err}
	}
}
func (m Model) loadDetailCmd(id string) tea.Cmd {
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
		return detailMsg{id, issue, down, up, err}
	}
}
func (m Model) loadGraphCmd() tea.Cmd {
	view, rows := m.view, append([]bd.Issue(nil), m.allRows...)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), bdTimeout)
		defer cancel()
		edges := make(map[string][]bd.DepRecord, len(rows))
		for _, row := range rows {
			deps, err := m.backend.Deps(ctx, row.ID, false)
			if err != nil {
				return graphMsg{view, edges, err}
			}
			edges[row.ID] = deps
		}
		return graphMsg{view, edges, nil}
	}
}

func (m *Model) applyBoard(msg boardMsg) tea.Cmd {
	m.loading = false
	if msg.err != nil {
		m.boardErr = msg.err.Error()
		return nil
	}
	if msg.view != m.view {
		return nil
	}
	previous := m.selectedID()
	m.boardErr, m.allRows = "", msg.issues
	m.rows, m.selected = ApplyBoard(m.allRows, m.filter, m.sortMode), 0
	m.lastSync = time.Now().Format("15:04:05")
	for i, row := range m.displayItems() {
		if row.Issue.ID == previous {
			m.selected = i
			break
		}
	}
	if len(m.rows) == 0 {
		m.detail, m.down, m.up, m.detailErr = nil, nil, nil, ""
		m.checking = false
		return nil
	}
	var cmds []tea.Cmd
	if selected := m.selectedIssue(); selected != nil && (m.detail == nil || m.detail.ID != selected.ID) {
		m.checking = true
		cmds = append(cmds, m.loadDetailCmd(selected.ID))
	}
	if m.tree {
		m.graph = map[string][]bd.DepRecord{}
		m.graphErr = ""
		m.graphLoading = true
		cmds = append(cmds, m.loadGraphCmd())
	}
	if len(cmds) > 0 {
		return tea.Batch(cmds...)
	}
	return nil
}

func (m *Model) applyDetail(msg detailMsg) tea.Cmd {
	if selected := m.selectedIssue(); selected == nil || msg.id != selected.ID {
		return nil
	}
	m.checking = false
	if msg.err != nil {
		m.detailErr, m.detail = msg.err.Error(), msg.issue
		m.down, m.up = msg.down, msg.up
		return nil
	}
	m.detailErr, m.detail, m.down, m.up, m.dOffset = "", msg.issue, msg.down, msg.up, 0
	return nil
}

func (m *Model) clampDetailOffset() {
	if m.detail == nil {
		return
	}
	_, maxOffset := m.detailContentBudget(len(BuildDetail(m.vocab, m.detail, m.down, m.up, m.detailWidth())))
	m.dOffset = clamp(m.dOffset, 0, maxOffset)
}

func (m Model) View() string {
	if m.quitting {
		return ""
	}
	if m.help {
		return m.renderHelp()
	}
	w, h := m.width, m.height
	if w <= 0 {
		w = 80
	}
	if h <= 0 {
		h = 24
	}
	contentH := max(1, h-2)
	listW := listPaneWidth(w)
	detailW := w - 1 - listW
	if detailW < 12 {
		detailW = 12
		listW = w - 1 - detailW
	}
	listPane, detailPane := m.renderListPane(listW, contentH), m.renderDetailPane(detailW, contentH)
	var sb strings.Builder
	sb.WriteString(m.renderHeader(w))
	sb.WriteByte('\n')
	for i := 0; i < contentH; i++ {
		sb.WriteString(listPane[i])
		sb.WriteByte(' ')
		sb.WriteString(detailPane[i])
		sb.WriteByte('\n')
	}
	sb.WriteString(m.renderFooter(w))
	return sb.String()
}

func (m Model) renderHeader(w int) string {
	left := styleBold.Render("beads-tui") + "  " + styleDim.Render("·") + "  " + m.view.Label() + " board"
	if m.tree {
		left += " " + lipgloss.NewStyle().Foreground(lipgloss.Color("magenta")).Render("TREE")
	}
	if m.filter.Active() {
		left += " " + lipgloss.NewStyle().Foreground(lipgloss.Color("yellow")).Render("● "+m.filter.String())
	}
	return fitLine(left, m.renderTabs(), w)
}
func (m Model) renderTabs() string {
	parts := make([]string, 0, len(bd.AllViews))
	for i, view := range bd.AllViews {
		label := fmt.Sprintf("[%d]%s", i+1, view.Label())
		if view == m.view {
			parts = append(parts, lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("cyan")).Render(label))
		} else {
			parts = append(parts, styleDim.Render(label))
		}
	}
	return strings.Join(parts, " ")
}
func (m Model) renderListPane(w, h int) []string {
	inner := max(1, w-2)
	var lines []string
	switch {
	case m.boardErr != "":
		lines = append(lines, lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("red")).Render("Could not load board."), styleDim.Render(truncate(m.boardErr, inner)))
	case m.loading && len(m.rows) == 0:
		lines = append(lines, styleDim.Render("Loading board…"))
	case len(m.rows) == 0:
		lines = append(lines, styleDim.Render(m.emptyBoardText()))
	default:
		items, vis := m.displayItems(), max(0, h-2)
		if vis > len(items) {
			vis = len(items)
		}
		top := m.scrollTop(vis)
		for i := top; i < top+vis; i++ {
			item := items[i]
			prefix := item.Prefix
			line := m.vocab.ListRow(item.Issue, max(1, inner-displayWidth(prefix)), i == m.selected)
			lines = append(lines, prefix+line)
		}
		if m.graphLoading {
			lines = append(lines, styleDim.Render("Loading dependency graph…"))
		} else if m.graphErr != "" {
			lines = append(lines, lipgloss.NewStyle().Foreground(lipgloss.Color("red")).Render("Graph unavailable: "+truncate(m.graphErr, inner)))
		}
	}
	title := m.view.Label()
	if m.tree {
		title = "Graph · " + title
	}
	if m.focus == FocusList {
		title = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("cyan")).Render(title)
	} else {
		title = styleDim.Render(title)
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
func (m Model) scrollTop(vis int) int {
	n := len(m.displayItems())
	if vis >= n {
		return 0
	}
	return clamp(m.selected-(vis-1)/2, 0, n-vis)
}

func (m Model) renderDetailPane(w, h int) []string {
	inner := max(1, w-2)
	var lines []string
	switch {
	case m.checking && m.detail == nil:
		lines = append(lines, styleDim.Render("Loading detail…"))
	case m.detailErr != "" && m.detail == nil:
		lines = append(lines, lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("red")).Render("Could not load detail."), styleDim.Render(truncate(m.detailErr, inner)))
	case m.detail != nil:
		all := BuildDetail(m.vocab, m.detail, m.down, m.up, inner)
		contentVis, maxOffset := m.detailContentBudget(len(all))
		offset := clamp(m.dOffset, 0, maxOffset)
		end := min(len(all), offset+contentVis)
		lines = append(lines, all[offset:end]...)
		if m.detailErr != "" {
			lines = append(lines, "", styleDim.Render("ⓘ "+truncate(m.detailErr, inner)))
		}
		if remaining := len(all) - end; remaining > 0 {
			lines = append(lines, styleDim.Render(fmt.Sprintf("↓ %d more lines", remaining)))
		}
	default:
		lines = append(lines, styleDim.Render("Select a bead for details."))
	}
	title := "Detail"
	if m.focus == FocusDetail {
		title = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("cyan")).Render(title)
	} else {
		title = styleDim.Render(title)
	}
	return pane(title, lines, w, h)
}

func (m Model) renderFooter(w int) string {
	if m.filtering {
		return fitLine(lipgloss.NewStyle().Foreground(lipgloss.Color("yellow")).Render("Filter /"+m.filterInput+"▌"), styleDim.Render("enter apply · esc clear"), w)
	}
	id, filter := "-", "—"
	if issue := m.selectedIssue(); issue != nil {
		id = issue.ID
	}
	if m.filter.Active() {
		filter = m.filter.String()
	}
	position := 0
	if len(m.displayItems()) > 0 {
		position = (m.selected + 1) * 100 / len(m.displayItems())
	}
	status := fmt.Sprintf("%s · sort:%s · filter:%s · %s · %d total · %d%% · q quit", m.view.Label(), m.sortMode, filter, id, len(m.rows), position)
	if m.boardErr != "" {
		status = "board error · " + status
	}
	return fitLine(styleDim.Render(status), "", w)
}

func (m Model) renderHelp() string {
	lines := []string{styleBold.Render("beads-tui · keyboard reference"), styleSection.Render("List view"), "  j/k or ↑/↓     move selection", "  g/G            first / last item", "  space, ctrl+f/b page down / up", "  ctrl+d/u       half-page down / up", "  enter          expand/collapse tree node", "  h/l or ←/→     switch list/detail focus", styleSection.Render("Board"), "  1 Ready · 2 Open · 3 All", "  tab / shift-tab cycle board views", "  s              cycle sort mode", "  f              filter: status:open, priority:P0, or text", "  v              flat list / dependency tree", "  r              refresh", styleSection.Render("Detail view"), "  j/k or ↑/↓     scroll markdown detail", "  g/G            top / bottom", "  ctrl+d/u       half-page scroll", "  esc or h        return to list", styleSection.Render("Global"), "  ?              show this help", "  q or ctrl+c     quit", styleDim.Render("Markers: ⇣N depends on N · ⇡N has N dependents"), styleDim.Render("Read-only: all data comes from bd; beads-tui never mutates the graph.")}
	w := max(80, m.width)
	boxWidth := min(88, w-4)
	box := pane("Help", lines, boxWidth, len(lines)+2)
	out := make([]string, 0, max(1, m.height))
	for i := 0; i < max(1, m.height); i++ {
		if i < len(box) {
			out = append(out, strings.Repeat(" ", max(0, (w-displayWidth(box[i]))/2))+box[i])
		} else {
			out = append(out, "")
		}
	}
	return strings.Join(out, "\n")
}

func (m Model) detailWidth() int    { w := max(80, m.width); return w - 3 - listPaneWidth(w) }
func (m Model) detailVisLines() int { return max(1, m.height-4) }
func (m Model) detailContentBudget(lines int) (int, int) {
	visible := m.detailVisLines()
	if m.detailErr != "" {
		visible -= 2
	}
	if lines > visible {
		visible--
	}
	visible = max(0, visible)
	return visible, max(0, lines-visible)
}

func pane(title string, content []string, w, h int) []string {
	inner := max(1, w-2)
	title = truncate(title, inner)
	out := []string{"┌" + title + strings.Repeat("─", max(0, inner-displayWidth(title))) + "┐"}
	for i := 0; i < max(0, h-2); i++ {
		line := ""
		if i < len(content) {
			line = content[i]
		}
		out = append(out, "│"+padRight(truncatePhys(line, inner), inner)+"│")
	}
	out = append(out, "└"+strings.Repeat("─", inner)+"┘")
	return out
}
func padRight(s string, cells int) string {
	return s + strings.Repeat(" ", max(0, cells-displayWidth(s)))
}
func truncatePhys(s string, cells int) string {
	if cells <= 0 {
		return ""
	}
	if displayWidth(s) <= cells {
		return s
	}
	return ansiPrefix(s) + truncate(stripANSI(s), cells) + "\x1b[0m"
}
func ansiPrefix(s string) string {
	pos := 0
	for {
		idx := strings.Index(s[pos:], "\x1b[")
		if idx != 0 {
			break
		}
		end := strings.IndexByte(s[pos:], 'm')
		if end < 0 {
			break
		}
		pos += end + 1
	}
	return s[:pos]
}
func fitLine(left, right string, w int) string {
	lw, rw := displayWidth(left), displayWidth(right)
	if lw+rw <= w {
		return left + strings.Repeat(" ", max(0, w-lw-rw)) + right
	}
	return truncatePhys(left, w)
}
func displayWidth(s string) int { return runewidth.StringWidth(stripANSI(s)) }

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

func clamp(value, low, high int) int {
	if high < low {
		return low
	}
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
