// Package proof decides whether a run has earned a merge: the gate is green and, when QA
// evidence is attached, the QA verdict is a pass.
package proof

import "github.com/KaaS-team/factory/ee/contract"

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
