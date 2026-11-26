package main

import (
	"context"
	"errors"
	"testing"

	"github.com/KaaS-team/factory/ee/contract"
	"github.com/KaaS-team/factory/internal/api"
	"github.com/KaaS-team/factory/internal/config"
	"github.com/KaaS-team/factory/internal/policygate"
)

type denyAll struct{}

func (denyAll) Evaluate(context.Context, contract.PolicyRequest) contract.Decision {
	return contract.Decision{Reason: contract.ReasonPolicyNotAllowlisted}
}
func (denyAll) Budgets() []contract.BudgetLimit { return nil }

// Every dispatcher built for a command is behind the policy gate.
func TestBuildDispatcherEnforcesPolicy(t *testing.T) {
	policygate.Configure(denyAll{})
	t.Cleanup(func() { policygate.Configure(nil) })
	inner := &countingDispatcher{}
	dispatchers["gated"] = func(context.Context, config.Config) (api.Dispatcher, error) { return inner, nil }
	t.Cleanup(func() { delete(dispatchers, "gated") })
	d, err := buildDispatcher(t.Context(), config.Config{Dispatcher: "gated"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Dispatch(t.Context(), api.Brief{Model: "m"}); !errors.Is(err, policygate.ErrDenied) || inner.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, inner.calls)
	}
}
