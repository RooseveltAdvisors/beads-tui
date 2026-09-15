package tui

import (
	"os"
	"strings"
	"testing"

	"github.com/RooseveltAdvisors/beads-tui/internal/bd"
)

func TestMain(m *testing.M) {
	sessionLookPath = func(string) (string, error) { return "", os.ErrNotExist }
	os.Exit(m.Run())
}

func TestParseHerdrSessionsSkipsStopped(t *testing.T) {
	raw := []byte(`{"sessions":[
		{"name":"wiseman","running":true},
		{"name":"ztest","running":false},
		{"name":"","running":true}
	]}`)
	got := parseHerdrSessions(raw)
	if len(got) != 1 || got[0].Name != "wiseman" || got[0].Kind != sessionHerdr {
		t.Fatalf("got %#v", got)
	}
}

func TestParseTmuxSessions(t *testing.T) {
	got := parseTmuxSessions([]byte("dotfiles\nsecond-brain\n\n"))
	if len(got) != 2 || got[0].Name != "dotfiles" || got[1].Kind != sessionTmux {
		t.Fatalf("got %#v", got)
	}
}

func TestMatchAssigneeExactPrefersHerdr(t *testing.T) {
	sessions := []LiveSession{
		{Kind: sessionTmux, Name: "wiseman"},
		{Kind: sessionHerdr, Name: "Wiseman"},
	}
	got := MatchAssignee("wiseman", sessions)
	if got == nil || got.Kind != sessionHerdr {
		t.Fatalf("got %#v", got)
	}
}

func TestMatchAssigneeExactTmux(t *testing.T) {
	sessions := []LiveSession{{Kind: sessionTmux, Name: "dotfiles"}}
	got := MatchAssignee("dotfiles", sessions)
	if got == nil || got.Kind != sessionTmux {
		t.Fatalf("got %#v", got)
	}
}

func TestMatchAssigneeSubstringPrefersTighterName(t *testing.T) {
	sessions := []LiveSession{
		{Kind: sessionHerdr, Name: "wiseman-lab"},
		{Kind: sessionHerdr, Name: "wiseman"},
	}
	got := MatchAssignee("wise", sessions)
	if got == nil || got.Name != "wiseman" {
		t.Fatalf("got %#v", got)
	}
}

func TestMatchAssigneeEmptyOrMissingIsNil(t *testing.T) {
	sessions := []LiveSession{{Kind: sessionHerdr, Name: "wiseman"}}
	if MatchAssignee("", sessions) != nil {
		t.Fatal("empty assignee matched")
	}
	if MatchAssignee("nobody", sessions) != nil {
		t.Fatal("unknown assignee matched")
	}
	if MatchAssignee("wiseman", nil) != nil {
		t.Fatal("empty session list matched")
	}
}

func TestAttachSessionCmd(t *testing.T) {
	h := attachSessionCmd(LiveSession{Kind: sessionHerdr, Name: "wiseman"})
	if h.Path == "" || len(h.Args) < 4 || h.Args[1] != "session" || h.Args[2] != "attach" || h.Args[3] != "wiseman" {
		t.Fatalf("herdr cmd = %q %q", h.Path, h.Args)
	}
	tm := attachSessionCmd(LiveSession{Kind: sessionTmux, Name: "dotfiles"})
	if len(tm.Args) < 4 || tm.Args[1] != "attach-session" || tm.Args[3] != "dotfiles" {
		t.Fatalf("tmux cmd = %q", tm.Args)
	}
}

func TestAttachSelectedSessionNoMatchIsNoop(t *testing.T) {
	m := New(nil)
	m.rows = []bd.Issue{{ID: "x", Assignee: "nobody-here"}}
	m.selected = 0
	if cmd := m.attachSelectedSession(); cmd != nil {
		t.Fatal("expected silent no-op")
	}
}

func TestAssigneeChipLiveMark(t *testing.T) {
	plain := stripANSI(assigneeChipLive("wiseman", true))
	if !strings.Contains(plain, liveSessionMark) || !strings.Contains(plain, "@wiseman") {
		t.Fatalf("live chip = %q", plain)
	}
	if strings.Contains(stripANSI(assigneeChip("wiseman")), liveSessionMark) {
		t.Fatal("idle chip carried live mark")
	}
}

func TestMatchAssigneeAssigneeContainsSessionName(t *testing.T) {
	sessions := []LiveSession{{Kind: sessionTmux, Name: "pi"}}
	got := MatchAssignee("pi-worker", sessions)
	if got == nil || got.Name != "pi" {
		t.Fatalf("got %#v", got)
	}
}
