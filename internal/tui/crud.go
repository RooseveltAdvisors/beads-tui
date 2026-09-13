package tui

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/RooseveltAdvisors/beads-tui/internal/bd"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Vim-spirited CRUD modes (board is still mostly read; these are explicit writes).
//
//	n     new issue (title; due defaults +7d for house due.required)
//	e     edit menu on selection (like :edit / change)
//	x     close with reason (finish)
//	D     delete with y confirm (destructive; capital D)
//
// Esc cancels. Enter commits. Lineage comments are stamped by the bd client.

type crudMode int

const (
	crudNone crudMode = iota
	crudMenu
	crudCreate
	crudEditTitle
	crudEditAssignee
	crudEditPriority
	crudEditStatus
	crudEditDue
	crudClose
	crudDeleteConfirm
)

type crudResultMsg struct {
	kind   string // create|update|close|delete
	id     string
	err    error
	reload bool
}

func (m *Model) initCrudInput() {
	if m.crudInput.Prompt != "" {
		return
	}
	in := textinput.New()
	in.CharLimit = 2000
	in.Width = 72
	m.crudInput = in
}

func (m *Model) openCrudCreate() tea.Cmd {
	m.initCrudInput()
	m.crudMode = crudCreate
	m.crudErr = ""
	m.crudBusy = false
	m.crudInput.Prompt = "New › "
	m.crudInput.Placeholder = "title  (due defaults to +7d)"
	m.crudInput.SetValue("")
	m.crudInput.Focus()
	return textinput.Blink
}

func (m *Model) openCrudMenu() tea.Cmd {
	// e → real $EDITOR (vim) on a text form of the bead. Field mini-editor was too painful.
	return m.openEditorEdit()
}

// openCrudMenuInline is the old in-TUI field menu (kept for tests / E if needed).
func (m *Model) openCrudMenuInline() tea.Cmd {
	if m.selectedID() == "" {
		return nil
	}
	m.crudMode = crudMenu
	m.crudErr = ""
	m.crudMenuIdx = 0
	m.crudBusy = false
	return nil
}

func (m *Model) openCrudClose() tea.Cmd {
	if m.selectedID() == "" {
		return nil
	}
	m.initCrudInput()
	m.crudMode = crudClose
	m.crudErr = ""
	m.crudBusy = false
	m.crudInput.Prompt = "Close reason › "
	m.crudInput.Placeholder = "short real reason (not Closed)"
	m.crudInput.SetValue("")
	m.crudInput.Focus()
	return textinput.Blink
}

func (m *Model) openCrudDelete() tea.Cmd {
	if m.selectedID() == "" {
		return nil
	}
	m.crudMode = crudDeleteConfirm
	m.crudErr = ""
	m.crudBusy = false
	return nil
}

func (m *Model) openCrudEditField(mode crudMode, prompt, placeholder, seed string) tea.Cmd {
	m.initCrudInput()
	m.crudMode = mode
	m.crudErr = ""
	m.crudBusy = false
	m.crudInput.Prompt = prompt
	m.crudInput.Placeholder = placeholder
	m.crudInput.SetValue(seed)
	m.crudInput.CursorEnd()
	m.crudInput.Focus()
	return textinput.Blink
}

func (m *Model) closeCrud() {
	m.crudMode = crudNone
	m.crudErr = ""
	m.crudBusy = false
	m.crudInput.Blur()
	m.crudInput.SetValue("")
}

func (m Model) crudActive() bool {
	return m.crudMode != crudNone
}

func (m Model) crudKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.crudBusy {
		return m, nil
	}
	key := msg.String()

	if m.crudMode == crudMenu {
		return m.crudMenuKey(key)
	}
	if m.crudMode == crudDeleteConfirm {
		switch key {
		case "esc", "n", "q":
			m.closeCrud()
			return m, nil
		case "y", "Y":
			id := m.selectedID()
			m.crudBusy = true
			return m, m.crudDeleteCmd(id)
		}
		return m, nil
	}

	// text-entry modes
	switch key {
	case "esc":
		if m.crudMode == crudCreate || m.crudMode == crudClose {
			m.closeCrud()
			return m, nil
		}
		// field edit → back to menu
		m.crudMode = crudMenu
		m.crudInput.Blur()
		m.crudErr = ""
		return m, nil
	case "enter":
		return m.crudSubmit()
	}
	var cmd tea.Cmd
	m.crudInput, cmd = m.crudInput.Update(msg)
	return m, cmd
}

func (m Model) crudMenuKey(key string) (tea.Model, tea.Cmd) {
	items := crudMenuItems
	switch key {
	case "esc", "q", "e":
		m.closeCrud()
		return m, nil
	case "j", "down":
		if m.crudMenuIdx < len(items)-1 {
			m.crudMenuIdx++
		}
		return m, nil
	case "k", "up":
		if m.crudMenuIdx > 0 {
			m.crudMenuIdx--
		}
		return m, nil
	case "1", "2", "3", "4", "5":
		m.crudMenuIdx = int(key[0] - '1')
		if m.crudMenuIdx >= len(items) {
			m.crudMenuIdx = len(items) - 1
		}
		fallthrough
	case "enter", "l", " ":
		return m.crudMenuChoose(items[m.crudMenuIdx].mode)
	}
	return m, nil
}

type crudMenuItem struct {
	label string
	mode  crudMode
}

var crudMenuItems = []crudMenuItem{
	{label: "title", mode: crudEditTitle},
	{label: "status", mode: crudEditStatus},
	{label: "assignee", mode: crudEditAssignee},
	{label: "priority", mode: crudEditPriority},
	{label: "due", mode: crudEditDue},
}

func (m Model) crudMenuChoose(mode crudMode) (tea.Model, tea.Cmd) {
	id := m.selectedID()
	iss := m.selectedIssue()
	switch mode {
	case crudEditTitle:
		seed := ""
		if iss != nil {
			seed = iss.Title
		}
		return m, m.openCrudEditField(crudEditTitle, "Title › ", "new title", seed)
	case crudEditStatus:
		seed := "in_progress"
		if iss != nil && iss.Status != "" {
			seed = iss.Status
		}
		return m, m.openCrudEditField(crudEditStatus, "Status › ", "open · in_progress · blocked · deferred · closed", seed)
	case crudEditAssignee:
		seed := ""
		if iss != nil {
			seed = iss.Assignee
		}
		return m, m.openCrudEditField(crudEditAssignee, "Assignee › ", "seat name", seed)
	case crudEditPriority:
		seed := "2"
		if iss != nil {
			seed = fmt.Sprintf("%d", iss.Priority)
		}
		return m, m.openCrudEditField(crudEditPriority, "Priority › ", "0-4 or P0-P4", seed)
	case crudEditDue:
		seed := ""
		if iss != nil {
			seed = iss.DueAt
		}
		return m, m.openCrudEditField(crudEditDue, "Due › ", "+6h · +1d · 2026-01-15 · empty clears", seed)
	default:
		_ = id
		return m, nil
	}
}

func (m Model) selectedIssue() *bd.Issue {
	id := m.selectedID()
	if id == "" {
		return nil
	}
	for i := range m.rows {
		if m.rows[i].ID == id {
			iss := m.rows[i]
			return &iss
		}
	}
	if m.detail != nil && m.detail.ID == id {
		return m.detail
	}
	return nil
}

func (m Model) crudSubmit() (tea.Model, tea.Cmd) {
	val := strings.TrimSpace(m.crudInput.Value())
	switch m.crudMode {
	case crudCreate:
		if val == "" {
			m.crudErr = "title cannot be empty"
			return m, nil
		}
		m.crudBusy = true
		return m, m.crudCreateCmd(val, "+7d")
	case crudClose:
		if val == "" || strings.EqualFold(val, "closed") || strings.EqualFold(val, "done") {
			m.crudErr = "need a real close reason"
			return m, nil
		}
		m.crudBusy = true
		return m, m.crudCloseCmd(m.selectedID(), val)
	case crudEditTitle:
		if val == "" {
			m.crudErr = "title cannot be empty"
			return m, nil
		}
		m.crudBusy = true
		return m, m.crudUpdateCmd(m.selectedID(), map[string]string{"title": val})
	case crudEditStatus:
		if val == "" {
			m.crudErr = "status cannot be empty"
			return m, nil
		}
		m.crudBusy = true
		return m, m.crudUpdateCmd(m.selectedID(), map[string]string{"status": val})
	case crudEditAssignee:
		m.crudBusy = true
		return m, m.crudUpdateCmd(m.selectedID(), map[string]string{"assignee": val})
	case crudEditPriority:
		if val == "" {
			m.crudErr = "priority cannot be empty"
			return m, nil
		}
		m.crudBusy = true
		return m, m.crudUpdateCmd(m.selectedID(), map[string]string{"priority": val})
	case crudEditDue:
		// empty allowed (clear) — pass single space? bd uses empty to clear
		fields := map[string]string{"due": val}
		if val == "" {
			fields["due"] = ""
			// UpdateIssue skips empty — need special case: use mut with explicit empty
			// For now require a value or use "-" to clear
			m.crudErr = "set a due (+1d) or type - to clear"
			if val == "-" {
				m.crudErr = ""
				m.crudBusy = true
				return m, m.crudUpdateCmd(m.selectedID(), map[string]string{"due": ""})
			}
			return m, nil
		}
		m.crudBusy = true
		return m, m.crudUpdateCmd(m.selectedID(), fields)
	default:
		return m, nil
	}
}

func (m Model) crudCreateCmd(title, due string) tea.Cmd {
	b := m.backend
	return func() tea.Msg {
		type creator interface {
			CreateIssue(context.Context, string, string) (*bd.Issue, error)
		}
		c, ok := b.(creator)
		if !ok {
			return crudResultMsg{kind: "create", err: fmt.Errorf("backend cannot create issues")}
		}
		ctx, cancel := context.WithTimeout(context.Background(), boardRetryTimeout)
		defer cancel()
		iss, err := c.CreateIssue(ctx, title, due)
		id := ""
		if iss != nil {
			id = iss.ID
		}
		return crudResultMsg{kind: "create", id: id, err: err, reload: err == nil}
	}
}

func (m Model) crudUpdateCmd(id string, fields map[string]string) tea.Cmd {
	b := m.backend
	return func() tea.Msg {
		type updater interface {
			UpdateIssue(context.Context, string, map[string]string) error
		}
		u, ok := b.(updater)
		if !ok {
			return crudResultMsg{kind: "update", id: id, err: fmt.Errorf("backend cannot update issues")}
		}
		ctx, cancel := context.WithTimeout(context.Background(), boardRetryTimeout)
		defer cancel()
		err := u.UpdateIssue(ctx, id, fields)
		return crudResultMsg{kind: "update", id: id, err: err, reload: err == nil}
	}
}

func (m Model) crudCloseCmd(id, reason string) tea.Cmd {
	b := m.backend
	return func() tea.Msg {
		type closer interface {
			CloseIssue(context.Context, string, string) error
		}
		c, ok := b.(closer)
		if !ok {
			return crudResultMsg{kind: "close", id: id, err: fmt.Errorf("backend cannot close issues")}
		}
		ctx, cancel := context.WithTimeout(context.Background(), boardRetryTimeout)
		defer cancel()
		err := c.CloseIssue(ctx, id, reason)
		return crudResultMsg{kind: "close", id: id, err: err, reload: err == nil}
	}
}

func (m Model) crudDeleteCmd(id string) tea.Cmd {
	b := m.backend
	return func() tea.Msg {
		type deleter interface {
			DeleteIssue(context.Context, string) error
		}
		d, ok := b.(deleter)
		if !ok {
			return crudResultMsg{kind: "delete", id: id, err: fmt.Errorf("backend cannot delete issues")}
		}
		ctx, cancel := context.WithTimeout(context.Background(), boardRetryTimeout)
		defer cancel()
		err := d.DeleteIssue(ctx, id)
		return crudResultMsg{kind: "delete", id: id, err: err, reload: err == nil}
	}
}

func (m *Model) applyCrudResult(msg crudResultMsg) tea.Cmd {
	m.crudBusy = false
	if msg.err != nil {
		log.Printf("beads-tui: crud %s failed (id=%s): %v", msg.kind, msg.id, msg.err)
		m.crudErr = msg.err.Error()
		if m.crudMode == crudDeleteConfirm {
			return nil
		}
		if m.crudMode != crudMenu && m.crudMode != crudNone {
			m.crudInput.Focus()
			return textinput.Blink
		}
		return nil
	}
	m.closeCrud()
	m.statusFlash = fmt.Sprintf("%s ok %s", msg.kind, msg.id)
	if msg.reload {
		return m.startBoardLoad()
	}
	return nil
}

func (m Model) renderCrud() string {
	w := m.width
	if w <= 0 {
		w = 80
	}
	var b strings.Builder
	title := styleBold.Render("beads-tui CRUD")
	b.WriteString(title)
	b.WriteString("\n")
	id := m.selectedID()
	if id != "" {
		b.WriteString(styleDim.Render("bead " + id))
		b.WriteString("\n")
	}
	switch m.crudMode {
	case crudMenu:
		b.WriteString("Edit field  (j/k · 1-5 · enter · esc)\n")
		for i, it := range crudMenuItems {
			prefix := "  "
			line := fmt.Sprintf("%d %s", i+1, it.label)
			if i == m.crudMenuIdx {
				prefix = "▸ "
				line = styleBold.Render(line)
			}
			b.WriteString(prefix + line + "\n")
		}
	case crudDeleteConfirm:
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Bold(true).Render("DELETE forever?"))
		b.WriteString("\n")
		b.WriteString(fmt.Sprintf("  %s\n", id))
		b.WriteString("  y confirm · n/esc cancel\n")
	default:
		if m.crudBusy {
			b.WriteString(styleDim.Render("working…"))
			b.WriteString("\n")
		} else {
			b.WriteString(m.crudInput.View())
			b.WriteString("\n")
			b.WriteString(styleDim.Render("enter commit · esc cancel · lineage comment auto-stamped"))
			b.WriteString("\n")
		}
	}
	if m.crudErr != "" {
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Render(m.crudErr))
		b.WriteString("\n")
	}
	// pad
	lines := strings.Count(b.String(), "\n") + 1
	for lines < m.height {
		b.WriteString("\n")
		lines++
	}
	return b.String()
}
