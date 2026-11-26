package mergeq

import (
	"context"
	"strings"
	"testing"

	"github.com/KaaS-team/factory/ee/contract"
	"github.com/KaaS-team/factory/internal/api"
	"github.com/KaaS-team/factory/internal/attention"
	"github.com/KaaS-team/factory/internal/proof"
	"github.com/KaaS-team/factory/internal/risk"
)

type classPolicy map[contract.RiskClass]bool

func (p classPolicy) Evaluate(_ context.Context, r contract.PolicyRequest) contract.Decision {
	if r.Check == contract.CheckAutoMerge && p[contract.RiskClass(r.Value)] {
		return contract.Decision{Allow: true, Reason: contract.ReasonPolicyAllowed}
	}
	return contract.Decision{Reason: contract.ReasonPolicyNotAllowlisted}
}
func (classPolicy) Budgets() []contract.BudgetLimit { return nil }

var (
	permissive = classPolicy{contract.RiskLow: true}
	strict     = classPolicy{}
	docsOnly   = risk.Change{Files: []risk.File{{Path: "docs/guide.md", Added: 5}}}
	green      = proof.Evidence{GateKnown: true, GateGreen: true}
)

func guarded(g *rig, store *attention.Store, p contract.Policy, ev proof.Evidence, ch risk.Change) *CoreQueue {
	q := g.queue()
	q.d.Guard = &Guard{Policy: p, Attention: store, Facts: func(context.Context, Entry) (Facts, error) {
		return Facts{Proof: ev, Change: ch}, nil
	}}
	return q
}

func TestGuardMissingProofBlocksMerge(t *testing.T) {
	for name, ev := range map[string]proof.Evidence{
		"no gate result": {},
		"gate red":       {GateKnown: true},
		"qa failed":      {GateKnown: true, GateGreen: true, QA: &contract.LedgerQA{Verdict: "fail"}},
	} {
		t.Run(name, func(t *testing.T) {
			g := newRig(t, 1)
			store, _ := attention.Open("")
			q := guarded(g, store, permissive, ev, docsOnly)
			g.enqueue(t, q, 1)
			rep, err := q.Run(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(rep.Landed) != 0 || g.k.Phases["T1"] != api.PhaseNeedsHuman {
				t.Fatalf("landed %v, phase %s", rep.Landed, g.k.Phases["T1"])
			}
			if c, _ := g.k.Comments(context.Background(), "T1"); !strings.Contains(c[len(c)-1], "Missing proof") {
				t.Fatalf("no reason on the ticket: %v", c)
			}
			if open, _ := store.Open(context.Background()); len(open) != 1 || open[0].Kind != attention.MissingProof {
				t.Fatalf("attention = %+v", open)
			}
		})
	}
}

func TestGuardDocsOnlyAutoMergesUnderPermissiveNotStrict(t *testing.T) {
	g := newRig(t, 1)
	store, _ := attention.Open("")
	q := guarded(g, store, permissive, green, docsOnly)
	g.enqueue(t, q, 1)
	rep, err := q.Run(context.Background())
	if err != nil || len(rep.Landed) != 1 || g.k.Phases["T1"] != api.PhaseMerged {
		t.Fatalf("permissive: landed %v phase %s err %v", rep.Landed, g.k.Phases["T1"], err)
	}

	g = newRig(t, 1)
	store, _ = attention.Open("")
	q = guarded(g, store, strict, green, docsOnly)
	g.enqueue(t, q, 1)
	rep, err = q.Run(context.Background())
	if err != nil || len(rep.Landed) != 0 || g.k.Phases["T1"] != api.PhaseNeedsHuman {
		t.Fatalf("strict: landed %v phase %s err %v", rep.Landed, g.k.Phases["T1"], err)
	}
	open, _ := store.Open(context.Background())
	if len(open) != 1 || open[0].Kind != attention.MergeApproval {
		t.Fatalf("strict: attention = %+v", open)
	}

	// A person approves; the ticket is queued again and lands.
	if err := store.Resolve(open[0].ID, attention.Approve, "me"); err != nil {
		t.Fatal(err)
	}
	g.enqueue(t, q, 1)
	rep, err = q.Run(context.Background())
	if err != nil || len(rep.Landed) != 1 {
		t.Fatalf("after approval: landed %v err %v", rep.Landed, err)
	}
}

func TestGuardApprovalDoesNotStandInForProof(t *testing.T) {
	g := newRig(t, 1)
	store, _ := attention.Open("")
	it, _ := store.Raise(attention.MergeApproval, "T1", "x")
	_ = store.Resolve(it.ID, attention.Approve, "me")
	q := guarded(g, store, strict, proof.Evidence{}, docsOnly)
	g.enqueue(t, q, 1)
	if rep, _ := q.Run(context.Background()); len(rep.Landed) != 0 {
		t.Fatalf("landed without proof: %v", rep.Landed)
	}
}
