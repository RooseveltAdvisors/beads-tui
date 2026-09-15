package tui

import (
	"os"
	"testing"
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

func TestMatchAssigneeEmptyOrMissingIsNil(t *testing.T) {
	sessions := []LiveSession{{Kind: sessionHerdr, Name: "wiseman"}}
	if MatchAssignee("", sessions) != nil {
		t.Fatal("empty assignee matched")
	}
	if MatchAssignee("nobody", sessions) != nil {
		t.Fatal("unknown assignee matched")
	}
}
