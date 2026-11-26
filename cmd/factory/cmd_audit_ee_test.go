//go:build ee

// SPDX-License-Identifier: LicenseRef-KaaS-EE

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	eeaudit "github.com/KaaS-team/factory/ee/audit"
	"github.com/KaaS-team/factory/ee/contract"
)

// TestAuditVerifyFailsOnEditedRow: `factory audit verify` passes on an intact chain and fails,
// naming the seq, after one stored row is edited.
func TestAuditVerifyFailsOnEditedRow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chain.jsonl")
	t.Setenv("FACTORY_AUDIT_CHAIN", path)
	log := eeaudit.Open(path)
	for _, a := range []contract.Action{contract.ActionRunStart, contract.ActionMerge, contract.ActionRunCancel} {
		if _, err := log.Record(context.Background(), contract.AuditEvent{Actor: "u1", Action: a, Resource: "o/r", Outcome: contract.OutcomeAllow}); err != nil {
			t.Fatal(err)
		}
	}
	if out, _, err := runCLI(t, "audit", "verify"); err != nil || !strings.Contains(out, "intact") {
		t.Fatalf("intact chain: %q %v", out, err)
	}
	data, _ := os.ReadFile(path)
	if err := os.WriteFile(path, []byte(strings.Replace(string(data), `"actor":"u1"`, `"actor":"mallory"`, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := runCLI(t, "audit", "verify")
	if err == nil || !strings.Contains(err.Error(), "audit.chain_broken at seq 1") {
		t.Fatalf("edited row: got %v", err)
	}
}

func TestAuditSearchFilters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chain.jsonl")
	t.Setenv("FACTORY_AUDIT_CHAIN", path)
	log := eeaudit.Open(path)
	for _, e := range []contract.AuditEvent{
		{Actor: "a", Action: contract.ActionRunStart, Resource: "o/one", Outcome: contract.OutcomeAllow},
		{Actor: "b", Action: contract.ActionMerge, Resource: "o/two", Outcome: contract.OutcomeDeny},
		{Actor: "a", Action: contract.ActionMerge, Resource: "o/two", Outcome: contract.OutcomeAllow},
	} {
		if _, err := log.Record(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
	for args, want := range map[string]int{
		"--actor a": 2, "--action merge": 2, "--resource o/two": 2, "--outcome deny": 1, "--actor a --action merge": 1, "--limit 1": 1,
	} {
		out, _, err := runCLI(t, append([]string{"audit", "search"}, strings.Fields(args)...)...)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Count(out, "\n"); got != want {
			t.Errorf("search %s: %d lines, want %d\n%s", args, got, want, out)
		}
	}
}

func TestAuditForwardToFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FACTORY_AUDIT_CHAIN", filepath.Join(dir, "chain.jsonl"))
	if _, err := eeaudit.Open(filepath.Join(dir, "chain.jsonl")).Record(context.Background(),
		contract.AuditEvent{Actor: "a", Action: contract.ActionMerge, Outcome: contract.OutcomeAllow}); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "siem.jsonl")
	if out, _, err := runCLI(t, "audit", "forward", "--to", "file:"+dst); err != nil || !strings.Contains(out, "sent 1 events") {
		t.Fatalf("%q %v", out, err)
	}
	if data, _ := os.ReadFile(dst); !strings.Contains(string(data), `"action":"merge"`) {
		t.Errorf("siem file: %q", data)
	}
	if _, _, err := runCLI(t, "audit", "forward", "--to", "ftp:x"); err == nil {
		t.Error("a bad target must fail")
	}
}
