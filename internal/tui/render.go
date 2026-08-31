// Package tui renders the beads-tui board: a navigable list of beads with a
// detail pane showing issue detail, dependency edges, and the status
// vocabulary.
package tui

import (
	"log"
	"strconv"
	"strings"

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
	"in_progress": "yellow",
	"deferred":    "gray",
	"closed":      "gray",
	"pinned":      "magenta",
	"hooked":      "cyan",
}

var (
	styleDim      = lipgloss.NewStyle().Foreground(lipgloss.Color("gray"))
	styleBold     = lipgloss.NewStyle().Bold(true)
	styleSection  = lipgloss.NewStyle().Foreground(lipgloss.Color("cyan")).Bold(true)
	styleSelected = lipgloss.NewStyle().
			Background(lipgloss.Color("238")).
			Foreground(lipgloss.Color("252"))
)

// Vocab carries the status vocabulary into rendering: icon + category per
// status name, falling back to the built-in vocabulary when bd never
// answered.
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
			"open": "○", "in_progress": "◐", "blocked": "●",
			"deferred": "❄", "closed": "✓", "pinned": "📌", "hooked": "◇",
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
	return lipgloss.NewStyle().Foreground(lipgloss.Color(color))
}

// StatusPill renders "○ open" colored for the given status.
func (v Vocab) StatusPill(status string) string {
	return v.statusStyle(status).Render(v.Icon(status) + " " + status)
}

// ListRow renders one flat board row at the given width.
func (v Vocab) ListRow(issue bd.Issue, width int, selected bool) string {
	return v.renderRow(issue, "", "", width, selected)
}

// TreeRow renders one dependency-tree row, including its branch connector
// and expand/collapse marker.
func (v Vocab) TreeRow(row TreeRow, width int, selected bool) string {
	marker := "  "
	if row.HasChildren {
		marker = "▾ "
		if !row.Expanded {
			marker = "▸ "
		}
	}
	return v.renderRow(row.Issue, row.Prefix, marker, width, selected)
}

func (v Vocab) renderRow(issue bd.Issue, prefix, marker string, width int, selected bool) string {
	var b strings.Builder
	b.WriteString(prefix)
	b.WriteString(marker)
	icon := v.Icon(issue.Status)
	b.WriteString(icon)
	b.WriteString(" ")
	b.WriteString(formatPriority(issue.Priority))
	b.WriteString(" ")
	b.WriteString(issue.ID)
	if issue.Title != "" {
		b.WriteString(" ")
		b.WriteString(issue.Title)
	}
	tags := renderTags(issue.Labels)
	counts := ""
	if issue.DependencyCount > 0 {
		counts += " ⇣" + itoa(issue.DependencyCount)
	}
	if issue.DependentCount > 0 {
		counts += " ⇡" + itoa(issue.DependentCount)
	}
	line := b.String()
	if tags != "" && width > 4 {
		tagBudget := min(displayWidth(tags), max(3, width/2))
		line = truncatePhys(line, width-tagBudget-1) + " " + truncatePhys(tags, tagBudget)
	} else {
		line = truncatePhys(line, width)
	}
	if counts != "" {
		rest := width - runewidth.StringWidth(stripANSI(line))
		dw := displayWidth(counts)
		if rest > dw+2 {
			line = lipgloss.JoinHorizontal(lipgloss.Left, line,
				styleDim.Render(strings.Repeat(" ", rest-dw-1)+counts))
		}
	}
	if selected {
		line = truncatePhys(line, width-2)
		return styleSelected.Render("▸ " + line)
	}
	return line
}

var tagColors = []lipgloss.Color{"39", "141", "42", "208", "81", "177"}

// renderTags keeps labels compact while giving each tag a distinct accent.
func renderTags(labels []string) string {
	var tags []string
	for i, label := range labels {
		if strings.TrimSpace(label) == "" {
			continue
		}
		style := lipgloss.NewStyle().Foreground(tagColors[i%len(tagColors)]).Bold(true)
		tags = append(tags, style.Render("["+label+"]"))
	}
	return strings.Join(tags, " ")
}

// formatPriority renders the P0-P4 marker, emphasizing P0/P1.
func formatPriority(p int) string {
	s := "P" + itoa(p)
	switch p {
	case 0:
		return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("red")).Render(s)
	case 1:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("yellow")).Render(s)
	default:
		return styleDim.Render(s)
	}
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
	lines = append(lines, v.StatusPill(d.Status))
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
		lines = append(lines, styleSection.Render("Depends on ("+itoa(len(down))+")"))
		for _, dep := range down {
			lines = append(lines, depLine(v, dep, width, "↳ "))
		}
		lines = append(lines, "")
	}
	if len(up) > 0 {
		lines = append(lines, styleSection.Render("Dependents ("+itoa(len(up))+")"))
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
