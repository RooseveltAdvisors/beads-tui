package tui

import (
	"sort"
	"strings"

	"github.com/RooseveltAdvisors/beads-tui/internal/bd"
)

// FilterSuggestion is one completion offered while the / prompt is open.
type FilterSuggestion struct {
	// Insert replaces the current (partial) token.
	Insert string
	// Label is the short name shown in the assist strip.
	Label string
	// Hint is a one-line description of what the token does.
	Hint string
}

// filterTokenSpan returns the [start,end) byte range of the token under the
// cursor. Tokens split on whitespace and the boolean operators | & ( ) !.
func filterTokenSpan(query string, cursor int) (int, int) {
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(query) {
		cursor = len(query)
	}
	start := cursor
	for start > 0 {
		r := rune(query[start-1])
		if isFilterBreak(r) {
			break
		}
		start--
	}
	end := cursor
	for end < len(query) {
		r := rune(query[end])
		if isFilterBreak(r) {
			break
		}
		end++
	}
	return start, end
}

func isFilterBreak(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '|', '&', '(', ')', '!':
		return true
	default:
		return false
	}
}

// filterSuggestCatalog is the static set of filter prefixes and keywords.
// Value completions (assignee, label, status) are filled from the live board.
var filterSuggestCatalog = []FilterSuggestion{
	{Insert: "overdue", Label: "overdue", Hint: "due_at in the past (not closed)"},
	{Insert: "recurring", Label: "recurring", Hint: "has a repeat schedule"},
	{Insert: "recurring:true", Label: "recurring:true", Hint: "same as recurring"},
	{Insert: "recurring:false", Label: "recurring:false", Hint: "one-shot beads only"},
	{Insert: "status:", Label: "status:", Hint: "open · in_progress · blocked · …"},
	{Insert: "priority:", Label: "priority:", Hint: "P0 · P1 · P2 · P3 · P4"},
	{Insert: "label:", Label: "label:", Hint: "match any listed label"},
	{Insert: "assignee:", Label: "assignee:", Hint: "exact assignee match"},
	{Insert: "comments:", Label: "comments:", Hint: "true · false · or a count"},
	{Insert: "text:", Label: "text:", Hint: "substring in title/description"},
	{Insert: "status:open", Label: "status:open", Hint: "open beads"},
	{Insert: "status:in_progress", Label: "status:in_progress", Hint: "in progress"},
	{Insert: "status:blocked", Label: "status:blocked", Hint: "blocked"},
	{Insert: "status:deferred", Label: "status:deferred", Hint: "deferred"},
	{Insert: "status:closed", Label: "status:closed", Hint: "closed"},
	{Insert: "status:hold", Label: "status:hold", Hint: "on hold"},
	{Insert: "priority:P0", Label: "priority:P0", Hint: "P0"},
	{Insert: "priority:P1", Label: "priority:P1", Hint: "P1"},
	{Insert: "priority:P2", Label: "priority:P2", Hint: "P2"},
	{Insert: "priority:P3", Label: "priority:P3", Hint: "P3"},
	{Insert: "priority:P4", Label: "priority:P4", Hint: "P4"},
	{Insert: "comments:true", Label: "comments:true", Hint: "has comments"},
	{Insert: "comments:false", Label: "comments:false", Hint: "no comments"},
}

// FilterSuggestions returns ranked completions for the token under cursor.
// statuses/assignees/labels come from the loaded board so value completion
// stays grounded in real data.
func FilterSuggestions(query string, cursor int, statuses, assignees, labels []string) []FilterSuggestion {
	start, end := filterTokenSpan(query, cursor)
	token := query[start:end]
	lower := strings.ToLower(token)
	// '!' is a token break (boolean NOT), so it sits just before the span.
	// "!rec" → token "rec" with negated=true; suggestions insert "!recurring".
	negated := start > 0 && query[start-1] == '!'

	var out []FilterSuggestion
	seen := map[string]bool{}
	add := func(s FilterSuggestion) {
		key := strings.ToLower(s.Insert)
		if seen[key] {
			return
		}
		seen[key] = true
		if negated && !strings.HasPrefix(s.Insert, "!") {
			s.Insert = "!" + s.Insert
			s.Label = "!" + s.Label
		}
		out = append(out, s)
	}

	// Prefix-specific value completions.
	switch {
	case strings.HasPrefix(lower, "status:"):
		want := strings.TrimPrefix(lower, "status:")
		for _, st := range uniqueSorted(statuses) {
			cand := "status:" + st
			if want == "" || strings.HasPrefix(st, want) {
				add(FilterSuggestion{Insert: cand, Label: cand, Hint: "status match"})
			}
		}
		for _, s := range filterSuggestCatalog {
			if strings.HasPrefix(s.Insert, "status:") && (want == "" || strings.HasPrefix(strings.TrimPrefix(s.Insert, "status:"), want)) {
				add(s)
			}
		}
	case strings.HasPrefix(lower, "priority:"):
		want := strings.TrimPrefix(lower, "priority:")
		for _, p := range []string{"P0", "P1", "P2", "P3", "P4"} {
			if want == "" || strings.HasPrefix(strings.ToLower(p), want) {
				add(FilterSuggestion{Insert: "priority:" + p, Label: "priority:" + p, Hint: "priority " + p})
			}
		}
	case strings.HasPrefix(lower, "assignee:"):
		want := strings.TrimPrefix(lower, "assignee:")
		for _, a := range uniqueSorted(assignees) {
			if want == "" || strings.HasPrefix(strings.ToLower(a), want) {
				add(FilterSuggestion{Insert: "assignee:" + a, Label: "assignee:" + a, Hint: "assignee match"})
			}
		}
	case strings.HasPrefix(lower, "label:") || strings.HasPrefix(lower, "tag:"):
		prefix := "label:"
		if strings.HasPrefix(lower, "tag:") {
			prefix = "tag:"
		}
		want := strings.TrimPrefix(lower, prefix)
		for _, l := range uniqueSorted(labels) {
			if want == "" || strings.HasPrefix(strings.ToLower(l), want) {
				add(FilterSuggestion{Insert: prefix + l, Label: prefix + l, Hint: "label match"})
			}
		}
	case strings.HasPrefix(lower, "comments:"):
		want := strings.TrimPrefix(lower, "comments:")
		for _, v := range []string{"true", "false", "any", "none"} {
			if want == "" || strings.HasPrefix(v, want) {
				add(FilterSuggestion{Insert: "comments:" + v, Label: "comments:" + v, Hint: "comment presence"})
			}
		}
	case strings.HasPrefix(lower, "recurring:"):
		want := strings.TrimPrefix(lower, "recurring:")
		for _, v := range []string{"true", "false"} {
			if want == "" || strings.HasPrefix(v, want) {
				add(FilterSuggestion{Insert: "recurring:" + v, Label: "recurring:" + v, Hint: "repeat schedule"})
			}
		}
	default:
		for _, s := range filterSuggestCatalog {
			if lower == "" || strings.HasPrefix(strings.ToLower(s.Insert), lower) || strings.HasPrefix(strings.ToLower(s.Label), lower) {
				add(s)
			}
		}
		// Also offer bare status names as status:X shortcuts when they match.
		for _, st := range uniqueSorted(statuses) {
			if lower != "" && strings.HasPrefix(st, lower) {
				add(FilterSuggestion{Insert: "status:" + st, Label: "status:" + st, Hint: "status match"})
			}
		}
	}

	// Empty prompt: lead with the highest-signal keywords, then prefixes.
	if lower == "" && !negated {
		preferred := []string{"overdue", "recurring", "status:", "priority:", "label:", "assignee:", "comments:", "text:"}
		rank := map[string]int{}
		for i, p := range preferred {
			rank[p] = i
		}
		sort.SliceStable(out, func(i, j int) bool {
			ri, iok := rank[out[i].Insert]
			rj, jok := rank[out[j].Insert]
			if iok && jok {
				return ri < rj
			}
			if iok {
				return true
			}
			if jok {
				return false
			}
			return out[i].Insert < out[j].Insert
		})
	}

	if len(out) > 12 {
		out = out[:12]
	}
	return out
}

// ApplyFilterSuggestion replaces the token under cursor with s.Insert and
// returns the new query plus the cursor position after the inserted token.
func ApplyFilterSuggestion(query string, cursor int, s FilterSuggestion) (string, int) {
	start, end := filterTokenSpan(query, cursor)
	insert := s.Insert
	// Suggestions for a negated token already include a leading '!'. The
	// existing '!' sits just before the span, so fold it into the replace
	// range instead of producing "!!recurring".
	if start > 0 && query[start-1] == '!' && strings.HasPrefix(insert, "!") {
		start--
	}
	newQuery := query[:start] + insert + query[end:]
	newCursor := start + len(insert)
	// Offer a trailing space after a completed keyword (not a bare prefix:).
	if !strings.HasSuffix(insert, ":") {
		if newCursor >= len(newQuery) || newQuery[newCursor] != ' ' {
			newQuery = newQuery[:newCursor] + " " + newQuery[newCursor:]
		}
		newCursor++
	}
	return newQuery, newCursor
}

func uniqueSorted(values []string) []string {
	seen := map[string]string{}
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		key := strings.ToLower(v)
		if _, ok := seen[key]; !ok {
			seen[key] = v
		}
	}
	out := make([]string, 0, len(seen))
	for _, v := range seen {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i]) < strings.ToLower(out[j])
	})
	return out
}

// boardFilterVocab collects status/assignee/label values from loaded rows so
// the / prompt can complete against the live board.
func boardFilterVocab(issues []bd.Issue, statuses []string) (st, assignees, labels []string) {
	st = append([]string(nil), statuses...)
	if len(st) == 0 {
		st = []string{"open", "in_progress", "blocked", "deferred", "closed", "hold"}
	}
	for _, issue := range issues {
		if a := strings.TrimSpace(issue.Assignee); a != "" {
			assignees = append(assignees, a)
		}
		for _, l := range issue.Labels {
			if l = strings.TrimSpace(l); l != "" {
				labels = append(labels, l)
			}
		}
		if s := strings.TrimSpace(issue.Status); s != "" {
			st = append(st, s)
		}
	}
	return uniqueSorted(st), uniqueSorted(assignees), uniqueSorted(labels)
}
