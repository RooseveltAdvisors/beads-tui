package tui

import (
	"strings"
	"testing"

	"github.com/RooseveltAdvisors/beads-tui/internal/bd"
	tea "github.com/charmbracelet/bubbletea"
)

// Key dispatch contract: a terminal read that batches ESC with the next rune
// must not lose that rune to an alt+<rune> binding the TUI never defines, and
// a multi-rune burst must keep every command its runes produce, not just the
// last one.

func twoRowBoard() *fakeClient {
	return &fakeClient{issues: map[bd.View][]bd.Issue{bd.ViewOpen: {
		{ID: "fm-a", Title: "Alpha", Status: "open"},
		{ID: "fm-b", Title: "Beta", Status: "open"},
	}}}
}

func TestEscRuneSequenceDoesNotSwallowTheRune(t *testing.T) {
	m := drive(t, twoRowBoard())
	// ESC + l in one tty read is parsed by bubbletea as alt+l.
	m = applyMsg(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}, Alt: true})
	if m.focus != FocusDetail {
		t.Fatalf("ESC-then-l lost the rune: focus = %v, want detail focus", m.focus)
	}

	// The same batching must not swallow navigation runes either.
	m = drive(t, twoRowBoard())
	m = applyMsg(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}, Alt: true})
	if m.selectedID() != "fm-b" {
		t.Fatalf("ESC-then-j lost the rune: selected = %q, want fm-b", m.selectedID())
	}
}

func TestRuneBurstKeepsEveryCommand(t *testing.T) {
	m := drive(t, twoRowBoard())
	// "jr" arrives in one read: j moves the cursor (detail debounce) and r
	// reloads the board. Both commands must survive the burst.
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("jr")})
	if _, ok := updated.(Model); !ok {
		t.Fatalf("update returned %T, want Model", updated)
	}
	var sawDebounce, sawBoard bool
	for _, msg := range gatherMsgs(cmd) {
		switch msg.(type) {
		case detailDebounceMsg:
			sawDebounce = true
		case boardMsg:
			sawBoard = true
		}
	}
	if !sawDebounce || !sawBoard {
		t.Fatalf("rune burst dropped commands: detail debounce=%v board reload=%v", sawDebounce, sawBoard)
	}
}

// G is "go to end": when the detail content lands after the keypress (slow
// detail load), the viewport must be waiting at the tail instead of silently
// snapping back to the top.
func TestDetailGotoEndStaysPinnedWhileDetailLoads(t *testing.T) {
	m := drive(t, twoRowBoard())
	m.width, m.height = 160, 12 // keep the pane geometry and the scroll budget consistent
	// Model an in-flight detail fetch: nothing to scroll yet.
	m.detail, m.down, m.up = nil, nil, nil
	m.comments = []bd.Comment{{ID: "c1", Author: "Ada", Text: "late comment lands last", CreatedAt: "2026-09-25T10:00:00Z"}}
	m = sendKey(t, m, "l")
	m = sendKey(t, m, "G")
	if !m.dFollowTail {
		t.Fatal("G must pin the detail viewport to its tail")
	}

	// The fetched detail lands afterwards; the goto-end intent sticks.
	m = applyMsg(t, m, detailMsg{
		id:         "fm-a",
		generation: m.detailGen,
		issue:      &bd.Issue{ID: "fm-a", Title: "Alpha", Status: "open", CommentCount: 1},
	})
	plain := stripANSI(strings.Join(m.renderDetailPane(m.width, 10), "\n"))
	if !strings.Contains(plain, "late comment lands last") {
		t.Fatalf("goto-end was lost while the detail loaded:\n%s", plain)
	}

	// Any explicit scroll drops the pin.
	m = sendKey(t, m, "k")
	if m.dFollowTail {
		t.Fatal("scrolling must drop the tail pin")
	}
}
