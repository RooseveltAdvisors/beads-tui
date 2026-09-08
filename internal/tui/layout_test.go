package tui

import (
	"strings"
	"testing"

	"github.com/RooseveltAdvisors/beads-tui/internal/bd"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestLayoutSelectionFollowsWidth(t *testing.T) {
	m := New(nil)
	m.layout = LayoutAuto
	m.width, m.height = wideLayoutColumns, 40
	if !m.layoutSideBySide() {
		t.Fatal("auto layout should split side-by-side at 140 columns")
	}
	m.width = wideLayoutColumns - 1
	if m.layoutSideBySide() {
		t.Fatal("auto layout should stack below 140 columns")
	}
	// Forced modes win at any width.
	m.width = 60
	m.layout = LayoutSide
	if !m.layoutSideBySide() {
		t.Fatal("forced side layout must hold on narrow terminals")
	}
	m.layout = LayoutStacked
	m.width = 200
	if m.layoutSideBySide() {
		t.Fatal("forced stacked layout must hold on wide terminals")
	}
}

func TestSideBySideGivesListTheMajority(t *testing.T) {
	for _, width := range []int{140, 160, 200} {
		got := listPaneWidth(width)
		if got < width*55/100 || got > width*60/100+1 {
			t.Fatalf("width %d: list pane = %d, want 55-60%% of the terminal", width, got)
		}
		if width-1-got < 12 {
			t.Fatalf("width %d: detail pane = %d, too narrow", width, width-1-got)
		}
	}
}

func TestStackedHeightGivesListSixtyPercent(t *testing.T) {
	for _, contentH := range []int{2, 5, 10, 22, 38} {
		got := stackedListHeight(contentH)
		if got < 1 || got > contentH-1 {
			t.Fatalf("contentH %d: stacked list height = %d, out of range", contentH, got)
		}
	}
	if got := stackedListHeight(20); got != 12 {
		t.Fatalf("stackedListHeight(20) = %d, want 12 (~60%%)", got)
	}
}

func TestStackedViewPlacesDetailBelowList(t *testing.T) {
	m := drive(t, nil)
	m.width, m.height = 100, 30
	m.layout = LayoutAuto
	lines := strings.Split(stripANSI(m.View()), "\n")
	detailTop := -1
	for i, line := range lines {
		if strings.HasPrefix(line, "┌Detail") {
			detailTop = i
			break
		}
	}
	if detailTop < 1 {
		t.Fatalf("stacked view missing detail pane top border:\n%s", strings.Join(lines, "\n"))
	}
	if !strings.HasPrefix(lines[detailTop-1], "└") {
		t.Fatalf("list pane is not stacked above the detail pane:\n%s", strings.Join(lines, "\n"))
	}
	// The full-width list shows rows at the terminal width, not a half split.
	for _, line := range lines[:detailTop] {
		if displayWidth(line) > 100 {
			t.Fatalf("stacked line overflowed the terminal: %q", line)
		}
	}
}

func TestSideBySideViewPutsPanesOnSharedRows(t *testing.T) {
	m := drive(t, nil)
	m.width, m.height = 160, 30
	m.layout = LayoutAuto
	lines := strings.Split(stripANSI(m.View()), "\n")
	if len(lines) < 3 {
		t.Fatalf("view too short:\n%s", strings.Join(lines, "\n"))
	}
	if !strings.HasPrefix(lines[1], "┌") || !strings.Contains(lines[1], "┌Detail") {
		t.Fatalf("side-by-side panes do not share row 1: %q", lines[1])
	}
	for i, line := range lines[1 : len(lines)-1] {
		if strings.HasPrefix(line, "┌Detail") {
			t.Fatalf("detail pane started on its own row %d; layout stacked instead of side-by-side", i+1)
		}
	}
}

func TestLayoutToggleKeyCyclesAndPersists(t *testing.T) {
	m := drive(t, nil)
	m.layout = LayoutAuto
	m = sendKey(t, m, "V")
	if m.layout != LayoutSide || !m.layoutSideBySide() {
		t.Fatalf("V from auto = %v, want forced side", m.layout)
	}
	m = sendKey(t, m, "V")
	if m.layout != LayoutStacked || m.layoutSideBySide() {
		t.Fatalf("V from side = %v, want forced stacked", m.layout)
	}
	m = sendKey(t, m, "V")
	if m.layout != LayoutAuto {
		t.Fatalf("V from stacked = %v, want auto", m.layout)
	}
}

func TestViewOptionsPersistAcrossRestart(t *testing.T) {
	t.Setenv("BEADS_TUI_CONFIG_DIR", t.TempDir())
	f := &fakeClient{}
	m := newTestModel(f)
	m.visibility.List.ID = false
	m.visibility.List.Labels = true
	m.visibility.Detail.Notes = false
	m.visibility.DetailPane = false
	m.saveState()

	reloaded := New(f)
	if reloaded.visibility.List.ID || !reloaded.visibility.List.Labels || reloaded.visibility.Detail.Notes || reloaded.visibility.DetailPane {
		t.Fatalf("visibility did not persist: %+v", reloaded.visibility)
	}
}

func TestHiddenDetailPaneUsesFullSpaceAndRestoresDefaults(t *testing.T) {
	m := drive(t, nil)
	m.width, m.height = 80, 20
	m.focus = FocusDetail
	m.options = true
	m.optionIndex = 0
	m = sendKey(t, m, "enter")
	if m.visibility.DetailPane || m.focus != FocusList {
		t.Fatalf("detail toggle = visible:%v focus:%v", m.visibility.DetailPane, m.focus)
	}
	view := stripANSI(m.View())
	if strings.Contains(view, "┌Detail") {
		t.Fatalf("hidden detail pane still rendered:\n%s", view)
	}
	for _, line := range strings.Split(view, "\n") {
		if displayWidth(line) > 80 {
			t.Fatalf("hidden-pane line overflowed: %q", line)
		}
	}
	m.options = true
	m = sendKey(t, m, "r")
	if !m.visibility.DetailPane || m.visibility.List.Labels {
		t.Fatalf("restore defaults failed: %+v", m.visibility)
	}
}

func TestDefaultRowsPreferTitleSpaceAndOptionsRespectResize(t *testing.T) {
	m := drive(t, &fakeClient{issues: map[bd.View][]bd.Issue{bd.ViewOpen: {
		{
			ID: "fm-density", Title: strings.Repeat("useful title ", 8), Status: "open", Priority: 1,
			Assignee: "pi", CommentCount: 3, Labels: []string{"a-very-long-tag-that-should-stay-hidden"},
		},
	}}})
	for _, width := range []int{42, 180} {
		m.width = width
		view := stripANSI(strings.Join(m.renderListPane(width, 8), "\n"))
		if strings.Contains(view, "a-very-long-tag") || !strings.Contains(view, "pi") || !strings.Contains(view, "💬3") {
			t.Fatalf("width %d default row fields wrong: %q", width, view)
		}
		for _, line := range strings.Split(view, "\n") {
			if displayWidth(line) > width {
				t.Fatalf("width %d overflow: %q", width, line)
			}
		}
	}
}

func TestDetailSectionsToggleIndependently(t *testing.T) {
	m := New(nil)
	m.detail = testDetail()
	m.visibility.Detail.Description = false
	m.visibility.Detail.Notes = true
	plain := stripANSI(strings.Join(m.buildDetail(70), "\n"))
	if strings.Contains(plain, "Description") || !strings.Contains(plain, "Notes") {
		t.Fatalf("detail section visibility ignored: %q", plain)
	}
	m.visibility.Detail.Notes = false
	m.visibility.Detail.Metadata = false
	plain = stripANSI(strings.Join(m.buildDetail(70), "\n"))
	if strings.Contains(plain, "Assigned last sprint") || strings.Contains(plain, "Assignee:") {
		t.Fatalf("hidden detail sections leaked: %q", plain)
	}
}

func TestViewOptionsKeepSelectionVisibleInNarrowTerminal(t *testing.T) {
	m := New(nil)
	m.width, m.height = 38, 7
	m.options = true
	m.optionIndex = len(viewOptionLabels) - 1
	view := stripANSI(m.View())
	if !strings.Contains(view, "Detail comments") || !strings.Contains(view, "r defaults") {
		t.Fatalf("narrow options hid selection or restore control: %q", view)
	}
}

func TestDetailScrollOffsetStaysValidAcrossLayoutChange(t *testing.T) {
	issues := []bd.Issue{{ID: "fm-long", Title: "Long", Status: "open"}}
	long := testDetailOf("fm-long")
	long.Description = strings.Repeat("word ", 600)
	f := &fakeClient{issues: map[bd.View][]bd.Issue{bd.ViewOpen: issues}, issue: long}
	m := drive(t, f)
	m.width, m.height = 160, 20
	m = applyMsg(t, m, detailMsg{id: "fm-long", generation: m.detailGen, issue: long, err: nil})
	m = sendKey(t, m, "enter")
	m = sendKey(t, m, "G")
	if m.dOffset <= 0 {
		t.Fatalf("detail did not scroll: offset = %d", m.dOffset)
	}
	before := m.dOffset
	m = sendKey(t, m, "V") // auto (wide, 160) -> side stays side? No: auto->side
	m = sendKey(t, m, "V") // side -> stacked, with offset clamping
	if m.layout != LayoutStacked {
		t.Fatalf("V toggle landed on %v, want stacked", m.layout)
	}
	lines := len(m.buildDetail(m.detailWidth()))
	_, maxOffset := m.detailContentBudget(lines)
	if m.dOffset > maxOffset {
		t.Fatalf("layout switch left offset %d above stacked max %d", m.dOffset, maxOffset)
	}
	if m.dOffset > before {
		t.Fatalf("layout switch grew offset from %d to %d", before, m.dOffset)
	}
}

func TestWindowResizeClampsDetailOffset(t *testing.T) {
	issues := []bd.Issue{{ID: "fm-long", Title: "Long", Status: "open"}}
	long := testDetailOf("fm-long")
	long.Description = strings.Repeat("word ", 600)
	f := &fakeClient{issues: map[bd.View][]bd.Issue{bd.ViewOpen: issues}, issue: long}
	m := drive(t, f)
	m.width, m.height = 160, 30
	m = applyMsg(t, m, detailMsg{id: "fm-long", generation: m.detailGen, issue: long, err: nil})
	m = sendKey(t, m, "enter")
	m = sendKey(t, m, "G")
	if m.dOffset <= 0 {
		t.Fatalf("detail did not scroll before resize: offset = %d", m.dOffset)
	}
	m = applyMsg(t, m, tea.WindowSizeMsg{Width: 100, Height: 12})
	lines := len(m.buildDetail(m.detailWidth()))
	_, maxOffset := m.detailContentBudget(lines)
	if m.dOffset > maxOffset {
		t.Fatalf("after shrink offset = %d, max = %d", m.dOffset, maxOffset)
	}
}

func TestRowsCapInlineTagsWithOverflowMarker(t *testing.T) {
	vocab := NewVocab(nil)
	issue := bd.Issue{
		ID: "fm-x", Title: "Task", Status: "open",
		Labels: []string{"alpha", "beta", "gamma", "delta", "epsilon"},
	}
	row := stripANSI(vocab.ListRow(issue, 120, false))
	if got := strings.Count(row, "["); got != maxInlineTags {
		t.Fatalf("row rendered %d inline labels, want %d: %q", got, maxInlineTags, row)
	}
	for _, want := range []string{"[alpha]", "[beta]", "+3"} {
		if !strings.Contains(row, want) {
			t.Errorf("row missing %q: %q", want, row)
		}
	}
	for _, hidden := range []string{"gamma", "delta", "epsilon"} {
		if strings.Contains(row, hidden) {
			t.Errorf("row leaked hidden label %q: %q", hidden, row)
		}
	}
}

func TestDetailShowsFullLabelSet(t *testing.T) {
	vocab := NewVocab(nil)
	detail := testDetail()
	detail.Labels = []string{"alpha", "beta", "gamma", "delta"}
	lines := strings.Join(buildDetail(vocab, detail, nil, nil, nil, nil, 80, nil), "\n")
	plain := stripANSI(lines)
	if !strings.Contains(plain, "Labels: alpha, beta, gamma, delta") {
		t.Fatalf("detail missing full label set: %q", plain)
	}
}

func TestSingleAndEmptyTagRowsStayClean(t *testing.T) {
	vocab := NewVocab(nil)
	one := stripANSI(vocab.ListRow(bd.Issue{ID: "fm-x", Title: "T", Status: "open", Labels: []string{"solo"}}, 60, false))
	if !strings.Contains(one, "[solo]") || strings.Contains(one, "+") {
		t.Fatalf("single-label row wrong: %q", one)
	}
	none := stripANSI(vocab.ListRow(bd.Issue{ID: "fm-x", Title: "T", Status: "open", Labels: []string{" ", ""}}, 60, false))
	if strings.Contains(none, "[") || strings.Contains(none, "+") {
		t.Fatalf("blank-label row rendered markers: %q", none)
	}
}

// colorCode extracts the ANSI code from a lipgloss terminal color.
func colorCode(tc lipgloss.TerminalColor) string {
	if c, ok := tc.(lipgloss.Color); ok {
		return string(c)
	}
	return ""
}

func TestPaletteFamiliesNeverCollide(t *testing.T) {
	vocab := NewVocab(nil)
	priority := map[string]string{}
	for p := 0; p <= 4; p++ {
		code := colorCode(priorityStyle(p).GetForeground())
		priority[code] = "P" + itoa(p)
	}
	statuses := []string{"open", "in_progress", "blocked", "deferred", "closed", "hooked"}
	status := map[string]string{}
	for _, name := range statuses {
		code := colorCode(vocab.statusStyle(name).GetForeground())
		if owner, clash := status[code]; clash {
			t.Fatalf("status %s shares color %s with %s", name, code, owner)
		}
		status[code] = name
	}
	// The hold aliases intentionally share one color, but that color must be
	// unique across the rest of the status family and the priority ramp.
	holdCode := colorCode(vocab.statusStyle("hold").GetForeground())
	for _, alias := range []string{"on_hold", "held", "pinned"} {
		if code := colorCode(vocab.statusStyle(alias).GetForeground()); code != holdCode {
			t.Fatalf("hold alias %s color = %s, want %s", alias, code, holdCode)
		}
	}
	if _, clash := status[holdCode]; clash {
		t.Fatalf("hold family color %s collides with %s", holdCode, status[holdCode])
	}
	status[holdCode] = "hold-family"
	// Categories are the fallback for custom statuses; they must stay inside
	// the status family and out of the priority ramp too.
	for cat, color := range statusColors {
		if owner, clash := priority[color]; clash {
			t.Fatalf("status category %s shares color %s with priority %s", cat, color, owner)
		}
	}
	// Priority levels must also be mutually distinct.
	if len(priority) != 5 {
		t.Fatalf("priority ramp collapsed: %v", priority)
	}
}

func TestErrorMarkersAreBoldRedWithDistinctGlyphs(t *testing.T) {
	if code := colorCode(styleError.GetForeground()); code != priorityP0 {
		t.Fatalf("error marker color = %s, want bold red %s", code, priorityP0)
	}
	if !styleError.GetBold() {
		t.Fatal("error marker style is not bold")
	}
	m := New(nil)
	m.width, m.height = 160, 30
	m.boardErr = "store unreachable"
	view := stripANSI(m.View())
	if !strings.Contains(view, "✗ Could not load board.") {
		t.Fatalf("board error missing ✗ glyph: %q", view)
	}
	graph := New(nil)
	graph.width, graph.height = 160, 30
	graph.rows = []bd.Issue{{ID: "a"}, {ID: "b"}}
	graph.allRows = graph.rows
	graph.deps = map[string][]bd.DepRecord{"a": {{ID: "b"}}, "b": {{ID: "a"}}}
	graph.selected = 0
	if !strings.Contains(stripANSI(graph.renderGraph()), "⚠ CYCLE") {
		t.Fatal("cycle marker lost ⚠ glyph")
	}
}

func TestHelpShowsPaletteLegend(t *testing.T) {
	m := New(nil)
	m.width, m.height = 160, 40
	legend := strings.Join(m.helpLines(72), "\n")
	plain := stripANSI(legend)
	for _, want := range []string{"Priority:", "P0", "P4", "Status:", "V cycles"} {
		if !strings.Contains(plain, want) {
			t.Errorf("help legend missing %q", want)
		}
	}
	m.help = true
	view := m.View()
	if !strings.Contains(stripANSI(view), "Priority:") {
		t.Fatal("rendered help does not show the palette legend")
	}
}
