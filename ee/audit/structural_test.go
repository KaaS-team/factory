//go:build ee

// SPDX-License-Identifier: LicenseRef-KaaS-EE

package audit

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KaaS-team/factory/ee/contract"
)

// unwired are the contract actions with no Record call site yet. The site lives in code another
// ticket owns, so each entry names where. The test fails when an entry gains a site (delete it)
// and when a new contract action has neither a site nor an entry.
var unwired = map[contract.Action]string{
	contract.ActionRunRead:       "gap: reads are recorded where the authorizer lands (E1)",
	contract.ActionRunSteer:      "gap: internal/core steer path; not an E3 path",
	contract.ActionRunRetry:      "gap: internal/core retry path; not an E3 path",
	contract.ActionMerge:         "gap: internal/mergeq (F10) calls Record on merge",
	contract.ActionApprove:       "gap: internal/core approval path; not an E3 path",
	contract.ActionPolicyRead:    "gap: the policy engine (E2) records decisions",
	contract.ActionPolicyEdit:    "gap: the policy engine (E2) records edits",
	contract.ActionBudgetRead:    "gap: the spend ledger API (F12)",
	contract.ActionBudgetEdit:    "gap: the admin UI (F15) edits limits",
	contract.ActionAdminUsers:    "gap: SCIM and SSO (E1) record role changes and logins",
	contract.ActionWorkerManage:  "gap: internal/worker (F3)",
	contract.ActionRepoConnect:   "gap: internal/admin (F15)",
	contract.ActionSettingsWrite: "gap: internal/admin (F15)",
}

// sites returns every contract action that appears inside a call to Record or Emit in non-test
// code, plus the actions the core's own records are mapped to by ActionFor.
func sites(t *testing.T) map[contract.Action]string {
	t.Helper()
	names := map[string]contract.Action{}
	for _, a := range contract.Actions {
		names[actionConst(t, a)] = a
	}
	found := map[contract.Action]string{}
	for _, a := range ActionFor {
		found[a] = "ActionFor"
	}
	root := "../.."
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "node_modules" || strings.HasSuffix(filepath.ToSlash(p), "ee/contract")) {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || (sel.Sel.Name != "Record" && sel.Sel.Name != "Emit") {
				return true
			}
			ast.Inspect(call, func(m ast.Node) bool {
				if s, ok := m.(*ast.SelectorExpr); ok {
					if x, ok := s.X.(*ast.Ident); ok && x.Name == "contract" {
						if a, ok := names[s.Sel.Name]; ok {
							found[a] = filepath.ToSlash(p)
						}
					}
				}
				return true
			})
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return found
}

// actionConst finds the Go constant name of a, by reading ee/contract/authz.go.
func actionConst(t *testing.T, a contract.Action) string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "../contract/authz.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var name string
	ast.Inspect(f, func(n ast.Node) bool {
		if vs, ok := n.(*ast.ValueSpec); ok && len(vs.Values) == 1 {
			if lit, ok := vs.Values[0].(*ast.BasicLit); ok && lit.Value == `"`+string(a)+`"` && strings.HasPrefix(vs.Names[0].Name, "Action") {
				name = vs.Names[0].Name
			}
		}
		return true
	})
	if name == "" {
		t.Fatalf("no constant for action %s", a)
	}
	return name
}

func TestEveryActionHasARecordSite(t *testing.T) {
	found := sites(t)
	for _, a := range contract.Actions {
		_, hasSite := found[a]
		why, listed := unwired[a]
		switch {
		case !hasSite && !listed:
			t.Errorf("action %s has no Record call site and is not listed as unwired", a)
		case hasSite && listed:
			t.Errorf("action %s now has a site (%s); remove it from unwired (%s)", a, found[a], why)
		}
	}
	for a := range unwired {
		if !strings.HasPrefix(unwired[a], "gap:") {
			t.Errorf("unwired %s needs a reason starting gap:", a)
		}
	}
	// The walk must find the sites E3 owns, or it is not walking.
	for _, a := range []contract.Action{contract.ActionRunStart, contract.ActionRunCancel, contract.ActionAuditRead, contract.ActionAuditExport} {
		if found[a] == "" {
			t.Errorf("the walk did not find the E3 site of %s", a)
		}
	}
}
