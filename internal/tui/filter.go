package tui

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/RooseveltAdvisors/beads-tui/internal/bd"
)

// SortMode controls the order of rows in the board.
type SortMode uint8

const (
	SortPriority SortMode = iota
	SortCreated
	SortUpdated
	SortAlphabetical
	SortDependencies
	SortDependents
)

// String returns the short name shown in the status bar.
func (s SortMode) String() string {
	switch s {
	case SortCreated:
		return "created"
	case SortUpdated:
		return "updated"
	case SortAlphabetical:
		return "alphabetical"
	case SortDependencies:
		return "dependencies"
	case SortDependents:
		return "depends"
	default:
		return "priority"
	}
}

// Next cycles through the board's named sort modes.
func (s SortMode) Next() SortMode {
	order := [...]SortMode{SortCreated, SortUpdated, SortAlphabetical, SortDependencies, SortDependents, SortPriority}
	for i, mode := range order {
		if s == mode {
			return order[(i+1)%len(order)]
		}
	}
	return SortCreated
}

// FilterKind describes how a filter query is matched.
type FilterKind uint8

const (
	FilterNone FilterKind = iota
	FilterStatus
	FilterPriority
	FilterLabel
	FilterText
	FilterSearch
	FilterRecurring
	FilterAssignee
	FilterComments
	FilterExpression
)

// Filter is the parsed form of the filter prompt. Prefixes are optional for
// the common status and priority forms (for example, "blocked" and "P1").
type Filter struct {
	Kind  FilterKind
	Query string
	expr  *filterExpr
}

func (f Filter) Active() bool { return f.Kind != FilterNone && strings.TrimSpace(f.Query) != "" }

// String is the compact representation used by the prompt and status bar.
func (f Filter) String() string {
	if !f.Active() {
		return "none"
	}
	switch f.Kind {
	case FilterStatus:
		return "status:" + f.Query
	case FilterPriority:
		return "priority:" + f.Query
	case FilterLabel:
		return "label:" + f.Query
	case FilterRecurring:
		if f.Query == "true" {
			return "recurring"
		}
		return "recurring:" + f.Query
	case FilterAssignee:
		return "assignee:" + f.Query
	case FilterComments:
		return "comments:" + f.Query
	default:
		return f.Query
	}
}

// ParseFilter turns the prompt syntax into a filter. Unknown values remain
// valid filters and simply produce no matches, which makes typos visible.
func ParseFilter(input string) Filter {
	return parseFilterInput(input, false)
}

func parseLeaf(input string, search bool) Filter {
	input = strings.TrimSpace(input)
	if input == "" {
		return Filter{}
	}
	lower := strings.ToLower(input)
	for _, prefix := range []struct {
		name string
		kind FilterKind
	}{
		{"status:", FilterStatus},
		{"priority:", FilterPriority},
		{"label:", FilterLabel},
		{"tag:", FilterLabel},
		{"recurring:", FilterRecurring},
		{"assignee:", FilterAssignee},
		{"comments:", FilterComments},
		{"text:", FilterText},
	} {
		if strings.HasPrefix(lower, prefix.name) {
			return Filter{Kind: prefix.kind, Query: strings.TrimSpace(lower[len(prefix.name):])}
		}
	}
	if lower == "recurring" {
		return Filter{Kind: FilterRecurring, Query: "true"}
	}
	if lower == "comments" {
		return Filter{Kind: FilterComments, Query: "true"}
	}
	if strings.HasPrefix(lower, "p") && isPriority(lower) {
		return Filter{Kind: FilterPriority, Query: lower}
	}
	switch lower {
	case "open", "in_progress", "blocked", "closed", "deferred":
		return Filter{Kind: FilterStatus, Query: lower}
	default:
		if search {
			return SearchFilter(lower)
		}
		return Filter{Kind: FilterText, Query: lower}
	}
}

// SearchFilter builds the free-text slash-search variant and includes bead IDs.
func SearchFilter(input string) Filter {
	query := strings.ToLower(strings.TrimSpace(input))
	if query == "" {
		return Filter{}
	}
	return Filter{Kind: FilterSearch, Query: query}
}

// ParseSearchFilter combines structured status/priority/label queries with
// free-text slash search.
func ParseSearchFilter(input string) Filter {
	return parseFilterInput(input, true)
}

func parseFilterInput(input string, search bool) Filter {
	input = strings.TrimSpace(input)
	if input == "" {
		return Filter{}
	}
	tokens := filterTokens(input)
	if len(tokens) == 1 {
		return parseLeaf(tokens[0], search)
	}
	p := filterParser{tokens: tokens, search: true}
	expr := p.parseOr()
	if expr == nil || p.pos != len(tokens) {
		return Filter{Kind: FilterExpression, Query: strings.ToLower(input)}
	}
	return Filter{Kind: FilterExpression, Query: strings.ToLower(input), expr: expr}
}

// Matches reports whether issue satisfies f. Text searches intentionally use
// only title and description so metadata does not create surprising hits.
func (f Filter) Matches(issue bd.Issue) bool {
	if !f.Active() {
		return true
	}
	switch f.Kind {
	case FilterStatus:
		return strings.EqualFold(issue.Status, f.Query)
	case FilterPriority:
		if !isPriority(f.Query) {
			return false
		}
		p, _ := strconv.Atoi(f.Query[1:])
		return issue.Priority == p
	case FilterLabel:
		for _, wanted := range strings.Split(f.Query, ",") {
			wanted = strings.TrimSpace(wanted)
			for _, label := range issue.Labels {
				if wanted != "" && strings.EqualFold(label, wanted) {
					return true
				}
			}
		}
		return false
	case FilterRecurring:
		switch f.Query {
		case "true":
			return issue.IsRecurring()
		case "false":
			return !issue.IsRecurring()
		default:
			return false
		}
	case FilterAssignee:
		return strings.EqualFold(strings.TrimSpace(issue.Assignee), strings.TrimSpace(f.Query))
	case FilterComments:
		switch f.Query {
		case "true", "yes", "any":
			return issue.CommentCount > 0
		case "false", "no", "none":
			return issue.CommentCount == 0
		default:
			n, err := strconv.Atoi(f.Query)
			return err == nil && issue.CommentCount == n
		}
	case FilterExpression:
		expr := f.expr
		if expr == nil {
			p := filterParser{tokens: filterTokens(f.Query), search: true}
			expr = p.parseOr()
			if p.pos != len(p.tokens) {
				return false
			}
		}
		return expr != nil && expr.matches(issue)
	case FilterSearch:
		needle := strings.ToLower(f.Query)
		return strings.Contains(strings.ToLower(issue.Title), needle) ||
			strings.Contains(strings.ToLower(issue.Description), needle) ||
			strings.Contains(strings.ToLower(issue.ID), needle)
	default:
		needle := strings.ToLower(f.Query)
		return strings.Contains(strings.ToLower(issue.Title), needle) ||
			strings.Contains(strings.ToLower(issue.Description), needle)
	}
}

type filterExpr struct {
	op          byte
	term        Filter
	left, right *filterExpr
}

func (e *filterExpr) matches(issue bd.Issue) bool {
	if e == nil {
		return false
	}
	switch e.op {
	case '!':
		return !e.left.matches(issue)
	case '&':
		return e.left.matches(issue) && e.right.matches(issue)
	case '|':
		return e.left.matches(issue) || e.right.matches(issue)
	default:
		return e.term.Matches(issue)
	}
}

// filterTokens keeps the existing one-term grammar and adds a small boolean
// layer: whitespace (or &) means AND, | means OR, ! negates, and parentheses
// group. Quoted text remains one search term.
func filterTokens(input string) []string {
	var tokens []string
	var word strings.Builder
	var quote rune
	flush := func() {
		if word.Len() > 0 {
			tokens = append(tokens, word.String())
			word.Reset()
		}
	}
	for _, r := range input {
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				word.WriteRune(r)
			}
			continue
		}
		switch r {
		case '\'', '"':
			quote = r
		case '(', ')', '!', '|', '&':
			flush()
			tokens = append(tokens, string(r))
		case ' ', '\t', '\n':
			flush()
		default:
			word.WriteRune(r)
		}
	}
	flush()
	return tokens
}

type filterParser struct {
	tokens []string
	pos    int
	search bool
}

func (p *filterParser) parseOr() *filterExpr {
	left := p.parseAnd()
	for p.take("|") {
		right := p.parseAnd()
		if left == nil || right == nil {
			return nil
		}
		left = &filterExpr{op: '|', left: left, right: right}
	}
	return left
}

func (p *filterParser) parseAnd() *filterExpr {
	left := p.parseUnary()
	for left != nil && p.pos < len(p.tokens) && p.tokens[p.pos] != ")" && p.tokens[p.pos] != "|" {
		p.take("&")
		right := p.parseUnary()
		if right == nil {
			return nil
		}
		left = &filterExpr{op: '&', left: left, right: right}
	}
	return left
}

func (p *filterParser) parseUnary() *filterExpr {
	if p.take("!") {
		child := p.parseUnary()
		if child == nil {
			return nil
		}
		return &filterExpr{op: '!', left: child}
	}
	if p.take("(") {
		e := p.parseOr()
		if !p.take(")") {
			return nil
		}
		return e
	}
	if p.pos >= len(p.tokens) || strings.ContainsAny(p.tokens[p.pos], "()!|&") {
		return nil
	}
	term := parseLeaf(p.tokens[p.pos], p.search)
	p.pos++
	return &filterExpr{term: term}
}

func (p *filterParser) take(token string) bool {
	if p.pos < len(p.tokens) && p.tokens[p.pos] == token {
		p.pos++
		return true
	}
	return false
}

// FilterIssues returns a new slice containing only matching issues.
func FilterIssues(issues []bd.Issue, filter Filter) []bd.Issue {
	filtered := make([]bd.Issue, 0, len(issues))
	for _, issue := range issues {
		if filter.Matches(issue) {
			filtered = append(filtered, issue)
		}
	}
	return filtered
}

// withAncestors extends the matching set with the parent-child ancestors of
// every match so a filtered board keeps the hierarchy navigable: a child that
// matches never appears without its parents. Ancestors come from all, the
// unfiltered snapshot, so a hidden parent is restored verbatim.
func withAncestors(matching, all []bd.Issue) []bd.Issue {
	byID := make(map[string]bd.Issue, len(all))
	for _, issue := range all {
		if issue.ID != "" {
			byID[issue.ID] = issue
		}
	}
	kept := make([]bd.Issue, 0, len(matching))
	seen := make(map[string]struct{}, len(matching))
	for _, issue := range matching {
		if issue.ID == "" {
			continue
		}
		if _, ok := seen[issue.ID]; ok {
			continue
		}
		seen[issue.ID] = struct{}{}
		kept = append(kept, issue)
		parentID := strings.TrimSpace(issue.ParentID)
		for parentID != "" {
			if _, ok := seen[parentID]; ok {
				break
			}
			parent, ok := byID[parentID]
			if !ok {
				break
			}
			seen[parentID] = struct{}{}
			kept = append(kept, parent)
			parentID = strings.TrimSpace(parent.ParentID)
		}
	}
	return kept
}

// SortIssues returns a sorted copy, leaving the backend snapshot untouched.
func SortIssues(issues []bd.Issue, mode SortMode) []bd.Issue {
	sorted := append([]bd.Issue(nil), issues...)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		switch mode {
		case SortCreated:
			if c := compareTimestamp(a.CreatedAt, b.CreatedAt); c != 0 {
				return c < 0
			}
		case SortUpdated:
			if c := compareTimestamp(a.UpdatedAt, b.UpdatedAt); c != 0 {
				return c < 0
			}
		case SortAlphabetical:
			if c := strings.Compare(strings.ToLower(a.Title), strings.ToLower(b.Title)); c != 0 {
				return c < 0
			}
		case SortDependencies:
			if a.DependencyCount != b.DependencyCount {
				return a.DependencyCount > b.DependencyCount
			}
		case SortDependents:
			if a.DependentCount != b.DependentCount {
				return a.DependentCount > b.DependentCount
			}
			if c := compareTimestamp(a.CreatedAt, b.CreatedAt); c != 0 {
				return c < 0
			}
		default:
			if a.Priority != b.Priority {
				return a.Priority < b.Priority
			}
		}
		return strings.ToLower(a.ID) < strings.ToLower(b.ID)
	})
	return sorted
}

// compareTimestamp orders non-empty timestamps newest first. bd emits RFC3339
// timestamps, while the lexical fallback keeps malformed values deterministic.
func compareTimestamp(a, b string) int {
	if a == "" && b != "" {
		return 1
	}
	if a != "" && b == "" {
		return -1
	}
	ta, errA := time.Parse(time.RFC3339, a)
	tb, errB := time.Parse(time.RFC3339, b)
	if errA == nil && errB == nil {
		if ta.After(tb) {
			return -1
		}
		if ta.Before(tb) {
			return 1
		}
		return 0
	}
	if a > b {
		return -1
	}
	if a < b {
		return 1
	}
	return 0
}

func isPriority(value string) bool {
	if len(value) != 2 || value[0] != 'p' {
		return false
	}
	p, err := strconv.Atoi(value[1:])
	return err == nil && p >= 0 && p <= 4
}
