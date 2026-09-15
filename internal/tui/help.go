package tui

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/RooseveltAdvisors/beads-tui/internal/bd"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Help follows herdr's keybind help spirit:
//   - named groups (lowercase)
//   - key column width = max key width in the filtered set
//   - bold keys, plain labels
//   - / filters by key or label
//   - scrollable modal body

type helpEntry struct {
	Key   string
	Label string
}

type helpGroup struct {
	Name    string
	Entries []helpEntry
}

func (m Model) helpGroups() []helpGroup {
	var views []helpEntry
	for i, view := range m.views {
		if i == 9 {
			break
		}
		views = append(views, helpEntry{fmt.Sprintf("%d", i+1), view.Label()})
	}

	return []helpGroup{
		{
			Name: "global",
			Entries: []helpEntry{
				{"?", "this help"},
				{"q  Ctrl+C", "quit"},
				{"Esc", "back / clear filter / leave detail"},
				{"r", "reload board (keep view/sort/search)"},
				{"R", "reset to ready + defaults"},
				{"y", "yank menu (id · title · url)"},
				{"a", "attach to assignee's live Herdr or tmux session"},
			},
		},
		{
			Name: "navigation",
			Entries: []helpEntry{
				{"j  k   ↑ ↓", "move selection"},
				{"g", "top of list"},
				{"G", "bottom · or open dependency graph"},
				{"space  PgDn", "page down"},
				{"b  PgUp", "page up"},
				{"Ctrl-d  Ctrl-u", "half-page down / up"},
			},
		},
		{
			Name: "tree / panes",
			Entries: []helpEntry{
				{"enter  tab", "toggle fold"},
				{"h  ←", "collapse · or leave detail"},
				{"l  →", "unfold · or open detail"},
				{"L", "focus detail pane"},
				{"*", "expand all folds"},
				{"v", "flat list ↔ tree"},
				{"V", "layout: side · stacked · auto"},
			},
		},
		{
			Name:    "views",
			Entries: views,
		},
		{
			Name: "sort / filter",
			Entries: []helpEntry{
				{"s", "sort: created · updated · alpha · deps · depends · priority"},
				{"t", "filter to selected bead's labels"},
				{"o", "view options (row/detail fields)"},
				{"/", "search prompt (board) · filter this help when open"},
			},
		},
		{
			Name: "search syntax",
			Entries: []helpEntry{
				{"Enter", "apply board filter"},
				{"Tab", "accept completion"},
				{"↑ ↓", "cycle completions"},
				{"spaces", "AND"},
				{"|", "OR"},
				{"!", "NOT"},
				{"status:open", "by status"},
				{"priority:P1", "by priority"},
				{"label:x", "by label"},
				{"assignee:pi", "by assignee"},
				{"overdue", "past due"},
				{"recurring", "has repeat"},
				{"comments:true", "has comments"},
				{"text:word", "free text"},
			},
		},
		{
			Name: "comments",
			Entries: []helpEntry{
				{"c", "open comment thread"},
				{"C", "open thread + focus composer"},
				{"a", "add comment (in thread)"},
				{"Enter", "submit comment"},
				{"Esc  q", "back to board"},
			},
		},
		{
			Name: "crud",
			Entries: []helpEntry{
				{"n", "new issue (title; due +7d)"},
				{"e", "edit bead in $EDITOR / vim (save applies)"},
				{"x", "close with reason"},
				{"D", "delete forever (y / n)"},
				{"Enter", "commit field / create / close"},
				{"Esc", "cancel CRUD"},
			},
		},
		{
			Name: "audit",
			Entries: []helpEntry{
				{"tui-lineage", "comment stamped on every write"},
				{"actor=", "BEADS_ACTOR (agents) or git/$USER (you)"},
				{"bd --actor", "same identity on events"},
			},
		},
		{
			Name: "legend",
			Entries: []helpEntry{
				{m.vocab.Icon("open") + " open", "row status"},
				{m.vocab.Icon("in_progress") + " in_progress", "row status"},
				{m.vocab.Icon("blocked") + " blocked", "row status"},
				{m.vocab.Icon("closed") + " closed", "row status"},
				{m.vocab.Icon("deferred") + " deferred", "row status"},
				{m.vocab.Icon("hold") + " hold", "row status"},
				{"●", "assignee has a live Herdr/tmux session"},
				{"↻", "recurring"},
				{"⚠", "overdue"},
				{"⇣N", "blocked-by N"},
				{"⇡N", "blocks N"},
				{"P0…P4", "priority (P0 highest)"},
			},
		},
	}
}

func filterHelpGroups(groups []helpGroup, query string) []helpGroup {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return groups
	}
	var out []helpGroup
	for _, g := range groups {
		var entries []helpEntry
		if strings.Contains(strings.ToLower(g.Name), q) {
			entries = append(entries, g.Entries...)
		} else {
			for _, e := range g.Entries {
				if strings.Contains(strings.ToLower(e.Key), q) || strings.Contains(strings.ToLower(e.Label), q) {
					entries = append(entries, e)
				}
			}
		}
		if len(entries) > 0 {
			out = append(out, helpGroup{Name: g.Name, Entries: entries})
		}
	}
	return out
}

func helpKeyWidth(groups []helpGroup) int {
	w := 8
	for _, g := range groups {
		for _, e := range g.Entries {
			if n := displayWidth(e.Key); n > w {
				w = n
			}
		}
	}
	if w > 22 {
		w = 22
	}
	return w
}

func padHelpKey(key string, width int) string {
	n := displayWidth(key)
	if n >= width {
		return key
	}
	return key + strings.Repeat(" ", width-n)
}

func (m Model) helpLines(width int) []string {
	groups := filterHelpGroups(m.helpGroups(), m.helpFilter)
	inner := max(1, width-2)

	keyStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("183")).Bold(true) // mauve-ish
	headStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("117")).Bold(true) // accent
	labelStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("244"))

	if len(groups) == 0 {
		msg := dimStyle.Render(" no matching keybinds")
		return []string{msg}
	}

	kw := helpKeyWidth(groups)
	var lines []string

	title := styleBold.Render("beads-tui keybinds")
	lines = append(lines, title)
	if m.helpFilterActive {
		lines = append(lines, keyStyle.Render(" / ")+styleBold.Render(m.helpFilter))
	} else {
		lines = append(lines, dimStyle.Render(" press / to filter by command or shortcut · esc closes"))
	}
	lines = append(lines, "")

	for _, g := range groups {
		lines = append(lines, headStyle.Render(" "+g.Name))
		for _, e := range g.Entries {
			row := keyStyle.Render(" "+padHelpKey(e.Key, kw)+" ") + labelStyle.Render(e.Label)
			// wrap long labels under the key column
			for _, wline := range wrapHelpRow(row, e.Key, e.Label, kw, inner, keyStyle, labelStyle) {
				lines = append(lines, wline)
			}
		}
		lines = append(lines, "")
	}

	// Priority pills on their own line at end when unfiltered
	if strings.TrimSpace(m.helpFilter) == "" {
		lines = append(lines, "Priority: "+strings.Join([]string{
			priorityStyle(0).Render("P0"), priorityStyle(1).Render("P1"),
			priorityStyle(2).Render("P2"), priorityStyle(3).Render("P3"),
			priorityStyle(4).Render("P4"),
		}, " "))
		lines = append(lines, "Status:   "+strings.Join([]string{
			m.vocab.StatusPill("open"), m.vocab.StatusPill("in_progress"),
			m.vocab.StatusPill("blocked"), m.vocab.StatusPill("closed"),
			m.vocab.StatusPill("deferred"), m.vocab.StatusPill("hold"),
		}, " "))
	}

	_ = bd.ViewReady // keep import if views empty edge - actually unused; leave vocab
	return lines
}

func wrapHelpRow(full, key, label string, kw, inner int, keyStyle, labelStyle lipgloss.Style) []string {
	if displayWidth(stripANSI(full)) <= inner {
		return []string{full}
	}
	// first line: key + as much label as fits
	prefix := " " + padHelpKey(key, kw) + " "
	prefixW := displayWidth(prefix)
	budget := max(8, inner-prefixW)
	var out []string
	rest := label
	first := true
	for rest != "" {
		chunk, next := takeWidth(rest, budget)
		if first {
			out = append(out, keyStyle.Render(prefix)+labelStyle.Render(chunk))
			first = false
			budget = max(8, inner-prefixW)
			rest = next
			continue
		}
		out = append(out, strings.Repeat(" ", prefixW)+labelStyle.Render(chunk))
		rest = next
	}
	return out
}

func takeWidth(s string, width int) (chunk, rest string) {
	if width < 1 {
		width = 1
	}
	w := 0
	for i, r := range s {
		rw := 1
		if r == '\t' {
			rw = 4
		} else if r > unicode.MaxASCII {
			rw = 2 // rough
		}
		if w+rw > width && i > 0 {
			return s[:i], strings.TrimLeft(s[i:], " ")
		}
		w += rw
	}
	return s, ""
}

func (m Model) helpKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if m.helpFilterActive {
		switch key {
		case "esc":
			if m.helpFilter != "" {
				m.helpFilter = ""
				m.helpOffset = 0
				return m, nil
			}
			m.helpFilterActive = false
			return m, nil
		case "enter":
			m.helpFilterActive = false
			return m, nil
		case "backspace":
			if m.helpFilter != "" {
				r := []rune(m.helpFilter)
				m.helpFilter = string(r[:len(r)-1])
				m.helpOffset = 0
			}
			return m, nil
		case "ctrl+u":
			m.helpFilter = ""
			m.helpOffset = 0
			return m, nil
		}
		if len(key) == 1 && key[0] >= 32 && key[0] < 127 {
			m.helpFilter += key
			m.helpOffset = 0
			return m, nil
		}
		// ignore other keys while filtering
		return m, nil
	}
	switch key {
	case "/":
		m.helpFilterActive = true
		return m, nil
	case "j", "down":
		m.helpOffset = min(m.helpOffset+1, m.helpMaxOffset())
		return m, nil
	case "k", "up":
		m.helpOffset = max(m.helpOffset-1, 0)
		return m, nil
	case "esc", "q", "?":
		m.help = false
		m.helpOffset = 0
		m.helpFilter = ""
		m.helpFilterActive = false
		return m, nil
	case "ctrl+c":
		m.quitting = true
		m.saveState()
		return m, tea.Quit
	}
	// any other key closes help (herdr: esc is primary; we keep one-shot dismiss for muscle memory)
	m.help = false
	m.helpOffset = 0
	m.helpFilter = ""
	m.helpFilterActive = false
	return m, nil
}
