//go:build ee

// SPDX-License-Identifier: LicenseRef-KaaS-EE

package policy_test

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KaaS-team/factory/ee/contract"
	"github.com/KaaS-team/factory/ee/policy"
)

const example = "../contract/testdata/kaas-policy.example.toml"

// TestConformancePolicy runs the shared cases against Compiler.
func TestConformancePolicy(t *testing.T) {
	raw, err := os.ReadFile("../contract/testdata/conformance.json")
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		Policy []struct {
			Name      string                `json:"name"`
			Layers    []contract.PolicyFile `json:"layers"`
			WantError *string               `json:"want_error"`
			Checks    []struct {
				Request contract.PolicyRequest `json:"request"`
				Want    contract.Decision      `json:"want"`
			} `json:"checks"`
			Budgets []contract.BudgetLimit `json:"want_budgets"`
		} `json:"policy"`
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	sentinel := map[string]error{
		string(contract.ReasonPolicyWidening): contract.ErrPolicyWidening, string(contract.ReasonPolicyLayerOrder): contract.ErrPolicyLayerOrder,
		string(contract.ReasonPolicyInvalid): contract.ErrPolicyInvalid,
	}
	if len(c.Policy) == 0 {
		t.Fatal("no policy cases")
	}
	for _, tc := range c.Policy {
		p, err := policy.Compiler{}.Compile(tc.Layers...)
		if tc.WantError != nil {
			if !errors.Is(err, sentinel[*tc.WantError]) {
				t.Errorf("%s: got %v, want %s", tc.Name, err, *tc.WantError)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", tc.Name, err)
			continue
		}
		for _, ch := range tc.Checks {
			if got := p.Evaluate(t.Context(), ch.Request); got != ch.Want {
				t.Errorf("%s: %+v: got %+v, want %+v", tc.Name, ch.Request, got, ch.Want)
			}
		}
		if len(tc.Budgets) > 0 && len(p.Budgets()) != len(tc.Budgets) {
			t.Errorf("%s: budgets %v, want %v", tc.Name, p.Budgets(), tc.Budgets)
		}
	}
}

func TestExampleFileIsValid(t *testing.T) {
	if vs := policy.CheckFile(example, policy.Paths{}); len(vs) != 0 {
		t.Fatalf("example has violations: %v", vs)
	}
}

func write(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const widenedRepo = `version = 1
layer = "repo"
name = "acme/api"
[models]
allowed = ["provider-large-2026", "some-other-model"]
`

// A repo file cannot widen the org file: it is a violation with the widening code, and Resolve
// refuses to compile it.
func TestRepoCannotWidenOrg(t *testing.T) {
	repo := write(t, "kaas-policy.toml", widenedRepo)
	vs := policy.CheckFile(repo, policy.Paths{Org: example})
	if len(vs) != 1 || vs[0].Code != contract.ReasonPolicyWidening || !strings.Contains(vs[0].Message, "some-other-model") {
		t.Fatalf("violations = %v", vs)
	}
	if _, err := policy.Resolve(policy.Paths{Org: example, Repo: repo}); !errors.Is(err, contract.ErrPolicyWidening) {
		t.Fatalf("Resolve err = %v", err)
	}
	// Narrowing is fine.
	ok := write(t, "kaas-policy.toml", strings.Replace(widenedRepo, `, "some-other-model"`, "", 1))
	if vs := policy.CheckFile(ok, policy.Paths{Org: example}); len(vs) != 0 {
		t.Fatalf("narrowing flagged: %v", vs)
	}
}

func TestLowerLayerNeedsOrg(t *testing.T) {
	repo := write(t, "kaas-policy.toml", widenedRepo)
	t.Setenv(policy.EnvOrg, "")
	vs := policy.CheckFile(repo, policy.Paths{})
	if len(vs) != 1 || !strings.Contains(vs[0].Message, "needs the org file") {
		t.Fatalf("violations = %v", vs)
	}
}

func TestValidateReportsEveryProblem(t *testing.T) {
	bad := write(t, "bad.toml", `version = 1
layer = "org"
name = "acme"
[egress]
allowed_hosts = ["https://github.com", "ok.com", "ok.com", "*.b.*"]
[merge]
auto_merge_risk = ["critical"]
[approval]
require_human = ["nonsense"]
[[budgets]]
scope = "org"
id = "acme"
usd = 0
period = "year"
`)
	vs := policy.CheckFile(bad, policy.Paths{})
	if len(vs) < 6 {
		t.Fatalf("want every problem, got %v", vs)
	}
	for _, v := range vs {
		if v.Code != contract.ReasonPolicyInvalid {
			t.Errorf("code %s for %s", v.Code, v.Message)
		}
	}
}

func TestUnknownKeyIsInvalid(t *testing.T) {
	p := write(t, "typo.toml", "version = 1\nlayer = \"org\"\nname = \"a\"\n[models]\nallow = [\"x\"]\n")
	if vs := policy.CheckFile(p, policy.Paths{}); len(vs) == 0 || !strings.Contains(vs[0].Message, "models.allow") {
		t.Fatalf("violations = %v", vs)
	}
}

// Default deny: an org that sets no model list allows no model.
func TestDefaultDeny(t *testing.T) {
	p, err := policy.Compiler{}.Compile(contract.PolicyFile{Version: 1, Layer: contract.LayerOrg, Name: "a"})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []contract.Check{contract.CheckModel, contract.CheckHarness, contract.CheckMCPServer, contract.CheckEgress, contract.CheckAutoMerge} {
		if d := p.Evaluate(t.Context(), contract.PolicyRequest{Check: c, Value: "x"}); d.Allow || d.Reason != contract.ReasonPolicyNotAllowlisted {
			t.Errorf("%s: %+v", c, d)
		}
	}
}

func TestExplainNamesTheDecidingLayer(t *testing.T) {
	team := write(t, "team.toml", `version = 1
layer = "team"
name = "payments"
[models]
allowed = ["provider-small-2026"]
[approval]
require_human = ["run.start"]
`)
	p := policy.Paths{Org: example, Team: team}
	for _, tc := range []struct {
		check, value string
		allow        bool
		reason       contract.ReasonCode
		layer        contract.Layer
	}{
		{"model", "provider-small-2026", true, contract.ReasonPolicyAllowed, contract.LayerTeam},
		{"model", "provider-large-2026", false, contract.ReasonPolicyNotAllowlisted, contract.LayerTeam},
		{"harness", "codex", true, contract.ReasonPolicyAllowed, contract.LayerOrg},
		{"action", "merge", false, contract.ReasonPolicyApprovalRequired, contract.LayerOrg},
		{"action", "run.start", false, contract.ReasonPolicyApprovalRequired, contract.LayerTeam},
		{"action", "run.read", true, contract.ReasonPolicyAllowed, ""},
		{"egress_host", "raw.githubusercontent.com", true, contract.ReasonPolicyAllowed, contract.LayerOrg},
	} {
		r, err := policy.Explain(p, tc.check, tc.value)
		if err != nil {
			t.Fatal(err)
		}
		if r.Allow != tc.allow || r.Reason != tc.reason || r.Layer != tc.layer {
			t.Errorf("%s %s: got %+v", tc.check, tc.value, r)
		}
	}
}

// A repo cannot swap the org file: with a key set, an org file without a valid signature does
// not load, and the repo layer never needs one.
func TestSignedOrgCannotBeSwapped(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	key := base64.StdEncoding.EncodeToString(pub)
	org, _ := os.ReadFile(example)
	dir := t.TempDir()
	orgPath := filepath.Join(dir, "org.toml")
	sign := func(data []byte) {
		if err := os.WriteFile(orgPath, data, 0o600); err != nil {
			t.Fatal(err)
		}
		sig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, data))
		if err := os.WriteFile(orgPath+".sig", []byte(sig), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sign(org)
	paths := policy.Paths{Org: orgPath, Key: key}
	if _, err := policy.Resolve(paths); err != nil {
		t.Fatalf("signed org: %v", err)
	}
	// Swap in a widened org file without re-signing.
	if err := os.WriteFile(orgPath, []byte(strings.Replace(string(org), `"provider-small-2026"]`, `"provider-small-2026", "any"]`, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := policy.Resolve(paths); err == nil || !strings.Contains(err.Error(), "signature") {
		t.Fatalf("swapped org loaded: %v", err)
	}
	if _, err := policy.Resolve(policy.Paths{Org: orgPath, Key: "not-a-key"}); !errors.Is(err, contract.ErrPolicyInvalid) {
		t.Fatalf("bad key: %v", err)
	}
	// With no key configured the file is read as is.
	if _, err := policy.Resolve(policy.Paths{Org: orgPath}); err != nil {
		t.Fatal(err)
	}
}
