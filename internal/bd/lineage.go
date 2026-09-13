package bd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Actor on Client (optional) overrides env/git identity for --actor and lineage.

// SetActor records who is driving the TUI for audit stamps.
func (c *Client) SetActor(name string) {
	if c != nil {
		c.Actor = strings.TrimSpace(name)
	}
}

// ResolveActor picks the audit identity.
// Priority: Client.Actor > BEADS_ACTOR > BD_ACTOR > git user.name > USER > "unknown".
// Humans usually resolve via git/USER; agents should export BEADS_ACTOR=<seat>.
func ResolveActor() string {
	for _, k := range []string{"BEADS_ACTOR", "BD_ACTOR"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	if out, err := exec.Command("git", "config", "user.name").Output(); err == nil {
		if v := strings.TrimSpace(string(out)); v != "" {
			return v
		}
	}
	if v := strings.TrimSpace(os.Getenv("USER")); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv("USERNAME")); v != "" {
		return v
	}
	return "unknown"
}

func (c *Client) actor() string {
	if c != nil && strings.TrimSpace(c.Actor) != "" {
		return strings.TrimSpace(c.Actor)
	}
	return ResolveActor()
}

func (c *Client) withActor(args []string) []string {
	a := c.actor()
	if a == "" {
		return args
	}
	out := make([]string, 0, len(args)+2)
	out = append(out, "--actor", a)
	out = append(out, args...)
	return out
}

func lineageLine(kind, detail string, actor string) string {
	return fmt.Sprintf("tui-lineage %s %s actor=%q via=beads-tui", kind, detail, actor)
}

// CreateIssue creates a bead and stamps lineage with actor identity.
func (c *Client) CreateIssue(ctx context.Context, title, due string) (*Issue, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return nil, errors.New("beads-tui: empty title")
	}
	args := []string{"create", title, "--json"}
	if d := strings.TrimSpace(due); d != "" {
		args = append(args, "--due", d)
	}
	issue, err := c.createJSON(ctx, args...)
	if err != nil {
		return nil, err
	}
	_ = c.AddComment(ctx, issue.ID, lineageLine("create",
		fmt.Sprintf("title=%q due=%q", title, strings.TrimSpace(due)), c.actor()))
	return issue, nil
}

func (c *Client) createJSON(ctx context.Context, args ...string) (*Issue, error) {
	raw, err := c.jsonBytes(ctx, args...)
	if err != nil {
		return nil, err
	}
	var one Issue
	if err := json.Unmarshal(raw, &one); err == nil && strings.TrimSpace(one.ID) != "" {
		return &one, nil
	}
	var many []Issue
	if err := json.Unmarshal(raw, &many); err == nil && len(many) > 0 && strings.TrimSpace(many[0].ID) != "" {
		return &many[0], nil
	}
	return nil, fmt.Errorf("bd %s: unexpected create JSON", strings.Join(args, " "))
}

func (c *Client) jsonBytes(ctx context.Context, args ...string) ([]byte, error) {
	path, err := c.lookPath("bd")
	if err != nil {
		return nil, errors.New("bd not found in PATH; install Beads first (https://github.com/steveyegge/beads)")
	}
	args = c.withActor(args)
	cmdDesc := "bd " + strings.Join(args, " ")
	delay := c.waitOverride
	if delay <= 0 {
		delay = retryDelay
	}
	for attempt := 1; ; attempt++ {
		attemptCtx, cancel := context.WithTimeout(ctx, attemptTimeout)
		stdout, stderr, err := c.run(attemptCtx, path, args...)
		busy := storeBusy(attemptCtx, err, stderr)
		cancel()
		if err == nil {
			return []byte(stdout), nil
		}
		if !busy || attempt >= callAttempts {
			if busy {
				return nil, fmt.Errorf("%s: timed out; the beads store is busy or locked", cmdDesc)
			}
			return nil, fmt.Errorf("%s: %s", cmdDesc, c.hint(stderr, err))
		}
		select {
		case <-ctx.Done():
			if ctx.Err() == context.DeadlineExceeded {
				return nil, fmt.Errorf("%s: timed out; the beads store is busy or locked", cmdDesc)
			}
			return nil, fmt.Errorf("%s: %s", cmdDesc, c.hint(stderr, ctx.Err()))
		case <-time.After(delay):
		}
	}
}

// UpdateIssue updates fields, then stamps field:old→new lineage with actor.
func (c *Client) UpdateIssue(ctx context.Context, id string, fields map[string]string) error {
	if id == "" {
		return errors.New("beads-tui: empty bead id")
	}
	before, _ := c.Show(ctx, id)
	args := []string{"update", id}
	var parts []string
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := strings.TrimSpace(fields[k])
		if k == "" {
			continue
		}
		// allow empty due to clear via special token handled by caller
		args = append(args, "--"+k, v)
		parts = append(parts, fmt.Sprintf("%s:%s→%s", k, fieldSnapshot(before, k), emptyDash(v)))
	}
	if len(args) == 2 {
		return errors.New("beads-tui: no fields to update")
	}
	if err := c.mutCall(ctx, args...); err != nil {
		return err
	}
	line := lineageLine("update", strings.Join(parts, " "), c.actor())
	if err := c.AddComment(ctx, id, line); err != nil {
		return fmt.Errorf("updated %s but lineage comment failed: %w", id, err)
	}
	return nil
}

func emptyDash(s string) string {
	if s == "" {
		return "∅"
	}
	return s
}

func fieldSnapshot(issue *Issue, field string) string {
	if issue == nil {
		return "?"
	}
	switch field {
	case "title":
		return emptyDash(issue.Title)
	case "status":
		return emptyDash(issue.Status)
	case "assignee":
		return emptyDash(issue.Assignee)
	case "priority":
		return fmt.Sprintf("%d", issue.Priority)
	case "due":
		return emptyDash(issue.DueAt)
	case "description":
		d := issue.Description
		if len(d) > 40 {
			d = d[:40] + "…"
		}
		return emptyDash(d)
	default:
		return "?"
	}
}

// CloseIssue stamps lineage (with actor) then closes with reason.
func (c *Client) CloseIssue(ctx context.Context, id, reason string) error {
	if id == "" {
		return errors.New("beads-tui: empty bead id")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return errors.New("beads-tui: empty close reason")
	}
	_ = c.AddComment(ctx, id, lineageLine("close", "reason="+strconv.Quote(reason), c.actor()))
	return c.mutCall(ctx, "close", id, "--reason", reason)
}

// DeleteIssue stamps lineage then force-deletes.
func (c *Client) DeleteIssue(ctx context.Context, id string) error {
	if id == "" {
		return errors.New("beads-tui: empty bead id")
	}
	before, _ := c.Show(ctx, id)
	title := ""
	if before != nil {
		title = before.Title
	}
	_ = c.AddComment(ctx, id, lineageLine("delete", fmt.Sprintf("title=%q", title), c.actor()))
	return c.mutCall(ctx, "delete", id, "--force")
}

func (c *Client) mutCall(ctx context.Context, args ...string) error {
	path, err := c.lookPath("bd")
	if err != nil {
		return errors.New("bd not found in PATH; install Beads first (https://github.com/steveyegge/beads)")
	}
	if c.run == nil {
		return errors.New("beads-tui: bd runner unavailable")
	}
	args = c.withActor(args)
	cmdDesc := "bd " + strings.Join(args, " ")
	delay := c.waitOverride
	if delay <= 0 {
		delay = retryDelay
	}
	for attempt := 1; ; attempt++ {
		attemptCtx, cancel := context.WithTimeout(ctx, attemptTimeout)
		_, stderr, err := c.run(attemptCtx, path, args...)
		busy := storeBusy(attemptCtx, err, stderr)
		cancel()
		if err == nil {
			return nil
		}
		if !busy || attempt >= callAttempts {
			if busy {
				return fmt.Errorf("%s: timed out; the beads store is busy or locked", cmdDesc)
			}
			return fmt.Errorf("%s: %s", cmdDesc, c.hint(stderr, err))
		}
		select {
		case <-ctx.Done():
			if ctx.Err() == context.DeadlineExceeded {
				return fmt.Errorf("%s: timed out; the beads store is busy or locked", cmdDesc)
			}
			return fmt.Errorf("%s: %s", cmdDesc, c.hint(stderr, ctx.Err()))
		case <-time.After(delay):
		}
	}
}

// SetLabels replaces all labels on an issue and stamps lineage.
func (c *Client) SetLabels(ctx context.Context, id string, labels []string) error {
	if id == "" {
		return errors.New("beads-tui: empty bead id")
	}
	before, _ := c.Show(ctx, id)
	args := []string{"update", id}
	if len(labels) == 0 {
		// clear: set empty via single empty set-labels if supported; otherwise skip
		args = append(args, "--set-labels", "")
	} else {
		for _, l := range labels {
			l = strings.TrimSpace(l)
			if l != "" {
				args = append(args, "--set-labels", l)
			}
		}
	}
	if err := c.mutCall(ctx, args...); err != nil {
		return err
	}
	old := ""
	if before != nil {
		old = strings.Join(before.Labels, ",")
	}
	line := lineageLine("update", fmt.Sprintf("labels:%s→%s", emptyDash(old), emptyDash(strings.Join(labels, ","))), c.actor())
	_ = c.AddComment(ctx, id, line)
	return nil
}
