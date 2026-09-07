// Package logfile gives beads-tui a durable, timestamped trail on disk.
//
// The TUI owns the terminal: anything written to stderr is either painted over
// by the alt screen or lost outright when the host window closes (a Herdr
// `prefix + h` popup dies with its pane). Bubbletea makes that worse by
// recovering panics itself - it prints the stack to stderr and returns
// ErrProgramPanic - so a crash leaves no trace at all. Init points both the
// standard logger and os.Stderr at a file under the XDG state directory, so
// crashes, panics, and error paths survive the process that produced them.
//
// The log is a diagnostic trail only: errors, panics, and stack traces. Issue
// titles, descriptions, and other bead content are never written here.
package logfile

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// maxSize caps the live log; crossing it rotates the file to <name>.old, so
// the on-disk cost stays bounded at roughly 2 MiB while the previous session's
// crash context survives one restart.
const maxSize = 1 << 20

// Path reports where the log lives: $BEADS_TUI_LOG_DIR, else
// $XDG_STATE_HOME/beads-tui, else ~/.local/state/beads-tui.
func Path() (string, error) {
	if dir := strings.TrimSpace(os.Getenv("BEADS_TUI_LOG_DIR")); dir != "" {
		return filepath.Join(dir, "beads-tui.log"), nil
	}
	if dir := strings.TrimSpace(os.Getenv("XDG_STATE_HOME")); dir != "" {
		return filepath.Join(dir, "beads-tui", "beads-tui.log"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home directory: %w", err)
	}
	return filepath.Join(home, ".local", "state", "beads-tui", "beads-tui.log"), nil
}

// Init opens the log, redirects the standard logger and os.Stderr to it, and
// records a start line. The returned func restores stderr and closes the file;
// it is safe to call even when Init failed. A logging failure is never fatal -
// the caller keeps its original stderr and runs unlogged.
func Init(version string) (path string, done func(), err error) {
	restore := func() {}
	path, err = Path()
	if err != nil {
		return "", restore, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return path, restore, fmt.Errorf("create log directory: %w", err)
	}
	rotate(path)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return path, restore, fmt.Errorf("open log: %w", err)
	}

	prevOut, prevFlags, prevErr := log.Writer(), log.Flags(), os.Stderr
	log.SetOutput(f)
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	// Bubbletea's panic handler and the runtime write stack traces to
	// os.Stderr; point it at the file so those land next to our own lines.
	os.Stderr = f
	log.Printf("start: beads-tui %s pid=%d", version, os.Getpid())

	return path, func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
		os.Stderr = prevErr
		_ = f.Close()
	}, nil
}

// Recover logs a panic with its stack and re-panics, so a crash is durable
// without changing the program's exit behaviour. Use as `defer Recover()`.
func Recover() {
	if r := recover(); r != nil {
		log.Printf("panic: %v\n%s", r, debug.Stack())
		panic(r)
	}
}

// rotate moves an oversized log aside, keeping exactly one generation.
func rotate(path string) {
	if fi, err := os.Stat(path); err == nil && fi.Size() > maxSize {
		_ = os.Rename(path, path+".old")
	}
}

// Guard wraps a Bubbletea model so a panic in Init/Update/View is logged with
// its value and stack before Bubbletea's own handler takes it. That handler
// prints the panic value to stdout - which a dying popup discards - and does
// not re-panic, so without this the value is lost and only the stack (via the
// redirected stderr) survives. Guard re-panics, leaving Bubbletea's terminal
// restore intact.
func Guard(m tea.Model) tea.Model { return guard{m} }

type guard struct{ tea.Model }

func (g guard) Init() tea.Cmd {
	defer Recover()
	return g.Model.Init()
}

func (g guard) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	defer Recover()
	inner, cmd := g.Model.Update(msg)
	return guard{inner}, cmd
}

func (g guard) View() string {
	defer Recover()
	return g.Model.View()
}
