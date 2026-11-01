package tui

import (
	"fmt"
	"testing"

	"github.com/RooseveltAdvisors/beads-tui/internal/bd"
	tea "github.com/charmbracelet/bubbletea"
)

func TestCommentsVimMotionsScroll(t *testing.T) {
	m := New(&fakeClient{issues: map[bd.View][]bd.Issue{bd.ViewOpen: {
		{ID: "a", Title: "Alpha", Status: "open"},
	}}})
	m.width, m.height = 80, 24
	m.commentsOpen = true
	m.commentsID = "a"
	m.comments = nil
	// Long thread so offsets exceed one viewport.
	var comments []bd.Comment
	for i := 0; i < 60; i++ {
		comments = append(comments, bd.Comment{Author: "x", Text: fmt.Sprintf("comment %d", i)})
	}
	m.comments = comments
	max := m.commentsMaxOffset()
	if max < 10 {
		t.Fatalf("fixture too short: max=%d", max)
	}
	// G → bottom.
	m2 := m
	m2.commentsOffset = 0
	m2 = applyMsg(t, m2, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}})
	if m2.commentsOffset != max {
		t.Fatalf("G offset=%d want %d", m2.commentsOffset, max)
	}
	// gg (two g presses) → top.
	m2 = applyMsg(t, m2, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{103}})
	m2 = applyMsg(t, m2, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{103}})
	if m2.commentsOffset != 0 {
		t.Fatalf("gg offset=%d want 0", m2.commentsOffset)
	}
	// ctrl+d half page down, ctrl+u back up.
	half := m.commentsPageStep("ctrl+d")
	m2 = applyMsg(t, m2, tea.KeyMsg{Type: tea.KeyCtrlD})
	if m2.commentsOffset != half {
		t.Fatalf("ctrl+d offset=%d want %d", m2.commentsOffset, half)
	}
	m2 = applyMsg(t, m2, tea.KeyMsg{Type: tea.KeyCtrlU})
	if m2.commentsOffset != 0 {
		t.Fatalf("ctrl+u offset=%d want 0", m2.commentsOffset)
	}
	// ctrl+f full page.
	m2 = applyMsg(t, m2, tea.KeyMsg{Type: tea.KeyCtrlF})
	full := m.commentsPageStep("ctrl+f")
	if m2.commentsOffset != full {
		t.Fatalf("ctrl+f offset=%d want %d", m2.commentsOffset, full)
	}
}
