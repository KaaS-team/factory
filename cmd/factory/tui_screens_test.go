package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KaaS-team/factory/internal/plan"
	"github.com/KaaS-team/factory/internal/tui"
)

// The plan-review screen shows the PRD's epic, its tickets in wave order, and their criteria.
func TestLoadPlanReview(t *testing.T) {
	r, err := loadPlanReview("../../internal/plan/testdata/prd/search.md")
	if err != nil {
		t.Fatal(err)
	}
	if r.Epic != "Saved searches" || len(r.Problems) != 0 {
		t.Fatalf("epic %q problems %v", r.Epic, r.Problems)
	}
	if len(r.Tickets) < 3 {
		t.Fatalf("tickets = %d", len(r.Tickets))
	}
	first := r.Tickets[0]
	if first.Title != "Store saved searches" || first.Wave != 0 || len(first.Acceptance) != 2 ||
		!strings.HasPrefix(first.Acceptance[0], "AC-1: A saved search persists") || len(first.Files) != 2 || first.Why == "" {
		t.Fatalf("first ticket = %+v", first)
	}
	for i := 1; i < len(r.Tickets); i++ {
		if r.Tickets[i].Wave < r.Tickets[i-1].Wave {
			t.Fatalf("tickets out of wave order: %+v", r.Tickets)
		}
		if len(r.Tickets[i].Depends) == 0 {
			t.Fatalf("ticket %s lost its dependency", r.Tickets[i].ID)
		}
	}
	if _, err := loadPlanReview("missing.md"); err == nil {
		t.Fatal("a missing PRD loaded")
	}
}

// A plan with problems keeps every ticket on screen and lists the problems.
func TestPlanReviewViewProblems(t *testing.T) {
	a, err := plan.NewTicket("T1", "One", plan.Marker+"\n\n## Why\nx\n\n## Acceptance\n- AC-1: ok\n")
	if err != nil {
		t.Fatal(err)
	}
	r := planReviewView("E", plan.Plan{Tickets: []plan.Ticket{a}})
	if len(r.Tickets) != 1 || len(r.Problems) == 0 || !strings.Contains(r.Problems[0], "lists no Files") {
		t.Fatalf("review = %+v", r)
	}
}

// The ready screen runs the firstrun checks, and its fix repairs in place and reports what it did.
func TestReadyFuncs(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FACTORY_CONFIG", filepath.Join(dir, "none.toml"))
	fn := readyFuncs(nil, dir)
	r, err := fn.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if r.OK() || len(r.Checks) != 6 {
		t.Fatalf("empty repo report = %+v", r)
	}
	r, err = fn.Fix(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Fixed) != 3 || !strings.HasPrefix(r.Fixed[0], "AGENTS.md: ") {
		t.Fatalf("fixed = %v", r.Fixed)
	}
	for _, c := range r.Checks {
		if c.Name == "AGENTS.md" && c.Status != tui.CheckPass {
			t.Fatalf("AGENTS.md still %s after the fix", c.Status)
		}
	}
}
