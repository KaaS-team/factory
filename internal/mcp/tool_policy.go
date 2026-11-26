package mcp

import (
	"context"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/KaaS-team/factory/internal/api"
)

// PolicyInput asks the policy engine to check a kaas-policy.toml file or to explain one decision.
type PolicyInput struct {
	Action string `json:"action" jsonschema:"check or explain"`
	Path   string `json:"path,omitempty" jsonschema:"check: the file to validate; default .factory/kaas-policy.toml"`
	Check  string `json:"check,omitempty" jsonschema:"explain: model, harness, mcp_server, egress_host, auto_merge or action; default action"`
	Value  string `json:"value,omitempty" jsonschema:"explain: the model, harness, server, host, risk class or action to ask about"`
	Org    string `json:"org,omitempty" jsonschema:"path to the org layer file"`
	Team   string `json:"team,omitempty" jsonschema:"path to the team layer file"`
	Repo   string `json:"repo,omitempty" jsonschema:"explain: path to the repo layer file"`
}

// PolicyRun is set by the enterprise build. Without it the tool answers that it is not available.
var PolicyRun func(ctx context.Context, in PolicyInput) (string, error)

func init() {
	registerTool("policy", false, func(s *sdk.Server, _ api.Control, _ Handlers) {
		sdk.AddTool(s, &sdk.Tool{Name: "policy", Description: "Check a kaas-policy.toml file for violations, or explain which layer decided an action, model, harness, MCP server, host or risk class. Read-only."}, textTool("policy", PolicyRun))
	})
}
