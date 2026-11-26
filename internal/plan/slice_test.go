package plan

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"testing"
)

var updateSlice = flag.Bool("update-slice", false, "rewrite testdata/slice/*.golden")

func stk(t *testing.T, id string, files string, deps ...string) Ticket {
	t.Helper()
	d := "none"
	if len(deps) > 0 {
		d = strings.Join(deps, ", ")
	}
	body := fmt.Sprintf("%s\nDeps: %s\n## Why\n\nwhy %s\n\n## Acceptance\n\n- AC-1: %s works\n- AC-2: %s fails clean\n\n## Files\n\n- `%s`\n", Marker, d, id, id, id, files)
	x, err := NewTicket(id, "Do "+id, body)
	if err != nil {
		t.Fatal(err)
	}
	return x
}

func chainPlan(t *testing.T, n int) Plan {
	var p Plan
	for i := 1; i <= n; i++ {
		id := fmt.Sprintf("T%d", i)
		if i == 1 {
			p.Tickets = append(p.Tickets, stk(t, id, "a/f1.go"))
		} else {
			p.Tickets = append(p.Tickets, stk(t, id, fmt.Sprintf("a/f%d.go", i), fmt.Sprintf("T%d", i-1)))
		}
	}
	return p
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := "testdata/slice/" + name + ".golden"
	if *updateSlice {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("%s changed; rerun with -update-slice if intended.\ngot:\n%s", name, got)
	}
}

func show(s Sliced) string {
	var b strings.Builder
	fmt.Fprintf(&b, "chain %d -> %d\n", s.ChainBefore, s.ChainAfter)
	for _, u := range s.Units {
		var ids []string
		for _, k := range u.Tasks {
			ids = append(ids, k.ID)
		}
		fmt.Fprintf(&b, "%s [%s] %q deps=%v\n", u.ID, strings.Join(ids, " "), u.Title, u.Depends)
	}
	return b.String()
}

// A three-deep chain is one epic with three ordered tasks, so the longest chain drops from 3 to 1.
func TestSliceThreeDeepChainIsOneEpic(t *testing.T) {
	s, err := Slice(chainPlan(t, 3), 0)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "chain3", show(s))
	if len(s.Units) != 1 || len(s.Units[0].Tasks) != 3 {
		t.Fatalf("want one unit of three tasks, got %s", show(s))
	}
	u := s.Plan.Tickets[0]
	if got := len(u.Acceptance); got != 6 {
		t.Errorf("acceptance criteria kept: %d, want 6", got)
	}
	if pr := s.Plan.Problems(); len(pr) > 0 {
		t.Errorf("sliced plan has problems: %v", pr)
	}
	golden(t, "chain3-ticket", u.Body.Render())
}

// A chain above the cap is split at a task boundary: every task lands whole in exactly one unit, in order.
func TestSliceSplitsAtTaskBoundary(t *testing.T) {
	s, err := Slice(chainPlan(t, 7), 3)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "chain7", show(s))
	var order []string
	for _, u := range s.Units {
		if len(u.Tasks) > 3 {
			t.Errorf("unit %s has %d tasks, cap is 3", u.ID, len(u.Tasks))
		}
		for _, k := range u.Tasks {
			order = append(order, k.ID)
		}
	}
	if strings.Join(order, " ") != "T1 T2 T3 T4 T5 T6 T7" {
		t.Errorf("tasks out of order or lost: %v", order)
	}
	if s.ChainAfter != 3 || ChainWarning(s.ChainAfter) == "" {
		t.Errorf("3 units in a row must warn: chain %d", s.ChainAfter)
	}
}

// Fan-out and fan-in are not chains: each ticket stays its own unit and the dependencies carry over.
func TestSliceKeepsDiamondApart(t *testing.T) {
	p := Plan{Tickets: []Ticket{
		stk(t, "T1", "a/1.go"), stk(t, "T2", "a/2.go", "T1"), stk(t, "T3", "a/3.go", "T1"), stk(t, "T4", "a/4.go", "T2", "T3"),
	}}
	s, err := Slice(p, 0)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "diamond", show(s))
	if len(s.Units) != 4 || s.ChainAfter != 3 {
		t.Errorf("diamond changed: %s", show(s))
	}
	if string(s.Plan.Tickets[0].Body.Render()) != p.Tickets[0].Body.Render() {
		t.Error("a lone task must keep its body")
	}
}

// A chain whose tail feeds a later ticket: the later ticket waits for the unit that holds the tail.
func TestSliceRewiresDependencyOnTail(t *testing.T) {
	p := Plan{Tickets: []Ticket{
		stk(t, "T1", "a/1.go"), stk(t, "T2", "a/2.go", "T1"), stk(t, "T3", "a/3.go", "T2", "T9"), stk(t, "T9", "a/9.go"),
	}}
	s, err := Slice(p, 0)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "tail", show(s))
	if pr := s.Plan.Problems(); len(pr) > 0 {
		t.Fatalf("problems: %v", pr)
	}
}

// The walk over the fixture PRD: the 3-deep PRD chain slices to one unit, so the longest chain is at most two.
func TestSlicedPRDChainIsAtMostTwo(t *testing.T) {
	md, _ := os.ReadFile("testdata/prd/search.md")
	pr, err := FromPRD(string(md))
	if err != nil {
		t.Fatal(err)
	}
	var pl Plan
	for _, d := range pr.Tickets {
		x, err := NewTicket(d.Key, d.Title, d.Body)
		if err != nil {
			t.Fatal(err)
		}
		pl.Tickets = append(pl.Tickets, x)
	}
	if pl.LongestChain() != 4 {
		t.Fatalf("fixture chain = %d, want 4", pl.LongestChain())
	}
	s, err := Slice(pl, MaxUnitTasks)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "prd", show(s))
	if s.ChainAfter > MaxChain {
		t.Errorf("longest chain %d after slicing, want at most %d", s.ChainAfter, MaxChain)
	}
}

func TestSliceRefusesAPlanWithProblems(t *testing.T) {
	if _, err := Slice(Plan{}, 0); err == nil {
		t.Error("an empty plan must be refused")
	}
}

func TestChainWarningThreshold(t *testing.T) {
	if ChainWarning(2) != "" || ChainWarning(3) == "" {
		t.Error("warn above two, not at two")
	}
}
