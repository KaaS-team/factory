//go:build ee

// SPDX-License-Identifier: LicenseRef-KaaS-EE

package authz_test

import (
	"encoding/json"
	"os"
	"slices"
	"testing"

	"github.com/KaaS-team/factory/ee/authz"
	"github.com/KaaS-team/factory/ee/contract"
)

// TestConformanceAuthz runs every authz case in the contract's conformance.json.
func TestConformanceAuthz(t *testing.T) {
	b, err := os.ReadFile("../contract/testdata/conformance.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Authz []struct {
			Name      string             `json:"name"`
			Principal contract.Principal `json:"principal"`
			Action    contract.Action    `json:"action"`
			Resource  contract.Resource  `json:"resource"`
			Want      contract.Decision  `json:"want"`
		} `json:"authz"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Authz) == 0 {
		t.Fatal("no authz cases")
	}
	a := authz.New()
	for _, c := range f.Authz {
		t.Run(c.Name, func(t *testing.T) {
			if got := a.Can(t.Context(), c.Principal, c.Action, c.Resource); got != c.Want {
				t.Errorf("got %+v, want %+v", got, c.Want)
			}
		})
	}
}

func user(roles ...contract.RoleBinding) contract.Principal {
	return contract.Principal{ID: "u", Kind: contract.KindUser, Org: "acme", Roles: roles}
}

func bind(r contract.Role, k contract.ScopeKind, id string) contract.RoleBinding {
	return contract.RoleBinding{Role: r, Scope: contract.Scope{Kind: k, ID: id}}
}

// Every role, action and scope kind against the table in the contract: org-only actions need an org binding.
func TestScopeMatrix(t *testing.T) {
	a := authz.New()
	res := contract.Resource{Org: "acme", Team: "core", Repo: "acme/api"}
	for _, role := range contract.Roles {
		for _, act := range contract.Actions {
			if act == contract.ActionApprove {
				continue // covered by TestApproveNeedsAUser
			}
			granted := slices.Contains(contract.Grants[role], act)
			for _, sc := range []struct {
				kind contract.ScopeKind
				id   string
			}{{contract.ScopeOrg, "acme"}, {contract.ScopeTeam, "core"}, {contract.ScopeRepo, "acme/api"}} {
				want := granted && (sc.kind == contract.ScopeOrg || !slices.Contains(contract.OrgOnlyActions, act))
				if got := a.Can(t.Context(), user(bind(role, sc.kind, sc.id)), act, res).Allow; got != want {
					t.Errorf("%s %s at %s: allow=%v, want %v", role, act, sc.kind, got, want)
				}
			}
		}
	}
}

func TestApproveNeedsAUser(t *testing.T) {
	a := authz.New()
	for _, k := range []contract.PrincipalKind{contract.KindBot, contract.KindService} {
		p := user(bind(contract.RoleOwner, contract.ScopeOrg, "acme"))
		p.Kind = k
		if d := a.Can(t.Context(), p, contract.ActionApprove, contract.Resource{Org: "acme"}); d.Allow || d.Reason != contract.ReasonHumanRequired {
			t.Errorf("%s approved: %+v", k, d)
		}
	}
}

// Bindings add up and a narrower one never removes a wider grant.
func TestBindingsUnion(t *testing.T) {
	p := user(bind(contract.RoleViewer, contract.ScopeRepo, "acme/api"), bind(contract.RoleMember, contract.ScopeTeam, "core"))
	d := authz.New().Can(t.Context(), p, contract.ActionRunStart, contract.Resource{Org: "acme", Team: "core", Repo: "acme/api"})
	if !d.Allow {
		t.Fatalf("%+v", d)
	}
	if d := authz.New().Can(t.Context(), p, contract.ActionRunStart, contract.Resource{Org: "acme", Team: "other", Repo: "acme/web"}); d.Reason != contract.ReasonNoBinding {
		t.Fatalf("%+v", d)
	}
}

func TestGroupMap(t *testing.T) {
	m := authz.GroupMap{
		{Group: "eng-admins", Role: contract.RoleAdmin},
		{Group: "core-devs", Role: contract.RoleMember, Scope: contract.Scope{Kind: contract.ScopeTeam, ID: "core"}},
		{Group: "everyone", Role: contract.RoleViewer},
	}
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	b, teams := m.Bindings("acme", []string{"core-devs", "everyone", "unmapped"})
	want := []contract.RoleBinding{bind(contract.RoleMember, contract.ScopeTeam, "core"), bind(contract.RoleViewer, contract.ScopeOrg, "acme")}
	if !slices.Equal(b, want) || !slices.Equal(teams, []string{"core"}) {
		t.Fatalf("bindings %+v teams %v", b, teams)
	}
	if b, _ := m.Bindings("acme", nil); len(b) != 0 {
		t.Fatalf("no groups earned %+v", b)
	}
}

func TestGroupMapValidate(t *testing.T) {
	for name, m := range map[string]authz.GroupMap{
		"no group":      {{Role: contract.RoleAdmin}},
		"unknown role":  {{Group: "g", Role: "root"}},
		"team no id":    {{Group: "g", Role: contract.RoleMember, Scope: contract.Scope{Kind: contract.ScopeTeam}}},
		"unknown scope": {{Group: "g", Role: contract.RoleMember, Scope: contract.Scope{Kind: "galaxy", ID: "x"}}},
	} {
		if m.Validate() == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}
