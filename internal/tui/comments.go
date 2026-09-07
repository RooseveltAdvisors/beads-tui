package tui

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/RooseveltAdvisors/beads-tui/internal/bd"
	tea "github.com/charmbracelet/bubbletea"
)

func (m *Model) openComments(focusInput bool) tea.Cmd {
	id := m.selectedID()
	if id == "" {
		return nil
	}
	m.commentsOpen = true
	m.commentsID = id
	m.comments = nil
	m.commentsErr = ""
	m.commentsLoading = true
	m.commentsOffset = 0
	m.commentsSubmitting = false
	m.commentsGeneration++
	m.commentsInput.SetValue("")
	m.commentsInputActive = focusInput
	var focus tea.Cmd
	if focusInput {
		focus = m.commentsInput.Focus()
	}
	load := m.loadCommentsCmd(id, m.commentsGeneration)
	if focus != nil {
		return tea.Batch(focus, load)
	}
	return load
}

func (m *Model) closeComments() {
	m.commentsOpen = false
	m.commentsInputActive = false
	m.commentsSubmitting = false
	m.commentsInput.Blur()
	m.commentsInput.SetValue("")
	m.commentsErr = ""
	m.commentsLoading = false
}

func (m Model) commentsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if m.help {
		switch key {
		case "j", "down":
			m.helpOffset = min(m.helpOffset+1, m.helpMaxOffset())
			return m, nil
		case "k", "up":
			m.helpOffset = max(m.helpOffset-1, 0)
			return m, nil
		}
		m.help = false
		m.helpOffset = 0
		return m, nil
	}
	if m.commentsInputActive {
		if m.commentsSubmitting {
			return m, nil
		}
		switch key {
		case "esc":
			m.commentsInputActive = false
			m.commentsInput.Blur()
			m.commentsErr = ""
			return m, nil
		case "enter":
			text := strings.TrimSpace(m.commentsInput.Value())
			if text == "" {
				m.commentsErr = "Comment cannot be empty"
				return m, nil
			}
			m.commentsSubmitting = true
			m.commentsInput.Blur()
			return m, m.submitCommentCmd(m.commentsID, text)
		}
		var cmd tea.Cmd
		m.commentsInput, cmd = m.commentsInput.Update(msg)
		return m, cmd
	}
	switch key {
	case "esc", "q":
		m.closeComments()
		return m, nil
	case "r":
		m.commentsErr = ""
		m.commentsLoading = true
		m.commentsGeneration++
		return m, m.loadCommentsCmd(m.commentsID, m.commentsGeneration)
	case "a":
		m.commentsInputActive = true
		m.commentsErr = ""
		return m, m.commentsInput.Focus()
	case "j", "down":
		m.commentsOffset++
	case "k", "up":
		m.commentsOffset--
	case "g":
		m.commentsOffset = 0
	case "G":
		m.commentsOffset = m.commentsMaxOffset()
	default:
		return m, nil
	}
	m.commentsOffset = max(0, min(m.commentsOffset, m.commentsMaxOffset()))
	return m, nil
}

func (m Model) loadCommentsCmd(id string, generation uint64) tea.Cmd {
	backend := m.backend
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), boardRetryTimeout)
		defer cancel()
		comments, err := backend.Comments(ctx, id)
		return commentsMsg{id: id, generation: generation, comments: comments, err: err}
	}
}

func (m Model) submitCommentCmd(id, text string) tea.Cmd {
	backend := m.backend
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), boardRetryTimeout)
		defer cancel()
		err := backend.AddComment(ctx, id, text)
		return commentSubmitMsg{id: id, text: text, err: err}
	}
}

func (m *Model) applyComments(msg commentsMsg) tea.Cmd {
	if !m.commentsOpen || msg.id != m.commentsID || msg.generation != m.commentsGeneration {
		return nil
	}
	m.commentsLoading = false
	if msg.err != nil {
		log.Printf("beads-tui: comments load failed (id=%s): %v", msg.id, msg.err)
		m.commentsErr = msg.err.Error()
		return nil
	}
	m.commentsErr = ""
	m.comments = sortComments(msg.comments)
	m.updateCommentCount(msg.id, len(m.comments))
	m.commentsOffset = m.commentsMaxOffset()
	return nil
}

func (m *Model) applyCommentSubmit(msg commentSubmitMsg) tea.Cmd {
	if !m.commentsOpen || msg.id != m.commentsID {
		return nil
	}
	m.commentsSubmitting = false
	if msg.err != nil {
		log.Printf("beads-tui: comment add failed (id=%s): %v", msg.id, msg.err)
		m.commentsErr = msg.err.Error()
		m.commentsInputActive = true
		m.commentsInput.SetValue(msg.text)
		return m.commentsInput.Focus()
	}
	m.commentsErr = ""
	m.commentsInputActive = false
	m.commentsInput.SetValue("")
	m.commentsLoading = true
	m.commentsGeneration++
	return m.loadCommentsCmd(msg.id, m.commentsGeneration)
}

func sortComments(comments []bd.Comment) []bd.Comment {
	comments = append([]bd.Comment(nil), comments...)
	sort.SliceStable(comments, func(i, j int) bool {
		left, leftErr := time.Parse(time.RFC3339Nano, comments[i].CreatedAt)
		right, rightErr := time.Parse(time.RFC3339Nano, comments[j].CreatedAt)
		if leftErr != nil || rightErr != nil {
			return false
		}
		return left.Before(right)
	})
	return comments
}

func (m Model) commentIssue() *bd.Issue {
	for _, issue := range m.rows {
		if issue.ID == m.commentsID {
			copy := issue
			return &copy
		}
	}
	if m.detail != nil && m.detail.ID == m.commentsID {
		copy := *m.detail
		return &copy
	}
	return &bd.Issue{ID: m.commentsID}
}

func (m Model) commentsThreadLines(width int) []string {
	issue := m.commentIssue()
	lines := []string{styleBold.Render(orDash(issue.Title)), styleDim.Render("ID " + issue.ID), ""}
	switch {
	case m.commentsLoading && len(m.comments) == 0:
		lines = append(lines, styleDim.Render("Loading comments…"))
	case m.commentsErr != "" && len(m.comments) == 0:
		lines = append(lines, styleError.Render("✗ Could not load comments."))
		lines = append(lines, styleDim.Render(m.commentsErr))
		lines = append(lines, styleDim.Render("Retry with r."))
	case len(m.comments) == 0:
		lines = append(lines, styleDim.Render("No comments - press a to add"))
	default:
		for _, comment := range m.comments {
			author := comment.Author
			if author == "" {
				author = comment.CreatedBy
			}
			meta := orDash(author) + " · " + relativeCommentTime(comment.CreatedAt)
			lines = append(lines, styleSection.Render(meta))
			text := strings.TrimSpace(comment.Text)
			if text == "" {
				text = "(empty comment)"
			}
			for _, line := range wrapText(text, max(1, width-2)) {
				lines = append(lines, "  "+line)
			}
			lines = append(lines, "")
		}
	}
	if m.commentsErr != "" && len(m.comments) > 0 {
		lines = append(lines, styleError.Render("✗ "+truncate(m.commentsErr, width)))
	}
	return lines
}

func (m Model) commentsMaxOffset() int {
	w, h := m.width, m.height
	if w <= 0 {
		w = 80
	}
	if h <= 0 {
		h = 24
	}
	visible := h - 2
	if m.commentsInputActive {
		visible--
	}
	return max(0, len(m.commentsThreadLines(max(1, w-2)))-max(1, visible))
}

func (m Model) renderComments() string {
	w, h := m.width, m.height
	if w <= 0 {
		w = 80
	}
	if h <= 0 {
		h = 24
	}
	inner := max(1, w-2)
	content := m.commentsThreadLines(inner)
	visible := max(1, h-2)
	inputLine := ""
	if m.commentsInputActive {
		visible = max(1, visible-1)
		inputLine = m.commentsInput.View()
	}
	maxOffset := max(0, len(content)-visible)
	offset := max(0, min(m.commentsOffset, maxOffset))
	shown := content[offset:]
	if len(shown) > visible {
		shown = shown[:visible]
	}
	lines := append([]string(nil), shown...)
	if inputLine != "" {
		lines = append(lines, inputLine)
	}
	title := "Comments · esc/q back"
	return strings.Join(pane(title, lines, w, h), "\n")
}

func relativeCommentTime(raw string) string {
	t, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return "unknown time"
	}
	delta := time.Since(t)
	if delta < 0 {
		delta = 0
	}
	switch {
	case delta < time.Minute:
		return "just now"
	case delta < time.Hour:
		return formatRelative(int(delta/time.Minute), "m") + " ago"
	case delta < 24*time.Hour:
		return formatRelative(int(delta/time.Hour), "h") + " ago"
	default:
		return formatRelative(int(delta/(24*time.Hour)), "d") + " ago"
	}
}

func formatRelative(value int, unit string) string {
	return fmt.Sprintf("%d%s", value, unit)
}

func (m *Model) updateCommentCount(id string, count int) {
	update := func(issue *bd.Issue) {
		if issue.ID == id {
			issue.CommentCount = count
		}
	}
	for i := range m.allRows {
		update(&m.allRows[i])
	}
	for i := range m.rows {
		update(&m.rows[i])
	}
	for i := range m.treeRows {
		update(&m.treeRows[i].Issue)
	}
	for i := range m.graphRows {
		update(&m.graphRows[i])
	}
	if m.detail != nil {
		update(m.detail)
	}
	for key, entry := range m.detailCache {
		if entry.issue.ID == id {
			entry.issue.CommentCount = count
			m.detailCache[key] = entry
		}
	}
	if m.lastSnapshot != nil {
		for i := range m.lastSnapshot.Issues {
			update(&m.lastSnapshot.Issues[i])
		}
	}
}
