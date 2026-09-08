// Package tui renders the beads-tui board: a navigable list of beads with a
// detail pane showing issue detail, dependency edges, and the status
// vocabulary.
package tui

import (
	"log"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/RooseveltAdvisors/beads-tui/internal/bd"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

// The semantic palette is defined once, here. Each family owns disjoint ANSI
// color codes so meaning is never ambiguous:
//
//   - Priority P0..P4 use a red -> orange -> yellow -> blue -> gray ramp and
//     are rendered ONLY for the priority glyph.
//   - Statuses use a separate green/cyan/magenta/purple/dim family, rendered
//     ONLY for status glyphs, pills, and tabs.
//   - Cycle and error markers keep bold red but are always paired with their
//     own glyphs (⚠ for cycles, ✗ for errors), so red never reads as P0.
const (
	recurringIcon = "↻"

	priorityP0 = "196" // red
	priorityP1 = "208" // orange
	priorityP2 = "220" // yellow
	priorityP3 = "39"  // blue
	priorityP4 = "245" // gray

	statusOpen       = "35"  // green
	statusInProgress = "45"  // cyan
	statusBlocked    = "201" // magenta
	statusDeferred   = "141" // purple
	statusClosed     = "240" // dim
	statusHold       = "164" // pink
	statusHooked     = "43"  // teal
)

// statusColors maps a status category to a terminal color so custom statuses
// inherit a sensible color from their category. The vocabulary shape comes
// from `bd statuses --json collision-free` colors.
var statusColors = map[string]string{
	"active": statusOpen,
	"wip":    statusInProgress,
	"frozen": statusDeferred,
	"done":   statusClosed,
}

// statusOverrides overrides colors per status name (blocked must read as
// stalled, closed as finished, pinned as sticky).
var statusOverrides = map[string]string{
	"blocked":     statusBlocked,
	"in_progress": statusInProgress,
	"deferred":    statusDeferred,
	"closed":      statusClosed,
	"hold":        statusHold,
	"on_hold":     statusHold,
	"held":        statusHold,
	"pinned":      statusHold,
	"hooked":      statusHooked,
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
	styleDim           = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	styleBold          = lipgloss.NewStyle().Bold(true)
	styleSection       = lipgloss.NewStyle().Foreground(lipgloss.Color("cyan")).Bold(true)
	styleCommentAuthor = lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Bold(true)
	styleCommentBadge  = lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Bold(true)
	styleSelected      = lipgloss.NewStyle().
				Background(lipgloss.Color("238"))
)

func viewStyle(view bd.View) lipgloss.Style {
	color := statusInProgress
	switch strings.ToLower(string(view)) {
	case "in_progress":
		color = statusInProgress
	case "blocked":
		color = statusBlocked
	case "closed":
		color = statusClosed
	case "deferred":
		color = statusDeferred
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

// ListFields is the persisted set of optional board-row fields. Title stays
// on because a task list without it is not useful.
type ListFields struct {
	Status       bool `json:"status"`
	Priority     bool `json:"priority"`
	ID           bool `json:"id"`
	Assignee     bool `json:"assignee"`
	Comments     bool `json:"comments"`
	Recurrence   bool `json:"recurrence"`
	Labels       bool `json:"labels"`
	Dependencies bool `json:"dependencies"`
}

func defaultListFields() ListFields {
	return ListFields{Status: true, Priority: true, ID: true, Assignee: true, Comments: true, Recurrence: true, Dependencies: true}
}

type DetailFields struct {
	Metadata    bool `json:"metadata"`
	Description bool `json:"description"`
	Notes       bool `json:"notes"`
	Relations   bool `json:"relations"`
	Comments    bool `json:"comments"`
}

func defaultDetailFields() DetailFields {
	return DetailFields{Metadata: true, Description: true, Notes: true, Relations: true, Comments: true}
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
		name := strings.ToLower(strings.TrimSpace(s.Name))
		if name == "" {
			continue
		}
		v.icons[name] = s.Icon
		v.cats[name] = strings.ToLower(strings.TrimSpace(s.Category))
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
	if icon, ok := v.icons[normalized]; ok && strings.TrimSpace(icon) != "" {
		return icon
	}
	return "○"
}

// Category returns the category for a status name.
func (v Vocab) Category(status string) string {
	if cat, ok := v.cats[strings.ToLower(strings.TrimSpace(status))]; ok {
		return cat
	}
	return "active"
}

// statusStyle returns the lipgloss style for a status name.
func (v Vocab) statusStyle(status string) lipgloss.Style {
	cat := v.Category(status)
	color := statusColors[cat]
	if c, ok := statusOverrides[strings.ToLower(strings.TrimSpace(status))]; ok {
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
	fields := defaultListFields()
	fields.Labels = true // compatibility for callers that explicitly render a row
	return v.ListRowWith(issue, width, selected, fields)
}

func (v Vocab) ListRowWith(issue bd.Issue, width int, selected bool, fields ListFields) string {
	return v.renderRow(issue, "", "", "", width, selected, fields)
}

// TreeRow renders one dependency-tree row, including its branch connector,
// expand/collapse marker, and depth-cap marker.
func (v Vocab) TreeRow(row TreeRow, width int, selected bool) string {
	fields := defaultListFields()
	fields.Labels = true
	return v.TreeRowWith(row, width, selected, fields)
}

func (v Vocab) TreeRowWith(row TreeRow, width int, selected bool, fields ListFields) string {
	deeper := ""
	if row.Deeper > 0 {
		deeper = styleDim.Render("+" + itoa(row.Deeper) + " deeper")
	}
	return v.renderRow(row.Issue, row.Prefix, v.treeMarker(row), deeper, width, selected, fields)
}

// treeMarker picks the expand/collapse glyph. A row carrying hidden deeper
// levels is not foldable, so it shows a continuation marker instead.
func (v Vocab) treeMarker(row TreeRow) string {
	if row.Deeper > 0 {
		return "⋯ "
	}
	if !row.HasChildren {
		return "  "
	}
	if row.Expanded {
		return "▾ "
	}
	return "▸ "
}

func (v Vocab) renderRow(issue bd.Issue, treePrefix, marker, suffix string, width int, selected bool, fields ListFields) string {
	usable := width
	if selected {
		usable -= 2
	}
	if usable < 1 {
		usable = 1
	}
	icon := ""
	if fields.Status {
		icon = v.Icon(issue.Status)
	}
	if fields.Recurrence && issue.IsRecurring() {
		icon += recurringIcon
	}
	counts := ""
	if fields.Dependencies && issue.DependencyCount > 0 {
		counts += "⇣" + itoa(issue.DependencyCount)
	}
	if fields.Dependencies && issue.DependentCount > 0 {
		if counts != "" {
			counts += " "
		}
		counts += "⇡" + itoa(issue.DependentCount)
	}
	commentBadge := ""
	if fields.Comments {
		commentBadge = commentBadgeText(issue.CommentCount)
	}
	if width >= 48 && counts != "" {
		counts = dependencyChips(issue)
	}
	// The depth-cap marker rides in the right-hand metadata slot.
	if suffix != "" {
		if counts != "" {
			counts += "  "
		}
		counts += suffix
	}
	if commentBadge != "" {
		if counts != "" {
			counts += "  "
		}
		counts += styleCommentBadge.Render(commentBadge)
	}
	metadataIssue := issue
	if !fields.Dependencies {
		metadataIssue.DependencyCount, metadataIssue.DependentCount = 0, 0
	}
	if !fields.Comments {
		metadataIssue.CommentCount = 0
	}
	compactCounts := compactRowMetadata(metadataIssue, displayWidth(counts))
	reservedCounts := 0
	compactCountReserve := 0
	if fields.Dependencies && (issue.DependencyCount > 0 || issue.DependentCount > 0) {
		reservedCounts = displayWidth(counts) + 1
		compactCountReserve = 2
		if issue.DependencyCount > 0 && issue.DependentCount > 0 {
			compactCountReserve = 4
		}
	}
	if commentBadge != "" {
		compactCountReserve += displayWidth(commentBadge) + 1
	}
	core := func() string {
		var parts []string
		if icon != "" {
			parts = append(parts, v.statusStyle(issue.Status).Render(icon))
		}
		if fields.Priority {
			parts = append(parts, formatPriority(issue.Priority))
		}
		return marker + strings.Join(parts, " ")
	}
	corePrefix := core()
	treePrefix = truncate(treePrefix, max(0, usable-displayWidth(corePrefix)-compactCountReserve))
	statusBudget := max(0, usable-displayWidth(treePrefix)-displayWidth(corePrefix)-reservedCounts-1)
	if fields.Assignee && strings.TrimSpace(issue.Assignee) != "" {
		// Keep ownership recognizable without letting a long agent id consume
		// the title column.
		statusBudget = min(statusBudget, min(18, max(6, usable/4)))
	}
	status := compactRowStatus(issue, statusBudget, fields.Assignee)
	prefixWithStatus := func() string {
		prefix := treePrefix + core()
		if status != "" {
			prefix += " " + v.statusStyle(issue.Status).Render(status)
		}
		return prefix
	}
	prefix := prefixWithStatus()
	if fields.Dependencies && issue.DependencyCount > 0 && issue.DependentCount > 0 && displayWidth(icon) > 1 && usable-displayWidth(prefix)-1 < 3 {
		icon = compactStatusIcon(issue.Status)
		prefix = prefixWithStatus()
	}

	var body strings.Builder
	if fields.ID && issue.ID != "" {
		body.WriteString(" ")
		body.WriteString(issue.ID)
	}
	if issue.Title != "" {
		body.WriteString(" ")
		body.WriteString(issue.Title)
	}
	tags := ""
	if fields.Labels {
		tags = renderTags(issue.Labels)
	}

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
		if compact := compactRowMetadata(metadataIssue, countBudget); compact != "" {
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

// commentBadgeText is deliberately kept in the row metadata slot so it stays
// right-aligned beside dependency counts without requiring another bd call.
// TERM=dumb is the conventional terminal signal that Unicode glyphs should
// not be used; C1 keeps the count legible in that mode.
func commentBadgeText(count int) string {
	if count <= 0 {
		return ""
	}
	if strings.EqualFold(strings.TrimSpace(os.Getenv("TERM")), "dumb") {
		return "C" + itoa(count)
	}
	return "💬" + itoa(count)
}

func compactRowMetadata(issue bd.Issue, width int) string {
	if width <= 0 {
		return ""
	}
	badge := commentBadgeText(issue.CommentCount)
	if badge == "" {
		return compactDependencyCounts(issue.DependencyCount, issue.DependentCount, width)
	}
	if issue.DependencyCount == 0 && issue.DependentCount == 0 {
		if displayWidth(badge) <= width {
			return badge
		}
		return ""
	}
	dependencyWidth := width - displayWidth(badge) - 1
	dependency := compactDependencyCounts(issue.DependencyCount, issue.DependentCount, dependencyWidth)
	if dependency == "" {
		if displayWidth(badge) <= width {
			return badge
		}
		return ""
	}
	return dependency + " " + badge
}

// rowStatus renders the bd status.
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

func compactRowStatus(issue bd.Issue, width int, showAssignee bool) string {
	if width <= 0 {
		return ""
	}
	status := rowStatusTextForView(issue, showAssignee)
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

func rowStatusTextForView(issue bd.Issue, showAssignee bool) string {
	status := strings.TrimSpace(issue.Status)
	owner := ""
	if showAssignee {
		// Assignee is the canonical owning agent. Owner may identify a delegated
		// worker, so it is intentionally never used in task rows.
		owner = strings.TrimSpace(issue.Assignee)
	}
	switch strings.ToLower(status) {
	case "open", "blocked", "closed":
		if owner != "" {
			return "· " + owner
		}
		return ""
	case "in_progress":
		if owner != "" {
			return "· " + owner
		}
		return ""
	case "deferred":
		if until := strings.TrimSpace(issue.DeferUntil); until != "" {
			if date, _, ok := strings.Cut(until, "T"); ok {
				until = date
			}
			if owner != "" {
				return "until " + until + " · " + owner
			}
			return "until " + until
		}
		if owner != "" {
			return "· " + owner
		}
		return ""
	}
	if owner != "" {
		return status + " · " + owner
	}
	return status
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

// maxInlineTags caps how many labels a board row renders inline; the rest
// collapse into a "+N" marker and the full set lives in the detail pane.
const maxInlineTags = 2

// renderTags keeps labels compact and deliberately low-contrast: at most two
// dim labels render inline after the title, overflow becomes "+N", and the
// detail pane carries the full set. Labels are metadata; status and priority
// carry the visual meaning.
func renderTags(labels []string) string {
	var tags []string
	overflow := 0
	for _, label := range labels {
		label = strings.TrimSpace(label)
		if label == "" {
			continue
		}
		if len(tags) < maxInlineTags {
			tags = append(tags, styleDim.Render("["+label+"]"))
		} else {
			overflow++
		}
	}
	if overflow > 0 {
		tags = append(tags, styleDim.Render("+"+itoa(overflow)))
	}
	return strings.Join(tags, " ")
}

func priorityStyle(p int) lipgloss.Style {
	switch p {
	case 0:
		return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(priorityP0))
	case 1:
		return lipgloss.NewStyle().Foreground(lipgloss.Color(priorityP1))
	case 2:
		return lipgloss.NewStyle().Foreground(lipgloss.Color(priorityP2))
	case 3:
		return lipgloss.NewStyle().Foreground(lipgloss.Color(priorityP3))
	default:
		return lipgloss.NewStyle().Foreground(lipgloss.Color(priorityP4))
	}
}

// styleError marks load failures and cycle warnings. It shares P0's red by
// design but is always paired with a dedicated glyph (✗ or ⚠).
var styleError = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(priorityP0))

// formatPriority renders the P0-P4 marker with a clear urgency scale.
func formatPriority(p int) string {
	return priorityStyle(p).Render("P" + itoa(p))
}

// BuildDetail renders the detail pane for a bead as wrapped, optionally
// truncated lines. Every line fits `width` cells.
func BuildDetail(v Vocab, d *bd.Issue, down, up []bd.DepRecord, width int) []string {
	return buildDetail(v, d, down, up, nil, nil, width, nil)
}

func buildDetail(v Vocab, d *bd.Issue, down, up []bd.DepRecord, chain, children []bd.Issue, width int, markdown *markdownRenderer) []string {
	return buildDetailWithComments(v, d, down, up, chain, children, width, markdown, nil, false, "", 0, 0)
}

func buildDetailWithComments(v Vocab, d *bd.Issue, down, up []bd.DepRecord, chain, children []bd.Issue, width int, markdown *markdownRenderer, comments []bd.Comment, commentsLoading bool, commentsErr string, commentCount, inlineCommentsMaxLines int) []string {
	return buildDetailVisible(v, d, down, up, chain, children, width, markdown, comments, commentsLoading, commentsErr, commentCount, inlineCommentsMaxLines, defaultDetailFields())
}

func buildDetailVisible(v Vocab, d *bd.Issue, down, up []bd.DepRecord, chain, children []bd.Issue, width int, markdown *markdownRenderer, comments []bd.Comment, commentsLoading bool, commentsErr string, commentCount, inlineCommentsMaxLines int, fields DetailFields) []string {
	if d == nil {
		return []string{styleDim.Render("No selection.")}
	}
	var lines []string
	lines = append(lines, v.StatusPillIssue(*d))
	if d.Title != "" {
		lines = append(lines, styleBold.Render(d.Title))
	}
	if fields.Metadata {
		meta := "ID " + d.ID + "  ·  " + formatPriority(d.Priority)
		if d.IssueType != "" {
			meta += "  ·  " + d.IssueType
		}
		lines = append(lines, styleDim.Render(meta))
		lines = append(lines, styleDim.Render("Assignee: "+orDash(d.Assignee)))
		if len(d.Labels) > 0 {
			lines = append(lines, styleDim.Render("Labels: "+strings.Join(d.Labels, ", ")))
		}
	}
	if fields.Relations && len(chain) > 0 {
		parts := make([]string, 0, len(chain))
		for _, ancestor := range chain {
			parts = append(parts, ancestor.ID)
		}
		lines = append(lines, styleDim.Render(truncate("Path: "+strings.Join(parts, " › "), width)))
	}
	if fields.Metadata && d.CreatedAt != "" {
		lines = append(lines, styleDim.Render("Created: "+d.CreatedAt+"   Updated: "+orDash(d.UpdatedAt)))
	}
	dependencyCount := d.DependencyCount
	dependentCount := d.DependentCount
	if len(down) > dependencyCount {
		dependencyCount = len(down)
	}
	if len(up) > dependentCount {
		dependentCount = len(up)
	}
	if fields.Relations || fields.Comments {
		counts := ""
		if fields.Relations {
			counts = "Depends " + itoa(dependencyCount) + " · Dependents " + itoa(dependentCount)
		}
		if fields.Comments {
			if counts != "" {
				counts += " · "
			}
			counts += "Comments " + itoa(d.CommentCount)
		}
		lines = append(lines, styleDim.Render(counts))
	}
	if fields.Comments {
		lines = append(lines, inlineCommentLines(comments, commentsLoading, commentsErr, commentCount, width, inlineCommentsMaxLines)...)
	}
	lines = append(lines, "")

	if fields.Description && d.Description != "" {
		lines = append(lines, styleSection.Render("Description"))
		if markdown == nil {
			markdown = &markdownRenderer{}
		}
		lines = append(lines, markdown.render(d.Description, width)...)
		lines = append(lines, "")
	}
	if fields.Notes && d.Notes != "" {
		lines = append(lines, styleSection.Render("Notes"))
		lines = append(lines, wrapText(strings.TrimSpace(d.Notes), width)...)
		lines = append(lines, "")
	}
	if fields.Relations && len(down) > 0 {
		lines = append(lines, styleSection.Render("Depends on ("+itoa(len(down))+") · blocked-by"))
		for _, dep := range down {
			lines = append(lines, depLine(v, dep, width, "↳ "))
		}
		lines = append(lines, "")
	}
	if fields.Relations && len(children) > 0 {
		lines = append(lines, styleSection.Render("Children ("+itoa(len(children))+")"))
		for _, child := range children {
			lines = append(lines, depLine(v, bd.DepRecord{
				ID:        child.ID,
				Title:     child.Title,
				Status:    child.Status,
				Priority:  child.Priority,
				IssueType: child.IssueType,
			}, width, "↳ "))
		}
	}
	if fields.Relations && len(up) > 0 {
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
