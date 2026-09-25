package tui

import (
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/RooseveltAdvisors/beads-tui/internal/bd"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
	"github.com/muesli/termenv"
)

// The HUD decoration contract: the focused-row highlight spans the full row
// width (including padding and everything after styled chips), content
// columns stay aligned across rows, multi-select is cyan while focus is
// magenta, and every degraded colour tier keeps a static, drift-free row.

// Golden tint sequences under the TrueColor tier (see hudFocusTint/hudMarkTint
// mixed over #0d1116 with #f94dff / #38d9ff).
const (
	focusTintSeqGolden = "48;2;60;29;69"
	markTintSeqGolden  = "48;2;22;61;73"
)

func withColorProfile(t *testing.T, p termenv.Profile) {
	t.Helper()
	orig := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(p)
	t.Cleanup(func() { lipgloss.SetColorProfile(orig) })
}

// cellBackgrounds reports the background SGR active at every visible cell of
// a rendered row, exactly as a terminal applies it: "" means the cell carries
// no background at all.
func cellBackgrounds(s string) []string {
	var bgs []string
	bg := ""
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			j := i + 1
			if j < len(s) && s[j] == '[' {
				j++
				start := j
				for j < len(s) && (s[j] < 0x40 || s[j] > 0x7e) {
					j++
				}
				params := s[start:j]
				if j < len(s) {
					j++
				}
				bg = sgrBackground(params, bg)
				i = j
				continue
			}
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		for k := 0; k < runewidth.RuneWidth(r); k++ {
			bgs = append(bgs, bg)
		}
		i += size
	}
	return bgs
}

// sgrBackground resolves the background state after one SGR parameter list.
func sgrBackground(params, cur string) string {
	parts := strings.Split(params, ";")
	for i := 0; i < len(parts); i++ {
		p := parts[i]
		switch {
		case p == "" || p == "0" || p == "49":
			cur = ""
		case p == "48" || p == "38":
			bg := p == "48"
			if i+1 < len(parts) && parts[i+1] == "5" && i+2 < len(parts) {
				if bg {
					cur = "48;5;" + parts[i+2]
				}
				i += 2
			} else if i+1 < len(parts) && parts[i+1] == "2" && i+4 < len(parts) {
				if bg {
					cur = "48;2;" + strings.Join(parts[i+2:i+5], ";")
				}
				i += 4
			}
		default:
			if n, err := strconv.Atoi(p); err == nil && (40 <= n && n <= 47 || 100 <= n && n <= 107) {
				cur = "48;" + p
			}
		}
	}
	return cur
}

// assertFullyPainted checks that no cell of a decorated row is left on the
// bare terminal background (the historic bug: highlights died at the first
// inner SGR reset).
func assertFullyPainted(t *testing.T, row string) []string {
	t.Helper()
	bgs := cellBackgrounds(row)
	if len(bgs) != displayWidth(row) {
		t.Fatalf("cell accounting = %d cells, display width %d: %q", len(bgs), displayWidth(row), stripANSI(row))
	}
	for i, bg := range bgs {
		if bg == "" {
			t.Fatalf("cell %d of %d carries no background (highlight died mid-row): %q", i, len(bgs), stripANSI(row))
		}
	}
	return bgs
}

func TestListRowHighlightSpansFullWidth(t *testing.T) {
	withColorProfile(t, termenv.TrueColor)
	v := NewVocab(nil)
	// Chip-free rows let the exact tint cover every cell.
	issue := bd.Issue{ID: "fm-hl", Title: "Highlight spans the row", Status: "open", Priority: 1}
	for _, width := range []int{24, 120} {
		row := v.ListRowLiveDecor(issue, width, defaultListFields(), false, RowDecor{Focused: true})
		if got := displayWidth(row); got != width {
			t.Fatalf("width %d: focused row is %d cells wide, want exactly %d: %q", width, got, width, stripANSI(row))
		}
		bgs := assertFullyPainted(t, row)
		for i, bg := range bgs {
			if bg != focusTintSeqGolden {
				t.Fatalf("width %d: cell %d background = %q, want %q", width, i, bg, focusTintSeqGolden)
			}
		}
	}
}

func TestListRowHighlightSurvivesStyledSpans(t *testing.T) {
	withColorProfile(t, termenv.TrueColor)
	v := NewVocab(nil)
	issue := bd.Issue{
		ID: "fm-chips", Title: "Row with styled chips", Status: "blocked", Priority: 0,
		Assignee: "jr", DependencyCount: 2, DependentCount: 1, CommentCount: 4,
		Labels: []string{"infra"}, Repeat: "weekly",
	}
	row := v.ListRowLiveDecor(issue, 80, defaultListFields(), false, RowDecor{Focused: true})
	bgs := assertFullyPainted(t, row)
	// The trailing padding (after the last chip's reset) must keep the tint:
	// that is the part of the row a bare outer style always lost.
	if bgs[len(bgs)-1] != focusTintSeqGolden {
		t.Fatalf("trailing cell background = %q, want %q", bgs[len(bgs)-1], focusTintSeqGolden)
	}
	if bgs[0] != focusTintSeqGolden {
		t.Fatalf("leading cell background = %q, want %q", bgs[0], focusTintSeqGolden)
	}
}

func TestListRowHighlightKeepsColumnsAligned(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	withColorProfile(t, termenv.TrueColor)
	v := NewVocab(nil)
	rows := []bd.Issue{
		{ID: "fm-short", Title: "S", Status: "open"},
		{ID: "fm-long", Title: "A very long title that eats most of the row budget", Status: "open", CommentCount: 3},
	}
	width := 80
	for _, focused := range []bool{false, true} {
		offsets := make([]int, len(rows))
		for i, issue := range rows {
			row := v.ListRowLiveDecor(issue, width, defaultListFields(), false, RowDecor{Focused: focused})
			if got := displayWidth(row); got != width {
				t.Fatalf("focused=%v row %d width = %d, want %d", focused, i, got, width)
			}
			plain := stripANSI(row)
			byteIdx := strings.Index(plain, issue.ID)
			if byteIdx < 0 {
				t.Fatalf("focused=%v row %d lost its id: %q", focused, i, plain)
			}
			offsets[i] = displayWidth(plain[:byteIdx])
		}
		if offsets[0] != offsets[1] {
			t.Fatalf("focused=%v id columns drifted: %v", focused, offsets)
		}
	}
	// The counts column is right-aligned: identical badges sit at identical
	// offsets whether or not the row is highlighted.
	var offsets []int
	for _, focused := range []bool{false, true} {
		row := v.ListRowLiveDecor(rows[1], width, defaultListFields(), false, RowDecor{Focused: focused})
		plain := stripANSI(row)
		byteIdx := strings.Index(plain, "💬3")
		if byteIdx < 0 {
			t.Fatalf("focused=%v row lost its comment badge: %q", focused, plain)
		}
		offsets = append(offsets, displayWidth(plain[:byteIdx]))
	}
	if offsets[0] < 0 || offsets[0] != offsets[1] {
		t.Fatalf("comment badge column drifted between plain and focused rows: %v", offsets)
	}
}

func TestMultiSelectMarkersAreCyanAndFocusIsMagenta(t *testing.T) {
	withColorProfile(t, termenv.TrueColor)
	v := NewVocab(nil)
	issue := bd.Issue{ID: "fm-multi", Title: "Multi-select row", Status: "open"}

	marked := v.ListRowLiveDecor(issue, 60, defaultListFields(), false, RowDecor{Checks: true, Marked: true})
	if plain := stripANSI(marked); !strings.Contains(plain, markedBox) {
		t.Fatalf("marked row lost its %s marker: %q", markedBox, plain)
	}
	if !strings.Contains(marked, colorSeq(hudCyanHex, false)) {
		t.Fatalf("marked checkbox is not cyan: %q", marked)
	}
	if bgs := assertFullyPainted(t, marked); bgs[len(bgs)-1] != markTintSeqGolden {
		t.Fatalf("marked row tint = %q, want %q", bgs[len(bgs)-1], markTintSeqGolden)
	}

	focused := v.ListRowLiveDecor(issue, 60, defaultListFields(), false, RowDecor{Focused: true, Checks: true})
	if plain := stripANSI(focused); strings.Contains(plain, markedBox) {
		t.Fatalf("focus-only row shows a multi-select marker: %q", plain)
	}
	if !strings.Contains(focused, colorSeq(hudMagentaHex, false)) {
		t.Fatalf("focus bar is not magenta: %q", focused)
	}
	if bgs := assertFullyPainted(t, focused); bgs[len(bgs)-1] != focusTintSeqGolden {
		t.Fatalf("focus tint = %q, want %q", bgs[len(bgs)-1], focusTintSeqGolden)
	}

	// A row that is both focused and marked keeps both signals legible.
	both := v.ListRowLiveDecor(issue, 60, defaultListFields(), false, RowDecor{Focused: true, Checks: true, Marked: true})
	plain := stripANSI(both)
	if !strings.Contains(plain, markedBox) || !strings.Contains(plain, focusBar) {
		t.Fatalf("overlapping focus+multi-select row lost a signal: %q", plain)
	}
	if !strings.Contains(both, colorSeq(hudCyanHex, false)) || !strings.Contains(both, colorSeq(hudMagentaHex, false)) {
		t.Fatalf("overlapping row lost a color signal: %q", both)
	}

	// Visual mode marks unselected rows with the empty checkbox, never the bar.
	unmarked := v.ListRowLiveDecor(issue, 60, defaultListFields(), false, RowDecor{Checks: true})
	if plain := stripANSI(unmarked); !strings.Contains(plain, unmarkedBox) || strings.Contains(plain, markedBox) {
		t.Fatalf("unmarked visual row = %q", plain)
	}
	if focusTintSeqGolden == markTintSeqGolden {
		t.Fatal("focus and multi-select tints must differ")
	}
}

func TestNoColorRowsKeepGlyphsAndCarryNoEscapes(t *testing.T) {
	withColorProfile(t, termenv.Ascii)
	v := NewVocab(nil)
	row := v.ListRowLiveDecor(bd.Issue{ID: "fm-plain", Title: "No color row", Status: "open"},
		40, defaultListFields(), false, RowDecor{Focused: true, Checks: true, Marked: true})
	if strings.Contains(row, "\x1b") {
		t.Fatalf("no-color row emitted ANSI garbage: %q", row)
	}
	if got := displayWidth(row); got != 40 {
		t.Fatalf("no-color row width = %d, want 40: %q", got, row)
	}
	if !strings.Contains(row, focusBar) || !strings.Contains(row, markedBox) {
		t.Fatalf("no-color row lost its glyph affordances: %q", row)
	}
}

func TestStaticColorTiersGetFullRowHighlight(t *testing.T) {
	v := NewVocab(nil)
	issue := bd.Issue{ID: "fm-tier", Title: "Static tier row", Status: "open"}
	for _, profile := range []termenv.Profile{termenv.ANSI, termenv.ANSI256} {
		withColorProfile(t, profile)
		row := v.ListRowLiveDecor(issue, 40, defaultListFields(), false, RowDecor{Focused: true})
		if got := displayWidth(row); got != 40 {
			t.Fatalf("profile %v: row width = %d, want 40: %q", profile, got, stripANSI(row))
		}
		bgs := assertFullyPainted(t, row)
		for i, bg := range bgs {
			if bg != bgs[0] {
				t.Fatalf("profile %v: cell %d background = %q, want uniform %q", profile, i, bg, bgs[0])
			}
		}
	}
}

func TestFocusHighlightSpansRowsAcrossBoardModes(t *testing.T) {
	withColorProfile(t, termenv.TrueColor)
	f := &fakeClient{issues: map[bd.View][]bd.Issue{bd.ViewOpen: {
		{ID: "fm-a", Title: "Alpha row", Status: "open", Priority: 1, DependencyCount: 2, DependentCount: 1, CommentCount: 3},
		{ID: "fm-b", Title: "Beta row", Status: "blocked", Priority: 0},
		{ID: "fm-c", Title: "Gamma row", Status: "open", Labels: []string{"infra"}},
	}}}
	m := drive(t, f)
	m.smear = false
	m.width, m.height = 60, 20
	m.treeMode = true
	for _, tc := range []struct {
		name  string
		setup func(*Model)
	}{
		{"tree view", func(m *Model) { m.treeMode = true; m.filter = Filter{}; m.projectRows("") }},
		{"flat view", func(m *Model) { m.treeMode = false; m.filter = Filter{}; m.projectRows("") }},
		{"filtered view", func(m *Model) { m.treeMode = false; m.filter = ParseSearchFilter("row"); m.projectRows("") }},
		{"detail-pane focus", func(m *Model) { m.focus = FocusDetail }},
		{"visual multi-select", func(m *Model) { m.visualMode = true; m.visualAnchor = 0 }},
		{"narrow rows", func(m *Model) { m.width = 30 }},
	} {
		m.width, m.height = 60, 20
		m.visualMode = false
		m.focus = FocusList
		tc.setup(&m)
		m = m.clampSelection()
		// Render the full pane and locate the focused row line.
		paneLines := m.renderListPane(m.width, m.height-2)
		rowLine := ""
		for _, line := range paneLines {
			if strings.Contains(line, focusBar) {
				rowLine = line
				break
			}
		}
		if rowLine == "" {
			t.Fatalf("%s: no focused row marker rendered:\n%s", tc.name, strings.Join(paneLines, "\n"))
		}
		inner := strings.TrimSuffix(strings.TrimPrefix(rowLine, "│"), "│")
		if len([]rune(inner)) == 0 {
			t.Fatalf("%s: empty focused row line: %q", tc.name, rowLine)
		}
		bgs := assertFullyPainted(t, inner)
		if tc.name != "visual multi-select" && bgs[len(bgs)-1] != focusTintSeqGolden {
			t.Fatalf("%s: focused row does not carry the magenta tint to the last cell: %q", tc.name, bgs[len(bgs)-1])
		}
		if displayWidth(inner) != m.width-2 {
			t.Fatalf("%s: row inner width = %d, want %d", tc.name, displayWidth(inner), m.width-2)
		}
	}
}
