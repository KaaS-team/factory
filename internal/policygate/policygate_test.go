package policygate_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/KaaS-team/factory/ee/contract"
	"github.com/KaaS-team/factory/internal/api"
	"github.com/KaaS-team/factory/internal/policygate"
)

// listPolicy allows exactly the values in allow, for every check.
type listPolicy struct{ allow map[string]bool }

func (p listPolicy) Evaluate(_ context.Context, r contract.PolicyRequest) contract.Decision {
	if p.allow[string(r.Check)+":"+r.Value] {
		return contract.Decision{Allow: true, Reason: contract.ReasonPolicyAllowed}
	}
	return contract.Decision{Reason: contract.ReasonPolicyNotAllowlisted}
}
func (listPolicy) Budgets() []contract.BudgetLimit { return nil }

type fakeDispatcher struct {
	api.Dispatcher
	calls int
}

func (f *fakeDispatcher) Dispatch(context.Context, api.Brief) (api.Session, error) {
	f.calls++
	return api.Session{ID: "s"}, nil
}

func TestNoPolicyAllowsEverything(t *testing.T) {
	policygate.Configure(nil)
	d := &fakeDispatcher{}
	if _, err := policygate.Wrap(d).Dispatch(t.Context(), api.Brief{Model: "any", Agent: "any"}); err != nil || d.calls != 1 {
		t.Fatalf("err=%v calls=%d", err, d.calls)
	}
}

func TestDispatchRefusesDisallowedModelAndHarness(t *testing.T) {
	policygate.Configure(listPolicy{map[string]bool{"harness:claude-code": true, "model:m-ok": true}})
	t.Cleanup(func() { policygate.Configure(nil) })
	for _, tc := range []struct {
		name  string
		brief api.Brief
		ok    bool
	}{
		{"allowed", api.Brief{Model: "m-ok"}, true},
		{"no model named", api.Brief{}, true},
		{"claude preset is claude-code", api.Brief{Agent: "claude-plan", Model: "m-ok"}, true},
		{"model refused", api.Brief{Model: "m-bad"}, false},
		{"harness refused", api.Brief{Agent: "codex", Model: "m-ok"}, false},
	} {
		d := &fakeDispatcher{}
		_, err := policygate.Wrap(d).Dispatch(t.Context(), tc.brief)
		switch {
		case tc.ok && err != nil:
			t.Errorf("%s: %v", tc.name, err)
		case !tc.ok && (!errors.Is(err, policygate.ErrDenied) || !strings.Contains(err.Error(), string(contract.ReasonPolicyNotAllowlisted)) || d.calls != 0):
			t.Errorf("%s: err=%v calls=%d", tc.name, err, d.calls)
		case !tc.ok && strings.Contains(err.Error(), "m-bad"):
			t.Errorf("%s: refusal names the model: %v", tc.name, err)
		}
	}
}
