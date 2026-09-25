package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// The cursor smear is the Ghostty-style trail behind cursor movement: the row
// the cursor left keeps a fading ghost (decay pow(1-x, 3.0) over ~200ms) and
// the row it entered wipes its highlight in from the left (ease-out over
// ~120ms) with a brief arrival pulse. Timing is hardcoded; the only knob is
// the colour tier (rowdecor.go). Frame ticks run ONLY while the smear is
// active and stop the moment it settles, so there is no busy loop and no
// input handling delay.
const (
	cursorTrailDuration = 200 * time.Millisecond
	cursorWipeDuration  = 120 * time.Millisecond
	cursorFrameInterval = 16 * time.Millisecond
)

// cursorAnim is the smear state. Progress is recomputed on each frame message
// from the frame's timestamp, so rendering reads plain 0..1 values and tests
// can drive frames deterministically.
type cursorAnim struct {
	active  bool
	seq     uint64 // frame chain generation; stale frames never reschedule
	started time.Time
	fromID  string // bead the cursor left: ghost trail
	toID    string // bead the cursor entered: wipe + pulse
	ghost   float64
	wipe    float64 // settled highlight coverage (already eased, 0..1)
	pulse   float64
}

// cursorFrameMsg is one smear frame, timestamped at fire time.
type cursorFrameMsg struct {
	seq uint64
	at  time.Time
}

// cursorDecay is the locked decay curve pow(1-x, 3.0).
func cursorDecay(x float64) float64 {
	return pow1m3(clamp01(x))
}

// cursorEaseOut is the locked wipe easing: ease-out coverage.
func cursorEaseOut(x float64) float64 {
	return 1 - pow1m3(clamp01(x))
}

func pow1m3(x float64) float64 {
	one := 1 - x
	return one * one * one
}

// cursorFrameCmd schedules the next smear frame.
func cursorFrameCmd(seq uint64) tea.Cmd {
	return tea.Tick(cursorFrameInterval, func(time.Time) tea.Msg {
		return cursorFrameMsg{seq: seq, at: time.Now()}
	})
}

// syncCursorAnim starts or restarts the smear whenever the focused bead
// changes and returns the frame tick that drives it. Static terminals get the
// settled state immediately: full-row highlight, no ticks.
func (m *Model) syncCursorAnim() tea.Cmd {
	if m.quitting {
		return nil
	}
	id := m.selectedID()
	if id == "" {
		m.cursorAnim = cursorAnim{wipe: 1}
		return nil
	}
	if id == m.cursorAnim.toID {
		return nil
	}
	from := m.cursorAnim.toID
	seq := m.cursorAnim.seq
	m.cursorAnim = cursorAnim{toID: id, wipe: 1}
	m.cursorAnim.seq = seq // frame chains must stay unique across moves
	if from != "" {
		m.cursorAnim.fromID = from
	}
	// Only real cursor movement smears: the first paint after a load
	// settles at the full highlight instead of animating in.
	if !m.smear || from == "" {
		return nil
	}
	m.cursorAnim.seq++
	m.cursorAnim.started = time.Now()
	m.cursorAnim.active = true
	m.cursorAnim.ghost, m.cursorAnim.wipe, m.cursorAnim.pulse = 1, 0, 1
	return cursorFrameCmd(m.cursorAnim.seq)
}

// cursorFrame advances the smear one frame. Stale or settled frames never
// reschedule, so the tick chain stops the moment the animation is over.
func (m Model) cursorFrame(msg cursorFrameMsg) (tea.Model, tea.Cmd) {
	if !m.cursorAnim.active || msg.seq != m.cursorAnim.seq {
		return m, nil
	}
	elapsed := msg.at.Sub(m.cursorAnim.started)
	if elapsed < 0 {
		elapsed = 0
	}
	if elapsed >= cursorTrailDuration {
		m.cursorAnim.ghost, m.cursorAnim.wipe, m.cursorAnim.pulse = 0, 1, 0
		m.cursorAnim.active = false
		return m, nil
	}
	m.cursorAnim.ghost = cursorDecay(float64(elapsed) / float64(cursorTrailDuration))
	if elapsed >= cursorWipeDuration {
		m.cursorAnim.wipe, m.cursorAnim.pulse = 1, 0
	} else {
		x := float64(elapsed) / float64(cursorWipeDuration)
		m.cursorAnim.wipe = cursorEaseOut(x)
		m.cursorAnim.pulse = cursorDecay(x)
	}
	return m, cursorFrameCmd(msg.seq)
}
