package tui

import (
	"fmt"
	"strings"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Help follows Herdr's keybind help modal:
//   - named lowercase groups
//   - aligned key column (bold mauve) + plain labels
//   - / opens filter; type to search keys or labels
//   - j/k · pgup/pgdn scroll; esc closes (does not dismiss on random keys)
//   - footer chrome: search · scroll · close

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
		views = append(views, helpEntry{fmt.Sprintf("%d", i+1), "switch to " + view.Label()})
	}

	return []helpGroup{
		{
			Name: "global",
			Entries: []helpEntry{
				{"?", "open this help"},
				{"/", "search help (when open) · board search (on board)"},
				{"q  esc  ctrl+c", "quit beads-tui"},
				{"esc", "back one layer · then quit on the plain board"},
				{"r", "reload board (keep view / sort / search)"},
				{"R", "reset to ready + default sort/filter"},
				{"y", "yank menu (id · title · url)"},
				{"a", "attach to assignee Herdr / tmux session"},
			},
		},
		{
			Name: "databases",
			Entries: []helpEntry{
				{"ctrl+o", "database picker (switch workspace)"},
				{"ctrl+e", "toggle global board (all workspaces)"},
				{"enter", "open selected database (in picker)"},
				{"R", "rescan local .beads stores (in picker)"},
				{"j  k", "move in picker"},
				{"esc", "close picker"},
			},
		},
		{
			Name: "navigation",
			Entries: []helpEntry{
				{"j  k   ↑  ↓", "move selection"},
				{"g", "top of list"},
				{"G", "bottom · or open dependency graph"},
				{"space  pgdn  ctrl+f", "page down"},
				{"b  pgup  ctrl+b", "page up"},
				{"ctrl+d  ctrl+u", "half-page down / up"},
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
				{"T", "flat list ↔ tree"},
				{"|", "layout: side · stacked · auto (cycle)"},
			},
		},
		{
			Name:    "views",
			Entries: views,
		},
		{
			Name: "sort / filter",
			Entries: []helpEntry{
				{"s", "cycle sort: created · updated · alpha · deps · depends · due · priority"},
				{"t", "filter to selected bead's labels"},
				{"o", "view options (row / detail fields)"},
				{"/", "board search prompt"},
				{"enter", "apply board filter"},
				{"tab", "accept search completion"},
				{"↑  ↓", "cycle search completions"},
			},
		},
		{
			Name: "search syntax",
			Entries: []helpEntry{
				{"word word", "AND"},
				{"a | b", "OR"},
				{"!word", "NOT"},
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
				{"enter", "submit comment"},
				{"esc  q", "back to board"},
			},
		},
		{
			Name: "crud",
			Entries: []helpEntry{
				{"v  V", "visual multi-select"},
				{"n", "new issue (title; due +7d)"},
				{"e", "edit bead in $EDITOR"},
				{"x", "close with reason (visual: delete)"},
				{"d  D", "delete forever (y / n)"},
				{"enter", "commit field / create / close"},
				{"esc", "cancel visual / CRUD"},
			},
		},
		{
			Name: "audit",
			Entries: []helpEntry{
				{"tui-lineage", "comment stamped on every write"},
				{"actor=", "BEADS_ACTOR (agents) or git/$USER"},
				{"bd --actor", "same identity on events"},
				{"BEADS_ACTOR", "override actor for agents"},
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
				{"●", "assignee has live Herdr/tmux session"},
				{"↻", "recurring"},
				{"⚠", "overdue"},
				{"⇣N", "blocked-by N"},
				{"⇡N", "blocks N"},
				{"P0…P4", "priority (P0 highest)"},
			},
		},
	}
}

// filterHelpGroups matches keys, labels, and group names (group hit keeps all its rows).
func filterHelpGroups(groups []helpGroup, query string) []helpGroup {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return groups
	}
	var out []helpGroup
	for _, g := range groups {
		if strings.Contains(strings.ToLower(g.Name), q) {
			out = append(out, g)
			continue
		}
		var entries []helpEntry
		for _, e := range g.Entries {
			if strings.Contains(strings.ToLower(e.Key), q) || strings.Contains(strings.ToLower(e.Label), q) {
				entries = append(entries, e)
			}
		}
		if len(entries) > 0 {
			out = append(out, helpGroup{Name: g.Name, Entries: entries})
		}
	}
	return out
}

func helpKeyWidth(groups []helpGroup) int {
	w := 10
	for _, g := range groups {
		for _, e := range g.Entries {
			if n := displayWidth(e.Key); n > w {
				w = n
			}
		}
	}
	if w > 24 {
		w = 24
	}
	return w
}

func padHelpKey(key string, width int) string {
	n := displayWidth(key)
	if n >= width {
		return truncate(key, width)
	}
	return key + strings.Repeat(" ", width-n)
}

func (m Model) helpLines(width int) []string {
	groups := filterHelpGroups(m.helpGroups(), m.helpFilter)
	inner := max(1, width-2)

	// Herdr palette mapping: accent headings, mauve keys, body text labels.
	keyStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("183")).Bold(true)
	headStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("45")).Bold(true)
	labelStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("255"))
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	ruleStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("238"))

	if len(groups) == 0 {
		var lines []string
		if m.helpFilterActive {
			lines = append(lines, keyStyle.Render(" / ")+styleBold.Render(m.helpFilter)+keyStyle.Render("█"))
		} else if q := strings.TrimSpace(m.helpFilter); q != "" {
			lines = append(lines, dimStyle.Render(" filter: ")+styleBold.Render(q))
		}
		lines = append(lines, dimStyle.Render(" no matching keybinds"))
		return lines
	}

	kw := helpKeyWidth(groups)
	var lines []string

	// Search row (Herdr header second line).
	if m.helpFilterActive {
		cursor := "█"
		lines = append(lines, keyStyle.Render(" / ")+styleBold.Render(m.helpFilter)+keyStyle.Render(cursor))
	} else if q := strings.TrimSpace(m.helpFilter); q != "" {
		lines = append(lines, dimStyle.Render(" filter: ")+styleBold.Render(q)+dimStyle.Render("  ·  / edit · esc clear"))
	} else {
		lines = append(lines, dimStyle.Render(" press / to filter by command or shortcut"))
	}
	lines = append(lines, ruleStyle.Render(strings.Repeat("─", inner)))

	// Column header for the table.
	colKey := dimStyle.Render(" " + padHelpKey("KEY", kw) + " ")
	colAct := dimStyle.Render("ACTION")
	lines = append(lines, colKey+colAct)
	lines = append(lines, ruleStyle.Render(strings.Repeat("─", inner)))

	for _, g := range groups {
		lines = append(lines, headStyle.Render(" "+g.Name))
		for _, e := range g.Entries {
			for _, wline := range wrapHelpRow(e.Key, e.Label, kw, inner, keyStyle, labelStyle) {
				lines = append(lines, wline)
			}
		}
		lines = append(lines, "")
	}

	if strings.TrimSpace(m.helpFilter) == "" {
		lines = append(lines, ruleStyle.Render(strings.Repeat("─", inner)))
		lines = append(lines, dimStyle.Render(" Priority: ")+strings.Join([]string{
			priorityStyle(0).Render("P0"), priorityStyle(1).Render("P1"),
			priorityStyle(2).Render("P2"), priorityStyle(3).Render("P3"),
			priorityStyle(4).Render("P4"),
		}, " "))
		lines = append(lines, dimStyle.Render(" Status:   ")+strings.Join([]string{
			m.vocab.StatusPill("open"), m.vocab.StatusPill("in_progress"),
			m.vocab.StatusPill("blocked"), m.vocab.StatusPill("closed"),
			m.vocab.StatusPill("deferred"), m.vocab.StatusPill("hold"),
		}, " "))
	}

	return lines
}

func wrapHelpRow(key, label string, kw, inner int, keyStyle, labelStyle lipgloss.Style) []string {
	prefix := " " + padHelpKey(key, kw) + " "
	prefixW := displayWidth(prefix)
	budget := max(8, inner-prefixW)
	full := keyStyle.Render(prefix) + labelStyle.Render(label)
	if displayWidth(stripANSI(full)) <= inner {
		return []string{full}
	}
	var out []string
	rest := label
	first := true
	for rest != "" {
		chunk, next := takeWidth(rest, budget)
		if first {
			out = append(out, keyStyle.Render(prefix)+labelStyle.Render(chunk))
			first = false
		} else {
			out = append(out, strings.Repeat(" ", prefixW)+labelStyle.Render(chunk))
		}
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
			rw = 2
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
		case "j", "down", "ctrl+n":
			m.helpOffset = min(m.helpOffset+1, m.helpMaxOffset())
			return m, nil
		case "k", "up", "ctrl+p":
			m.helpOffset = max(m.helpOffset-1, 0)
			return m, nil
		case "pgdown", "ctrl+f", " ":
			m.helpOffset = min(m.helpOffset+m.helpPageStep(), m.helpMaxOffset())
			return m, nil
		case "pgup", "ctrl+b", "b":
			m.helpOffset = max(m.helpOffset-m.helpPageStep(), 0)
			return m, nil
		}
		if len(key) == 1 && key[0] >= 32 && key[0] < 127 {
			m.helpFilter += key
			m.helpOffset = 0
			return m, nil
		}
		return m, nil
	}
	switch key {
	case "/":
		m.helpFilterActive = true
		return m, nil
	case "j", "down", "ctrl+n":
		m.helpOffset = min(m.helpOffset+1, m.helpMaxOffset())
		return m, nil
	case "k", "up", "ctrl+p":
		m.helpOffset = max(m.helpOffset-1, 0)
		return m, nil
	case "pgdown", "ctrl+f", " ", "ctrl+d":
		m.helpOffset = min(m.helpOffset+m.helpPageStep(), m.helpMaxOffset())
		return m, nil
	case "pgup", "ctrl+b", "b", "ctrl+u":
		m.helpOffset = max(m.helpOffset-m.helpPageStep(), 0)
		return m, nil
	case "g", "home":
		m.helpOffset = 0
		return m, nil
	case "G", "end":
		m.helpOffset = m.helpMaxOffset()
		return m, nil
	case "esc", "q", "?", "enter":
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
	// Herdr keeps the modal open on unmatched keys so / discovery is not punished.
	return m, nil
}

func (m Model) helpPageStep() int {
	_, _, height := m.helpDimensions()
	return max(1, height-4)
}
