package tui

import (
	"strings"
	"testing"

	"github.com/RooseveltAdvisors/beads-tui/internal/bd"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

func TestSortIssuesModes(t *testing.T) {
	issues := []bd.Issue{
		{ID: "c", Title: "Charlie", Priority: 2, CreatedAt: "2026-08-01T00:00:00Z", UpdatedAt: "2026-08-03T00:00:00Z", DependencyCount: 1, DependentCount: 3},
		{ID: "a", Title: "alpha", Priority: 0, CreatedAt: "2026-08-03T00:00:00Z", UpdatedAt: "2026-08-01T00:00:00Z", DependencyCount: 0, DependentCount: 1},
		{ID: "b", Title: "Bravo", Priority: 1, CreatedAt: "2026-08-02T00:00:00Z", UpdatedAt: "2026-08-04T00:00:00Z", DependencyCount: 2, DependentCount: 3},
	}
	for _, tc := range []struct {
		name string
		mode SortMode
		want []string
	}{
		{"priority", SortPriority, []string{"a", "b", "c"}},
		{"created newest first", SortCreated, []string{"a", "b", "c"}},
		{"updated newest first", SortUpdated, []string{"b", "c", "a"}},
		{"alphabetical", SortAlphabetical, []string{"a", "b", "c"}},
		{"dependencies with created tie-break", SortDependencies, []string{"b", "c", "a"}},
		{"depends with created tie-break", SortDependents, []string{"b", "c", "a"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := SortIssues(issues, tc.mode)
			for i, want := range tc.want {
				if got[i].ID != want {
					t.Fatalf("position %d = %q, want %q", i, got[i].ID, want)
				}
			}
		})
	}
	if issues[0].ID != "c" {
		t.Fatal("SortIssues mutated the backend snapshot")
	}
}

func TestFilterMatching(t *testing.T) {
	issues := []bd.Issue{
		{ID: "open", Title: "Write API docs", Description: "Explain the filter syntax", Status: "open", Priority: 1, Labels: []string{"docs", "ux"}},
		{ID: "blocked", Title: "Fix runner", Description: "Needs credentials", Status: "blocked", Priority: 2, Labels: []string{"infra"}},
		{ID: "closed", Title: "Ship it", Status: "closed", Priority: 0},
	}
	tests := []struct {
		input string
		want  []string
	}{
		{"status:blocked", []string{"blocked"}},
		{"priority:P1", []string{"open"}},
		{"label:UX", []string{"open"}},
		{"credentials", []string{"blocked"}},
		{"status:open", []string{"open"}},
	}
	for _, tc := range tests {
		got := FilterIssues(issues, ParseFilter(tc.input))
		if len(got) != len(tc.want) {
			t.Fatalf("%q matched %d rows, want %d", tc.input, len(got), len(tc.want))
		}
		for i, want := range tc.want {
			if got[i].ID != want {
				t.Errorf("%q match %d = %q, want %q", tc.input, i, got[i].ID, want)
			}
		}
	}
}

func TestSlashSearchParsesStructuredAndFreeTextQueries(t *testing.T) {
	if got := ParseSearchFilter("status:blocked"); got.Kind != FilterStatus || got.Query != "blocked" {
		t.Fatalf("structured slash search = %+v", got)
	}
	if got := ParseSearchFilter("fm-123"); got.Kind != FilterSearch || got.Query != "fm-123" {
		t.Fatalf("free-text slash search = %+v", got)
	}
	issues := []bd.Issue{{ID: "project-42", Title: "Unrelated title"}}
	if got := FilterIssues(issues, ParseSearchFilter("project")); len(got) != 1 {
		t.Fatalf("slash search omitted ID-only match: %+v", got)
	}
	if got := ParseSearchFilter("project:42"); got.Kind != FilterSearch {
		t.Fatalf("unknown colon query became structured filter: %+v", got)
	}
}

func TestNewKeyDispatchAndStatusBar(t *testing.T) {
	f := &fakeClient{issues: map[bd.View][]bd.Issue{bd.ViewOpen: {
		{ID: "fm-a", Title: "Alpha", Status: "open", Priority: 2, Labels: []string{"frontend"}},
		{ID: "fm-b", Title: "Beta", Status: "blocked", Priority: 1, Labels: []string{"backend"}},
	}}}
	m := drive(t, f)
	if m.sortMode != SortCreated {
		t.Fatalf("initial sort = %s, want created", m.sortMode)
	}
	m = sendKey(t, m, "s")
	if m.sortMode != SortUpdated || m.rows[0].ID != "fm-a" {
		t.Fatalf("s sort = %s, first row %q; want updated/fm-a", m.sortMode, m.rows[0].ID)
	}
	m = sendKey(t, m, "/")
	if !m.filtering {
		t.Fatal("/ did not open the search prompt")
	}
	m = sendKey(t, m, "status:blocked")
	m = sendKey(t, m, "enter")
	if m.filter.Kind != FilterStatus || len(m.rows) != 1 || m.rows[0].ID != "fm-b" {
		t.Fatalf("filter = %+v, rows = %+v", m.filter, m.rows)
	}
	m = sendKey(t, m, "esc")
	if m.filter.Active() || len(m.rows) != 2 {
		t.Fatalf("esc did not clear search: %+v rows=%d", m.filter, len(m.rows))
	}
	m = sendKey(t, m, "t")
	if m.filter.Kind != FilterLabel || m.filter.Query != "backend" || len(m.rows) != 1 {
		t.Fatalf("t filter = %+v, rows=%d", m.filter, len(m.rows))
	}
	view := stripANSI(m.View())
	for _, want := range []string{"sort:updated", "query:label:backend", "sel:fm-b", "total:1", "scroll:0%"} {
		if !strings.Contains(view, want) {
			t.Errorf("status bar missing %q: %s", want, view)
		}
	}
}

func TestResetKeyRestoresBoardDefaults(t *testing.T) {
	t.Setenv("BEADS_TUI_CONFIG_DIR", t.TempDir())
	m := drive(t, nil)
	m.view = bd.ViewClosed
	m.sortMode = SortDependents
	m.filter = ParseFilter("status:blocked")
	m = sendKey(t, m, "R")
	if m.view != bd.ViewOpen || m.sortMode != SortCreated || m.filter.Active() {
		t.Fatalf("R state = view:%s sort:%s filter:%s", m.view, m.sortMode, m.filter)
	}
}

func TestBoardStatePersistsAcrossModels(t *testing.T) {
	t.Setenv("BEADS_TUI_CONFIG_DIR", t.TempDir())
	f := &fakeClient{}
	m := newTestModel(f)
	m.view = bd.ViewClosed
	m.sortMode = SortDependencies
	m.filter = ParseFilter("priority:P1")
	m.saveState()

	reloaded := New(f)
	if reloaded.view != bd.ViewClosed || reloaded.sortMode != SortDependencies || reloaded.filter.String() != "priority:p1" {
		t.Fatalf("reloaded state = view:%s sort:%s filter:%s", reloaded.view, reloaded.sortMode, reloaded.filter)
	}
}

func TestBoardSnapshotLoadsAndReplacesAcrossModels(t *testing.T) {
	t.Setenv("BEADS_TUI_CONFIG_DIR", t.TempDir())
	f := &fakeClient{}
	m := newTestModel(f)
	cached := []bd.Issue{{ID: "cached", Title: "Cached board", Status: "open"}}
	m = applyMsg(t, m, boardMsg{view: bd.ViewOpen, generation: m.boardGen, issues: cached})

	reloaded := New(f)
	if len(reloaded.rows) != 1 || reloaded.rows[0].ID != "cached" || !reloaded.loading {
		t.Fatalf("reloaded snapshot = loading:%v rows:%+v", reloaded.loading, reloaded.rows)
	}

	live := []bd.Issue{{ID: "live", Title: "Live board", Status: "open"}}
	reloaded = applyMsg(t, reloaded, boardMsg{view: bd.ViewOpen, generation: reloaded.boardGen, issues: live})
	latest := New(f)
	if len(latest.rows) != 1 || latest.rows[0].ID != "live" {
		t.Fatalf("replaced snapshot rows = %+v", latest.rows)
	}
}

func TestListRowsRenderColoredLabels(t *testing.T) {
	row := NewVocab(nil).ListRow(bd.Issue{ID: "fm-x", Title: "T", Status: "open", Labels: []string{"frontend", "urgent"}}, 50, false)
	plain := stripANSI(row)
	if !strings.Contains(plain, "[frontend] [urgent]") {
		t.Fatalf("labels missing from row: %q", plain)
	}
}

func TestListRowsRenderTagsWithActiveRenderer(t *testing.T) {
	renderer := lipgloss.DefaultRenderer()
	previousProfile := renderer.ColorProfile()
	renderer.SetColorProfile(termenv.ANSI256)
	t.Cleanup(func() { renderer.SetColorProfile(previousProfile) })

	row := NewVocab(nil).ListRow(bd.Issue{
		ID: "fm-x", Title: "Task", Status: "open", Labels: []string{"ops"},
	}, 80, false)
	wantTag := renderer.NewStyle().Foreground(lipgloss.Color("245")).Render("[ops]")
	if !strings.Contains(row, wantTag) {
		t.Fatalf("tag was not rendered with the active color renderer: %q", row)
	}
}

func TestListRowsRenderNativeStatusColumn(t *testing.T) {
	vocab := NewVocab(nil)
	for _, issue := range []bd.Issue{
		{ID: "open", Title: "Open", Status: "open"},
		{ID: "progress", Title: "Progress", Status: "in_progress"},
		{ID: "closed", Title: "Closed", Status: "closed"},
		{ID: "deferred", Title: "Deferred", Status: "deferred", DeferUntil: "2026-09-04T12:00:00Z"},
	} {
		row := stripANSI(vocab.ListRow(issue, 80, false))
		if !strings.Contains(row, vocab.Icon(issue.Status)) {
			t.Errorf("row for %q missing native status glyph: %q", issue.ID, row)
		}
		if strings.Contains(row, "ready") {
			t.Errorf("row for %q rendered computed ready status: %q", issue.ID, row)
		}
		if issue.Status == "deferred" && !strings.Contains(row, "until 2026-09-04") {
			t.Errorf("deferred row missing until date: %q", row)
		}
	}
}

func TestRowsShowWorkStateInsteadOfComputedOpen(t *testing.T) {
	vocab := NewVocab(nil)
	claimable := stripANSI(vocab.ListRow(bd.Issue{ID: "claim", Title: "Claim me", Status: "open"}, 80, false))
	if !strings.Contains(claimable, "○") || strings.Contains(claimable, " open") {
		t.Fatalf("claimable Ready row should use only the hollow glyph: %q", claimable)
	}
	claimed := stripANSI(vocab.ListRow(bd.Issue{
		ID: "claimed", Title: "In flight", Status: "in_progress", Assignee: "ada",
	}, 80, false))
	if !strings.Contains(claimed, "●") || !strings.Contains(claimed, "· ada") || strings.Contains(claimed, "in_progress") {
		t.Fatalf("claimed Ready row lost real work state or owner: %q", claimed)
	}
	blocked := stripANSI(vocab.ListRow(bd.Issue{ID: "blocked", Title: "Blocked", Status: "blocked"}, 80, false))
	if !strings.Contains(blocked, "⊘") || !strings.Contains(blocked, "blocked") {
		t.Fatalf("blocked Ready row lost its visible state: %q", blocked)
	}
}

func TestYankOpensMenuWithIssueValues(t *testing.T) {
	f := &fakeClient{issues: map[bd.View][]bd.Issue{bd.ViewOpen: {
		{ID: "fm-yank", Title: "Copy this", Status: "open", URL: "https://example.test/fm-yank"},
	}}}
	m := drive(t, f)
	m = sendKey(t, m, "y")
	if !m.yank {
		t.Fatal("y did not open the yank menu")
	}
	view := stripANSI(m.View())
	for _, want := range []string{"Yank", "ID: fm-yank", "Title: Copy this", "URL: https://example.test/fm-yank"} {
		if !strings.Contains(view, want) {
			t.Errorf("yank menu missing %q: %s", want, view)
		}
	}
	m = sendKey(t, m, "esc")
	if m.yank {
		t.Fatal("esc did not close the yank menu")
	}
}

func TestBuiltInStatusGlyphsAndPriorityColors(t *testing.T) {
	vocab := NewVocab([]bd.StatusInfo{
		{Name: "open", Icon: "custom-open", Category: "active"},
		{Name: "in_progress", Icon: "custom-progress", Category: "wip"},
		{Name: "blocked", Icon: "custom-blocked", Category: "wip"},
		{Name: "closed", Icon: "custom-closed", Category: "done"},
		{Name: "deferred", Icon: "custom-deferred", Category: "frozen"},
	})
	for status, want := range map[string]string{
		"open": "○", "in_progress": "●", "blocked": "⊘", "closed": "✓", "deferred": "◷", "hold": "📌",
	} {
		if got := vocab.Icon(status); got != want {
			t.Errorf("%s glyph = %q, want %q", status, got, want)
		}
	}
	for priority, want := range map[int]lipgloss.TerminalColor{
		0: lipgloss.Color("196"), 1: lipgloss.Color("208"), 2: lipgloss.Color("220"), 3: lipgloss.Color("39"),
	} {
		if got := priorityStyle(priority).GetForeground(); got != want {
			t.Errorf("P%d color = %v, want %v", priority, got, want)
		}
	}
}

func TestHelpLegendUsesRenderedWorkStateGlyphs(t *testing.T) {
	m := New(nil)
	legend := strings.Join(m.helpLines(80), "\n")
	for status, label := range map[string]string{
		"open": "open", "in_progress": "in_progress", "blocked": "blocked",
		"closed": "closed", "deferred": "deferred", "hold": "hold",
	} {
		want := m.vocab.Icon(status) + " " + label
		if !strings.Contains(legend, want) {
			t.Errorf("help legend missing rendered state %q", want)
		}
	}
}

func TestListRowsRenderNativeStatusAtStandardPaneWidth(t *testing.T) {
	vocab := NewVocab(nil)
	for _, issue := range []bd.Issue{
		{ID: "open", Title: "Open", Status: "open"},
		{ID: "progress", Title: "Progress", Status: "in_progress"},
		{ID: "closed", Title: "Closed", Status: "closed"},
		{ID: "deferred", Title: "Deferred", Status: "deferred", DeferUntil: "2026-09-04T12:00:00Z"},
	} {
		row := stripANSI(vocab.ListRow(issue, 38, false))
		if !strings.Contains(row, vocab.Icon(issue.Status)) {
			t.Errorf("row for %q missing native status glyph at standard width: %q", issue.ID, row)
		}
		if displayWidth(row) > 38 {
			t.Errorf("row for %q overflowed standard width: %q", issue.ID, row)
		}
	}
}

func TestClosedNativeStatusIsFaint(t *testing.T) {
	vocab := NewVocab(nil)
	if !vocab.statusStyle("closed").GetFaint() {
		t.Fatal("closed native status style is not faint")
	}
	if got := vocab.statusStyle("blocked").GetForeground(); got != lipgloss.Color("196") {
		t.Fatalf("blocked native status color = %v, want red 196", got)
	}
	if got := vocab.statusStyle("in_progress").GetForeground(); got != lipgloss.Color("39") {
		t.Fatalf("in_progress native status color = %v, want vibrant 39", got)
	}
	if vocab.statusStyle("open").GetFaint() {
		t.Fatal("open native status style is unexpectedly faint")
	}
}

func TestLongNativeStatusPreservesDependencyDirections(t *testing.T) {
	vocab := NewVocab([]bd.StatusInfo{{Name: "awaiting_external_approval", Icon: "○", Category: "active"}})
	row := vocab.ListRow(bd.Issue{
		ID: "fm-x", Title: strings.Repeat("long title ", 4), Status: "awaiting_external_approval",
		Priority: 1, DependencyCount: 123, DependentCount: 456,
	}, 40, false)
	plain := stripANSI(row)
	if displayWidth(row) > 40 {
		t.Fatalf("custom-status row overflowed: %q", plain)
	}
	for _, want := range []string{"awaiting", "⇣123", "⇡456"} {
		if !strings.Contains(plain, want) {
			t.Errorf("custom-status row missing %q: %q", want, plain)
		}
	}
}

func TestStatusTabNames(t *testing.T) {
	m := New(nil)
	m.width, m.height = 80, 24
	view := stripANSI(m.renderTabs())
	for _, want := range []string{"[1]open", "[2]in_progress", "[3]blocked", "[4]closed", "[5]deferred"} {
		if !strings.Contains(view, want) {
			t.Fatalf("tabs missing status label %q: %q", want, view)
		}
	}
}

func TestRequiredFooterFieldsSurviveNarrowWidths(t *testing.T) {
	m := New(nil)
	m.rows = []bd.Issue{{ID: "fm-long-selected-id"}}
	m.filter = ParseFilter("label:extraordinarily-long-label")
	for _, width := range []int{80, 48} {
		footer := stripANSI(m.renderFooter(width))
		if displayWidth(footer) > width {
			t.Fatalf("width %d footer overflowed: %q", width, footer)
		}
		for _, field := range []string{"view:", "sort:", "query:", "sel:", "total:"} {
			if !strings.Contains(footer, field) {
				t.Errorf("width %d footer missing %q: %q", width, field, footer)
			}
		}
	}
}

func TestFooterReportsLoadedGraphEdges(t *testing.T) {
	m := New(nil)
	m.graphEdges = 1
	footer := stripANSI(m.renderFooter(120))
	if !strings.Contains(footer, "graph:1 edges") {
		t.Fatalf("footer omitted loaded graph evidence: %q", footer)
	}
}

func TestCompactFooterPreservesFallbackFields(t *testing.T) {
	m := New(nil)
	m.rows = []bd.Issue{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	m.selected = 1
	m.filter = ParseFilter("status:open")
	footer := stripANSI(m.renderFooter(40))
	for _, want := range []string{"open", "query:on", "50%"} {
		if !strings.Contains(footer, want) {
			t.Errorf("compact footer missing %q: %q", want, footer)
		}
	}
	if displayWidth(footer) > 40 {
		t.Fatalf("compact footer overflowed: %q", footer)
	}
}

func TestHelpWrapsEveryBindingIntoVisiblePane(t *testing.T) {
	m := New(nil)
	m.width, m.height = 80, 24
	view := stripANSI(m.renderHelp())
	for _, want := range []string{
		"j/k", "↑/↓", "g/G", "space/PgDn/Ctrl+F", "b/PgUp/Ctrl+B",
		"enter/l/→", "h/←", "1 open", "2 in_progress", "3 blocked", "4 closed", "5 deferred", "s cycle",
		"esc close detail / clear search", "Search:", "Enter apply", "status:open",
		"priority:P1", "label:frontend", "t search", "r ·", "Reset: R", "Help: ?", "q/Ctrl+C",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("wrapped help missing %q: %s", want, view)
		}
	}
	for i, line := range strings.Split(view, "\n") {
		if displayWidth(line) > 80 {
			t.Errorf("help line %d overflowed: %q", i, line)
		}
	}
}

func TestNarrowHelpScrollMakesEveryBindingReachable(t *testing.T) {
	m := New(nil)
	m.width, m.height = 40, 10
	m = sendKey(t, m, "?")
	var seen strings.Builder
	for i := 0; i <= m.helpMaxOffset(); i++ {
		seen.WriteString(stripANSI(m.View()))
		seen.WriteByte('\n')
		m = sendKey(t, m, "j")
	}
	all := strings.Join(strings.Fields(seen.String()), " ")
	for _, want := range []string{
		"esc close detail /", "clear search", "space/PgDn/Ctrl+F", "label:frontend", "q/Ctrl+C", "Read-only:",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("scrollable help never exposed %q", want)
		}
	}
	if !m.help || m.helpOffset != m.helpMaxOffset() {
		t.Fatalf("j closed or overscrolled help: open=%v offset=%d max=%d", m.help, m.helpOffset, m.helpMaxOffset())
	}
	m = sendKey(t, m, "k")
	if !m.help || m.helpOffset != m.helpMaxOffset()-1 {
		t.Fatalf("k did not scroll help upward: open=%v offset=%d", m.help, m.helpOffset)
	}
}

func TestFilteredEmptyStateNamesActiveFilter(t *testing.T) {
	m := New(nil)
	m.allRows = []bd.Issue{{ID: "fm-a", Status: "open"}}
	m.filter = ParseFilter("status:closed")
	m.rebuildRows("")
	got := stripANSI(strings.Join(m.renderListPane(60, 8), "\n"))
	if !strings.Contains(got, "No matches for search: status:closed") {
		t.Fatalf("filtered empty state = %q", got)
	}
}

func TestPriorityFilterAcceptsP4(t *testing.T) {
	issues := []bd.Issue{{ID: "p4", Priority: 4}, {ID: "p3", Priority: 3}}
	got := FilterIssues(issues, ParseFilter("priority:P4"))
	if len(got) != 1 || got[0].ID != "p4" {
		t.Fatalf("priority:P4 matched %+v", got)
	}
}

func TestTruncatedRowKeepsTagStyleAndSpace(t *testing.T) {
	row := NewVocab(nil).ListRow(bd.Issue{
		ID: "fm-x", Title: strings.Repeat("long title ", 8), Status: "open", Labels: []string{"frontend"},
	}, 32, false)
	if displayWidth(row) > 32 || !strings.Contains(stripANSI(row), "[f") {
		t.Fatalf("truncated row lost reserved tag: %q", stripANSI(row))
	}
	tag := strings.Index(row, "[f")
	if tag < 0 || !strings.Contains(row[:tag], "\x1b[") {
		t.Fatalf("truncated tag lost ANSI style: %q", row)
	}
}

func TestTruncatedTaggedRowsReserveDependencyMarkers(t *testing.T) {
	vocab := NewVocab(nil)
	issue := bd.Issue{
		ID: "fm-x", Title: strings.Repeat("long title ", 8), Status: "blocked", Priority: 1,
		Labels: []string{"frontend"}, DependencyCount: 12, DependentCount: 3,
	}
	for _, selected := range []bool{false, true} {
		row := vocab.ListRow(issue, 36, selected)
		plain := stripANSI(row)
		if displayWidth(row) > 36 {
			t.Fatalf("selected=%v row overflowed: %q", selected, plain)
		}
		for _, want := range []string{vocab.Icon("blocked"), "P1", "⇣12", "⇡3"} {
			if !strings.Contains(plain, want) {
				t.Errorf("selected=%v row missing reserved %q: %q", selected, want, plain)
			}
		}
		if !strings.HasSuffix(plain, "⇣12 ⇡3") {
			t.Errorf("selected=%v markers are not right-edge content: %q", selected, plain)
		}
	}
}

func TestNarrowRowsCompressButRetainDependencyCounts(t *testing.T) {
	vocab := NewVocab(nil)
	issue := bd.Issue{
		ID: "fm-x", Title: strings.Repeat("long title ", 4), Status: "blocked", Priority: 1,
		Labels: []string{"frontend"}, DependencyCount: 123, DependentCount: 456,
	}
	for _, selected := range []bool{false, true} {
		width := 10
		row := vocab.ListRow(issue, width, selected)
		plain := stripANSI(row)
		if displayWidth(row) > width {
			t.Fatalf("selected=%v narrow row overflowed: %q", selected, plain)
		}
		for _, want := range []string{vocab.Icon("blocked"), "P1", "1", "/", "4"} {
			if !strings.Contains(plain, want) {
				t.Errorf("selected=%v narrow row lost reserved %q: %q", selected, want, plain)
			}
		}
		if selected && !strings.Contains(plain, "1/4") {
			t.Errorf("selected narrow row did not preserve compact digits: %q", plain)
		}
		if strings.Contains(plain, "frontend") {
			t.Errorf("selected=%v labels did not collapse first: %q", selected, plain)
		}
	}
}

func TestSelectedNarrowRowCompactsWideStatusIcon(t *testing.T) {
	row := NewVocab(nil).ListRow(bd.Issue{
		Status: "pinned", Priority: 1, DependencyCount: 123, DependentCount: 456,
	}, 10, true)
	plain := stripANSI(row)
	if displayWidth(row) > 10 {
		t.Fatalf("wide-icon row overflowed: %q", plain)
	}
	for _, want := range []string{"p", "P1", "1/4"} {
		if !strings.Contains(plain, want) {
			t.Errorf("wide-icon row missing %q: %q", want, plain)
		}
	}
}

func TestCustomWideStatusUsesSingleCellFallback(t *testing.T) {
	vocab := NewVocab([]bd.StatusInfo{{Name: "待处理", Icon: "📌", Category: "wip"}})
	row := vocab.ListRow(bd.Issue{
		Status: "待处理", Priority: 1, DependencyCount: 123, DependentCount: 456,
	}, 10, true)
	plain := stripANSI(row)
	if displayWidth(row) > 10 {
		t.Fatalf("custom-status row overflowed: %q", plain)
	}
	for _, want := range []string{"•", "P1", "1/4"} {
		if !strings.Contains(plain, want) {
			t.Errorf("custom-status row missing %q: %q", want, plain)
		}
	}
}

func TestDeepTreePrefixPreservesEdgesAndDetailCounts(t *testing.T) {
	vocab := NewVocab(nil)
	issue := bd.Issue{ID: "deep", Status: "open", Priority: 1, DependencyCount: 123, DependentCount: 456}
	row := vocab.TreeRow(TreeRow{Issue: issue, Prefix: strings.Repeat("│   ", 8)}, 14, true)
	plain := stripANSI(row)
	if displayWidth(row) > 14 {
		t.Fatalf("deep tree row overflowed: %q", plain)
	}
	for _, want := range []string{"…", "○", "P1", "1/4"} {
		if !strings.Contains(plain, want) {
			t.Errorf("deep tree row missing %q: %q", want, plain)
		}
	}
	detail := stripANSI(strings.Join(BuildDetail(vocab, &issue, nil, nil, 60), "\n"))
	if !strings.Contains(detail, "Depends 123 · Dependents 456") {
		t.Errorf("detail pane lost full dependency counts: %q", detail)
	}
}
