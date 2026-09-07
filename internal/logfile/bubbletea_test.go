package logfile

import (
	"bytes"
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// Bubbletea recovers panics itself - printing the value to stdout and the
// stack to stderr, then returning ErrProgramPanic instead of re-panicking - so
// a crash inside the event loop leaves nothing behind on its own. This pins
// both halves of the fix: Guard captures the panic value, and the redirected
// os.Stderr captures the stack Bubbletea prints.
type panicModel struct{}

func (panicModel) Init() tea.Cmd                       { return func() tea.Msg { return "go" } }
func (panicModel) Update(tea.Msg) (tea.Model, tea.Cmd) { panic("simulated tui crash") }
func (panicModel) View() string                        { return "" }

func TestBubbleteaPanicLandsInLog(t *testing.T) {
	t.Setenv("BEADS_TUI_LOG_DIR", t.TempDir())
	path, done, err := Init("test-version")
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	p := tea.NewProgram(Guard(panicModel{}), tea.WithInput(bytes.NewReader(nil)), tea.WithOutput(new(bytes.Buffer)))
	if _, runErr := p.Run(); runErr == nil {
		t.Fatal("expected a panic error from Run")
	}
	done()

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	for _, want := range []string{"panic: simulated tui crash", "logfile.panicModel.Update"} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("crash log missing %q:\n%s", want, body)
		}
	}
}
