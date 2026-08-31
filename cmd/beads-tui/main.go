// Command beads-tui renders the Beads board in the terminal: a read-only,
// keyboard-driven view of `bd ready` / `bd list` with per-bead detail,
// dependency edges, and the status vocabulary.
//
// With no arguments (and a TTY on stdin) it starts the interactive TUI.
// Subcommands give agents and scripts the same data without a TTY:
//
//	beads-tui list [--view ready|open|all]   # board as JSON
//	beads-tui show <id>                      # one bead as JSON
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/RooseveltAdvisors/beads-tui/internal/bd"
	"github.com/RooseveltAdvisors/beads-tui/internal/tui"
	tea "github.com/charmbracelet/bubbletea"
)

// version is stamped at release time; keep in sync with the git tag.
var version = "0.1.0"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "beads-tui: "+err.Error())
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "list":
			return runList(args[1:])
		case "show":
			return runShow(args[1:])
		case "tui":
			return runTUI()
		case "version", "--version", "-v":
			fmt.Printf("beads-tui %s\n", version)
			return nil
		case "help", "--help", "-h":
			printUsage(os.Stdout)
			return nil
		}
	}
	return runTUI()
}

// runTUI starts the interactive board. With a non-TTY stdin it degrades to a
// one-shot JSON dump of the ready board so scripts get content, not a pager.
func runTUI() error {
	if !isTTY(os.Stdin) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		issues, err := bd.New().List(ctx, bd.ViewReady)
		if err != nil {
			return err
		}
		return printJSON(issues)
	}
	p := tea.NewProgram(tui.New(bd.New()), tea.WithAltScreen())
	_, err := p.Run()
	return err
}

func runList(args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	view := fs.String("view", "ready", "board view: ready, open, all")
	fs.Usage = func() {
		_, _ = fmt.Fprintf(fs.Output(), "Usage: beads-tui list [--view ready|open|all]\n\n")
		_, _ = fmt.Fprintf(fs.Output(), "Print a board as JSON (same data the TUI renders):\n")
		_, _ = fmt.Fprintf(fs.Output(), "  beads-tui list                  # ready work: open issues with no blockers\n")
		_, _ = fmt.Fprintf(fs.Output(), "  beads-tui list --view all       # every bead including closed\n")
		_, _ = fmt.Fprintf(fs.Output(), "  beads-tui list --view open      # open issues regardless of blockers\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("list: unexpected argument %q (see 'beads-tui list --help')", fs.Arg(0))
	}
	v := bd.View(*view)
	if !v.Valid() {
		return fmt.Errorf("list: unsupported view %q (choose ready, open or all)", *view)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	issues, err := bd.New().List(ctx, v)
	if err != nil {
		return err
	}
	return printJSON(issues)
}

func runShow(args []string) error {
	fs := flag.NewFlagSet("show", flag.ContinueOnError)
	fs.Usage = func() {
		_, _ = fmt.Fprintf(fs.Output(), "Usage: beads-tui show <id>\n\n")
		_, _ = fmt.Fprintf(fs.Output(), "Print one bead's detail (issue fields plus dependency counts) as JSON:\n")
		_, _ = fmt.Fprintf(fs.Output(), "  beads-tui show fm-ju3\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("show: expected one bead id (see 'beads-tui show --help')")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	issue, err := bd.New().Show(ctx, fs.Arg(0))
	if err != nil {
		return err
	}
	return printJSON(issue)
}

func printJSON(v any) error {
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding output: %w", err)
	}
	fmt.Println(string(out))
	return nil
}

func isTTY(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

func printUsage(w *os.File) {
	_, _ = fmt.Fprintf(w, `beads-tui %s - read-only board for Beads (bd)

Usage:
  beads-tui                interactive board (ready work by default)
  beads-tui list [--view ready|open|all]
  beads-tui show <id>
  beads-tui --version

The TUI is keyboard-driven: j/k or up/down move, g/G jump, f/b page, and
ctrl-u/ctrl-d move half a page. Enter focuses detail; h/l or left/right switch
panes; Esc returns to the list. 1/2/3 switch views (ready/open/all), r refreshes,
? shows help, and q quits.
`, strings.TrimSpace(version))
}
