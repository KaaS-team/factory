package ledger_test

import (
	"flag"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KaaS-team/factory/ee/contract"
	"github.com/KaaS-team/factory/internal/ledger"
)

var update = flag.Bool("update", false, "rewrite the golden files")

func out(kind, state string) *contract.LedgerOutcome {
	return &contract.LedgerOutcome{Kind: kind, State: state}
}

// fixture is a fixture history: every outcome state, two variants of one workflow, two teams, a
// pending run and a legacy run (status merged, no outcome).
func fixture() []ledger.Entry {
	mk := func(id, team, wf, variant string, cost float64, status string, o *contract.LedgerOutcome) ledger.Entry {
		e := ledger.Entry{Schema: contract.RunLedgerSchema, RunID: id, Repo: "acme/" + team, Team: team, Workflow: wf, Variant: variant,
			StartedAt: t0, Status: status, Outcome: o, CostUSD: &cost}
		e.Ticket.Source, e.Ticket.ID = "github", id
		e.Agent.Harness = "claude-code"
		return e
	}
	return []ledger.Entry{
		mk("r1", "core", "fix-bug", "a", 2, "merged", out("pr", "merged")),
		mk("r2", "core", "fix-bug", "a", 2, "abandoned", out("pr", "closed")),
		mk("r3", "core", "fix-bug", "a", 2, "open", nil),
		mk("r4", "core", "fix-bug", "b", 1, "merged", out("pr", "merged")),
		mk("r5", "web", "fix-bug", "b", 1, "merged", out("pr", "merged")),
		mk("r6", "web", "fix-bug", "b", 1, "merged", nil), // legacy: status only
		mk("r7", "web", "triage", "", 0.5, "merged", out("issue", "kept")),
		mk("r8", "web", "triage", "", 0.5, "merged", out("issue", "dismissed")),
		mk("r9", "core", "review", "", 0.25, "merged", out("comment", "acted")),
		mk("r10", "core", "review", "", 0.25, "merged", out("comment", "ignored")),
		mk("r11", "core", "", "", 3, "failed", nil),
	}
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	p := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(p, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != got {
		t.Errorf("%s differs:\n--- want\n%s--- got\n%s", name, want, got)
	}
}

// TestOutcomeStates: each outcome state lands in the right bucket.
func TestOutcomeStates(t *testing.T) {
	want := map[string]string{"merged": "A", "closed": "R", "kept": "A", "dismissed": "R", "acted": "A", "ignored": "R"}
	kind := map[string]string{"merged": "pr", "closed": "pr", "kept": "issue", "dismissed": "issue", "acted": "comment", "ignored": "comment"}
	for state, w := range want {
		e := ledger.Entry{Outcome: out(kind[state], state), Status: "open"}
		got := "P"
		if ledger.Accepted(e) {
			got = "A"
		} else if ledger.Rejected(e) {
			got = "R"
		}
		if got != w {
			t.Errorf("%s: %s, want %s", state, got, w)
		}
	}
	if ledger.Accepted(ledger.Entry{Status: "open"}) || ledger.Rejected(ledger.Entry{Status: "open"}) {
		t.Error("a run with no outcome must be pending")
	}
	if !ledger.Accepted(ledger.Entry{Status: "merged"}) {
		t.Error("a legacy merged run must count as accepted")
	}
	// A rejected state with a merged status is still rejected: repo state wins.
	if ledger.Accepted(ledger.Entry{Status: "merged", Outcome: out("pr", "closed")}) {
		t.Error("outcome must win over status")
	}
}

func TestValidateOutcome(t *testing.T) {
	e := fixture()[0]
	if err := ledger.Validate(e); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []*contract.LedgerOutcome{out("pr", "kept"), out("issue", "merged"), out("note", "acted"), out("", "")} {
		e.Outcome = bad
		if ledger.Validate(e) == nil {
			t.Errorf("accepted outcome %+v", bad)
		}
	}
}

// TestScoreGolden: cost per accepted outcome by workflow, by team, and the variant comparison.
func TestScoreGolden(t *testing.T) {
	all := fixture()
	golden(t, "by_workflow.golden", ledger.Text(ledger.Score(all, ledger.ByWorkflow)))
	golden(t, "by_team.golden", ledger.Text(ledger.Score(all, ledger.ByTeam)))
	var fix []ledger.Entry
	for _, e := range all {
		if e.Workflow == "fix-bug" {
			fix = append(fix, e)
		}
	}
	golden(t, "variants.golden", ledger.Text(ledger.Score(fix, ledger.ByVariant)))
}

// TestScoreUsesTheSpendCost: one cost formula. The scored cost adds up to the spend summary's, and a
// group's cost per accepted outcome is its Totals cost over its accepted runs.
func TestScoreUsesTheSpendCost(t *testing.T) {
	all := fixture()
	sum := 0.0
	for _, s := range ledger.Score(all, ledger.ByWorkflow) {
		sum += s.CostUSD
	}
	if want := ledger.Summarize(all).Factory.CostUSD; math.Abs(sum-want) > 1e-9 {
		t.Errorf("scored cost %v, spend %v", sum, want)
	}
	for _, s := range ledger.Score(all, ledger.ByVariant) {
		if s.Key == "a" && s.CostPerAcceptedUSD != 6 { // 3 runs at 2 each, 1 accepted
			t.Errorf("variant a per accepted = %v, want 6", s.CostPerAcceptedUSD)
		}
	}
}

// TestCheaperByDoingLess: a variant that cost less but got nothing accepted does not look better.
func TestCheaperByDoingLess(t *testing.T) {
	rows := ledger.Score(fixture()[:0], ledger.ByVariant)
	if len(rows) != 0 {
		t.Fatal("empty history must give no rows")
	}
	c := 0.1
	lazy := ledger.Entry{Variant: "lazy", Status: "abandoned", CostUSD: &c, Outcome: out("pr", "closed")}
	r := ledger.Score([]ledger.Entry{lazy}, ledger.ByVariant)[0]
	if r.CostPerAcceptedUSD != 0 || r.Accepted != 0 || r.Rejected != 1 {
		t.Errorf("%+v", r)
	}
	if !strings.Contains(ledger.Text([]ledger.Scored{r}), " -") {
		t.Error("no accepted outcome must print as -, not as 0.00")
	}
}

// TestPickVariant: deterministic, covers every variant, and splits a large set close to evenly.
func TestPickVariant(t *testing.T) {
	vs := []string{"a", "b"}
	n := map[string]int{}
	for i := range 1000 {
		id := "run-" + strings.Repeat("x", i%7) + string(rune('a'+i%26)) + string(rune('0'+i%10)) + strings.Repeat("y", i/10)
		v := ledger.PickVariant(id, vs)
		if v != ledger.PickVariant(id, vs) {
			t.Fatal("not deterministic")
		}
		n[v]++
	}
	if n["a"] < 300 || n["b"] < 300 {
		t.Errorf("uneven split %v", n)
	}
	if ledger.PickVariant("x", nil) != "" {
		t.Error("no variants gives empty")
	}
}
