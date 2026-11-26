package plan

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// ErrNoAcceptance refuses an issue that has no acceptance lines, with the one line a person reads.
var ErrNoAcceptance = errors.New("refused: the issue has no acceptance lines, so there is no goal to verify")

// Row is one acceptance line of the goal checklist.
type Row struct {
	ID   string // AC-n as written, or AC-<position> when the line has no id
	Text string
}

// Goal is the checklist a build must tick: one row per acceptance line.
type Goal struct {
	Rows []Row
}

// Checkpoint is a deviation question the agent answers before each commit.
type Checkpoint struct {
	ID       string
	Question string
}

// Checkpoints are the deviations a build must rule out before it commits.
var Checkpoints = []Checkpoint{
	{"files-outside-plan", "Did you touch any file the plan does not list? Name each, or write none."},
	{"scope-beyond-acceptance", "Did you add behaviour no acceptance line asks for? Name it, or write none."},
	{"acceptance-reworded", "Did you reword or weaken an acceptance line to make it pass? Say which, or write none."},
	{"test-seen-failing", "Did you see each new test fail before the change? Name the test and the failure you saw."},
	{"cap-or-pin-raised", "Did you raise a byte cap, a pinned count or a ratchet? Name it and the measured delta, or write none."},
	{"importers-of-changed-files", "Which tests import a file you changed, and did you run them? Name them."},
}

var (
	rowLine    = regexp.MustCompile(`^\s*[-*]\s*(?:\[[ xX]\]\s*)?(?:(AC-\d+):\s*)?(\S.*)$`)
	reportLine = regexp.MustCompile(`^\s*[-*]\s*\[([ xX])\]\s*([A-Za-z][\w-]*):\s*(.*)$`)
	testNamed  = regexp.MustCompile(`(?i)\|\s*test:\s*(\S.*)$`)
)

// ParseGoal reads the "## Acceptance" bullets of an issue body. The body needs no planned marker.
func ParseGoal(body string) Goal {
	var g Goal
	in := false
	for _, l := range strings.Split(body, "\n") {
		if strings.HasPrefix(l, "## ") {
			in = strings.EqualFold(strings.TrimSpace(strings.TrimPrefix(l, "## ")), "Acceptance")
			continue
		}
		if !in {
			continue
		}
		m := rowLine.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		id := m[1]
		if id == "" {
			id = fmt.Sprintf("AC-%d", len(g.Rows)+1)
		}
		g.Rows = append(g.Rows, Row{ID: id, Text: strings.TrimSpace(m[2])})
	}
	return g
}

// Prompt is the opening of a build prompt: the goal checklist, the checkpoints and the report format.
func (g Goal) Prompt() string {
	var s strings.Builder
	s.WriteString("## Goal\n\nTick every row in your build report, and name the test that proves it.\n\n")
	for _, r := range g.Rows {
		fmt.Fprintf(&s, "- [ ] %s: %s | test: <name the test>\n", r.ID, r.Text)
	}
	s.WriteString("\n## Checkpoints\n\nBefore each commit, answer every checkpoint in your build report. An unanswered one fails verify.\n\n")
	for _, c := range Checkpoints {
		fmt.Fprintf(&s, "- [ ] %s: %s\n", c.ID, c.Question)
	}
	return s.String()
}

// BuildPrompt returns the goal checklist and checkpoints followed by the title and body, or
// ErrNoAcceptance when the body has no acceptance lines.
func BuildPrompt(title, body string) (string, error) {
	g := ParseGoal(body)
	if len(g.Rows) == 0 {
		return "", ErrNoAcceptance
	}
	return g.Prompt() + "\n" + title + "\n\n" + body, nil
}

// Verify lists every way the build report leaves the goal open: a row unchecked or without a named
// test, a checkpoint unchecked or unanswered. It returns nil when the report is complete.
func (g Goal) Verify(report string) []string {
	rows := map[string]string{}
	checked := map[string]bool{}
	for _, l := range strings.Split(report, "\n") {
		if m := reportLine.FindStringSubmatch(l); m != nil {
			rows[m[2]] = strings.TrimSpace(m[3])
			checked[m[2]] = m[1] != " "
		}
	}
	var out []string
	for _, r := range g.Rows {
		if t, ok := rows[r.ID]; !ok || !checked[r.ID] {
			out = append(out, fmt.Sprintf("goal row %s is unchecked", r.ID))
		} else if m := testNamed.FindStringSubmatch(t); m == nil || strings.HasPrefix(m[1], "<") {
			out = append(out, fmt.Sprintf("goal row %s names no test", r.ID))
		}
	}
	for _, c := range Checkpoints {
		if a, ok := rows[c.ID]; !ok || !checked[c.ID] || a == "" {
			out = append(out, fmt.Sprintf("checkpoint %s is unanswered", c.ID))
		}
	}
	return out
}
