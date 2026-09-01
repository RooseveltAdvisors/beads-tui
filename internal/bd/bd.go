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
)

// DefaultLookPath and DefaultRun are the production wiring for a Client.
var (
	DefaultLookPath = exec.LookPath
	DefaultRun      = runCommand
)

// Client executes the `bd` CLI. Dependencies are injectable so tests can
// exercise parsing and error handling without a real Beads install.
type Client struct {
	lookPath func(file string) (string, error)
	run      func(ctx context.Context, path string, args ...string) (stdout, stderr string, err error)
}

const depsBatchWorkers = 8

// New returns a Client wired to exec `bd` from PATH, inheriting the ambient
// environment (BEADS_DIR and friends) so the store resolves exactly as it
// would for the user.
func New() *Client {
	return &Client{lookPath: DefaultLookPath, run: DefaultRun}
}

// List returns the current board for the given view.
func (c *Client) List(ctx context.Context, view View) ([]Issue, error) {
	if !view.Valid() {
		return nil, fmt.Errorf("beads-tui: unsupported view %q", view)
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
func (c *Client) jsonCall(ctx context.Context, out any, args ...string) error {
	path, err := c.lookPath("bd")
	if err != nil {
		return errors.New("bd not found in PATH; install Beads first (https://github.com/steveyegge/beads)")
	}
	stdout, stderr, err := c.run(ctx, path, args...)
	cmdDesc := "bd " + strings.Join(args, " ")
	if err != nil {
		return fmt.Errorf("%s: %s", cmdDesc, c.hint(stderr, err))
	}
	if err := json.Unmarshal([]byte(stdout), out); err != nil {
		msg := strings.TrimSpace(stderr)
		if msg == "" {
			msg = fmt.Sprintf("unexpected output (not JSON): %v", err)
		}
		return fmt.Errorf("%s: %s", cmdDesc, c.hint(msg, err))
	}
	return nil
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
	cmd := exec.CommandContext(ctx, path, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}
