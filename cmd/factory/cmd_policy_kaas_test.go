//go:build ee

// SPDX-License-Identifier: LicenseRef-KaaS-EE

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const orgExample = "../../ee/contract/testdata/kaas-policy.example.toml"

func TestPolicyCheckCommand(t *testing.T) {
	out, _, err := runCLI(t, "policy", "check", orgExample)
	if err != nil || !strings.HasPrefix(out, "ok ") {
		t.Fatalf("example: %q %v", out, err)
	}
	repo := filepath.Join(t.TempDir(), "kaas-policy.toml")
	body := "version = 1\nlayer = \"repo\"\nname = \"acme/api\"\n[models]\nallowed = [\"provider-large-2026\", \"extra\"]\n"
	if err := os.WriteFile(repo, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	out, _, err = runCLI(t, "policy", "check", repo, "--org", orgExample)
	if err == nil || !strings.Contains(out, "violation policy.widening") || !strings.Contains(out, `"extra"`) {
		t.Fatalf("widened repo: %q %v", out, err)
	}
}

func TestPolicyExplainCommand(t *testing.T) {
	out, _, err := runCLI(t, "policy", "explain", "merge", "--org", orgExample)
	if err == nil || !strings.Contains(out, "deny policy.approval_required") || !strings.Contains(out, "layer: org") {
		t.Fatalf("explain merge: %q %v", out, err)
	}
	out, _, err = runCLI(t, "policy", "explain", "codex", "--check", "harness", "--org", orgExample)
	if err != nil || !strings.HasPrefix(out, "allow policy.allowed") {
		t.Fatalf("explain harness: %q %v", out, err)
	}
}
