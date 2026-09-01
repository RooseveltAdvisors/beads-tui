// Package tui renders the beads-tui board: a navigable list of beads with a
// detail pane showing issue detail, dependency edges, and the status
// vocabulary.
package tui

import (
	"log"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/RooseveltAdvisors/beads-tui/internal/bd"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

// statusColors maps a status category to a terminal color name so custom
// statuses inherit a sensible color from their category. The vocabulary shape
// comes from `bd statuses --json collision-free` colors.
var statusColors = map[string]string{
	"active": "green",
	"wip":    "yellow",
	"frozen": "gray",
	"done":   "blue",
}

// statusOverrides overrides colors per status name (blocked must read as
// urgent, closed as finished, pinned as sticky).
var statusOverrides = map[string]string{
	"blocked":     "red",
	"in_progress": "39",
	"deferred":    "208",
	"closed":      "gray",
	"hold":        "magenta",
	"on_hold":     "magenta",
	"held":        "magenta",
	"pinned":      "magenta",
	"hooked":      "cyan",
}

var workStateIcons = map[string]string{
	"open":        "○",
	"in_progress": "●",
	"blocked":     "⊘",
	"closed":      "✓",
	"deferred":    "◷",
	"hold":        "📌",
	"on_hold":     "📌",
	"pinned":      "📌",
}

var (
	styleDim      = lipgloss.NewStyle().Foreground(lipgloss.Color("gray"))
	styleBold     = lipgloss.NewStyle().Bold(true)
	styleSection  = lipgloss.NewStyle().Foreground(lipgloss.Color("cyan")).Bold(true)
	styleSelected = lipgloss.NewStyle().
			Background(lipgloss.Color("238")).
			Foreground(lipgloss.Color("252"))
)

func viewStyle(view bd.View) lipgloss.Style {
	color := "39"
	switch view {
	case bd.ViewOpen:
		color = "42"
	case bd.ViewAll:
		color = "141"
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color(color))
}

// Vocab carries status categories and custom icons into rendering, falling
// back to the built-in vocabulary when bd never answered. Core work-state
// icons are fixed by Icon so rows and the help legend cannot diverge.
type Vocab struct {
	icons map[string]string
	cats  map[string]string
}

// markdownRenderer owns the width-specific Glamour renderer used by a model.
// The TUI renders detail content for both scrolling and painting, so reusing
// this renderer avoids rebuilding it on every keypress and frame.
type markdownRenderer struct {
	renderer *glamour.TermRenderer
	width    int
	err      error
}

func (r *markdownRenderer) render(markdown string, width int) []string {
	markdown = strings.TrimSpace(markdown)
	if markdown == "" {
		return nil
	}
	if width < 1 {
		width = 1
	}
	if r.width != width {
		if r.renderer != nil {
			if err := r.renderer.Close(); err != nil {
				log.Printf("tui: close markdown renderer: %v", err)
			}
		}
		r.renderer, r.err, r.width = nil, nil, width
		r.renderer, r.err = glamour.NewTermRenderer(glamour.WithWordWrap(width))
		if r.err != nil {
			log.Printf("tui: initialize markdown renderer: %v", r.err)
		}
	}
	if r.err != nil {
		return wrapText(markdown, width)
	}
	rendered, err := r.renderer.Render(markdown)
	if err != nil {
		log.Printf("tui: render markdown: %v", err)
		return wrapText(markdown, width)
	}
	rendered = strings.TrimRight(rendered, "\n")
	if rendered == "" {
		return nil
	}
	lines := strings.Split(rendered, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " \r")
	}
	return lines
}

// NewVocab builds a Vocab from bd's status list.
func NewVocab(statuses []bd.StatusInfo) Vocab {
	v := Vocab{icons: map[string]string{}, cats: map[string]string{}}
	if len(statuses) == 0 {
		v.icons = map[string]string{
			"open": "○", "in_progress": "●", "blocked": "⊘",
			"deferred": "◷", "closed": "✓", "hold": "📌", "on_hold": "📌", "pinned": "📌", "hooked": "◇",
		}
		v.cats = map[string]string{
			"open": "active", "in_progress": "wip", "blocked": "wip",
			"deferred": "frozen", "closed": "done", "pinned": "frozen", "hooked": "wip",
		}
	}
	for _, s := range statuses {
		v.icons[s.Name] = s.Icon
		v.cats[s.Name] = s.Category
	}
	return v
}

// Icon returns the glyph for a status name.
func (v Vocab) Icon(status string) string {
	// Keep the high-signal built-in work states visually consistent even when
	// bd supplies a custom icon in its live vocabulary.
	normalized := strings.ToLower(strings.TrimSpace(status))
	if normalized == "" {
		normalized = "open"
	}
	if icon, ok := workStateIcons[normalized]; ok {
		return icon
	}
	if icon, ok := v.icons[status]; ok {
		return icon
	}
	return "○"
}

// Category returns the category for a status name.
func (v Vocab) Category(status string) string {
	if cat, ok := v.cats[status]; ok {
		return cat
	}
	return "active"
}

// statusStyle returns the lipgloss style for a status name.
func (v Vocab) statusStyle(status string) lipgloss.Style {
	cat := v.Category(status)
	color := statusColors[cat]
	if c, ok := statusOverrides[status]; ok {
		color = c
	}
	style := lipgloss.NewStyle().Foreground(lipgloss.Color(color))
	if strings.EqualFold(strings.TrimSpace(status), "closed") {
		style = style.Faint(true)
	}
	return style
}

// StatusPill renders "○ open" colored for the given status.
func (v Vocab) StatusPill(status string) string {
	return v.statusStyle(status).Render(v.Icon(status) + " " + status)
}

// StatusPillIssue keeps deferred timing visible in the detail header while
// retaining the native status color.
func (v Vocab) StatusPillIssue(issue bd.Issue) string {
	return v.statusStyle(issue.Status).Render(v.Icon(issue.Status) + " " + rowStatusText(issue))
}

// ListRow renders one flat board row at the given width.
func (v Vocab) ListRow(issue bd.Issue, width int, selected bool) string {
	return v.renderRow(issue, "", "", width, selected, false)
}

// ReadyRow renders a row in the computed Ready view. Claimable open beads
// intentionally have no text status; their hollow glyph is the status signal.
func (v Vocab) ReadyRow(issue bd.Issue, width int, selected bool) string {
	return v.renderRow(issue, "", "", width, selected, true)
}

// TreeRow renders one dependency-tree row, including its branch connector
// and expand/collapse marker.
func (v Vocab) TreeRow(row TreeRow, width int, selected bool) string {
	return v.treeRow(row, width, selected, false)
}

// ReadyTreeRow is the Ready-view variant of TreeRow.
func (v Vocab) ReadyTreeRow(row TreeRow, width int, selected bool) string {
	return v.treeRow(row, width, selected, true)
}

func (v Vocab) treeRow(row TreeRow, width int, selected, readyView bool) string {
	marker := "  "
	if row.HasChildren {
		marker = "▾ "
		if !row.Expanded {
			marker = "▸ "
		}
	}
	return v.renderRow(row.Issue, row.Prefix, marker, width, selected, readyView)
}

func (v Vocab) renderRow(issue bd.Issue, treePrefix, marker string, width int, selected, readyView bool) string {
	usable := width
	if selected {
		usable -= 2
	}
	if usable < 1 {
		usable = 1
	}
	icon := v.Icon(issue.Status)
	counts := ""
	compactCounts := ""
	if issue.DependencyCount > 0 {
		counts += "⇣" + itoa(issue.DependencyCount)
		compactCounts += itoa(issue.DependencyCount)
	}
	if issue.DependentCount > 0 {
		if counts != "" {
			counts += " "
			compactCounts += "/"
		}
		counts += "⇡" + itoa(issue.DependentCount)
		compactCounts += itoa(issue.DependentCount)
	}
	if width >= 48 && counts != "" {
		counts = dependencyChips(issue)
	}
	reservedCounts := 0
	compactCountReserve := 0
	if issue.DependencyCount > 0 || issue.DependentCount > 0 {
		reservedCounts = displayWidth(counts) + 1
		compactCountReserve = 2
		if issue.DependencyCount > 0 && issue.DependentCount > 0 {
			compactCountReserve = 4
		}
	}
	corePrefix := marker + v.statusStyle(issue.Status).Render(icon) + " " + formatPriority(issue.Priority)
	treePrefix = truncate(treePrefix, max(0, usable-displayWidth(corePrefix)-compactCountReserve))
	statusBudget := max(0, usable-displayWidth(treePrefix)-displayWidth(corePrefix)-reservedCounts-1)
	status := compactRowStatus(issue, statusBudget, readyView)
	prefixWithStatus := func() string {
		prefix := treePrefix + marker + v.statusStyle(issue.Status).Render(icon) + " " + formatPriority(issue.Priority)
		if status != "" {
			prefix += " " + v.statusStyle(issue.Status).Render(status)
		}
		return prefix
	}
	prefix := prefixWithStatus()
	if issue.DependencyCount > 0 && issue.DependentCount > 0 && displayWidth(icon) > 1 && usable-displayWidth(prefix)-1 < 3 {
		icon = compactStatusIcon(issue.Status)
		prefix = prefixWithStatus()
	}

	var body strings.Builder
	if issue.ID != "" {
		body.WriteString(" ")
		body.WriteString(issue.ID)
	}
	if issue.Title != "" {
		body.WriteString(" ")
		body.WriteString(issue.Title)
	}
	tags := renderTags(issue.Labels)

	// Status and priority stay present; extreme rows reduce wide status icons
	// to one cell before compressing count digits for both dependency directions.
	minimumBody := displayWidth(prefix)
	fullCountBudget := displayWidth(counts)
	if counts != "" && usable-fullCountBudget-1 < minimumBody && displayWidth(compactCounts) < fullCountBudget {
		counts = compactCounts
	}
	countWidth := displayWidth(counts)
	prefixWidth := displayWidth(prefix)
	if counts != "" && countWidth+1 > usable-prefixWidth {
		countBudget := max(0, usable-prefixWidth-1)
		line := prefix
		if compact := compactDependencyCounts(issue.DependencyCount, issue.DependentCount, countBudget); compact != "" {
			line += " " + styleDim.Render(compact)
		}
		if selected {
			return styleSelected.Render("▸ " + line)
		}
		return line
	}
	contentBudget := usable
	if counts != "" {
		contentBudget -= countWidth + 1
	}
	if contentBudget <= 0 {
		line := styleDim.Render(strings.Repeat(" ", max(0, usable-countWidth)) + truncate(counts, usable))
		if selected {
			return styleSelected.Render("▸ " + line)
		}
		return line
	}

	line := prefix + body.String()
	maxTagBudget := contentBudget - minimumBody - 1
	if tags != "" && maxTagBudget >= 3 {
		tagBudget := min(displayWidth(tags), maxTagBudget)
		line = truncatePhys(line, contentBudget-tagBudget-1) + " " + truncatePhys(tags, tagBudget)
	} else {
		line = truncatePhys(line, contentBudget)
	}
	if counts != "" {
		line = padRight(line, contentBudget) + " " + styleDim.Render(counts)
	}
	if selected {
		return styleSelected.Render("▸ " + line)
	}
	return line
}

// rowStatus renders the native bd status. Ready is deliberately never used
// here: it is a computed board view, not an issue status.
func rowStatusText(issue bd.Issue) string {
	status := strings.TrimSpace(issue.Status)
	if status == "" {
		return ""
	}
	if strings.EqualFold(status, "deferred") && strings.TrimSpace(issue.DeferUntil) != "" {
		until := strings.TrimSpace(issue.DeferUntil)
		if date, _, ok := strings.Cut(until, "T"); ok {
			until = date
		}
		status += " until " + until
	}
	return status
}

func compactRowStatus(issue bd.Issue, width int, readyView ...bool) string {
	if width <= 0 {
		return ""
	}
	ready := len(readyView) > 0 && readyView[0]
	status := rowStatusTextForView(issue, ready)
	if displayWidth(status) <= width {
		return status
	}
	short := map[string]string{
		"open": "open", "in_progress": "prog", "closed": "clsd", "deferred": "defr",
	}[strings.ToLower(strings.TrimSpace(issue.Status))]
	if short != "" && displayWidth(short) <= width {
		return short
	}
	if width < 2 {
		return ""
	}
	return truncate(status, width)
}

func rowStatusTextForView(issue bd.Issue, readyView bool) string {
	status := strings.TrimSpace(issue.Status)
	if readyView && strings.EqualFold(status, "open") {
		return ""
	}
	if readyView && strings.EqualFold(status, "in_progress") {
		owner := strings.TrimSpace(issue.Assignee)
		if owner == "" {
			owner = strings.TrimSpace(issue.Owner)
		}
		if owner != "" {
			return status + " · " + owner
		}
	}
	return rowStatusText(issue)
}

func compactDependencyCounts(down, up, width int) string {
	if width <= 0 {
		return ""
	}
	if down > 0 && up > 0 && width >= 3 {
		leftWidth := (width - 1) / 2
		rightWidth := width - 1 - leftWidth
		return truncateDigits(itoa(down), leftWidth) + "/" + truncateDigits(itoa(up), rightWidth)
	}
	value := down
	if value == 0 {
		value = up
	}
	return truncateDigits(itoa(value), width)
}

func dependencyChips(issue bd.Issue) string {
	parts := make([]string, 0, 2)
	if issue.DependencyCount > 0 {
		parts = append(parts, "⇣"+itoa(issue.DependencyCount)+" blocked-by")
	}
	if issue.DependentCount > 0 {
		parts = append(parts, "⇡"+itoa(issue.DependentCount)+" blocks")
	}
	return strings.Join(parts, "  ")
}

func truncateDigits(value string, width int) string {
	return runewidth.Truncate(value, width, "")
}

func compactStatusIcon(status string) string {
	if status != "" {
		r, _ := utf8.DecodeRuneInString(status)
		if runewidth.RuneWidth(r) == 1 {
			return string(r)
		}
	}
	return "•"
}

// renderTags keeps labels compact and deliberately low-contrast: labels are
// useful metadata, while status and priority carry the visual meaning.
func renderTags(labels []string) string {
	var tags []string
	for _, label := range labels {
		if strings.TrimSpace(label) == "" {
			continue
		}
		tags = append(tags, styleDim.Render("["+label+"]"))
	}
	return strings.Join(tags, " ")
}

func priorityStyle(p int) lipgloss.Style {
	switch p {
	case 0:
		return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("red"))
	case 1:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("208"))
	case 2:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("yellow"))
	default:
		return styleDim
	}
}

// formatPriority renders the P0-P4 marker with a clear urgency scale.
func formatPriority(p int) string {
	return priorityStyle(p).Render("P" + itoa(p))
}

// BuildDetail renders the detail pane for a bead as wrapped, optionally
// truncated lines. Every line fits `width` cells.
func BuildDetail(v Vocab, d *bd.Issue, down, up []bd.DepRecord, width int) []string {
	return buildDetail(v, d, down, up, width, nil)
}

func buildDetail(v Vocab, d *bd.Issue, down, up []bd.DepRecord, width int, markdown *markdownRenderer) []string {
	if d == nil {
		return []string{styleDim.Render("No selection.")}
	}
	var lines []string
	lines = append(lines, v.StatusPillIssue(*d))
	if d.Title != "" {
		lines = append(lines, styleBold.Render(d.Title))
	}
	meta := "ID " + d.ID + "  ·  " + formatPriority(d.Priority)
	if d.IssueType != "" {
		meta += "  ·  " + d.IssueType
	}
	lines = append(lines, styleDim.Render(meta))
	lines = append(lines, styleDim.Render("Assignee: "+orDash(d.Assignee)))
	if d.CreatedAt != "" {
		lines = append(lines, styleDim.Render("Created: "+d.CreatedAt+"   Updated: "+orDash(d.UpdatedAt)))
	}
	counts := "Depends " + itoa(d.DependencyCount) + " · Dependents " + itoa(d.DependentCount) + " · Comments " + itoa(d.CommentCount)
	lines = append(lines, styleDim.Render(counts))
	lines = append(lines, "")

	if d.Description != "" {
		lines = append(lines, styleSection.Render("Description"))
		if markdown == nil {
			markdown = &markdownRenderer{}
		}
		lines = append(lines, markdown.render(d.Description, width)...)
		lines = append(lines, "")
	}
	if d.Notes != "" {
		lines = append(lines, styleSection.Render("Notes"))
		lines = append(lines, wrapText(strings.TrimSpace(d.Notes), width)...)
		lines = append(lines, "")
	}
	if len(down) > 0 {
		lines = append(lines, styleSection.Render("Depends on ("+itoa(len(down))+") · blocked-by"))
		for _, dep := range down {
			lines = append(lines, depLine(v, dep, width, "↳ "))
		}
		lines = append(lines, "")
	}
	if len(up) > 0 {
		lines = append(lines, styleSection.Render("Dependents ("+itoa(len(up))+") · blocks"))
		for _, dep := range up {
			lines = append(lines, depLine(v, dep, width, "↳ "))
		}
	}
	return lines
}

// depLine renders one dependency edge row.
func depLine(v Vocab, dep bd.DepRecord, width int, prefix string) string {
	var b strings.Builder
	b.WriteString(prefix)
	b.WriteString(v.Icon(dep.Status))
	b.WriteString(" ")
	b.WriteString(dep.ID)
	if dep.Title != "" {
		b.WriteString(" ")
		b.WriteString(dep.Title)
	}
	suffix := ""
	if dep.DependencyType != "" {
		suffix = " [" + dep.DependencyType + "]"
	}
	line := b.String()
	if suffix != "" {
		line = truncate(line, width-displayWidth(suffix))
		line += styleDim.Render(suffix)
	} else {
		line = truncate(line, width)
	}
	return v.statusStyle(dep.Status).Render(line)
}

// wrapText wraps s to the given cell width, breaking words that are longer
// than the width. Blank handling keeps paragraphs intact.
func wrapText(s string, width int) []string {
	if width <= 0 {
		return []string{s}
	}
	var lines []string
	for _, para := range strings.Split(s, "\n") {
		if para == "" {
			lines = append(lines, "")
			continue
		}
		words := strings.Fields(para)
		var cur strings.Builder
		curW := 0
		flush := func() {
			if cur.Len() > 0 {
				lines = append(lines, cur.String())
				cur.Reset()
				curW = 0
			}
		}
		for _, word := range words {
			w := runewidth.StringWidth(word)
			if w > width {
				// Hard-break the word itself.
				flush()
				lines = append(lines, hardBreak(word, width)...)
				continue
			}
			if curW > 0 && curW+1+w > width {
				flush()
			}
			if curW > 0 {
				cur.WriteString(" ")
				curW++
			}
			cur.WriteString(word)
			curW += w
		}
		flush()
	}
	return lines
}

// hardBreak splits a word into width-sized pieces.
func hardBreak(word string, width int) []string {
	var pieces []string
	runes := []rune(word)
	for len(runes) > 0 {
		take := 0
		w := 0
		for take < len(runes) {
			rw := runewidth.RuneWidth(runes[take])
			if w+rw > width {
				break
			}
			w += rw
			take++
		}
		if take == 0 {
			take = 1
		}
		pieces = append(pieces, string(runes[:take]))
		runes = runes[take:]
	}
	return pieces
}

// truncate cuts s to width cells, replacing the tail with an ellipsis.
func truncate(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if runewidth.StringWidth(s) <= width {
		return s
	}
	w := width - 1
	if w <= 0 {
		return "…"
	}
	return runewidth.Truncate(s, w, "") + "…"
}

// stripANSI removes ANSI escape sequences (test helper and width math).
func stripANSI(s string) string {
	var b strings.Builder
	esc := false
	for _, r := range s {
		if r == '\x1b' {
			esc = true
			continue
		}
		if esc {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				esc = false
			}
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func itoa(n int) string {
	return strconv.Itoa(n)
}
