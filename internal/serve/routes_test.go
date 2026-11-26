package serve

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/KaaS-team/factory/ee/contract"
)

// TestEveryRouteDeclaresAnAction: each route names a known action or says why it is open, never both.
func TestEveryRouteDeclaresAnAction(t *testing.T) {
	routes := New(newFakeCore(), Options{}).Routes()
	if len(routes) == 0 {
		t.Fatal("empty route table")
	}
	seen := map[string]bool{}
	for _, rt := range routes {
		switch {
		case seen[rt.Pattern]:
			t.Errorf("%s is listed twice", rt.Pattern)
		case rt.Handler == nil:
			t.Errorf("%s has no handler", rt.Pattern)
		case rt.Action == "" && rt.Open == "":
			t.Errorf("%s has no action and no reason to be open", rt.Pattern)
		case rt.Action != "" && rt.Open != "":
			t.Errorf("%s is both guarded and open", rt.Pattern)
		case rt.Action != "" && !slices.Contains(contract.Actions, rt.Action):
			t.Errorf("%s names unknown action %q", rt.Pattern, rt.Action)
		}
		seen[rt.Pattern] = true
	}
}

// TestRoutesOnlyThroughTheTable: no non-test file registers a handler except routes.go, so a
// route cannot skip the table.
func TestRoutesOnlyThroughTheTable(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	found := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			if c, ok := n.(*ast.CallExpr); ok {
				if sel, ok := c.Fun.(*ast.SelectorExpr); ok && (sel.Sel.Name == "Handle" || sel.Sel.Name == "HandleFunc") {
					found++
					if f != "routes.go" {
						t.Errorf("%s registers a route outside the table", f)
					}
				}
			}
			return true
		})
	}
	if found != 1 {
		t.Errorf("want one registration in routes.go, found %d", found)
	}
}

type fakeIdentity struct {
	byToken map[string]contract.Principal
	err     map[string]error
}

func (f fakeIdentity) Authenticate(_ context.Context, c contract.Credential) (contract.Principal, error) {
	if e, ok := f.err[c.Value]; ok {
		return contract.Principal{}, e
	}
	if p, ok := f.byToken[c.Value]; ok && c.Kind == contract.CredOIDCIDToken {
		return p, nil
	}
	return contract.Principal{}, contract.ErrUnauthenticated
}
func (fakeIdentity) LookupBySCIM(context.Context, string) (contract.Principal, error) {
	return contract.Principal{}, contract.ErrNotFound
}
func (fakeIdentity) Deprovision(context.Context, string) error { return nil }

type allowOnly contract.Action

func (a allowOnly) Can(_ context.Context, _ contract.Principal, act contract.Action, _ contract.Resource) contract.Decision {
	if act == contract.Action(a) {
		return contract.Decision{Allow: true, Reason: contract.ReasonGranted}
	}
	return contract.Decision{Reason: contract.ReasonRoleLacksAction}
}

func TestAccessGuard(t *testing.T) {
	id := fakeIdentity{
		byToken: map[string]contract.Principal{"good": {ID: "u1", Org: "acme", Kind: contract.KindUser}},
		err:     map[string]error{"gone": fmt.Errorf("wrapped: %w", contract.ErrDeprovisioned)},
	}
	for _, tc := range []struct {
		name, path, auth string
		can              contract.Action
		want             int
	}{
		{"healthz is open", "/healthz", "", "", http.StatusServiceUnavailable}, // open: reaches the handler, which has not ticked
		{"no token", "/events", "", contract.ActionRunRead, 401},
		{"bad token", "/events", "Bearer nope", contract.ActionRunRead, 401},
		{"deprovisioned", "/events", "Bearer gone", contract.ActionRunRead, 403},
		{"action denied", "/events", "Bearer good", contract.ActionRunCancel, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := New(newFakeCore(), Options{Access: &Access{Identity: id, Authorizer: allowOnly(tc.can)}})
			r := httptest.NewRequest("GET", tc.path, nil)
			if tc.auth != "" {
				r.Header.Set("Authorization", tc.auth)
			}
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("status %d, want %d: %s", w.Code, tc.want, w.Body)
			}
			if strings.Contains(w.Body.String(), "good") || strings.Contains(w.Body.String(), "wrapped") {
				t.Fatalf("body leaks detail: %s", w.Body)
			}
		})
	}
}

// With no Access, routes behave as before: the password path stays the default.
func TestNoAccessLeavesRoutesOpen(t *testing.T) {
	s := New(newFakeCore(), Options{})
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
	if w.Code == 401 || w.Code == 403 {
		t.Fatalf("status %d", w.Code)
	}
}
