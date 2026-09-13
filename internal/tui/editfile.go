package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/RooseveltAdvisors/beads-tui/internal/bd"
	tea "github.com/charmbracelet/bubbletea"
)

// External $EDITOR edit (vim-native). e opens a text form of the bead;
// save+quit applies; quit without write cancels.

type editorDoneMsg struct {
	id      string
	path    string
	err     error
	canceled bool
}

func pickEditor() string {
	for _, k := range []string{"VISUAL", "EDITOR"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	for _, c := range []string{"nvim", "vim", "vi", "nano"} {
		if _, err := exec.LookPath(c); err == nil {
			return c
		}
	}
	return "vi"
}

func formatDueHuman(due string) string {
	due = strings.TrimSpace(due)
	if due == "" {
		return ""
	}
	for _, layout := range []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05",
		"2006-01-02",
	} {
		if t, err := time.Parse(layout, due); err == nil {
			return t.Local().Format("2006-01-02 15:04")
		}
		if t, err := time.ParseInLocation(layout, due, time.Local); err == nil {
			return t.Format("2006-01-02 15:04")
		}
	}
	return due
}

func beadEditDocument(iss *bd.Issue) string {
	if iss == nil {
		return ""
	}
	labels := strings.Join(iss.Labels, ", ")
	var b strings.Builder
	b.WriteString("# beads-tui edit — save & quit (:wq) to apply; quit! to cancel\n")
	b.WriteString("# id is read-only. due: human date (2026-09-14 15:04), +6h/+1d, or empty to clear\n")
	b.WriteString("# priority: 0-4 (0=highest). status: open|in_progress|blocked|deferred|closed|hold\n")
	b.WriteString("#\n")
	fmt.Fprintf(&b, "id: %s\n", iss.ID)
	fmt.Fprintf(&b, "title: %s\n", iss.Title)
	fmt.Fprintf(&b, "status: %s\n", iss.Status)
	fmt.Fprintf(&b, "assignee: %s\n", iss.Assignee)
	fmt.Fprintf(&b, "priority: %d\n", iss.Priority)
	fmt.Fprintf(&b, "due: %s\n", formatDueHuman(iss.DueAt))
	fmt.Fprintf(&b, "type: %s\n", iss.IssueType)
	fmt.Fprintf(&b, "labels: %s\n", labels)
	b.WriteString("\n--- description ---\n")
	b.WriteString(strings.TrimRight(iss.Description, "\n"))
	b.WriteString("\n\n--- notes ---\n")
	b.WriteString(strings.TrimRight(iss.Notes, "\n"))
	b.WriteString("\n")
	return b.String()
}

type parsedBeadEdit struct {
	ID          string
	Title       string
	Status      string
	Assignee    string
	Priority    string
	Due         string
	Type        string
	Labels      string
	Description string
	Notes       string
}

func parseBeadEditDocument(text string) (parsedBeadEdit, error) {
	var p parsedBeadEdit
	lines := strings.Split(text, "\n")
	section := "meta" // meta | description | notes
	var desc, notes []string
	for _, line := range lines {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "#") && section == "meta" {
			continue
		}
		if trim == "--- description ---" {
			section = "description"
			continue
		}
		if trim == "--- notes ---" {
			section = "notes"
			continue
		}
		switch section {
		case "description":
			desc = append(desc, line)
		case "notes":
			notes = append(notes, line)
		default:
			if trim == "" {
				continue
			}
			key, val, ok := strings.Cut(line, ":")
			if !ok {
				continue
			}
			key = strings.ToLower(strings.TrimSpace(key))
			val = strings.TrimSpace(val)
			switch key {
			case "id":
				p.ID = val
			case "title":
				p.Title = val
			case "status":
				p.Status = val
			case "assignee":
				p.Assignee = val
			case "priority":
				p.Priority = val
			case "due":
				p.Due = val
			case "type", "issue_type":
				p.Type = val
			case "labels":
				p.Labels = val
			}
		}
	}
	p.Description = strings.TrimRight(strings.Join(desc, "\n"), "\n")
	p.Notes = strings.TrimRight(strings.Join(notes, "\n"), "\n")
	if p.ID == "" {
		return p, fmt.Errorf("edit file missing id:")
	}
	if p.Title == "" {
		return p, fmt.Errorf("title cannot be empty")
	}
	return p, nil
}

func diffBeadFields(before *bd.Issue, after parsedBeadEdit) map[string]string {
	fields := map[string]string{}
	if before == nil {
		return fields
	}
	if after.Title != before.Title {
		fields["title"] = after.Title
	}
	if after.Status != "" && after.Status != before.Status {
		fields["status"] = after.Status
	}
	if after.Assignee != before.Assignee {
		fields["assignee"] = after.Assignee
	}
	if after.Priority != "" {
		want := after.Priority
		want = strings.TrimPrefix(strings.ToUpper(want), "P")
		cur := fmt.Sprintf("%d", before.Priority)
		if want != cur {
			fields["priority"] = after.Priority
		}
	}
	// due: compare humanized forms
	if formatDueHuman(after.Due) != formatDueHuman(before.DueAt) && after.Due != before.DueAt {
		fields["due"] = after.Due // may be empty clear — UpdateIssue needs support
	}
	if after.Description != strings.TrimRight(before.Description, "\n") {
		fields["description"] = after.Description
	}
	if after.Notes != strings.TrimRight(before.Notes, "\n") {
		fields["notes"] = after.Notes
	}
	// labels: only if string form changed
	curLabels := strings.Join(before.Labels, ", ")
	if after.Labels != curLabels {
		// bd update uses --set-labels or remove/add — check flags
		fields["labels"] = after.Labels
	}
	_ = after.Type // type changes rare; skip unless bd supports --type easily
	return fields
}

func (m *Model) openEditorEdit() tea.Cmd {
	id := m.selectedID()
	if id == "" {
		return nil
	}
	backend := m.backend
	return func() tea.Msg {
		type shower interface {
			Show(context.Context, string) (*bd.Issue, error)
		}
		s, ok := backend.(shower)
		if !ok {
			return editorDoneMsg{id: id, err: fmt.Errorf("backend cannot show issues")}
		}
		ctx, cancel := context.WithTimeout(context.Background(), boardRetryTimeout)
		defer cancel()
		iss, err := s.Show(ctx, id)
		if err != nil {
			return editorDoneMsg{id: id, err: err}
		}
		dir, err := os.MkdirTemp("", "beads-tui-edit-*")
		if err != nil {
			return editorDoneMsg{id: id, err: err}
		}
		path := filepath.Join(dir, id+".bead.txt")
		body := beadEditDocument(iss)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			return editorDoneMsg{id: id, err: err}
		}
		// Stash path+before on a message that tea.ExecProcess will follow
		return editorReadyMsg{id: id, path: path, before: *iss}
	}
}

type editorReadyMsg struct {
	id     string
	path   string
	before bd.Issue
}

func (m Model) runExternalEditor(path string, id string, before bd.Issue) tea.Cmd {
	editor := pickEditor()
	// shell form supports EDITOR="nvim -b" etc.
	cmd := exec.Command("sh", "-c", editor+` "$1"`, "sh", path)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		return editorDoneMsg{id: id, path: path, err: err}
	})
}

func (m *Model) applyEditorDone(msg editorDoneMsg) tea.Cmd {
	path := msg.path
	if path == "" {
		path = m.editorPath
	}
	id := msg.id
	if id == "" {
		id = m.editorID
	}
	before := m.editorBefore
	if path != "" {
		defer func() {
			_ = os.Remove(path)
			_ = os.Remove(filepath.Dir(path))
			m.editorPath, m.editorID, m.editorBefore = "", "", nil
		}()
	}
	if msg.err != nil {
		// non-zero exit (e.g. :cq) = cancel
		m.statusFlash = "edit aborted"
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		m.crudErr = err.Error()
		return nil
	}
	after, err := parseBeadEditDocument(string(data))
	if err != nil {
		m.crudErr = err.Error()
		return nil
	}
	if after.ID != "" && after.ID != id {
		m.crudErr = "id is read-only; do not change id:"
		return nil
	}
	fields := diffBeadFields(before, after)
	if len(fields) == 0 {
		m.statusFlash = "no changes"
		return nil
	}
	lab := ""
	if v, ok := fields["labels"]; ok {
		lab = v
		delete(fields, "labels")
	}
	return m.applyEditorFields(id, fields, lab)
}

func (m Model) applyEditorFields(id string, fields map[string]string, labels string) tea.Cmd {
	b := m.backend
	return func() tea.Msg {
		type updater interface {
			UpdateIssue(context.Context, string, map[string]string) error
		}
		u, ok := b.(updater)
		if !ok {
			return crudResultMsg{kind: "update", id: id, err: fmt.Errorf("no update")}
		}
		ctx, cancel := context.WithTimeout(context.Background(), boardRetryTimeout)
		defer cancel()
		if len(fields) > 0 {
			if err := u.UpdateIssue(ctx, id, fields); err != nil {
				return crudResultMsg{kind: "update", id: id, err: err}
			}
		}
		if labels != "" {
			if err := applySetLabels(ctx, b, id, labels); err != nil {
				return crudResultMsg{kind: "update", id: id, err: err}
			}
		}
		return crudResultMsg{kind: "update", id: id, err: nil, reload: true}
	}
}

func applySetLabels(ctx context.Context, b Backend, id, labelsCSV string) error {
	type mut interface {
		UpdateIssue(context.Context, string, map[string]string) error
	}
	// Prefer raw mut if client exposes it — use UpdateIssue with a pseudo field handled in lineage
	type labelSetter interface {
		SetLabels(context.Context, string, []string) error
	}
	if ls, ok := b.(labelSetter); ok {
		var labs []string
		for _, p := range strings.Split(labelsCSV, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				labs = append(labs, p)
			}
		}
		return ls.SetLabels(ctx, id, labs)
	}
	_ = id
	return nil // skip if unsupported
}
