//go:build ee

// SPDX-License-Identifier: LicenseRef-KaaS-EE

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KaaS-team/factory/ee/contract"
	"github.com/KaaS-team/factory/internal/mcp"
)

func policyWho(role contract.Role) contract.Principal {
	return contract.Principal{ID: "u1", Kind: contract.KindUser, Org: "acme",
		Roles: []contract.RoleBinding{{Role: role, Scope: contract.Scope{Kind: contract.ScopeOrg, ID: "acme"}}}}
}

func inRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".factory"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	return dir
}

func TestPolicyMCPRequiresPolicyAdmin(t *testing.T) {
	inRepo(t)
	in := mcp.PolicyInput{Action: "check"}
	if _, err := mcp.PolicyRun(context.Background(), in); err == nil {
		t.Fatal("no principal must be refused")
	}
	for _, r := range []contract.Role{contract.RoleViewer, contract.RoleMember, contract.RoleBot} {
		if _, err := mcp.PolicyRun(mcp.WithPrincipal(context.Background(), policyWho(r)), in); err == nil || err != errPolicyMCP {
			t.Fatalf("role %s: want the generic refusal, got %v", r, err)
		}
	}
}

func TestPolicyMCPConfinesPath(t *testing.T) {
	dir := inRepo(t)
	secret := filepath.Join(t.TempDir(), "secret.toml")
	if err := os.WriteFile(secret, []byte("not toml SECRET-CONTENT ="), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".factory", "kaas-policy.toml"), []byte("SECRET-PARSER = = ["), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(dir, ".factory", "link.toml")); err != nil {
		t.Fatal(err)
	}
	ctx := mcp.WithPrincipal(context.Background(), policyWho(contract.RoleAdmin))
	for _, path := range []string{secret, "/etc/passwd", "../x.toml", ".factory/../go.mod", "go.mod", ".factory", ".factory/link.toml"} {
		out, err := mcp.PolicyRun(ctx, mcp.PolicyInput{Action: "check", Path: path})
		if err != errPolicyMCP || out != "" {
			t.Errorf("path %q: out %q err %v, want the generic refusal", path, out, err)
		}
	}
	// A bad file inside .factory is checked, and the parser's text never goes back.
	out, err := mcp.PolicyRun(ctx, mcp.PolicyInput{Action: "check", Org: secret, Team: secret, Repo: secret})
	if err != nil || !strings.HasPrefix(out, "violation policy.") || strings.Contains(out, "SECRET") {
		t.Fatalf("check: %q %v", out, err)
	}
}

func TestPolicyMCPIgnoresClientLayerPaths(t *testing.T) {
	inRepo(t)
	secret := filepath.Join(t.TempDir(), "org.toml")
	if err := os.WriteFile(secret, []byte("SECRET-CONTENT"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := mcp.WithPrincipal(context.Background(), policyWho(contract.RoleAdmin))
	out, err := mcp.PolicyRun(ctx, mcp.PolicyInput{Action: "explain", Value: "merge", Org: secret, Repo: secret})
	if strings.Contains(out, "SECRET") || (err != nil && err != errPolicyMCP) {
		t.Fatalf("explain leaked or leaked detail: %q %v", out, err)
	}
}
