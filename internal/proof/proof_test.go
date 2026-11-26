package proof

import (
	"testing"

	"github.com/KaaS-team/factory/ee/contract"
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
