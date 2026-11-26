package mergeq

import (
	"context"
	"strings"
	"testing"

	"github.com/KaaS-team/factory/ee/contract"
	"github.com/KaaS-team/factory/internal/policygate"
)

type riskPolicy struct{ low bool }

func (p riskPolicy) Evaluate(_ context.Context, r contract.PolicyRequest) contract.Decision {
	if r.Check == contract.CheckAutoMerge && r.Value == "low" && p.low {
		return contract.Decision{Allow: true, Reason: contract.ReasonPolicyAllowed}
	}
	return contract.Decision{Reason: contract.ReasonPolicyNotAllowlisted}
}
func (riskPolicy) Budgets() []contract.BudgetLimit { return nil }

type ejectStore struct {
	Store
	ejected map[string]string
}

func (s *ejectStore) Eject(_ context.Context, id, tail string) error {
	s.ejected[id] = tail
	return nil
}

// A queued PR whose risk class the policy does not list is ejected with the reason code; a listed one stays.
func TestPolicyEjectsRiskOutsideAllowed(t *testing.T) {
	policygate.Configure(riskPolicy{low: true})
	t.Cleanup(func() { policygate.Configure(nil) })
	st := &ejectStore{ejected: map[string]string{}}
	r := &Runner{Store: st}
	var rep Report
	keep, err := r.policy(t.Context(), []Entry{{ID: "1", Risk: "low"}, {ID: "2", Risk: "high"}, {ID: "3"}}, &rep)
	if err != nil {
		t.Fatal(err)
	}
	if len(keep) != 1 || keep[0].ID != "1" {
		t.Fatalf("kept %v", keep)
	}
	for _, id := range []string{"2", "3"} {
		if !strings.Contains(st.ejected[id], string(contract.ReasonPolicyNotAllowlisted)) {
			t.Errorf("PR %s ejected with %q", id, st.ejected[id])
		}
	}
	if len(rep.Ejected) != 2 {
		t.Errorf("report ejected %v", rep.Ejected)
	}
}

func TestPolicyOffEjectsNothing(t *testing.T) {
	policygate.Configure(nil)
	r := &Runner{Store: &ejectStore{ejected: map[string]string{}}}
	keep, err := r.policy(t.Context(), []Entry{{ID: "1", Risk: "high"}}, &Report{})
	if err != nil || len(keep) != 1 {
		t.Fatalf("keep=%v err=%v", keep, err)
	}
}

// Run itself applies the policy: with a policy that allows no risk class, nothing lands.
func TestRunAppliesPolicy(t *testing.T) {
	policygate.Configure(riskPolicy{})
	t.Cleanup(func() { policygate.Configure(nil) })
	w := newWorld(2)
	rep, err := w.runner(2).Run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Ejected) != 2 || len(rep.Landed) != 0 || len(landedOrder(w)) != 0 {
		t.Fatalf("report %+v landed %v", rep, landedOrder(w))
	}
}
