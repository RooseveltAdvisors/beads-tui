package bd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// DefaultLookPath and DefaultRun are the production wiring for a Client.
var (
	DefaultLookPath = exec.LookPath
	DefaultRun      = runCommand
	DefaultRunInput = runCommandInput
)

// Store-lock contention policy: every bd invocation gets one
// attemptTimeout budget per attempt and, when the store looks busy or
// locked, waits retryDelay and tries again - up to callAttempts total -
// before surfacing the busy error to the caller.
const (
	attemptTimeout = 8 * time.Second
	callAttempts   = 3
	retryDelay     = 2 * time.Second
)

// Client executes the `bd` CLI. Dependencies are injectable so tests can
// exercise parsing and error handling without a real Beads install.
type Client struct {
	lookPath func(file string) (string, error)
	run      func(ctx context.Context, path string, args ...string) (stdout, stderr string, err error)
	runInput func(ctx context.Context, path, input string, args ...string) (stdout, stderr string, err error)
	// waitOverride shortens the inter-attempt retry delay; tests set it so
	// retries stay fast. Zero (production) means the full retryDelay.
	waitOverride time.Duration
}

const depsBatchWorkers = 8

// New returns a Client wired to exec `bd` from PATH, inheriting the ambient
// environment (BEADS_DIR and friends) so the store resolves exactly as it
// would for the user.
func New() *Client {
	return &Client{lookPath: DefaultLookPath, run: DefaultRun, runInput: DefaultRunInput}
}

// List returns the current board for the given view.
func (c *Client) List(ctx context.Context, view View) ([]Issue, error) {
	if !view.Valid() {
		return nil, fmt.Errorf("beads-tui: unsupported view %q", view)
	}
	if view == ViewReady {
		// Claimable work: open issues with no active blockers and no defer.
		// The ready projection omits description/parent/labels, which the
		// graph snapshot (bd list --all) fills back in for the board.
		var issues []Issue
		if err := c.jsonCall(ctx, &issues, "list", "--ready", "--json", "-n", "0"); err != nil {
			return nil, err
		}
		return issues, nil
	}
	return c.ListStatus(ctx, string(view))
}

// ListStatus returns issues for any native or custom bd status.
func (c *Client) ListStatus(ctx context.Context, status string) ([]Issue, error) {
	status = strings.TrimSpace(status)
	if status == "" {
		return nil, errors.New("beads-tui: empty status")
	}
	var issues []Issue
	if err := c.jsonCall(ctx, &issues, "list", "--status", status, "--json", "-n", "0"); err != nil {
		return nil, err
	}
	return issues, nil
}

// ListAll returns the complete issue set for graph-wide metadata.
func (c *Client) ListAll(ctx context.Context) ([]Issue, error) {
	var issues []Issue
	if err := c.jsonCall(ctx, &issues, "list", "--all", "--json", "-n", "0"); err != nil {
		return nil, err
	}
	return issues, nil
}

// Show returns the full detail for one bead.
func (c *Client) Show(ctx context.Context, id string) (*Issue, error) {
	if id == "" {
		return nil, errors.New("beads-tui: empty bead id")
	}
	var records []Issue
	if err := c.jsonCall(ctx, &records, "show", id, "--json"); err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("bd show %s: no issue returned", id)
	}
	issue := records[0]
	return &issue, nil
}

// Deps returns the dependency edges touching id. With up=false this is the
// "depends on" direction (what id needs); with up=true it is the dependents
// (what needs id). Direction names mirror `bd dep list --direction`.
func (c *Client) Deps(ctx context.Context, id string, up bool) ([]DepRecord, error) {
	if id == "" {
		return nil, errors.New("beads-tui: empty bead id")
	}
	args := []string{"dep", "list", id, "--json"}
	if up {
		args = append(args, "--direction", "up")
	}
	var records []DepRecord
	if err := c.jsonCall(ctx, &records, args...); err != nil {
		return nil, err
	}
	return records, nil
}

// DepsBatch returns dependency edges grouped by their requested anchor IDs.
func (c *Client) DepsBatch(ctx context.Context, ids []string, up bool) (map[string][]DepRecord, error) {
	ids = uniqueIDs(ids)
	result := make(map[string][]DepRecord, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	type depResult struct {
		records []DepRecord
		err     error
	}
	results := make([]depResult, len(ids))
	jobs := make(chan int)
	workerCount := min(depsBatchWorkers, len(ids))
	var workers sync.WaitGroup
	workers.Add(workerCount)
	for range workerCount {
		go func() {
			defer workers.Done()
			for i := range jobs {
				results[i].records, results[i].err = c.Deps(ctx, ids[i], up)
			}
		}()
	}
	for i := range ids {
		jobs <- i
	}
	close(jobs)
	workers.Wait()

	var firstErr error
	for i, item := range results {
		result[ids[i]] = item.records
		if firstErr == nil && item.err != nil {
			firstErr = item.err
		}
	}
	return result, firstErr
}

// Statuses loads the status vocabulary (icons and categories).
func (c *Client) Statuses(ctx context.Context) ([]StatusInfo, error) {
	var resp struct {
		BuiltIn []StatusInfo `json:"built_in_statuses"`
		Custom  []StatusInfo `json:"custom_statuses"`
	}
	if err := c.jsonCall(ctx, &resp, "statuses", "--json"); err != nil {
		return nil, err
	}
	return append(resp.BuiltIn, resp.Custom...), nil
}

// Comments returns an issue's comments in the order provided by bd.
func (c *Client) Comments(ctx context.Context, id string) ([]Comment, error) {
	if id == "" {
		return nil, errors.New("beads-tui: empty bead id")
	}
	var comments []Comment
	if err := c.jsonCall(ctx, &comments, "comments", id, "--json"); err != nil {
		return nil, err
	}
	return comments, nil
}

// AddComment appends one comment to an issue through bd's stdin interface.
func (c *Client) AddComment(ctx context.Context, id, text string) error {
	if id == "" {
		return errors.New("beads-tui: empty bead id")
	}
	if strings.TrimSpace(text) == "" {
		return errors.New("beads-tui: empty comment")
	}
	return c.writeCall(ctx, text, "comment", id, "--stdin")
}

func uniqueIDs(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	unique := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}
	return unique
}

// jsonCall runs a read-only bd invocation, requires JSON on stdout, and
// translates failures into a single clean error carrying the actionable part
// of bd's own stderr. Raw dependency output never leaks to the caller.
//
// Under store lock contention bd hangs until the attempt deadline or exits
// with a busy/locked diagnostic. That is transient, so the call is retried
// with a bounded attempt/attempt/wait schedule (see the constants above)
// before the single sanitized busy error is surfaced.
func (c *Client) jsonCall(ctx context.Context, out any, args ...string) error {
	path, err := c.lookPath("bd")
	if err != nil {
		return errors.New("bd not found in PATH; install Beads first (https://github.com/steveyegge/beads)")
	}
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
			// A JSON parse failure is not transient; return immediately.
			if uerr := json.Unmarshal([]byte(stdout), out); uerr != nil {
				msg := strings.TrimSpace(stderr)
				if msg == "" {
					msg = fmt.Sprintf("unexpected output (not JSON): %v", uerr)
				}
				return fmt.Errorf("%s: %s", cmdDesc, c.hint(msg, uerr))
			}
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
			// The caller's own window expired while we waited to retry:
			// that is still a store timeout, so name it plainly. A plain
			// cancellation is reported as-is.
			if ctx.Err() == context.DeadlineExceeded {
				return fmt.Errorf("%s: timed out; the beads store is busy or locked", cmdDesc)
			}
			return fmt.Errorf("%s: %s", cmdDesc, c.hint(stderr, ctx.Err()))
		case <-time.After(delay):
		}
	}
}

// writeCall runs a mutating bd command with the same bounded busy-store
// retry policy as jsonCall. Its diagnostics are sanitized before reaching the
// TUI, while comment text is sent through stdin rather than command arguments.
func (c *Client) writeCall(ctx context.Context, input string, args ...string) error {
	path, err := c.lookPath("bd")
	if err != nil {
		return errors.New("bd not found in PATH; install Beads first (https://github.com/steveyegge/beads)")
	}
	if c.runInput == nil {
		return errors.New("beads-tui: bd stdin adapter unavailable")
	}
	cmdDesc := "bd " + strings.Join(args, " ")
	delay := c.waitOverride
	if delay <= 0 {
		delay = retryDelay
	}
	for attempt := 1; ; attempt++ {
		attemptCtx, cancel := context.WithTimeout(ctx, attemptTimeout)
		_, stderr, err := c.runInput(attemptCtx, path, input, args...)
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

// storeBusy reports whether a failed attempt looks like transient store
// lock contention: the attempt ran out of its own deadline (bd killed with
// SIGKILL while wedged on the lock), the run reported the deadline itself,
// or bd reported a busy/locked store on stderr.
func storeBusy(attemptCtx context.Context, err error, stderr string) bool {
	if attemptCtx.Err() == context.DeadlineExceeded || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	msg := strings.ToLower(stderr)
	return strings.Contains(msg, "lock") || strings.Contains(msg, "busy")
}

// hint picks a short, actionable diagnostic: bd's stderr when it has
// something to say, otherwise the underlying error.
func (c *Client) hint(stderr string, err error) string {
	if msg := strings.TrimSpace(stderr); msg != "" {
		return truncateSoft(msg, 400)
	}
	return truncateSoft(err.Error(), 400)
}

// truncateSoft shortens long diagnostics while keeping the tail (the part
// that usually carries the reason) intact.
func truncateSoft(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return "…" + s[len(s)-max:]
}

// runCommand is the production exec path used by DefaultRun.
func runCommand(ctx context.Context, path string, args ...string) (string, string, error) {
	return runCommandInput(ctx, path, "", args...)
}

func runCommandInput(ctx context.Context, path, input string, args ...string) (string, string, error) {
	cmd := exec.CommandContext(ctx, path, args...)
	if input != "" {
		cmd.Stdin = strings.NewReader(input)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}
