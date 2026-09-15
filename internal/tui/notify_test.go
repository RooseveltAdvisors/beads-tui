package tui

import (
	"strings"
	"testing"

	"github.com/RooseveltAdvisors/beads-tui/internal/bd"
)

func TestPlanCommentNotifySelfNotifyGuard(t *testing.T) {
	sessions := []LiveSession{{Kind: sessionHerdr, Name: "wiseman"}}
	if got := PlanCommentNotify("wiseman", "wiseman", "fm-aaa", "hello", sessions); got != nil {
		t.Fatalf("self-notify planned: %#v", got)
	}
	if got := PlanCommentNotify("WiseMan", "wiseman", "fm-aaa", "hello", sessions); got != nil {
		t.Fatalf("case-insensitive self-notify planned: %#v", got)
	}
}

func TestPlanCommentNotifyNoAssigneeOrSessionIsNil(t *testing.T) {
	sessions := []LiveSession{{Kind: sessionHerdr, Name: "wiseman"}}
	if PlanCommentNotify("captain", "", "fm-aaa", "hello", sessions) != nil {
		t.Fatal("empty assignee planned notify")
	}
	if PlanCommentNotify("captain", "wiseman", "fm-aaa", "hello", nil) != nil {
		t.Fatal("missing session planned notify")
	}
}

func TestPlanCommentNotifyHerdrMessageAndPreview(t *testing.T) {
	sessions := []LiveSession{{Kind: sessionHerdr, Name: "wiseman"}}
	long := strings.Repeat("x", 120)
	got := PlanCommentNotify("captain", "wiseman", "fm-aaa", long, sessions)
	if got == nil || got.Kind != sessionHerdr || got.Target != "wiseman" {
		t.Fatalf("got %#v", got)
	}
	if !strings.HasPrefix(got.Message, "New comment on fm-aaa: ") {
		t.Fatalf("message = %q", got.Message)
	}
	preview := strings.TrimPrefix(got.Message, "New comment on fm-aaa: ")
	if got := len([]rune(preview)); got != 80 {
		t.Fatalf("preview runes = %d", got)
	}
	name, args := notifyCommand(*got)
	if name != "herdr" || len(args) != 4 || args[0] != "agent" || args[1] != "prompt" || args[2] != "wiseman" {
		t.Fatalf("cmd = %s %q", name, args)
	}
}

func TestPlanCommentNotifyTmuxCommand(t *testing.T) {
	sessions := []LiveSession{{Kind: sessionTmux, Name: "dotfiles"}}
	got := PlanCommentNotify("captain", "dotfiles", "fm-bbb", "ping", sessions)
	if got == nil || got.Kind != sessionTmux {
		t.Fatalf("got %#v", got)
	}
	name, args := notifyCommand(*got)
	if name != "tmux" || args[0] != "send-keys" || args[2] != "dotfiles" {
		t.Fatalf("cmd = %s %q", name, args)
	}
}

func TestApplyCommentSubmitNotifiesAssignee(t *testing.T) {
	var ran [][]string
	notifyRun = func(name string, args ...string) error {
		ran = append(ran, append([]string{name}, args...))
		return nil
	}
	liveSessionsFn = func() []LiveSession {
		return []LiveSession{{Kind: sessionHerdr, Name: "wiseman"}}
	}
	t.Cleanup(func() {
		notifyRun = func(string, ...string) error { return nil }
		liveSessionsFn = discoverLiveSessions
	})

	m := New(nil)
	m.actor = "captain"
	m.commentsOpen = true
	m.commentsID = "fm-aaa"
	m.rows = []bd.Issue{{ID: "fm-aaa", Assignee: "wiseman"}}
	cmd := m.applyCommentSubmit(commentSubmitMsg{id: "fm-aaa", text: "please look"})
	if cmd == nil {
		t.Fatal("expected reload+notify batch")
	}
	notify := m.notifyAssigneeCmd("fm-aaa", "please look")
	notify()
	if len(ran) == 0 || ran[0][0] != "herdr" || ran[0][3] != "wiseman" {
		t.Fatalf("ran = %#v", ran)
	}
}

func TestApplyCommentSubmitSelfNotifyDoesNotRun(t *testing.T) {
	var ran int
	notifyRun = func(string, ...string) error {
		ran++
		return nil
	}
	liveSessionsFn = func() []LiveSession {
		return []LiveSession{{Kind: sessionHerdr, Name: "wiseman"}}
	}
	t.Cleanup(func() {
		notifyRun = func(string, ...string) error { return nil }
		liveSessionsFn = discoverLiveSessions
	})

	m := New(nil)
	m.actor = "wiseman"
	m.commentsOpen = true
	m.commentsID = "fm-aaa"
	m.rows = []bd.Issue{{ID: "fm-aaa", Assignee: "wiseman"}}
	m.notifyAssigneeCmd("fm-aaa", "note")()
	if ran != 0 {
		t.Fatalf("self-notify ran %d times", ran)
	}
}
