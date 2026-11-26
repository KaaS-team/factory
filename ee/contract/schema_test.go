// SPDX-License-Identifier: Apache-2.0

package contract_test

import (
	"bytes"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/google/jsonschema-go/jsonschema"

	"github.com/KaaS-team/factory/ee/contract"
)

// TestPolicyExampleDecodes: the example file decodes into PolicyFile with no unknown key, sets
// every field, and compiles as an org layer whose budgets convert to Micros.
func TestPolicyExampleDecodes(t *testing.T) {
	var f contract.PolicyFile
	md, err := toml.DecodeFile(filepath.Join("testdata", "kaas-policy.example.toml"), &f)
	if err != nil {
		t.Fatal(err)
	}
	if u := md.Undecoded(); len(u) > 0 {
		t.Errorf("keys not in PolicyFile: %v", u)
	}
	unset(t, reflect.ValueOf(f), "PolicyFile")
	scopes := map[contract.BudgetScope]bool{}
	for _, b := range f.Budgets {
		scopes[b.Scope] = true
	}
	for _, s := range []contract.BudgetScope{contract.BudgetOrg, contract.BudgetTeam, contract.BudgetFactory, contract.BudgetBot, contract.BudgetAction} {
		if !scopes[s] {
			t.Errorf("example has no %s budget", s)
		}
	}
	p, err := refCompiler{}.Compile(f)
	if err != nil {
		t.Fatal(err)
	}
	if d := p.Evaluate(t.Context(), contract.PolicyRequest{Check: contract.CheckAction, Value: string(contract.ActionMerge)}); d.Reason != contract.ReasonPolicyApprovalRequired {
		t.Errorf("merge without approval: %+v", d)
	}
	if got := microsOf(p.Budgets()[0].USD); got != 5_000_000_000 {
		t.Errorf("org budget %d micros", got)
	}
}

// unset fails for every zero field under v, so a new PolicyFile field needs an example line.
func unset(t *testing.T, v reflect.Value, path string) {
	t.Helper()
	switch v.Kind() {
	case reflect.Struct:
		for i := range v.NumField() {
			unset(t, v.Field(i), path+"."+v.Type().Field(i).Name)
		}
	case reflect.Slice:
		if v.Len() == 0 {
			t.Errorf("example leaves %s empty", path)
		}
		for i := range v.Len() {
			unset(t, v.Index(i), path+"["+strconv.Itoa(i)+"]")
		}
	default:
		if v.IsZero() {
			t.Errorf("example leaves %s unset", path)
		}
	}
}

// TestPolicyTagsMatch: each PolicyFile field has the same name in its toml and json tags, so the
// TOML file and the conformance JSON describe one shape.
func TestPolicyTagsMatch(t *testing.T) {
	var walk func(reflect.Type)
	walk = func(ty reflect.Type) {
		for i := range ty.NumField() {
			f := ty.Field(i)
			tm, js := f.Tag.Get("toml"), strings.Split(f.Tag.Get("json"), ",")[0]
			if tm == "" || tm != js {
				t.Errorf("%s.%s: toml %q, json %q", ty.Name(), f.Name, tm, js)
			}
			e := f.Type
			if e.Kind() == reflect.Slice {
				e = e.Elem()
			}
			if e.Kind() == reflect.Struct {
				walk(e)
			}
		}
	}
	walk(reflect.TypeFor[contract.PolicyFile]())
}

func loadLedgerSchema(t *testing.T) (*jsonschema.Resolved, map[string]any) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "contracts", "run-ledger.v1.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var s jsonschema.Schema
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatal(err)
	}
	r, err := s.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	return r, raw
}

const ledgerSample = `{"schema":"kaas.run/v1","run_id":"run-42","repo":"acme/pay","team":"payments",
"ticket":{"source":"github","id":"123","url":"https://github.com/acme/pay/issues/123"},
"agent":{"harness":"claude-code","model":"provider-large-2026","worker":"k8s"},
"started_at":"2026-11-26T09:00:00Z","ended_at":"2026-11-26T09:42:10.5+02:00","status":"merged",
"failure_class":"retryable",
"pr":{"number":77,"url":"https://github.com/acme/pay/pull/77","head_sha":"abc","approved_sha":"abc","merged_sha":"def",
"opened_at":"2026-11-26T09:30:00Z","merged_at":"2026-11-26T09:42:00Z","stack_position":0,"lines_added":120,"lines_deleted":0},
"review":{"rounds":0,"human_approver":"u-alice","auto_merged":false},
"tokens":{"input":1000,"output":200,"cache_read":0,"cache_write":50},"cost_usd":1.25,
"qa":{"verdict":"pass","evidence_url":"https://example.com/qa/42"}}`

// TestRunLedgerEntry: the sample validates against the schema file and round-trips through
// RunLedgerEntry unchanged, zeros and falses included; a sample the schema refuses is refused.
func TestRunLedgerEntry(t *testing.T) {
	schema, _ := loadLedgerSchema(t)
	var inst map[string]any
	if err := json.Unmarshal([]byte(ledgerSample), &inst); err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(inst); err != nil {
		t.Fatalf("sample does not validate: %v", err)
	}
	var e contract.RunLedgerEntry
	dec := json.NewDecoder(strings.NewReader(ledgerSample))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&e); err != nil {
		t.Fatal(err)
	}
	if e.Schema != contract.RunLedgerSchema {
		t.Errorf("schema %q", e.Schema)
	}
	out, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(inst, back) {
		t.Errorf("round trip changed the entry:\n in  %s\n out %s", ledgerSample, out)
	}
	if err := schema.Validate(back); err != nil {
		t.Errorf("round-tripped entry does not validate: %v", err)
	}
	minimal := contract.RunLedgerEntry{Schema: contract.RunLedgerSchema, RunID: "r", Repo: "a/b", Status: "open",
		Ticket: contract.LedgerTicket{Source: "api", ID: "1"}, Agent: contract.LedgerAgent{Harness: "codex"}}
	var m map[string]any
	b, _ := json.Marshal(minimal)
	_ = json.Unmarshal(b, &m)
	if err := schema.Validate(m); err != nil {
		t.Errorf("minimal entry does not validate: %v\n%s", err, b)
	}
	m["status"] = "exploded"
	if schema.Validate(m) == nil {
		t.Error("schema accepted status exploded")
	}
}

// TestRunLedgerFieldsMatchSchema: every schema property is a json field of the Go type and every
// field is a property, recursively, so neither side can gain a field alone.
func TestRunLedgerFieldsMatchSchema(t *testing.T) {
	_, raw := loadLedgerSchema(t)
	var walk func(path string, s map[string]any, ty reflect.Type)
	walk = func(path string, s map[string]any, ty reflect.Type) {
		props, _ := s["properties"].(map[string]any)
		fields := map[string]reflect.Type{}
		for i := range ty.NumField() {
			f := ty.Field(i)
			ft := f.Type
			if ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			fields[strings.Split(f.Tag.Get("json"), ",")[0]] = ft
		}
		var want, got []string
		for k := range props {
			want = append(want, k)
		}
		for k := range fields {
			got = append(got, k)
		}
		sort.Strings(want)
		sort.Strings(got)
		if !slices.Equal(want, got) {
			t.Errorf("%s: schema has %v, Go has %v", path, want, got)
		}
		for k, p := range props {
			if sub, ok := p.(map[string]any); ok && sub["type"] == "object" && fields[k] != nil {
				walk(path+"."+k, sub, fields[k])
			}
		}
	}
	walk("entry", raw, reflect.TypeFor[contract.RunLedgerEntry]())
}

// TestRunLedgerSchemaPretty: the file is the pretty-printed form (two-space indent, trailing
// newline), so a reformat shows up as a diff and dx's sha256 pin stays meaningful.
func TestRunLedgerSchemaPretty(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "contracts", "run-ledger.v1.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, bytes.TrimSpace(data), "", "  "); err != nil {
		t.Fatal(err)
	}
	if buf.String()+"\n" != string(data) {
		t.Error("docs/contracts/run-ledger.v1.schema.json is not two-space pretty-printed with one trailing newline")
	}
}

// TestActionsComplete: every Action and Role constant in the source is in Actions or Roles and
// has a Grants entry, so a new constant cannot be left out of the lists Can checks.
func TestActionsComplete(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "authz.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var actions, roles []string
	for _, d := range f.Decls {
		g, ok := d.(*ast.GenDecl)
		if !ok || g.Tok != token.CONST {
			continue
		}
		for _, s := range g.Specs {
			vs := s.(*ast.ValueSpec)
			id, ok := vs.Type.(*ast.Ident)
			if !ok {
				continue
			}
			for _, v := range vs.Values {
				lit, _ := strconv.Unquote(v.(*ast.BasicLit).Value)
				switch id.Name {
				case "Action":
					actions = append(actions, lit)
				case "Role":
					roles = append(roles, lit)
				}
			}
		}
	}
	if got := strs(contract.Actions); !slices.Equal(got, actions) {
		t.Errorf("Actions is %v, the constants are %v", got, actions)
	}
	if got := strs(contract.Roles); !slices.Equal(got, roles) {
		t.Errorf("Roles is %v, the constants are %v", got, roles)
	}
	for _, r := range contract.Roles {
		if _, ok := contract.Grants[r]; !ok {
			t.Errorf("no Grants entry for %s", r)
		}
	}
}

// TestNoInternalImports: the package stays importable from other repositories.
func TestNoInternalImports(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	fset := token.NewFileSet()
	for _, name := range files {
		f, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly|parser.ParseComments)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			if strings.Contains(p, "/internal") {
				t.Errorf("%s imports %s", name, p)
			}
			if !strings.HasSuffix(name, "_test.go") && strings.Contains(strings.Split(p, "/")[0], ".") {
				t.Errorf("%s imports %s; the contract uses the standard library only", name, p)
			}
		}
		if !strings.Contains(f.Comments[0].Text(), "SPDX-License-Identifier: Apache-2.0") {
			t.Errorf("%s does not start with the Apache-2.0 SPDX line", name)
		}
	}
}
