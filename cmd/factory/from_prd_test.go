package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPlanFromPRD: `factory plan --from-prd` drafts four ordered tickets from the fixture PRD.
func TestPlanFromPRD(t *testing.T) {
	out, _, err := runCLI(t, "plan", "--from-prd", "../../internal/plan/testdata/prd/search.md", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var dag struct {
		Tickets  []struct{ ID, Title string }
		Waves    [][]string
		Problems []string
	}
	if err := json.Unmarshal([]byte(out), &dag); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(dag.Tickets) != 4 || len(dag.Waves) != 4 || len(dag.Problems) != 0 {
		t.Errorf("dag = %+v", dag)
	}
}

func TestPlanFromPRDRejectsABadPRD(t *testing.T) {
	p := filepath.Join(t.TempDir(), "bad.md")
	if err := os.WriteFile(p, []byte("# T\n## Slices\n### A\n- x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runCLI(t, "plan", "--from-prd", p); err == nil || !strings.Contains(err.Error(), "no \"Files:\" line") {
		t.Errorf("err = %v", err)
	}
	if _, _, err := runCLI(t, "plan", "--from-prd", p, "--spec", p); err == nil {
		t.Error("--from-prd with --spec was accepted")
	}
}
