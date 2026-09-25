package tui

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
	"github.com/muesli/termenv"
)

// Row decoration is the HUD chrome painted on board rows so the focused bead
// is identifiable at a glance: a magenta left bar ("▎") in a fixed marker
// slot, a magenta-tinted background spanning the full row width, and - while
// the cursor smears between rows (cursoranim.go) - a left-to-right highlight
// wipe plus a fading ghost on the row the cursor left. Visual multi-select
// rows carry cyan "[✓]" markers and a cyan tint, so focus (magenta) and
// multi-select (cyan) never read as one state.
//
// Colour tiers (resolved per render from the lipgloss profile):
//
//	TrueColor : exact hex tints and the animated smear.
//	ANSI256   : the same tints quantized to the 256-colour cube, static.
//	ANSI (16) : flat magenta/cyan background approximations, static.
//	Ascii     : no escape sequences at all (NO_COLOR, dumb terminals); the
//	            bar and checkbox glyphs still mark the row.
//
// Every row is laid out as [marker slot][content] with a slot width that is
// constant across rows, so content columns, the right-aligned counts column,
// and the pane borders stay aligned whatever the decoration does.

const (
	hudBaseHex    = "#0d1116" // base the tints are mixed on
	hudMagentaHex = "#f94dff" // primary accent: focus
	hudCyanHex    = "#38d9ff" // secondary accent: multi-select, beam edge

	hudFocusTint = 0.20 // focused-row tint: 20% magenta over the base
	hudPulseTint = 0.15 // arrival pulse peak, decays with the wipe
	hudMarkTint  = 0.22 // multi-select tint: 22% cyan over the base
	hudBeamTint  = 0.55 // scan-beam glow: cyan at the wipe's leading edge

	// 16-colour tier: flat approximations of the same two hues.
	hudAnsiFocusBg = "53"
	hudAnsiMarkBg  = "30"

	// The locked left affordance and the multi-select markers.
	focusBar    = "▎" // U+258E LEFT ONE EIGHTH BLOCK
	markedBox   = "[✓]"
	unmarkedBox = "[ ]"

	resetSeq = "\x1b[0m"
)

// RowDecor describes the HUD chrome for one board row. The zero value (plus
// Focused) renders a settled full-row highlight; Wipe and Pulse only take
// effect while Smearing is set by the cursor animation.
type RowDecor struct {
	Focused  bool    // cursor row: magenta bar + magenta tint
	Marked   bool    // inside the visual multi-select range: cyan checkbox
	Checks   bool    // visual mode: reserve the checkbox column on every row
	Smearing bool    // the cursor smear is animating this row
	Ghost    float64 // trail intensity on the row the cursor left (0..1)
	Wipe     float64 // highlight coverage on the focused row (0..1)
	Pulse    float64 // arrival pulse on the focused row (0..1)
}

// slotWidth is the fixed leading marker slot: the focus bar plus a gutter,
// and the checkbox column while visual multi-select is active. All rows in a
// render share it so content columns line up.
func (d RowDecor) slotWidth() int {
	if d.Checks {
		return 5 // bar + [✓] + gutter
	}
	return 2 // bar + gutter
}

// tintFractions resolves the magenta and cyan tint strength for a row.
func (d RowDecor) tintFractions() (mag, cyan float64) {
	if d.Focused {
		mag += hudFocusTint + hudPulseTint*clamp01(d.Pulse)
	}
	if d.Ghost > 0 {
		mag += hudFocusTint * clamp01(d.Ghost)
	}
	if d.Marked {
		cyan += hudMarkTint
	}
	return mag, cyan
}

// marker renders the leading marker slot: the focus bar (magenta, fading on a
// ghost row) plus, in visual mode, the cyan multi-select checkbox.
func (d RowDecor) marker() string {
	bar := " "
	switch {
	case d.Focused:
		bar = colorize(focusBar, hudMagentaHex)
	case d.Ghost > 0:
		bar = colorize(focusBar, hudTintHex(d.Ghost, 0))
	}
	if !d.Checks {
		return bar + " "
	}
	box := styleDim.Render(unmarkedBox)
	if d.Marked {
		box = colorize(markedBox, hudCyanHex)
	}
	return bar + box + " "
}

// decorateRow finishes one board row: it prepends the marker slot, pads the
// row to exactly width cells, and paints the HUD backgrounds so any highlight
// spans the full row width. The result is always exactly width cells wide,
// decorated or not, so rows stay aligned with the pane and the counts column.
func decorateRow(content string, width int, d RowDecor) string {
	usable := width - d.slotWidth()
	if usable < 1 {
		usable = 1
	}
	row := d.marker() + padRight(truncatePhys(content, usable), usable)
	if displayWidth(row) > width {
		row = truncatePhys(row, width)
	}
	return paintRowBackground(row, d)
}

// paintRowBackground applies the row tint across every cell. The focused row
// can be mid-wipe (Wipe < 1): only the left part is tinted, a cyan beam glows
// at the wipe's leading edge, and the rest stays untinted until the highlight
// settles at full width.
func paintRowBackground(row string, d RowDecor) string {
	mag, cyan := d.tintFractions()
	if mag <= 0 && cyan <= 0 {
		return row
	}
	tint := rowTintSeq(d.Focused, mag, cyan)
	if tint == "" {
		return row // no-colour terminal: glyph-only decoration
	}
	if d.Focused && d.Smearing && d.Wipe < 1 {
		total := displayWidth(row)
		k := int(math.Round(clamp01(d.Wipe) * float64(total)))
		if k <= 0 {
			return row // the beam has not entered yet; the bar marks focus
		}
		head, tail := splitStyledCells(row, k)
		core, beam := head, ""
		if beamFrom := max(0, k-2); beamFrom > 0 {
			core, beam = splitStyledCells(head, beamFrom)
		}
		out := applyRowBackground(core, tint)
		if beam != "" {
			out += applyRowBackground(beam, rowTintSeq(false, 0, hudBeamTint))
		}
		return out + tail
	}
	return applyRowBackground(row, tint)
}

// rowTintSeq picks the row-tint background sequence for the active colour
// tier. "" means the tier paints no background at all. The 16-colour tier
// cannot blend, so a focused row always takes the focus background there -
// the multi-select tint must never win over focus, whatever the fractions.
func rowTintSeq(focused bool, magFrac, cyanFrac float64) string {
	switch hudProfile() {
	case termenv.ANSI:
		// 16 colours cannot blend; approximate the two hues flat.
		if !focused && cyanFrac > magFrac {
			return colorSeq(hudAnsiMarkBg, true)
		}
		return colorSeq(hudAnsiFocusBg, true)
	case termenv.TrueColor, termenv.ANSI256:
		return colorSeq(hudTintHex(magFrac, cyanFrac), true)
	default:
		return ""
	}
}

// hudProfile resolves the active colour tier.
func hudProfile() termenv.Profile {
	return lipgloss.ColorProfile()
}

// smearEnabled reports whether this terminal renders the cursor smear. Only
// TrueColor interpolates fades smoothly; every other tier degrades to a
// static full-row highlight.
func smearEnabled() bool {
	return hudProfile() == termenv.TrueColor
}

// colorSeq resolves one palette hex (or 256-colour code) to an SGR sequence
// for the active colour tier. "" on no-colour terminals.
func colorSeq(color string, bg bool) string {
	p := hudProfile()
	if p == termenv.TrueColor && strings.HasPrefix(color, "#") {
		// Emit exact RGB channels: a parse/format round trip through a
		// color type can truncate a channel by one and drift the tint.
		r, g, b := hexRGB(color)
		prefix := "38"
		if bg {
			prefix = "48"
		}
		return fmt.Sprintf("\x1b[%s;2;%d;%d;%dm", prefix, r, g, b)
	}
	c := p.Color(color)
	if c == nil {
		return ""
	}
	seq := c.Sequence(bg)
	if seq == "" {
		return ""
	}
	return "\x1b[" + seq + "m"
}

// colorize renders text in one palette hex for the active colour tier.
func colorize(text, hex string) string {
	seq := colorSeq(hex, false)
	if seq == "" {
		return text
	}
	return seq + text + resetSeq
}

// hudTintHex mixes the accent tints into the base colour: magenta for focus
// and the ghost trail, cyan for multi-select and the scan beam.
func hudTintHex(magFrac, cyanFrac float64) string {
	br, bg, bb := hexRGB(hudBaseHex)
	mr, mg, mb := hexRGB(hudMagentaHex)
	cr, cg, cb := hexRGB(hudCyanHex)
	mix := func(base, mag, cyan int) int {
		v := float64(base) +
			float64(mag-base)*clamp01(magFrac) +
			float64(cyan-base)*clamp01(cyanFrac)
		return clampInt(int(math.Round(v)), 0, 255)
	}
	return fmt.Sprintf("#%02x%02x%02x", mix(br, mr, cr), mix(bg, mg, cg), mix(bb, mb, cb))
}

// hexRGB parses a #rrggbb literal.
func hexRGB(hex string) (r, g, b int) {
	if len(hex) != 7 || hex[0] != '#' {
		return 0, 0, 0
	}
	parse := func(s string) int {
		v, err := strconv.ParseInt(s, 16, 32)
		if err != nil {
			return 0
		}
		return int(v)
	}
	return parse(hex[1:3]), parse(hex[3:5]), parse(hex[5:7])
}

// applyRowBackground paints every cell of a styled line with bg. SGR resets
// inside chips and icons re-apply the background afterwards: without that,
// the highlight dies at the first inner reset and the row looks half-painted.
func applyRowBackground(line, bg string) string {
	if bg == "" {
		return line
	}
	return bg + strings.ReplaceAll(line, resetSeq, resetSeq+bg) + resetSeq
}

// styledRun is a run of visible text preceded by the SGR sequences styling it.
type styledRun struct {
	codes string
	text  string
}

// parseStyled splits ANSI-styled text into code/text runs.
func parseStyled(s string) []styledRun {
	var runs []styledRun
	var codes strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			start := i
			i++
			if i < len(s) && s[i] == '[' {
				i++
				for i < len(s) && !isCSIFinalByte(s[i]) {
					i++
				}
				if i < len(s) {
					i++
				}
			} else if i < len(s) {
				i++
			}
			codes.WriteString(s[start:i])
			continue
		}
		start := i
		for i < len(s) && s[i] != 0x1b {
			_, size := utf8.DecodeRuneInString(s[i:])
			i += size
		}
		runs = append(runs, styledRun{codes: codes.String(), text: s[start:i]})
		codes.Reset()
	}
	if codes.Len() > 0 {
		runs = append(runs, styledRun{codes: codes.String()})
	}
	return runs
}

func isCSIFinalByte(b byte) bool { return b >= 0x40 && b <= 0x7e }

// splitStyledCells splits styled text at the cell boundary before `cells`
// display cells, carrying the styling across the split so both halves render
// exactly as the unsplit string would.
func splitStyledCells(s string, cells int) (head, tail string) {
	used := 0
	cut := false
	var h, t strings.Builder
	for _, run := range parseStyled(s) {
		if cut {
			t.WriteString(run.codes)
			t.WriteString(run.text)
			continue
		}
		w := runewidth.StringWidth(run.text)
		if used+w <= cells {
			h.WriteString(run.codes)
			h.WriteString(run.text)
			used += w
			continue
		}
		// The run straddles the boundary: split its text on a cell edge and
		// re-apply its styling on the tail side.
		skip := cells - used
		byteIdx := 0
		seen := 0
		for byteIdx < len(run.text) {
			r, size := utf8.DecodeRuneInString(run.text[byteIdx:])
			rw := runewidth.RuneWidth(r)
			if seen+rw > skip {
				break
			}
			seen += rw
			byteIdx += size
		}
		h.WriteString(run.codes)
		h.WriteString(run.text[:byteIdx])
		h.WriteString(resetSeq)
		t.WriteString(run.codes)
		t.WriteString(run.text[byteIdx:])
		cut = true
	}
	return h.String(), t.String()
}

func clamp01(v float64) float64 {
	return math.Max(0, math.Min(1, v))
}

func clampInt(v, lo, hi int) int {
	return max(lo, min(hi, v))
}
