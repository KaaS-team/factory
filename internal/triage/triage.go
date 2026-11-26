// Package triage decides what to do with a new ticket before any work starts: its type, priority and
// labels, whether an open ticket already covers it, and one of four outcomes. The first pass is plain
// text matching and spends nothing. A model is asked only to confirm a likely duplicate.
package triage

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Outcome is what triage decided.
type Outcome string

// The four outcomes.
const (
	Proceed   Outcome = "proceed"
	NeedsInfo Outcome = "needs-info"
	Duplicate Outcome = "duplicate"
	Refused   Outcome = "refused"
)

// Issue is one ticket to triage.
type Issue struct {
	ID     string
	Title  string
	Body   string
	Labels []string
}

// Result is the decision. Reason is plain English and safe to show a customer.
type Result struct {
	Outcome     Outcome  `json:"outcome"`
	Type        string   `json:"type"`
	Priority    int      `json:"priority"` // 0 is most urgent
	Labels      []string `json:"labels"`
	DuplicateOf string   `json:"duplicate_of,omitempty"`
	Reason      string   `json:"reason"`
}

// Confirmer is the model step: is b the same request as a? It is called only for a likely duplicate
// that the text match could not settle. It goes through the repo's model client.
type Confirmer func(ctx context.Context, a, b Issue) (bool, error)

// Options configures Run.
type Options struct {
	// Open are the open tickets to compare with. The issue itself is skipped by ID.
	Open []Issue
	// Protected are path prefixes the charter keeps agents out of.
	Protected []string
	// Confirm settles a likely duplicate; nil means a likely duplicate is not called one.
	Confirm Confirmer
}

// Similarity bands. At or above sure, two tickets are a duplicate with no model call. From likely up
// to sure, the Confirmer decides.
const (
	sure   = 0.80
	likely = 0.50
	// minWords is the shortest body that can hold a checkable done-when.
	minWords = 12
)

var wordRe = regexp.MustCompile(`[a-z0-9]+`)

var stop = map[string]bool{"the": true, "a": true, "an": true, "and": true, "or": true, "to": true, "of": true, "in": true,
	"is": true, "it": true, "on": true, "for": true, "with": true, "when": true, "this": true, "that": true, "i": true, "we": true}

func tokens(s string) map[string]bool {
	m := map[string]bool{}
	for _, w := range wordRe.FindAllString(strings.ToLower(s), -1) {
		if !stop[w] && len(w) > 1 {
			m[w] = true
		}
	}
	return m
}

func jaccard(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	n := 0
	for w := range a {
		if b[w] {
			n++
		}
	}
	return float64(n) / float64(len(a)+len(b)-n)
}

// Similarity scores two tickets from 0 to 1. The title counts double: it is the part people write
// with care.
func Similarity(a, b Issue) float64 {
	return 0.6*jaccard(tokens(a.Title), tokens(b.Title)) + 0.4*jaccard(tokens(a.Body), tokens(b.Body))
}

var typeWords = []struct {
	typ   string
	words []string
}{
	{"security", []string{"security", "vulnerability", "cve", "exploit", "injection", "xss", "csrf", "leak", "credential"}},
	{"dependency", []string{"dependency", "dependencies", "upgrade", "bump", "renovate", "dependabot"}},
	{"docs", []string{"docs", "documentation", "readme", "typo", "guide"}},
	{"bug", []string{"bug", "crash", "panic", "error", "fails", "failing", "broken", "regression", "wrong", "exception"}},
}

// classify picks one type; a type label already on the issue wins.
func classify(i Issue) string {
	for _, l := range i.Labels {
		for _, t := range typeWords {
			if strings.EqualFold(l, t.typ) {
				return t.typ
			}
		}
		if strings.EqualFold(l, "feature") {
			return "feature"
		}
	}
	title := tokens(i.Title)
	all := tokens(i.Title + " " + i.Body)
	for _, pass := range []map[string]bool{title, all} {
		for _, t := range typeWords {
			for _, w := range t.words {
				if pass[w] {
					return t.typ
				}
			}
		}
	}
	return "feature"
}

var urgent = []string{"outage", "data loss", "production down", "cannot log in", "security"}

func priority(typ string, i Issue) int {
	text := strings.ToLower(i.Title + " " + i.Body)
	for _, l := range i.Labels {
		if len(l) == 2 && (l[0] == 'P' || l[0] == 'p') && l[1] >= '0' && l[1] <= '3' {
			return int(l[1] - '0')
		}
	}
	for _, u := range urgent {
		if strings.Contains(text, u) {
			return 0
		}
	}
	switch typ {
	case "security":
		return 0
	case "bug":
		return 1
	case "feature":
		return 2
	}
	return 3
}

// touches returns the protected prefix the issue names, if any.
func touches(i Issue, protected []string) (string, bool) {
	text := i.Title + "\n" + i.Body
	for _, p := range protected {
		if p != "" && strings.Contains(text, p) {
			return p, true
		}
	}
	return "", false
}

// Run triages one issue. It is deterministic unless a Confirmer is set.
func Run(ctx context.Context, i Issue, o Options) (Result, error) {
	typ := classify(i)
	r := Result{Type: typ, Priority: priority(typ, i)}
	r.Labels = labelsFor(typ, r.Priority)
	if p, ok := touches(i, o.Protected); ok {
		r.Outcome = Refused
		r.Reason = fmt.Sprintf("This asks for a change under %s, which agents may not edit. A person needs to make it.", p)
		return r, nil
	}
	if dup, ok, err := findDuplicate(ctx, i, o); err != nil {
		return r, err
	} else if ok {
		r.Outcome, r.DuplicateOf = Duplicate, dup
		r.Reason = fmt.Sprintf("This covers the same ground as #%s. Follow that ticket instead.", dup)
		return r, nil
	}
	if len(strings.Fields(i.Body)) < minWords {
		r.Outcome = NeedsInfo
		r.Reason = "Say what done looks like in one sentence a test could check: what you see now, what you expect, and where."
		return r, nil
	}
	r.Outcome = Proceed
	r.Reason = "Clear enough to plan."
	return r, nil
}

func findDuplicate(ctx context.Context, i Issue, o Options) (string, bool, error) {
	type scored struct {
		o Issue
		s float64
	}
	var c []scored
	for _, other := range o.Open {
		if other.ID == i.ID {
			continue
		}
		if s := Similarity(i, other); s >= likely {
			c = append(c, scored{other, s})
		}
	}
	// Best first; equal scores go to the older (smaller) id string so the answer is stable.
	sort.Slice(c, func(a, b int) bool {
		if c[a].s != c[b].s {
			return c[a].s > c[b].s
		}
		return c[a].o.ID < c[b].o.ID
	})
	for _, x := range c {
		if x.s >= sure {
			return x.o.ID, true, nil
		}
		if o.Confirm == nil {
			continue
		}
		same, err := o.Confirm(ctx, i, x.o)
		if err != nil {
			return "", false, fmt.Errorf("triage: confirm duplicate of #%s: %w", x.o.ID, err)
		}
		if same {
			return x.o.ID, true, nil
		}
	}
	return "", false, nil
}

func labelsFor(typ string, p int) []string {
	return []string{"type:" + typ, fmt.Sprintf("P%d", p)}
}

// Sink is the tracker write triage needs: one comment.
type Sink interface {
	Comment(ctx context.Context, id string, body string) error
}

// Labeler is the optional write of labels. A tracker without it gets the labels in the comment.
type Labeler interface {
	AddLabels(ctx context.Context, id string, labels []string) error
}

// AllLabels is the full label set to write: type, priority and the outcome.
func (r Result) AllLabels() []string {
	return append(append([]string{}, r.Labels...), "triage:"+string(r.Outcome))
}

// Comment is the text written back to the ticket. It names the outcome and the reason, nothing else.
// withLabels adds the label list, for a tracker that cannot set labels itself.
func (r Result) Comment(withLabels bool) string {
	s := fmt.Sprintf("Triage: **%s**. %s\n\nType: %s. Priority: P%d.\n", r.Outcome, r.Reason, r.Type, r.Priority)
	if withLabels {
		s += "Labels: " + strings.Join(r.AllLabels(), ", ") + "\n"
	}
	return s
}

// Apply writes the result back: labels when the sink can set them, then one comment.
func Apply(ctx context.Context, s Sink, id string, r Result) error {
	l, canLabel := s.(Labeler)
	if canLabel {
		if err := l.AddLabels(ctx, id, r.AllLabels()); err != nil {
			return fmt.Errorf("triage: label %s: %w", id, err)
		}
	}
	if err := s.Comment(ctx, id, r.Comment(!canLabel)); err != nil {
		return fmt.Errorf("triage: comment on %s: %w", id, err)
	}
	return nil
}
