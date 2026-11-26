package risk

import (
	"context"
	"testing"

	"github.com/KaaS-team/factory/ee/contract"
)

type fakePolicy struct{ allowed map[contract.RiskClass]bool }

func (f fakePolicy) Evaluate(_ context.Context, r contract.PolicyRequest) contract.Decision {
	if r.Check == contract.CheckAutoMerge && f.allowed[contract.RiskClass(r.Value)] {
		return contract.Decision{Allow: true, Reason: contract.ReasonPolicyAllowed}
	}
	return contract.Decision{Reason: contract.ReasonPolicyNotAllowlisted}
}
func (fakePolicy) Budgets() []contract.BudgetLimit { return nil }

func TestClassify(t *testing.T) {
	prot := []string{".github/workflows", "internal/core/*.go"}
	cases := []struct {
		name string
		c    Change
		want contract.RiskClass
	}{
		{"docs only", Change{Files: []File{{"docs/a.md", 10, 2}, {"README.md", 1, 1}}}, contract.RiskLow},
		{"small code with tests", Change{Files: []File{{"a/a.go", 10, 2}, {"a/a_test.go", 20, 0}}}, contract.RiskLow},
		{"code without tests", Change{Files: []File{{"a/a.go", 10, 2}}}, contract.RiskMedium},
		{"medium diff docs", Change{Files: []File{{"docs/a.md", 100, 0}}}, contract.RiskMedium},
		{"large diff docs", Change{Files: []File{{"docs/a.md", 400, 0}}}, contract.RiskHigh},
		{"protected prefix", Change{Files: []File{{".github/workflows/x.yml", 1, 0}}, Protected: prot}, contract.RiskHigh},
		{"protected glob", Change{Files: []File{{"internal/core/x.go", 1, 0}, {"internal/core/x_test.go", 1, 0}}, Protected: prot}, contract.RiskHigh},
		{"unknown files", Change{}, contract.RiskHigh},
	}
	for _, c := range cases {
		if got := Classify(c.c); got.Class != c.want {
			t.Errorf("%s: class %s (%v), want %s", c.name, got.Class, got.Reasons, c.want)
		}
	}
}

func TestAutoMergeAllowed(t *testing.T) {
	permissive := fakePolicy{map[contract.RiskClass]bool{contract.RiskLow: true}}
	if !AutoMergeAllowed(context.Background(), permissive, contract.RiskLow).Allow {
		t.Error("permissive policy must allow low")
	}
	if AutoMergeAllowed(context.Background(), permissive, contract.RiskMedium).Allow {
		t.Error("medium is not on the list")
	}
	if AutoMergeAllowed(context.Background(), fakePolicy{}, contract.RiskLow).Allow {
		t.Error("strict policy must deny low")
	}
}
