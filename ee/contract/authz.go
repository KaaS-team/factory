// SPDX-License-Identifier: Apache-2.0

package contract

import "context"

// Action is something a Principal asks to do. The set is closed: Actions lists every one, and
// Authorizer.Can denies any other string with ReasonUnknownAction.
type Action string

// The actions.
const (
	ActionRunRead       Action = "run.read"       // see runs, the board and their events
	ActionRunStart      Action = "run.start"      // dispatch a ticket to a session
	ActionRunCancel     Action = "run.cancel"     // cancel a running session
	ActionRunSteer      Action = "run.steer"      // send a message to a running session
	ActionRunRetry      Action = "run.retry"      // queue another attempt
	ActionMerge         Action = "merge"          // put a PR in the merge queue or merge it
	ActionApprove       Action = "approve"        // give the human approval a policy asks for
	ActionPolicyRead    Action = "policy.read"    // read the effective policy
	ActionPolicyEdit    Action = "policy.edit"    // change a policy layer
	ActionBudgetRead    Action = "budget.read"    // read budgets and spend
	ActionBudgetEdit    Action = "budget.edit"    // change a budget limit
	ActionAuditRead     Action = "audit.read"     // search the audit log
	ActionAuditExport   Action = "audit.export"   // export the whole audit log
	ActionAdminUsers    Action = "admin.users"    // add, remove and re-role users
	ActionWorkerManage  Action = "worker.manage"  // add, drain and remove workers
	ActionRepoConnect   Action = "repo.connect"   // connect or disconnect a repository
	ActionSettingsWrite Action = "settings.write" // change factory settings for the scope
)

// Actions is every Action, in the order above. A test fails when a constant is missing here.
var Actions = []Action{
	ActionRunRead, ActionRunStart, ActionRunCancel, ActionRunSteer, ActionRunRetry, ActionMerge,
	ActionApprove, ActionPolicyRead, ActionPolicyEdit, ActionBudgetRead, ActionBudgetEdit,
	ActionAuditRead, ActionAuditExport, ActionAdminUsers, ActionWorkerManage, ActionRepoConnect,
	ActionSettingsWrite,
}

// OrgOnlyActions are granted only by a binding at ScopeOrg. A team or repo binding whose role
// would grant one yields ReasonScopeTooNarrow.
var OrgOnlyActions = []Action{ActionAuditExport, ActionAdminUsers, ActionWorkerManage}

// Role is a named set of actions.
type Role string

// The roles, from most to least power. Each grants everything the next one grants, except bot,
// which is a separate set for agents.
const (
	RoleOwner  Role = "owner"
	RoleAdmin  Role = "admin"
	RoleMember Role = "member"
	RoleViewer Role = "viewer"
	RoleBot    Role = "bot"
)

// Roles is every Role.
var Roles = []Role{RoleOwner, RoleAdmin, RoleMember, RoleViewer, RoleBot}

// Grants is the action set of each role, and the one table of it. Viewer reads; bot adds what an
// agent needs to run work; member adds cancel, merge and approve; admin adds policy, budget,
// audit, worker, repo and settings changes; owner adds admin.users. The authz cases in
// testdata/conformance.json check every role and action pair at org scope against it.
var Grants = map[Role][]Action{
	RoleViewer: {ActionRunRead, ActionPolicyRead, ActionBudgetRead},
	RoleBot:    {ActionRunRead, ActionRunStart, ActionRunSteer, ActionRunRetry, ActionPolicyRead, ActionBudgetRead},
	RoleMember: {ActionRunRead, ActionPolicyRead, ActionBudgetRead, ActionRunStart, ActionRunCancel, ActionRunSteer,
		ActionRunRetry, ActionMerge, ActionApprove},
	RoleAdmin: {ActionRunRead, ActionPolicyRead, ActionBudgetRead, ActionRunStart, ActionRunCancel, ActionRunSteer,
		ActionRunRetry, ActionMerge, ActionApprove, ActionPolicyEdit, ActionBudgetEdit, ActionAuditRead,
		ActionAuditExport, ActionWorkerManage, ActionRepoConnect, ActionSettingsWrite},
	RoleOwner: {ActionRunRead, ActionPolicyRead, ActionBudgetRead, ActionRunStart, ActionRunCancel, ActionRunSteer,
		ActionRunRetry, ActionMerge, ActionApprove, ActionPolicyEdit, ActionBudgetEdit, ActionAuditRead,
		ActionAuditExport, ActionWorkerManage, ActionRepoConnect, ActionSettingsWrite, ActionAdminUsers},
}

// ScopeKind is the level a role binding applies at.
type ScopeKind string

// The three scopes. An org binding covers every team and repo in the org.
const (
	ScopeOrg  ScopeKind = "org"
	ScopeTeam ScopeKind = "team"
	ScopeRepo ScopeKind = "repo"
)

// Scope is one org, team or repo. ID is the org id, the team id, or the repo as "owner/name".
type Scope struct {
	Kind ScopeKind `json:"kind"`
	ID   string    `json:"id"`
}

// RoleBinding grants Role at Scope.
type RoleBinding struct {
	Role  Role  `json:"role"`
	Scope Scope `json:"scope"`
}

// Resource is what an action touches. Org is required; Team and Repo are empty when the action
// is org-wide (for example admin.users).
type Resource struct {
	Org  string `json:"org"`
	Team string `json:"team,omitempty"`
	Repo string `json:"repo,omitempty"`
}

// Authorizer decides whether a Principal may take an Action on a Resource.
type Authorizer interface {
	// Can never errors and gives the same Decision for the same inputs. It checks, in order, and returns at the first that holds:
	//
	//  1. a is not in Actions: deny ReasonUnknownAction.
	//  2. p.Disabled: deny ReasonDeprovisioned.
	//  3. r.Org != p.Org: deny ReasonCrossOrg.
	//  4. a is ActionApprove and p.Kind is not KindUser: deny ReasonHumanRequired.
	//  5. A binding covers r when it is {org, r.Org}, {team, r.Team} with r.Team set, or
	//     {repo, r.Repo} with r.Repo set. No binding covers r: deny ReasonNoBinding.
	//  6. A covering binding's role grants a (Grants), and a is not in OrgOnlyActions or that
	//     binding is at ScopeOrg: allow ReasonGranted.
	//  7. A covering binding's role grants a but only below org scope: deny ReasonScopeTooNarrow.
	//  8. Otherwise: deny ReasonRoleLacksAction.
	//
	// Bindings add up: the union of covering bindings counts, and no binding ever removes a grant.
	// An implementation may add custom roles, but must answer exactly this for the five built in.
	Can(ctx context.Context, p Principal, a Action, r Resource) Decision
}
