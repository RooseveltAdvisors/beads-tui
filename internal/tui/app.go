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

// Model is the bead board application state.
type Model struct {
	backend Backend

	view     bd.View
	rows     []bd.Issue
	vocab    Vocab
	boardErr string
	loading  bool

	selected int
	focus    Focus
	dOffset  int

	detail    *bd.Issue
	down      []bd.DepRecord
	up        []bd.DepRecord
	detailErr string
	checking  bool

	lastSync string
	help     bool
	quitting bool

	width  int
	height int
}

// boardMsg carries the result of a board load.
type boardMsg struct {
	view   bd.View
	issues []bd.Issue
	err    error
}

// statusMsg carries the status vocabulary.
type statusMsg struct {
	statuses []bd.StatusInfo
	err      error
}

// detailMsg carries the detail snapshot for one bead: the issue plus both
// dependency directions.
type detailMsg struct {
	id    string
	issue *bd.Issue
	down  []bd.DepRecord
	up    []bd.DepRecord
	err   error
}

// New builds the board model backed by the given read-only data source.
func New(backend Backend) Model {
	return Model{
		backend: backend,
		view:    bd.ViewReady,
		focus:   FocusList,
		vocab:   NewVocab(nil),
		width:   80,
		height:  24,
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
			lines := len(BuildDetail(m.vocab, m.detail, m.down, m.up, m.detailWidth()))
			if m.dOffset >= lines {
				m.dOffset = lines - 1
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
	if msg.String() == "ctrl+c" {
		m.quitting = true
		return m, tea.Quit
	}
	if m.help {
		m.help = false
		return m, nil
	}
	switch msg.String() {
	case "?":
		m.help = true
		return m, nil
	case "q":
		m.quitting = true
		return m, tea.Quit
	case "esc":
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
	case "r":
		m.loading = true
		if len(m.rows) > 0 {
			m.checking = true
			return m, tea.Batch(m.loadBoardCmd(), m.loadDetailCmd(m.rows[m.selected].ID))
		}
		return m, m.loadBoardCmd()
	}
	if m.focus == FocusList {
		return m.listKey(msg)
	}
	return m.detailKey(msg)
}

// switchView changes the board view, keeping the selection stable by id when
// the same bead is still present.
func (m Model) switchView(view bd.View) (tea.Model, tea.Cmd) {
	if view == m.view {
		return m, nil
	}
	m.view = view
	m.loading = true
	return m, m.loadBoardCmd()
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
		m.selected = n - 1
	case "f", " ", "pgdown", "ctrl+f":
		m.selected += page
	case "b", "pgup", "ctrl+b":
		m.selected -= page
	case "enter":
		m.focus = FocusDetail
		m.dOffset = 0
	case "l", "right":
		m.focus = FocusDetail
		m.dOffset = 0
	default:
		return m, nil
	}
	m = m.clampSelection()
	if m.selected != before {
		m.checking = true
		return m, m.loadDetailCmd(m.rows[m.selected].ID)
	}
	return m, nil
}

func (m Model) detailKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	lines := len(BuildDetail(m.vocab, m.detail, m.down, m.up, m.detailWidth()))
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
	case "f", " ", "pgdown", "ctrl+f":
		m.dOffset += page
	case "b", "pgup", "ctrl+b":
		m.dOffset -= page
	case "h", "left":
		m.focus = FocusList
		m.dOffset = 0
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

func (m Model) loadBoardCmd() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), bdTimeout)
		defer cancel()
		issues, err := m.backend.List(ctx, m.view)
		return boardMsg{view: m.view, issues: issues, err: err}
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
		return detailMsg{id: id, issue: issue, down: down, up: up, err: err}
	}
}

// applyBoard installs a fresh board snapshot.
func (m *Model) applyBoard(msg boardMsg) tea.Cmd {
	m.loading = false
	if msg.err != nil {
		m.boardErr = msg.err.Error()
		return nil
	}
	if msg.view != m.view {
		// A newer board superseded this one.
		return nil
	}
	m.boardErr = ""
	// Capture the previous selection from the OLD board before rows are
	// replaced, then restore it by id if the bead is still present.
	prev := ""
	if m.selected < len(m.rows) {
		prev = m.rows[m.selected].ID
	}
	m.rows = msg.issues
	m.lastSync = time.Now().Format("15:04:05")
	m.selected = 0
	if prev != "" {
		for i := range m.rows {
			if m.rows[i].ID == prev {
				m.selected = i
				break
			}
		}
	}
	if len(m.rows) == 0 {
		m.detail, m.down, m.up = nil, nil, nil
		m.detailErr = ""
		m.checking = false
		return nil
	}
	if cur := m.rows[m.selected].ID; m.detail == nil || m.detail.ID != cur {
		m.checking = true
		return m.loadDetailCmd(cur)
	}
	return nil
}

// applyDetail installs a detail snapshot, discarding stale responses.
func (m *Model) applyDetail(msg detailMsg) tea.Cmd {
	if m.selected >= len(m.rows) || msg.id != m.rows[m.selected].ID {
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
	w, h := m.width, m.height
	if w <= 0 {
		w = 80
	}
	if h <= 0 {
		h = 24
	}
	contentH := h - 2
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
	sb.WriteString(m.renderFooter(w))
	return sb.String()
}

// renderHeader paints the title bar: view name and tabs.
func (m Model) renderHeader(w int) string {
	left := styleBold.Render("beads-tui") + "  " + styleDim.Render("·") + "  " + m.vocabViewLabel()
	return fitLine(left, m.renderTabs(), w)
}

func (m Model) vocabViewLabel() string {
	return styleDim.Render(m.view.Label() + " board")
}

func (m Model) renderTabs() string {
	var parts []string
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
	case m.loading && len(m.rows) == 0:
		lines = append(lines, styleDim.Render("Loading board…"))
	case len(m.rows) == 0:
		lines = append(lines, styleDim.Render(m.emptyBoardText()))
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
			lines = append(lines, m.vocab.ListRow(m.rows[i], inner, i == m.selected))
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
		vis := h - 2
		all := BuildDetail(m.vocab, m.detail, m.down, m.up, inner)
		offset := m.dOffset
		contentVis := vis
		if m.detailErr != "" {
			contentVis -= 2
		}
		if len(all) > vis {
			contentVis--
		}
		if contentVis < 0 {
			contentVis = 0
		}
		maxOffset := len(all) - contentVis
		if maxOffset < 0 {
			maxOffset = 0
			offset = 0
		} else if offset > maxOffset {
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
	var left string
	switch {
	case m.boardErr != "":
		left = lipgloss.NewStyle().Foreground(lipgloss.Color("red")).Render("board error - check bd") +
			styleDim.Render(" · q quit")
		m.lastSync = ""
	case m.loading:
		left = styleDim.Render("loading…")
	case m.focus == FocusDetail:
		left = styleDim.Render("j·k/↑↓ scroll · space/page pg · g/G · esc back · q quit")
	default:
		left = styleDim.Render("↑↓/j·k select · enter detail · 1/2/3 view · r refresh · ? help · q quit")
	}
	right := ""
	if m.lastSync != "" {
		right = styleDim.Render("sync " + m.lastSync)
	}
	return fitLine(left, right, w)
}

// renderHelp overlays the key reference.
func (m Model) renderHelp() string {
	lines := []string{
		styleBold.Render("beads-tui - read-only board for Beads (bd)"),
		"",
		"  Nav list:      j/k or ↑/↓ move · g/G top/bottom · f/b page",
		"  Detail:        enter (or →) focus · j/k or ↑/↓ scroll · esc back",
		"  Views:         1 Ready · 2 Open · 3 All (work with no blockers / open / everything)",
		"  Refresh:       r  ·  Quit: q or ctrl+c  ·  Close this: any key",
		"",
		styleDim.Render("Rows: ○ open  ◐ in_progress  ● blocked  ✓ closed  ❄ deferred  📌 pinned  ◇ hooked"),
		styleDim.Render("Markers: ⇣N depends on N · ⇡N has N dependents"),
		"",
		styleDim.Render("Read-only: beads-tui never creates, edits or closes beads."),
	}
	w := m.width
	if w <= 0 {
		w = 80
	}
	width := w - 4
	if width > 72 {
		width = 72
	}
	box := pane("Help", lines, width, len(lines)+2)
	out := make([]string, 0, m.height)
	for i := 0; i < m.height; i++ {
		if i < len(box) {
			out = append(out, strings.Repeat(" ", (w-displayWidth(box[i]))/2)+box[i])
		} else {
			out = append(out, "")
		}
	}
	return strings.Join(out, "\n")
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

// pane frames content lines (which may carry ANSI) into a bordered pane of
// exactly h lines and w cells. Content gets w-2 cells; the border is drawn
// inside the same width budget.
func pane(title string, content []string, w, h int) []string {
	inner := w - 2
	if inner < 1 {
		inner = 1
	}
	topTitle := truncate(title, inner)
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

// truncatePhys truncates an ANSI string to cells display width, preserving
// the leading ANSI styling in the result.
func truncatePhys(s string, cells int) string {
	if cells <= 0 {
		return ""
	}
	plain := stripANSI(s)
	if runewidth.StringWidth(plain) <= cells {
		return s
	}
	prefix := ansiPrefix(s)
	return prefix + truncate(plain, cells) + "\x1b[0m"
}

// ansiPrefix extracts the leading ANSI SGR escape sequence from a styled string.
func ansiPrefix(s string) string {
	idx := strings.Index(s, "\x1b[")
	if idx == -1 {
		return ""
	}
	end := strings.IndexByte(s[idx:], 'm')
	if end == -1 {
		return ""
	}
	return s[:idx+end+1]
}

// fitLine lays a left/right pair out on one line, dropping the right part
// (decoration) before truncating the left when the line cannot fit.
func fitLine(left, right string, w int) string {
	lw, rw := displayWidth(left), displayWidth(right)
	if lw+rw <= w {
		return left + strings.Repeat(" ", w-lw-rw) + right
	}
	if lw > w {
		return truncatePhys(left, w)
	}
	return left
}

// displayWidth reports the cell width of ANSI-styled text.
func displayWidth(s string) int {
	return runewidth.StringWidth(stripANSI(s))
}
