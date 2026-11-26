package local

import (
	"context"
	"errors"
	"testing"

	"github.com/KaaS-team/factory/ee/contract"
	"github.com/KaaS-team/factory/internal/policygate"
)

type mcpPolicy struct{}

func (mcpPolicy) Evaluate(_ context.Context, r contract.PolicyRequest) contract.Decision {
	if r.Check == contract.CheckMCPServer && r.Value == "github" {
		return contract.Decision{Allow: true, Reason: contract.ReasonPolicyAllowed}
	}
	return contract.Decision{Reason: contract.ReasonPolicyNotAllowlisted}
}
func (mcpPolicy) Budgets() []contract.BudgetLimit { return nil }

// An MCP config naming a server the policy does not list builds no argv.
func TestClaudeArgvRefusesUnlistedMCPServer(t *testing.T) {
	policygate.Configure(mcpPolicy{})
	t.Cleanup(func() { policygate.Configure(nil) })
	in := ClaudeInput{Stage: "build", Issue: "7", MCPServers: []string{"github"}}
	if _, err := ClaudeArgv(in); err != nil {
		t.Fatalf("listed server: %v", err)
	}
	in.MCPServers = []string{"github", "evil"}
	if _, err := ClaudeArgv(in); !errors.Is(err, policygate.ErrDenied) {
		t.Fatalf("unlisted server: %v", err)
	}
}
