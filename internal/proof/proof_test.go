package proof

import (
	"strings"
	"testing"

	"github.com/KaaS-team/factory/ee/contract"
	"github.com/KaaS-team/factory/internal/plan"
)

func TestCheck(t *testing.T) {
	qa := func(v string) *contract.LedgerQA { return &contract.LedgerQA{Verdict: v} }
	cases := []struct {
		name string
		e    Evidence
		ok   bool
	}{
		{"no gate result", Evidence{}, false},
		{"gate red", Evidence{GateKnown: true}, false},
		{"gate green, no qa", Evidence{GateKnown: true, GateGreen: true}, true},
		{"qa pass", Evidence{GateKnown: true, GateGreen: true, QA: qa(QAPass)}, true},
		{"qa skipped", Evidence{GateKnown: true, GateGreen: true, QA: qa(QASkipped)}, true},
		{"qa fail", Evidence{GateKnown: true, GateGreen: true, QA: qa(QAFail)}, false},
		{"qa unverified", Evidence{GateKnown: true, GateGreen: true, QA: qa(QAUnverified)}, false},
		{"qa empty verdict", Evidence{GateKnown: true, GateGreen: true, QA: qa("")}, false},
		{"qa pass, gate red", Evidence{GateKnown: true, QA: qa(QAPass)}, false},
	}
	for _, c := range cases {
		r := Check(c.e)
		if r.OK != c.ok {
			t.Errorf("%s: OK = %v, want %v", c.name, r.OK, c.ok)
		}
		if r.OK == (r.Reason != "") {
			t.Errorf("%s: reason %q does not match OK=%v", c.name, r.Reason, r.OK)
		}
	}
}

func TestCheckBuildReport(t *testing.T) {
	g := plan.ParseGoal("## Acceptance\n- AC-1: a\n")
	full := "- [x] AC-1: a | test: TestA\n"
	for _, c := range plan.Checkpoints {
		full += "- [x] " + c.ID + ": none\n"
	}
	green := Evidence{GateKnown: true, GateGreen: true, Goal: &g}
	cases := []struct {
		name, report, reason string
		goal                 bool
	}{
		{"complete report", full, "", true},
		{"unchecked row", strings.Replace(full, "[x] AC-1", "[ ] AC-1", 1), "goal row AC-1 is unchecked", true},
		{"unanswered checkpoint", strings.Replace(full, "- [x] cap-or-pin-raised: none\n", "", 1), "checkpoint cap-or-pin-raised is unanswered", true},
		{"no goal attached", "", "", false},
	}
	for _, c := range cases {
		e := green
		e.Report = c.report
		if !c.goal {
			e.Goal = nil
		}
		r := Check(e)
		if r.OK != (c.reason == "") || !strings.Contains(r.Reason, c.reason) {
			t.Errorf("%s: %+v, want reason containing %q", c.name, r, c.reason)
		}
	}
}
