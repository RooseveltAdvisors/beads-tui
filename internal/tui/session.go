package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"time"
)

// SessionKind names the live-session backend.
type SessionKind string

const (
	sessionHerdr SessionKind = "herdr"
	sessionTmux  SessionKind = "tmux"
)

// LiveSession is a running Herdr or tmux session that can receive a prompt.
type LiveSession struct {
	Kind SessionKind
	Name string
}

type herdrSessionList struct {
	Sessions []struct {
		Name    string `json:"name"`
		Running bool   `json:"running"`
	} `json:"sessions"`
}

var sessionLookPath = exec.LookPath
var sessionCommandOutput = func(timeout time.Duration, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return exec.CommandContext(ctx, name, args...).Output()
}

const sessionProbeTimeout = 2 * time.Second

func discoverLiveSessions() []LiveSession {
	var out []LiveSession
	out = append(out, listHerdrSessions()...)
	out = append(out, listTmuxSessions()...)
	return out
}

func listHerdrSessions() []LiveSession {
	if _, err := sessionLookPath("herdr"); err != nil {
		return nil
	}
	raw, err := sessionCommandOutput(sessionProbeTimeout, "herdr", "session", "list", "--json")
	if err != nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	return parseHerdrSessions(raw)
}

func parseHerdrSessions(raw []byte) []LiveSession {
	var parsed herdrSessionList
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil
	}
	out := make([]LiveSession, 0, len(parsed.Sessions))
	for _, s := range parsed.Sessions {
		name := strings.TrimSpace(s.Name)
		if name == "" || !s.Running {
			continue
		}
		out = append(out, LiveSession{Kind: sessionHerdr, Name: name})
	}
	return out
}

func listTmuxSessions() []LiveSession {
	if _, err := sessionLookPath("tmux"); err != nil {
		return nil
	}
	raw, err := sessionCommandOutput(sessionProbeTimeout, "tmux", "list-sessions", "-F", "#{session_name}")
	if err != nil {
		return nil
	}
	return parseTmuxSessions(raw)
}

func parseTmuxSessions(raw []byte) []LiveSession {
	var out []LiveSession
	for _, line := range strings.Split(string(raw), "\n") {
		name := strings.TrimSpace(line)
		if name == "" {
			continue
		}
		out = append(out, LiveSession{Kind: sessionTmux, Name: name})
	}
	return out
}

// MatchAssignee picks the live session for an assignee.
// Exact case-insensitive match wins; otherwise a unique-best substring match.
// When both backends match at the same rank, Herdr wins.
func MatchAssignee(assignee string, sessions []LiveSession) *LiveSession {
	want := strings.ToLower(strings.TrimSpace(assignee))
	if want == "" || len(sessions) == 0 {
		return nil
	}
	var exactHerdr, exactTmux *LiveSession
	for i := range sessions {
		s := &sessions[i]
		if strings.ToLower(strings.TrimSpace(s.Name)) != want {
			continue
		}
		switch s.Kind {
		case sessionHerdr:
			if exactHerdr == nil {
				exactHerdr = s
			}
		default:
			if exactTmux == nil {
				exactTmux = s
			}
		}
	}
	if exactHerdr != nil {
		return exactHerdr
	}
	if exactTmux != nil {
		return exactTmux
	}

	var best *LiveSession
	bestScore := 0
	for i := range sessions {
		s := &sessions[i]
		name := strings.ToLower(strings.TrimSpace(s.Name))
		if name == "" {
			continue
		}
		if !strings.Contains(name, want) && !strings.Contains(want, name) {
			continue
		}
		score := substringScore(want, name, s.Kind)
		if best == nil || score > bestScore {
			best = s
			bestScore = score
		}
	}
	return best
}

func substringScore(want, name string, kind SessionKind) int {
	score := 0
	if name == want {
		score += 1000
	}
	score += 200 - min(200, len(name))
	if kind == sessionHerdr {
		score += 50
	}
	return score
}
