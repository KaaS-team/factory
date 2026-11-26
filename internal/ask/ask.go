// Package ask answers a read-only question about a run from what the run already left behind:
// its ledger entry, its events and its transcript. It reads and never writes, so asking changes
// no code, branch or ticket. The answer is built from the lines that share the most words with
// the question, and each line is cited by where it came from.
package ask

import (
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/KaaS-team/factory/internal/api"
	"github.com/KaaS-team/factory/internal/ledger"
	"github.com/KaaS-team/factory/internal/redact"
)

// MaxCites bounds how many lines an answer quotes.
const MaxCites = 5

// Sources is everything a question may be answered from. Any part may be empty.
type Sources struct {
	Ticket     api.TicketID
	Ledger     []ledger.Entry // all entries; Ask keeps the ticket's own
	Events     []api.Event
	Transcript []string
}

// Cite is one quoted line and where it came from: "ledger", "event" or "transcript:<line>".
type Cite struct {
	Source string `json:"source"`
	Text   string `json:"text"`
}

// Answer is the reply to a question. Found is false when no line shares a word with it.
type Answer struct {
	Ticket api.TicketID `json:"ticket"`
	Found  bool         `json:"found"`
	Cites  []Cite       `json:"cites"`
}

// Ask ranks the run's lines against question and returns the best MaxCites. Model names, token
// counts and cost are never read from the ledger, so they cannot appear in an answer. Every
// quoted line is redacted.
func Ask(question string, s Sources) Answer {
	a := Answer{Ticket: s.Ticket, Cites: []Cite{}}
	want := words(question)
	if len(want) == 0 {
		return a
	}
	type cand struct {
		c     Cite
		score int
		order int
	}
	var cs []cand
	add := func(src, text string) {
		text = strings.TrimSpace(redact.Strict(text))
		if text == "" {
			return
		}
		n := 0
		have := words(text)
		for w := range want {
			if have[w] {
				n++
			}
		}
		if n > 0 {
			cs = append(cs, cand{Cite{src, text}, n, len(cs)})
		}
	}
	for _, e := range s.Ledger {
		if e.Ticket.ID == string(s.Ticket) {
			add("ledger", ledgerLine(e))
		}
	}
	for _, ev := range s.Events {
		add("event", eventLine(ev))
	}
	for i, l := range s.Transcript {
		add(fmt.Sprintf("transcript:%d", i+1), l)
	}
	sort.SliceStable(cs, func(i, j int) bool { return cs[i].score > cs[j].score })
	for _, c := range cs {
		if len(a.Cites) == MaxCites {
			break
		}
		a.Cites = append(a.Cites, c.c)
	}
	a.Found = len(a.Cites) > 0
	return a
}

// Text renders an answer for a terminal.
func Text(a Answer) string {
	if !a.Found {
		return fmt.Sprintf("No record of %s answers that question.\n", a.Ticket)
	}
	var b strings.Builder
	for _, c := range a.Cites {
		fmt.Fprintf(&b, "[%s] %s\n", c.Source, c.Text)
	}
	return b.String()
}

func ledgerLine(e ledger.Entry) string {
	s := fmt.Sprintf("run %s status %s", e.RunID, e.Status)
	if e.FailureClass != "" {
		s += " failure " + e.FailureClass
	}
	if e.PR != nil && e.PR.URL != "" {
		s += " pr " + e.PR.URL
	}
	return s
}

func eventLine(ev api.Event) string {
	keys := make([]string, 0, len(ev.Data))
	for k := range ev.Data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	s := ev.Kind
	for _, k := range keys {
		if k == "usage" || k == "cost" || k == "model" || k == "tokens" {
			continue
		}
		s += fmt.Sprintf(" %s=%v", k, ev.Data[k])
	}
	return s
}

// stop words carry no meaning for a match.
var stop = map[string]bool{"the": true, "a": true, "an": true, "is": true, "was": true, "did": true, "do": true, "it": true, "of": true, "to": true, "why": true, "what": true, "how": true, "and": true, "in": true}

func words(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if len(w) > 1 && !stop[w] {
			out[w] = true
		}
	}
	return out
}
