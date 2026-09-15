package tui

import (
	"context"
	"log"
	"os/exec"
	"strings"
	"unicode/utf8"

	"github.com/RooseveltAdvisors/beads-tui/internal/bd"
	tea "github.com/charmbracelet/bubbletea"
)

const commentNotifyPreviewRunes = 80

// NotifyPlan is the command we would send to an assignee's live session.
type NotifyPlan struct {
	Kind    SessionKind
	Target  string
	Message string
}

var liveSessionsFn = discoverLiveSessions
var notifyRun = func(name string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), sessionProbeTimeout)
	defer cancel()
	return exec.CommandContext(ctx, name, args...).Run()
}

func sameActor(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

func truncateCommentPreview(text string) string {
	text = strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	if utf8.RuneCountInString(text) <= commentNotifyPreviewRunes {
		return text
	}
	runes := []rune(text)
	return string(runes[:commentNotifyPreviewRunes])
}

func commentNotifyMessage(id, text string) string {
	preview := truncateCommentPreview(text)
	if preview == "" {
		return "New comment on " + id
	}
	return "New comment on " + id + ": " + preview
}

// PlanCommentNotify returns a notification when the comment author is not the
// assignee and a live Herdr/tmux session matches the assignee. Nil means skip.
func PlanCommentNotify(author, assignee, id, text string, sessions []LiveSession) *NotifyPlan {
	assignee = strings.TrimSpace(assignee)
	if assignee == "" {
		return nil
	}
	if sameActor(author, assignee) {
		return nil
	}
	hit := MatchAssignee(assignee, sessions)
	if hit == nil {
		return nil
	}
	return &NotifyPlan{
		Kind:    hit.Kind,
		Target:  hit.Name,
		Message: commentNotifyMessage(id, text),
	}
}

func notifyCommand(plan NotifyPlan) (string, []string) {
	switch plan.Kind {
	case sessionHerdr:
		return "herdr", []string{"agent", "prompt", plan.Target, plan.Message}
	default:
		return "tmux", []string{"send-keys", "-t", plan.Target, "-l", plan.Message}
	}
}

func deliverCommentNotify(plan *NotifyPlan) {
	if plan == nil {
		return
	}
	name, args := notifyCommand(*plan)
	if err := notifyRun(name, args...); err != nil {
		log.Printf("beads-tui: assignee notify failed (id target=%s kind=%s): %v", plan.Target, plan.Kind, err)
		return
	}
	if plan.Kind == sessionTmux {
		if err := notifyRun("tmux", "send-keys", "-t", plan.Target, "Enter"); err != nil {
			log.Printf("beads-tui: assignee notify enter failed (target=%s): %v", plan.Target, err)
		}
	}
}

func (m Model) commentAuthor() string {
	if strings.TrimSpace(m.actor) != "" {
		return strings.TrimSpace(m.actor)
	}
	return bd.ResolveActor()
}

func (m Model) notifyAssigneeCmd(id, text string) tea.Cmd {
	author := m.commentAuthor()
	assignee := ""
	if iss := m.commentIssue(); iss != nil {
		assignee = iss.Assignee
	}
	return func() tea.Msg {
		plan := PlanCommentNotify(author, assignee, id, text, liveSessionsFn())
		deliverCommentNotify(plan)
		return nil
	}
}
