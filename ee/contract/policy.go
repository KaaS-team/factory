// SPDX-License-Identifier: Apache-2.0

package contract

import "context"

// PolicyFileName is the file each layer is read from: the org's in the org's config repo, a
// team's in its team directory, a repo's at the repo root.
const PolicyFileName = "kaas-policy.toml"

// PolicyVersion is the only schema version this package reads.
const PolicyVersion = 1

// Layer is where a policy file sits. Layers apply org, then team, then repo.
type Layer string

// The three layers, from widest to narrowest.
const (
	LayerOrg  Layer = "org"
	LayerTeam Layer = "team"
	LayerRepo Layer = "repo"
)

// PolicyFile is the kaas-policy.toml schema, one layer. The json tags give the same shape for the
// conformance file. testdata/kaas-policy.example.toml sets every field.
//
// An allowlist left out (nil) inherits the layer above; set to [] it allows nothing. An allowlist
// no layer sets allows nothing: the policy is default deny.
type PolicyFile struct {
	Version  int           `toml:"version" json:"version"` // must be PolicyVersion
	Layer    Layer         `toml:"layer" json:"layer"`
	Name     string        `toml:"name" json:"name"` // the org id, team id or "owner/name"
	Models   Allowlist     `toml:"models" json:"models"`
	Harness  Allowlist     `toml:"harnesses" json:"harnesses"`
	MCP      MCPPolicy     `toml:"mcp" json:"mcp"`
	Egress   Egress        `toml:"egress" json:"egress"`
	Merge    Merge         `toml:"merge" json:"merge"`
	Approval Approval      `toml:"approval" json:"approval"`
	Budgets  []BudgetLimit `toml:"budgets" json:"budgets,omitempty"`
}

// Allowlist is a list of exact names, such as model ids or harness names.
type Allowlist struct {
	Allowed []string `toml:"allowed" json:"allowed,omitempty"`
}

// MCPPolicy lists the MCP servers a session may connect to, by the name in its MCP config.
type MCPPolicy struct {
	AllowedServers []string `toml:"allowed_servers" json:"allowed_servers,omitempty"`
}

// Egress lists the hosts a session's sandbox may reach. An entry is a host name ("github.com") or
// a wildcard "*.example.com", which matches any host ending in ".example.com" but not
// "example.com" itself. No ports, schemes or paths.
type Egress struct {
	AllowedHosts []string `toml:"allowed_hosts" json:"allowed_hosts,omitempty"`
}

// RiskClass is the risk the classifier gives a change.
type RiskClass string

// The risk classes.
const (
	RiskLow    RiskClass = "low"
	RiskMedium RiskClass = "medium"
	RiskHigh   RiskClass = "high"
)

// Merge says which risk classes may merge without a human review.
type Merge struct {
	AutoMergeRisk []RiskClass `toml:"auto_merge_risk" json:"auto_merge_risk,omitempty"`
}

// Approval lists the actions that need a human approval (ActionApprove by a user) before they run.
type Approval struct {
	RequireHuman []Action `toml:"require_human" json:"require_human,omitempty"`
}

// BudgetLimit is one budget limit in a policy file. USD is the limit per Period.
type BudgetLimit struct {
	Scope  BudgetScope `toml:"scope" json:"scope"`
	ID     string      `toml:"id" json:"id"`
	USD    float64     `toml:"usd" json:"usd"`
	Period Period      `toml:"period" json:"period"`
}

// Period is the window a budget limit resets on, at 00:00 UTC.
type Period string

// The periods. A week starts on Monday.
const (
	PeriodDay   Period = "day"
	PeriodWeek  Period = "week"
	PeriodMonth Period = "month"
)

// Check is the kind of question a PolicyRequest asks.
type Check string

// The checks. Value holds, for each: a model id, a harness name, an MCP server name, a host, a
// RiskClass, or an Action.
const (
	CheckModel     Check = "model"
	CheckHarness   Check = "harness"
	CheckMCPServer Check = "mcp_server"
	CheckEgress    Check = "egress_host"
	CheckAutoMerge Check = "auto_merge"
	CheckAction    Check = "action"
)

// PolicyRequest is one question to Policy.Evaluate.
type PolicyRequest struct {
	Check         Check  `json:"check"`
	Value         string `json:"value"`
	HumanApproved bool   `json:"human_approved,omitempty"` // CheckAction only: a user approved it
}

// PolicyCompiler turns layers into one effective Policy.
type PolicyCompiler interface {
	// Compile layers files in order: exactly one org layer first, then at most one team layer,
	// then at most one repo layer. Any other order, a repeat, or a Version other than
	// PolicyVersion returns ErrPolicyLayerOrder or ErrPolicyInvalid.
	//
	// A lower layer may only narrow:
	//   - An allowlist the layer sets replaces the one above, and every entry must be covered by
	//     the effective list above. An egress entry is covered by an equal entry or by a wildcard
	//     "*.d" above whose suffix ".d" ends the entry (with its own "*" removed). An entry not
	//     covered returns ErrPolicyWidening naming it.
	//   - Approval.RequireHuman adds to the list above; nothing can remove an entry.
	//   - A budget with the same Scope, ID and Period as one above must have USD no larger, else
	//     ErrPolicyWidening; the effective limit is the smaller. A budget with a new key is added.
	Compile(files ...PolicyFile) (Policy, error)
}

// Policy is the effective policy for one repo, after layering.
type Policy interface {
	// Evaluate answers one request. It never errors:
	//   - CheckModel, CheckHarness, CheckMCPServer, CheckAutoMerge: allow ReasonPolicyAllowed when
	//     Value is on the effective list, else deny ReasonPolicyNotAllowlisted.
	//   - CheckEgress: the same, with the wildcard matching of Egress.
	//   - CheckAction: deny ReasonPolicyApprovalRequired when Value is in RequireHuman and
	//     HumanApproved is false; otherwise allow ReasonPolicyAllowed. Whether the caller may take
	//     the action at all is Authorizer's question, not this one.
	//   - Any other Check: deny ReasonPolicyUnknownCheck.
	Evaluate(ctx context.Context, r PolicyRequest) Decision

	// Budgets returns the effective budget limits, which a Budget is configured from.
	Budgets() []BudgetLimit
}
