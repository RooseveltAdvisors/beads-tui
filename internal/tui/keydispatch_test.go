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

// Every binding-free mode resolves an ESC-batched rune to its plain rune,
// not only the board navigation path.
func TestEscRuneSequenceReachesGraphMode(t *testing.T) {
	m := drive(t, twoRowBoard())
	m.graph = true
	m = applyMsg(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}, Alt: true})
	if m.graph {
		t.Fatal("ESC-then-G lost the rune: the graph stayed open")
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

// The navigation fall-through never applies to the input modes: an alt chord
// inside a text input must reach the embedded bubbles input, whose keymap
// binds the rune-bound word operations (alt+b = WordBackward).
func TestAltChordReachesFilterInput(t *testing.T) {
	m := drive(t, twoRowBoard())
	m = sendKey(t, m, "/")
	if !m.filtering {
		t.Fatal("/ did not open the filter prompt")
	}
	m = applyMsg(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("ab cd")})
	if got := m.filterInput.Value(); got != "ab cd" {
		t.Fatalf("filter input = %q, want %q", got, "ab cd")
	}
	// alt+b moves the cursor one word back; a stripped Alt would insert "b".
	m = applyMsg(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'b'}, Alt: true})
	m = applyMsg(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'X'}})
	if got := m.filterInput.Value(); got != "ab Xcd" {
		t.Fatalf("filter input = %q, want %q (word back, then insert)", got, "ab Xcd")
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
	// The first scroll starts from the position the pane showed: one line
	// above the tail, not a jump to the top.
	w := m.detailWidth() + 2 // the detail pane's outer width in the split
	all := m.buildDetail(m.detailWidth())
	_, maxOffset := m.detailContentBudget(len(all))
	if maxOffset < 2 {
		t.Fatalf("test fixture needs a scrollable detail, maxOffset = %d", maxOffset)
	}
	if m.dOffset != maxOffset-1 {
		t.Fatalf("first scroll after a pinned G starts at offset %d, want %d", m.dOffset, maxOffset-1)
	}
	got := m.renderDetailPane(w, 10)
	if len(got) < 3 {
		t.Fatalf("detail pane rendered %d lines", len(got))
	}
	first := strings.TrimSpace(stripANSI(strings.Trim(got[1], "│")))
	if want := strings.TrimSpace(stripANSI(all[maxOffset-1])); first != want {
		t.Fatalf("scrolled pane starts at %q, want the line just above the tail %q", first, want)
	}
}

// A batched ESC+<rune> must stay inert inside the destructive delete gate:
// bubbletea consumed the ESC as the modifier, so the chord may carry the
// user's cancel intent and must never confirm or cancel a delete.
func TestDeleteConfirmIgnoresChordedKey(t *testing.T) {
	f := twoRowBoard()
	m := drive(t, f)
	m = sendKey(t, m, "v")
	m = sendKey(t, m, "j")
	m = sendKey(t, m, "d")
	if m.crudMode != crudDeleteConfirm {
		t.Fatalf("crudMode = %v, want crudDeleteConfirm", m.crudMode)
	}
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}, Alt: true})
	m = runCmd(t, updated.(Model), cmd)
	if m.crudMode != crudDeleteConfirm {
		t.Fatalf("ESC-then-y acted inside the delete gate: crudMode = %v", m.crudMode)
	}
	if len(f.deletedIDs) != 0 {
		t.Fatalf("ESC-then-y submitted a delete of %v", f.deletedIDs)
	}
	// A plain, unmodified y still confirms the delete.
	m = step(t, m, "y")
	if len(f.deletedIDs) != 2 {
		t.Fatalf("plain y deleted %v, want both selected beads", f.deletedIDs)
	}
	if m.crudMode != crudNone {
		t.Fatalf("crudMode after confirm = %v, want crudNone", m.crudMode)
	}
}
