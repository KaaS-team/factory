// Package proof decides whether a run has earned a merge: the gate is green and, when QA
// evidence is attached, the QA verdict is a pass.
package proof

import (
	"strings"

	"github.com/KaaS-team/factory/ee/contract"
	"github.com/KaaS-team/factory/internal/plan"
)

// Verdicts the ledger's qa field may hold.
const (
	QAPass       = "pass"
	QAFail       = "fail"
	QAUnverified = "unverified"
	QASkipped    = "skipped"
)

// Evidence is what a run offers as proof.
type Evidence struct {
	GateKnown bool // the gate ran on this head at all
	GateGreen bool
	QA        *contract.LedgerQA // nil: no QA evidence attached
	Goal      *plan.Goal         // nil: the build had no goal checklist; set: Report must complete it
	Report    string             // the build report text
}

// Result is the verdict on a run's proof. Reason is plain English for a customer and is empty when OK.
type Result struct {
	OK     bool
	Reason string
}

// Check returns OK only when the gate is green and QA, if attached, passed. Missing proof blocks.
// A QA record with no verdict, or an unverified one, counts as attached but not passed.
func Check(e Evidence) Result {
	switch {
	case !e.GateKnown:
		return Result{Reason: "no gate result for this change"}
	case !e.GateGreen:
		return Result{Reason: "the gate is not green"}
	case e.Goal != nil && len(e.Goal.Verify(e.Report)) > 0:
		return CheckReport(*e.Goal, e.Report)
	case e.QA == nil, e.QA.Verdict == QASkipped:
		return Result{OK: true}
	case e.QA.Verdict == QAPass:
		return Result{OK: true}
	case e.QA.Verdict == QAFail:
		return Result{Reason: "QA failed"}
	default:
		return Result{Reason: "QA evidence is attached but not verified"}
	}
}

// CheckReport fails a build report that leaves a goal row unchecked or a checkpoint unanswered. The
// manager loop and Check both call it, so the agent hears what verify will say.
func CheckReport(g plan.Goal, report string) Result {
	p := g.Verify(report)
	if len(p) == 0 {
		return Result{OK: true}
	}
	return Result{Reason: "the build report is incomplete: " + strings.Join(p, "; ")}
}
