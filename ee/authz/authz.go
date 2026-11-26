//go:build ee

// SPDX-License-Identifier: LicenseRef-KaaS-EE

// Package authz decides what a principal may do. It implements contract.Authorizer: five roles
// (owner, admin, member, viewer, bot) bound at org, team or repo scope, and a map from IdP group
// names to role bindings. The rules and the role table are the contract's; this package adds none.
package authz

import (
	"context"
	"fmt"
	"slices"

	"github.com/KaaS-team/factory/ee/contract"
)

// Authorizer answers contract.Authorizer.Can from the principal's own bindings.
type Authorizer struct{}

var _ contract.Authorizer = Authorizer{}

// New returns an Authorizer. It holds no state: authority lives on the Principal.
func New() Authorizer { return Authorizer{} }

// Can follows the eight ordered rules in the contract's doc comment.
func (Authorizer) Can(_ context.Context, p contract.Principal, a contract.Action, r contract.Resource) contract.Decision {
	deny := func(c contract.ReasonCode) contract.Decision { return contract.Decision{Reason: c} }
	switch {
	case !slices.Contains(contract.Actions, a):
		return deny(contract.ReasonUnknownAction)
	case p.Disabled:
		return deny(contract.ReasonDeprovisioned)
	case r.Org != p.Org:
		return deny(contract.ReasonCrossOrg)
	case a == contract.ActionApprove && p.Kind != contract.KindUser:
		return deny(contract.ReasonHumanRequired)
	}
	covered, narrow := false, false
	for _, b := range p.Roles {
		if !covers(b.Scope, r) {
			continue
		}
		covered = true
		if !slices.Contains(contract.Grants[b.Role], a) {
			continue
		}
		if b.Scope.Kind == contract.ScopeOrg || !slices.Contains(contract.OrgOnlyActions, a) {
			return contract.Decision{Allow: true, Reason: contract.ReasonGranted}
		}
		narrow = true
	}
	switch {
	case !covered:
		return deny(contract.ReasonNoBinding)
	case narrow:
		return deny(contract.ReasonScopeTooNarrow)
	}
	return deny(contract.ReasonRoleLacksAction)
}

// covers reports whether a binding at s applies to r (rule 5).
func covers(s contract.Scope, r contract.Resource) bool {
	switch s.Kind {
	case contract.ScopeOrg:
		return s.ID == r.Org
	case contract.ScopeTeam:
		return r.Team != "" && s.ID == r.Team
	case contract.ScopeRepo:
		return r.Repo != "" && s.ID == r.Repo
	}
	return false
}

// GroupRole says that members of an IdP group hold Role at Scope. A zero Scope means the whole org.
type GroupRole struct {
	Group string         `json:"group"`
	Role  contract.Role  `json:"role"`
	Scope contract.Scope `json:"scope"`
}

// GroupMap is the role mapping from IdP groups, read from config.
type GroupMap []GroupRole

// Validate refuses an unknown role, an unknown scope kind or a scoped entry with no id. A bad map
// fails at load, so a typo never silently grants nothing or too much.
func (m GroupMap) Validate() error {
	for i, g := range m {
		switch {
		case g.Group == "":
			return fmt.Errorf("authz: group map entry %d has no group", i)
		case !slices.Contains(contract.Roles, g.Role):
			return fmt.Errorf("authz: group %q maps to unknown role %q", g.Group, g.Role)
		case g.Scope.Kind != "" && g.Scope.Kind != contract.ScopeOrg && g.Scope.ID == "":
			return fmt.Errorf("authz: group %q has a %s scope with no id", g.Group, g.Scope.Kind)
		case g.Scope.Kind != "" && !slices.Contains([]contract.ScopeKind{contract.ScopeOrg, contract.ScopeTeam, contract.ScopeRepo}, g.Scope.Kind):
			return fmt.Errorf("authz: group %q has unknown scope kind %q", g.Group, g.Scope.Kind)
		}
	}
	return nil
}

// Bindings returns the role bindings the groups earn in org, without duplicates, and the ids of the
// teams among them. A group in no entry earns nothing.
func (m GroupMap) Bindings(org string, groups []string) (bindings []contract.RoleBinding, teams []string) {
	for _, g := range m {
		if !slices.Contains(groups, g.Group) {
			continue
		}
		s := g.Scope
		if s.Kind == "" || s.Kind == contract.ScopeOrg {
			s = contract.Scope{Kind: contract.ScopeOrg, ID: org}
		}
		b := contract.RoleBinding{Role: g.Role, Scope: s}
		if !slices.Contains(bindings, b) {
			bindings = append(bindings, b)
		}
		if s.Kind == contract.ScopeTeam && !slices.Contains(teams, s.ID) {
			teams = append(teams, s.ID)
		}
	}
	return bindings, teams
}
