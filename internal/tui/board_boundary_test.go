package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RooseveltAdvisors/beads-tui/internal/bd"
)

const (
	boardProbeEnv    = "BEADS_TUI_BOARD_PROBE"
	boardProbePrefix = "BOARD_PROBE:"
	fixtureLogEnv    = "BEADS_TUI_FIXTURE_LOG"
	fixtureJSONEnv   = "BEADS_TUI_READY_JSON"
)

type boardProbe struct {
	CWD                string `json:"cwd"`
	BeadsDir           string `json:"beads_dir"`
	BeadsDirSet        bool   `json:"beads_dir_set"`
	IssueCount         int    `json:"issue_count"`
	RowCount           int    `json:"row_count"`
	BoardError         string `json:"board_error"`
	RenderedError      bool   `json:"rendered_error"`
	RenderedRoot       bool   `json:"rendered_root"`
	HasNestedChild     bool   `json:"has_nested_child"`
	RootDependentCount int    `json:"root_dependent_count"`
}

type fixtureCall struct {
	cwd      string
	beadsDir string
	args     string
}

func TestBoardProbeHelper(t *testing.T) {
	if os.Getenv(boardProbeEnv) != "1" {
		return
	}
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	probe := boardProbe{CWD: cwd}
	probe.BeadsDir, probe.BeadsDirSet = os.LookupEnv("BEADS_DIR")
	m := New(bd.New())
	msg := m.loadBoardCmd()()
	board, ok := msg.(boardMsg)
	if !ok {
		fmt.Fprintf(os.Stderr, "loadBoardCmd returned %T\n", msg)
		os.Exit(1)
	}
	probe.IssueCount = len(board.issues)
	updated, _ := m.Update(board)
	applied, ok := updated.(Model)
	if !ok {
		fmt.Fprintf(os.Stderr, "board update returned %T\n", updated)
		os.Exit(1)
	}
	if board.err != nil {
		probe.BoardError = board.err.Error()
		view := stripANSI(applied.View())
		probe.RenderedError = strings.Contains(view, "Could not load board") && strings.Contains(view, "BEADS_DIR")
	} else {
		probe.RowCount = len(applied.rows)
		probe.RenderedRoot = strings.Contains(stripANSI(applied.View()), "Fleet task 00")
		for _, row := range applied.treeRows {
			if row.Issue.ID == "fm-01" && row.Prefix != "" {
				probe.HasNestedChild = true
			}
		}
		for _, row := range applied.rows {
			if row.ID == "fm-00" {
				probe.RootDependentCount = row.DependentCount
			}
		}
	}
	payload, err := json.Marshal(probe)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("%s%s\n", boardProbePrefix, payload)
	os.Exit(0)
}

func TestEmptyWorkspaceUsesRealBdLoadingPath(t *testing.T) {
	if _, err := exec.LookPath("bd"); err != nil {
		t.Skipf("bd is required for workspace discovery coverage: %v", err)
	}
	emptyCWD := t.TempDir()
	env := withoutEnv("BEADS_DIR", boardProbeEnv, fixtureLogEnv, fixtureJSONEnv)
	env = append(env, boardProbeEnv+"=1")
	probe := runBoardProbe(t, emptyCWD, env)
	if probe.CWD != emptyCWD {
		t.Fatalf("probe cwd = %q, want %q", probe.CWD, emptyCWD)
	}
	if probe.BeadsDirSet {
		t.Fatalf("empty workspace probe inherited BEADS_DIR=%q", probe.BeadsDir)
	}
	if probe.IssueCount != 0 || probe.RowCount != 0 {
		t.Fatalf("empty workspace loaded issues=%d rows=%d", probe.IssueCount, probe.RowCount)
	}
	if !strings.Contains(probe.BoardError, "bd list --ready --json -n 0") {
		t.Errorf("workspace error %q missing the ready load command", probe.BoardError)
	}
	lowerError := strings.ToLower(probe.BoardError)
	if !strings.Contains(lowerError, "beads") || !strings.Contains(probe.BoardError, "BEADS_DIR") {
		t.Errorf("workspace error %q missing a workspace cause and BEADS_DIR hint", probe.BoardError)
	}
	if !probe.RenderedError {
		t.Fatalf("workspace error was not rendered as a loud actionable board error")
	}
}

func TestPopulatedReadyFixtureUsesRealBdAndGraphLoadingPath(t *testing.T) {
	issues := populatedReadyFixture()
	workspace, beadsDir, fixtureDir, logPath := installBoardFixture(t, issues)
	env := withoutEnv("BEADS_DIR", boardProbeEnv, fixtureLogEnv, fixtureJSONEnv)
	env = append(env,
		boardProbeEnv+"=1",
		fixtureLogEnv+"="+logPath,
		fixtureJSONEnv+"="+mustJSON(t, issues),
		"BEADS_DIR="+beadsDir,
		"PATH="+fixtureDir+string(os.PathListSeparator)+os.Getenv("PATH"),
	)
	probe := runBoardProbe(t, workspace, env)
	if probe.CWD != workspace {
		t.Fatalf("probe cwd = %q, want %q", probe.CWD, workspace)
	}
	if !probe.BeadsDirSet || probe.BeadsDir != beadsDir {
		t.Fatalf("probe BEADS_DIR = %q (set=%v), want %q", probe.BeadsDir, probe.BeadsDirSet, beadsDir)
	}
	if probe.IssueCount != len(issues) || probe.RowCount != len(issues) {
		t.Fatalf("ready fixture loaded issues=%d rows=%d, want %d", probe.IssueCount, probe.RowCount, len(issues))
	}
	if !probe.RenderedRoot || !probe.HasNestedChild {
		t.Fatalf("ready fixture did not render the populated graph: %+v", probe)
	}
	if probe.RootDependentCount != 1 {
		t.Fatalf("root dependent count = %d, want 1", probe.RootDependentCount)
	}
	calls := readFixtureCalls(t, logPath)
	listCalls, upCalls, downCalls := 0, 0, 0
	for _, call := range calls {
		if call.cwd != workspace || call.beadsDir != beadsDir {
			t.Fatalf("bd call environment = cwd %q, BEADS_DIR %q; want %q, %q", call.cwd, call.beadsDir, workspace, beadsDir)
		}
		switch {
		case call.args == "list --ready --json -n 0":
			listCalls++
		case strings.HasPrefix(call.args, "dep list "):
			fields := strings.Fields(call.args)
			switch {
			case len(fields) == 6 && fields[3] == "--json" && fields[4] == "--direction" && fields[5] == "up":
				upCalls++
			case len(fields) == 4 && fields[3] == "--json":
				downCalls++
			default:
				t.Fatalf("unexpected dependency command: %q", call.args)
			}
		default:
			t.Fatalf("unexpected bd command: %q", call.args)
		}
	}
	if listCalls != 1 || upCalls != len(issues) || downCalls != 1 {
		t.Fatalf("bd calls list=%d up=%d down=%d, want 1 %d 1", listCalls, upCalls, downCalls, len(issues))
	}
}

func runBoardProbe(t *testing.T, cwd string, env []string) boardProbe {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestBoardProbeHelper$")
	cmd.Dir = cwd
	cmd.Env = env
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("board probe failed: %v\n%s", err, output)
	}
	for _, line := range strings.Split(string(output), "\n") {
		if !strings.HasPrefix(line, boardProbePrefix) {
			continue
		}
		var probe boardProbe
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, boardProbePrefix)), &probe); err != nil {
			t.Fatalf("decode board probe %q: %v", line, err)
		}
		return probe
	}
	t.Fatalf("board probe emitted no result: %s", output)
	return boardProbe{}
}

func populatedReadyFixture() []bd.Issue {
	issues := make([]bd.Issue, 69)
	for i := range issues {
		issues[i] = bd.Issue{
			ID:       fmt.Sprintf("fm-%02d", i),
			Title:    fmt.Sprintf("Fleet task %02d", i),
			Status:   "open",
			Priority: i % 4,
		}
	}
	issues[1].ParentID = issues[0].ID
	issues[1].DependencyCount = 1
	return issues
}

func installBoardFixture(t *testing.T, issues []bd.Issue) (workspace, beadsDir, fixtureDir, logPath string) {
	t.Helper()
	workspace = t.TempDir()
	beadsDir = filepath.Join(workspace, ".beads")
	if err := os.Mkdir(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	fixtureDir = t.TempDir()
	logPath = filepath.Join(fixtureDir, "bd-calls.log")
	script := `#!/bin/sh
set -eu
printf '%s\t%s\t%s\n' "$PWD" "${BEADS_DIR-}" "$*" >> "$BEADS_TUI_FIXTURE_LOG"
if [ "${1-}" = "list" ] && [ "${2-}" = "--ready" ] && [ "${3-}" = "--json" ] && [ "${4-}" = "-n" ] && [ "${5-}" = "0" ]; then
  printf '%s' "$BEADS_TUI_READY_JSON"
  exit 0
fi
if [ "${1-}" = "dep" ] && [ "${2-}" = "list" ]; then
  if [ "${3-}" = "fm-00" ] && [ "${4-}" = "--json" ] && [ "${5-}" = "--direction" ] && [ "${6-}" = "up" ]; then
    printf '%s' '[{"id":"fm-01","title":"Fleet task 01","status":"open","dependency_type":"blocks"}]'
  else
    printf '[]'
  fi
  exit 0
fi
printf 'unsupported bd command: %s\n' "$*" >&2
exit 1
`
	if err := os.WriteFile(filepath.Join(fixtureDir, "bd"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return workspace, beadsDir, fixtureDir, logPath
}

func readFixtureCalls(t *testing.T, path string) []fixtureCall {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var calls []fixtureCall
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) != 3 {
			t.Fatalf("malformed bd call log line %q", line)
		}
		calls = append(calls, fixtureCall{cwd: fields[0], beadsDir: fields[1], args: fields[2]})
	}
	return calls
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func withoutEnv(names ...string) []string {
	var env []string
	for _, entry := range os.Environ() {
		keep := true
		for _, name := range names {
			if strings.HasPrefix(entry, name+"=") {
				keep = false
				break
			}
		}
		if keep {
			env = append(env, entry)
		}
	}
	return env
}
