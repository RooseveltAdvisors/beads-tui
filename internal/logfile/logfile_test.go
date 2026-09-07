package logfile

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPathPrefersExplicitDirThenXDG(t *testing.T) {
	t.Setenv("BEADS_TUI_LOG_DIR", "/explicit")
	t.Setenv("XDG_STATE_HOME", "/xdg")
	got, err := Path()
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	if want := filepath.Join("/explicit", "beads-tui.log"); got != want {
		t.Fatalf("Path = %q, want %q", got, want)
	}

	t.Setenv("BEADS_TUI_LOG_DIR", "")
	got, err = Path()
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	if want := filepath.Join("/xdg", "beads-tui", "beads-tui.log"); got != want {
		t.Fatalf("Path = %q, want %q", got, want)
	}
}

// A crash must leave a timestamped trail even though the TUI owns the screen:
// the start line, anything logged, and anything written to stderr (which is
// where Bubbletea prints recovered panic stacks) all land in the file.
func TestInitCapturesLogAndStderr(t *testing.T) {
	t.Setenv("BEADS_TUI_LOG_DIR", t.TempDir())
	realStderr := os.Stderr
	path, done, err := Init("test-version")
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if os.Stderr == realStderr {
		t.Fatal("Init did not redirect stderr")
	}
	log.Printf("beads-tui: bd list failed")
	if _, err := os.Stderr.WriteString("goroutine 1 [running]:\n"); err != nil {
		t.Fatalf("write stderr: %v", err)
	}
	done()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	body := string(data)
	for _, want := range []string{"start: beads-tui test-version", "bd list failed", "goroutine 1 [running]:"} {
		if !strings.Contains(body, want) {
			t.Fatalf("log missing %q:\n%s", want, body)
		}
	}
	// Every line is timestamped by the standard logger's date prefix.
	if first, _, _ := strings.Cut(body, " "); len(first) != len("2006/01/02") {
		t.Fatalf("log line is not timestamped: %q", body)
	}
	if os.Stderr != realStderr || log.Writer() != realStderr {
		t.Fatal("done() did not restore stderr and the standard logger")
	}
}

func TestRecoverLogsPanicAndRepanics(t *testing.T) {
	t.Setenv("BEADS_TUI_LOG_DIR", t.TempDir())
	path, done, err := Init("test-version")
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Error("Recover swallowed the panic instead of re-panicking")
			}
		}()
		defer Recover()
		panic("boom")
	}()
	done()

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(string(body), "panic: boom") || !strings.Contains(string(body), "logfile.Recover") {
		t.Fatalf("log missing panic and stack:\n%s", body)
	}
}

func TestRotateKeepsOneGenerationOverCap(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BEADS_TUI_LOG_DIR", dir)
	path := filepath.Join(dir, "beads-tui.log")
	if err := os.WriteFile(path, make([]byte, maxSize+1), 0o600); err != nil {
		t.Fatalf("seed log: %v", err)
	}

	_, done, err := Init("test-version")
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	done()

	if _, err := os.Stat(path + ".old"); err != nil {
		t.Fatalf("oversized log was not rotated: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat log: %v", err)
	}
	if fi.Size() > maxSize {
		t.Fatalf("live log still oversized: %d bytes", fi.Size())
	}
}
