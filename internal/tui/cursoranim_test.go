package tui

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/RooseveltAdvisors/beads-tui/internal/bd"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/muesli/termenv"
)

// The cursor smear contract: moving the cursor leaves a fading ghost on the
// row it came from (decay pow(1-x, 3.0) over ~200ms), wipes the highlight in
// from the left on the row it entered (~120ms ease-out with an arrival
// pulse), and schedules frame ticks ONLY while it runs - the tick chain ends
// the moment the animation settles or is superseded.

// smearModel drives a three-row board with the smear enabled on TrueColor.
func smearModel(t *testing.T) Model {
	t.Helper()
	withColorProfile(t, termenv.TrueColor)
	f := &fakeClient{issues: map[bd.View][]bd.Issue{bd.ViewOpen: {
		{ID: "fm-a", Title: "Alpha", Status: "open"},
		{ID: "fm-b", Title: "Beta", Status: "open"},
		{ID: "fm-c", Title: "Gamma", Status: "open"},
	}}}
	m := drive(t, f)
	m.smear = true
	return m
}

// gatherMsgs executes a command tree and returns the messages it produces
// without applying them, so tests can assert what would be scheduled.
func gatherMsgs(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if msg == nil {
		return nil
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		var msgs []tea.Msg
		for _, c := range batch {
			msgs = append(msgs, gatherMsgs(c)...)
		}
		return msgs
	}
	return []tea.Msg{msg}
}

func hasCursorFrame(msgs []tea.Msg, seq uint64) bool {
	for _, msg := range msgs {
		if frame, ok := msg.(cursorFrameMsg); ok && frame.seq == seq {
			return true
		}
	}
	return false
}

func moveCursor(t *testing.T, m Model, key string) Model {
	t.Helper()
	updated, _ := m.Update(teaKeyMsg(key))
	next, ok := updated.(Model)
	if !ok {
		t.Fatalf("update returned %T, want Model", updated)
	}
	return next
}

func near(got, want float64) bool { return math.Abs(got-want) < 1e-9 }

// seqParams strips the CSI wrapper so tests can compare SGR parameters.
func seqParams(seq string) string {
	return strings.TrimSuffix(strings.TrimPrefix(seq, "\x1b["), "m")
}

func TestCursorSmearStartsOnMoveAndSchedulesFrames(t *testing.T) {
	m := smearModel(t)
	updated, cmd := m.Update(teaKeyMsg("j"))
	m2 := updated.(Model)
	if !m2.cursorAnim.active {
		t.Fatal("cursor move did not start the smear")
	}
	if m2.cursorAnim.fromID != "fm-a" || m2.cursorAnim.toID != "fm-b" {
		t.Fatalf("smear rows = %q -> %q, want fm-a -> fm-b", m2.cursorAnim.fromID, m2.cursorAnim.toID)
	}
	if !near(m2.cursorAnim.ghost, 1) || !near(m2.cursorAnim.wipe, 0) || !near(m2.cursorAnim.pulse, 1) {
		t.Fatalf("smear start state = ghost %v wipe %v pulse %v, want 1/0/1",
			m2.cursorAnim.ghost, m2.cursorAnim.wipe, m2.cursorAnim.pulse)
	}
	if d := m2.rowDecor(0); !near(d.Ghost, 1) {
		t.Fatalf("left row ghost = %v, want 1", d.Ghost)
	}
	if d := m2.rowDecor(1); !d.Smearing || !near(d.Wipe, 0) {
		t.Fatalf("entered row decor = %+v, want a wiping highlight", d)
	}
	if !hasCursorFrame(gatherMsgs(cmd), m2.cursorAnim.seq) {
		t.Fatal("cursor move did not schedule a smear frame tick")
	}
}

func TestCursorSmearDecayCurve(t *testing.T) {
	m := smearModel(t)
	m2 := moveCursor(t, m, "j")
	seq, started := m2.cursorAnim.seq, m2.cursorAnim.started

	// 60ms is halfway through the 120ms wipe: coverage eased to 0.875.
	half := applyMsg(t, m2, cursorFrameMsg{seq: seq, at: started.Add(60 * time.Millisecond)})
	if !near(half.cursorAnim.wipe, 0.875) {
		t.Fatalf("wipe at 60ms = %v, want ease-out = 0.875", half.cursorAnim.wipe)
	}
	if !near(half.cursorAnim.pulse, 0.125) {
		t.Fatalf("pulse at 60ms = %v, want pow(1-0.5, 3) = 0.125", half.cursorAnim.pulse)
	}
	if !near(half.cursorAnim.ghost, cursorDecay(60.0/200.0)) {
		t.Fatalf("ghost at 60ms = %v, want %v", half.cursorAnim.ghost, cursorDecay(60.0/200.0))
	}
	if !half.cursorAnim.active {
		t.Fatal("smear ended before the trail duration elapsed")
	}

	// Halfway through the trail the ghost has decayed to pow(0.5, 3).
	mid := applyMsg(t, half, cursorFrameMsg{seq: seq, at: started.Add(100 * time.Millisecond)})
	if !near(mid.cursorAnim.ghost, 0.125) {
		t.Fatalf("ghost at 100ms = %v, want pow(1-0.5, 3) = 0.125", mid.cursorAnim.ghost)
	}

	// The ghost row renders with the decayed tint, the faded bar included.
	ghost := mid.vocab.ListRowLiveDecor(mid.rows[0], 40, mid.visibility.List, false, mid.rowDecor(0))
	if want := "48;2;19;19;28"; cellBackgrounds(ghost)[0] != want {
		t.Fatalf("ghost row background = %q, want %q", cellBackgrounds(ghost)[0], want)
	}
	if !strings.Contains(ghost, colorSeq(hudTintHex(mid.cursorAnim.ghost, 0), false)) {
		t.Fatalf("ghost bar did not fade with the trail: %q", ghost)
	}

	// Past the trail the smear decays to nothing and settles.
	end := applyMsg(t, mid, cursorFrameMsg{seq: seq, at: started.Add(cursorTrailDuration + time.Millisecond)})
	if end.cursorAnim.active || !near(end.cursorAnim.ghost, 0) || !near(end.cursorAnim.wipe, 1) || !near(end.cursorAnim.pulse, 0) {
		t.Fatalf("settled smear state = %+v", end.cursorAnim)
	}
	settled := end.vocab.ListRowLiveDecor(end.rows[0], 40, end.visibility.List, false, end.rowDecor(0))
	plain := end.vocab.ListRowLiveDecor(end.rows[0], 40, end.visibility.List, false, RowDecor{})
	if settled != plain {
		t.Fatalf("settled ghost row differs from a plain row:\n%q\n%q", settled, plain)
	}
}

func TestCursorSmearTickStopsWhenSettled(t *testing.T) {
	m := smearModel(t)
	m2 := moveCursor(t, m, "j")
	seq, started := m2.cursorAnim.seq, m2.cursorAnim.started

	// The terminal frame settles the smear and schedules nothing further.
	updated, cmd := m2.Update(cursorFrameMsg{seq: seq, at: started.Add(cursorTrailDuration)})
	if cmd != nil {
		t.Fatalf("settled frame rescheduled a tick: %v", gatherMsgs(cmd))
	}
	m3 := updated.(Model)
	if m3.cursorAnim.active {
		t.Fatal("smear still active after its final frame")
	}
	// Any late frame from the same chain is dropped without rescheduling.
	updated, cmd = m3.Update(cursorFrameMsg{seq: seq, at: started.Add(2 * cursorTrailDuration)})
	if cmd != nil || updated.(Model).cursorAnim.active {
		t.Fatal("late frames must be inert once the smear settles")
	}
}

func TestCursorSmearSupersededChainStops(t *testing.T) {
	m := smearModel(t)
	m2 := moveCursor(t, m, "j")
	firstSeq := m2.cursorAnim.seq
	m3 := moveCursor(t, m2, "j")
	if m3.cursorAnim.fromID != "fm-b" || m3.cursorAnim.toID != "fm-c" {
		t.Fatalf("second move smear rows = %q -> %q, want fm-b -> fm-c", m3.cursorAnim.fromID, m3.cursorAnim.toID)
	}
	if m3.cursorAnim.seq == firstSeq {
		t.Fatal("second move did not start a fresh frame chain")
	}
	// A frame from the superseded chain never advances the smear and never
	// reschedules itself.
	updated, cmd := m3.Update(cursorFrameMsg{seq: firstSeq, at: m3.cursorAnim.started.Add(50 * time.Millisecond)})
	if cmd != nil {
		t.Fatal("superseded frame chain rescheduled a tick")
	}
	if !near(updated.(Model).cursorAnim.ghost, 1) {
		t.Fatal("superseded frame advanced the smear")
	}
}

func TestCursorSmearWipesHighlightFromLeft(t *testing.T) {
	m := smearModel(t)
	m2 := moveCursor(t, m, "j")
	// 30ms in: wipe coverage is eased to 1-pow(0.75, 3) = 0.578125.
	m2 = applyMsg(t, m2, cursorFrameMsg{seq: m2.cursorAnim.seq, at: m2.cursorAnim.started.Add(30 * time.Millisecond)})
	row := m2.vocab.ListRowLiveDecor(m2.rows[1], 40, m2.visibility.List, false, m2.rowDecor(1))
	if got := displayWidth(row); got != 40 {
		t.Fatalf("mid-wipe row width = %d, want 40", got)
	}
	bgs := cellBackgrounds(row)
	k := int(math.Round(m2.cursorAnim.wipe * 40))
	if k < 2 || k >= len(bgs) {
		t.Fatalf("wipe coverage %d out of range for %d cells", k, len(bgs))
	}
	// The arrival pulse brightens the tint until it decays.
	tintSeq := seqParams(colorSeq(hudTintHex(hudFocusTint+hudPulseTint*m2.cursorAnim.pulse, 0), true))
	if bgs[0] != tintSeq {
		t.Fatalf("wipe did not start from the left: cell 0 background = %q, want %q", bgs[0], tintSeq)
	}
	if bgs[len(bgs)-1] != "" {
		t.Fatalf("tail of the row tinted before the wipe arrived: %q", bgs[len(bgs)-1])
	}
	beamSeq := "48;2;37;127;150" // hudBeamTint cyan edge glow
	for i := k - 2; i < k; i++ {
		if bgs[i] != beamSeq {
			t.Fatalf("beam edge cell %d background = %q, want %q (coverage %d)", i, bgs[i], beamSeq, k)
		}
	}
	for i := 0; i < k-2; i++ {
		if bgs[i] != tintSeq {
			t.Fatalf("cell %d behind the beam = %q, want %q", i, bgs[i], tintSeq)
		}
	}
	if bgs[k] != "" {
		t.Fatalf("cell %d beyond the wipe edge is tinted: %q", k, bgs[k])
	}
}

func TestCursorSmearStaticTiersSettleImmediately(t *testing.T) {
	for _, profile := range []termenv.Profile{termenv.ANSI256, termenv.ANSI} {
		withColorProfile(t, profile)
		f := &fakeClient{issues: map[bd.View][]bd.Issue{bd.ViewOpen: {
			{ID: "fm-a", Title: "Alpha", Status: "open"},
			{ID: "fm-b", Title: "Beta", Status: "open"},
		}}}
		m := New(f)
		if m.smear {
			t.Fatalf("profile %v must degrade to a static highlight", profile)
		}
		updated, _ := m.Update(boardMsg{view: m.view, generation: m.boardGen, issues: f.issues[bd.ViewOpen]})
		m = updated.(Model)
		updated, cmd := m.Update(teaKeyMsg("j"))
		m = updated.(Model)
		if m.cursorAnim.active {
			t.Fatalf("profile %v started a smear", profile)
		}
		if hasCursorFrame(gatherMsgs(cmd), m.cursorAnim.seq) {
			t.Fatalf("profile %v scheduled frame ticks", profile)
		}
		if d := m.rowDecor(1); d.Smearing || !near(d.Wipe, 1) || !near(d.Ghost, 0) {
			t.Fatalf("profile %v focused row = %+v, want a settled static highlight", profile, d)
		}
	}
}

func TestCursorSmearEnabledOnTrueColorOnly(t *testing.T) {
	withColorProfile(t, termenv.TrueColor)
	if m := New(nil); !m.smear {
		t.Fatal("TrueColor terminals should render the cursor smear")
	}
	withColorProfile(t, termenv.ANSI256)
	if m := New(nil); m.smear {
		t.Fatal("ANSI256 terminals should degrade to the static highlight")
	}
	withColorProfile(t, termenv.Ascii)
	if m := New(nil); m.smear {
		t.Fatal("no-color terminals should degrade to the static highlight")
	}
}
