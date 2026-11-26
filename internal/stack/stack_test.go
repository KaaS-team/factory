package stack

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/KaaS-team/factory/ee/contract"
	"github.com/KaaS-team/factory/internal/plan"
	"github.com/KaaS-team/factory/internal/runstate"
)

// fakeGitHub is an in-memory GitHub and git remote.
type fakeGitHub struct {
	heads    map[string]string
	prBase   map[int]string
	prMerged map[int]bool
	next     int
	opens    int
	rebases  []string
	failOpen int // fail the open call with this number (1-based); 0 never
	seq      int
}

func newFake() *fakeGitHub {
	return &fakeGitHub{heads: map[string]string{"main": "m0"}, prBase: map[int]string{}, prMerged: map[int]bool{}, next: 100}
}

func (f *fakeGitHub) sha() string { f.seq++; return fmt.Sprintf("s%d", f.seq) }

func (f *fakeGitHub) OpenPR(_ context.Context, head, base, _ string) (int, error) {
	f.opens++
	if f.failOpen == f.opens {
		return 0, errors.New("killed")
	}
	f.next++
	f.prBase[f.next] = base
	return f.next, nil
}
func (f *fakeGitHub) Retarget(_ context.Context, pr int, base string) error {
	f.prBase[pr] = base
	return nil
}
func (f *fakeGitHub) Merged(_ context.Context, pr int) (bool, error) { return f.prMerged[pr], nil }
func (f *fakeGitHub) Head(_ context.Context, b string) (string, error) {
	if h, ok := f.heads[b]; ok {
		return h, nil
	}
	f.heads[b] = f.sha()
	return f.heads[b], nil
}
func (f *fakeGitHub) Rebase(_ context.Context, branch, onto, oldBase string) (string, error) {
	f.rebases = append(f.rebases, branch+"<-"+onto)
	f.heads[branch] = f.sha()
	return f.heads[branch], nil
}

func threeTicketPlan(t *testing.T) plan.Plan {
	t.Helper()
	mk := func(id string, deps ...string) plan.Ticket {
		return plan.Ticket{ID: id, Title: "ticket " + id, Files: []string{id + ".go"}, Acceptance: []string{"works"}, Depends: deps}
	}
	return plan.Plan{Tickets: []plan.Ticket{mk("t1"), mk("t2", "t1"), mk("t3", "t2")}}
}

func loadStack(r Runner, id string) (Stack, error) {
	_, s, err := r.load(id)
	return s, err
}

func newRunner(h Host, dir string, got *[]contract.RunLedgerEntry) Runner {
	return Runner{Host: h, Store: runstate.Store{Root: dir}, Repo: "o/r", Ledger: func(e contract.RunLedgerEntry) error {
		if got != nil {
			*got = append(*got, e)
		}
		return nil
	}}
}

func TestNewOrdersDependenciesFirst(t *testing.T) {
	s, err := New(threeTicketPlan(t), "main", "stack/")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range s.Entries {
		got = append(got, fmt.Sprintf("%d:%s on %s", e.Position, e.Branch, e.Base))
	}
	want := "1:stack/t1 on main,2:stack/t2 on stack/t1,3:stack/t3 on stack/t2"
	if strings.Join(got, ",") != want {
		t.Fatalf("got %v", got)
	}
}

func TestNewRefusesBadPlan(t *testing.T) {
	if _, err := New(plan.Plan{}, "main", "s/"); err == nil {
		t.Fatal("empty plan must fail")
	}
	if _, err := New(threeTicketPlan(t), "", "s/"); err == nil {
		t.Fatal("no trunk must fail")
	}
}

// Three PRs, the bottom merges: the next retargets main and rebases, the top rebases on it.
func TestStackRetargetsAfterBottomMerges(t *testing.T) {
	f := newFake()
	var ledger []contract.RunLedgerEntry
	r := newRunner(f, t.TempDir(), &ledger)
	s, _ := New(threeTicketPlan(t), "main", "stack/")
	if err := r.Start(context.Background(), "run1", "#9", s); err != nil {
		t.Fatal(err)
	}
	got, _ := loadStack(r, "run1")
	if len(got.Entries) != 3 || got.Entries[0].PR != 101 || f.prBase[101] != "main" || f.prBase[102] != "stack/t1" || f.prBase[103] != "stack/t2" {
		t.Fatalf("after start: %+v bases %v", got.Entries, f.prBase)
	}
	if len(f.rebases) != 0 {
		t.Fatalf("a fresh stack needs no rebase, got %v", f.rebases)
	}
	for i, e := range ledger {
		if e.PR == nil || e.PR.StackPosition == nil || *e.PR.StackPosition != i+1 {
			t.Fatalf("ledger %d lacks stack_position %d: %+v", i, i+1, e.PR)
		}
	}

	// PR 101 merges (squash): main moves.
	f.prMerged[101] = true
	f.heads["main"] = "m1"
	if err := r.Sync(context.Background(), "run1"); err != nil {
		t.Fatal(err)
	}
	if f.prBase[102] != "main" {
		t.Fatalf("PR 102 base = %s, want main", f.prBase[102])
	}
	if f.prBase[103] != "stack/t2" {
		t.Fatalf("PR 103 base = %s, want stack/t2", f.prBase[103])
	}
	if strings.Join(f.rebases, ",") != "stack/t2<-main,stack/t3<-stack/t2" {
		t.Fatalf("rebases = %v", f.rebases)
	}
	got, _ = loadStack(r, "run1")
	if !got.Entries[0].Merged || got.Entries[1].Base != "main" || got.Entries[2].Base != "stack/t2" {
		t.Fatalf("state: %+v", got.Entries)
	}
	last := ledger[len(ledger)-1]
	if *last.PR.StackPosition != 3 {
		t.Fatalf("last ledger line: %+v", last.PR)
	}

	// A second sync with nothing changed does nothing.
	n := len(f.rebases)
	if err := r.Sync(context.Background(), "run1"); err != nil {
		t.Fatal(err)
	}
	if len(f.rebases) != n {
		t.Fatalf("idempotent sync rebased again: %v", f.rebases)
	}

	// The middle merges next: the top goes to main.
	f.prMerged[102] = true
	f.heads["main"] = "m2"
	if err := r.Sync(context.Background(), "run1"); err != nil {
		t.Fatal(err)
	}
	if f.prBase[103] != "main" {
		t.Fatalf("PR 103 base = %s, want main", f.prBase[103])
	}
}

// A lower branch gets a new commit: the upper ones are rebased, nothing is retargeted.
func TestLowerChangeRebasesUpper(t *testing.T) {
	f := newFake()
	r := newRunner(f, t.TempDir(), nil)
	s, _ := New(threeTicketPlan(t), "main", "stack/")
	if err := r.Start(context.Background(), "run1", "#9", s); err != nil {
		t.Fatal(err)
	}
	f.heads["stack/t1"] = "pushed-fix"
	if err := r.Sync(context.Background(), "run1"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(f.rebases, ",") != "stack/t2<-stack/t1,stack/t3<-stack/t2" {
		t.Fatalf("rebases = %v", f.rebases)
	}
	if f.prBase[102] != "stack/t1" {
		t.Fatalf("base changed: %v", f.prBase)
	}
}

// The run dies while opening PRs. A new process resumes from the directory and opens only the rest.
func TestCrashResume(t *testing.T) {
	dir := t.TempDir()
	f := newFake()
	f.failOpen = 2
	s, _ := New(threeTicketPlan(t), "main", "stack/")
	err := newRunner(f, dir, nil).Start(context.Background(), "run1", "#9", s)
	if err == nil || !strings.Contains(err.Error(), "killed") {
		t.Fatalf("err = %v", err)
	}
	st, _ := runstate.Store{Root: dir}.Load("run1")
	if !st.Done("open:t1") || st.Done("open:t2") {
		t.Fatalf("steps after crash: %v", st.Steps)
	}

	f.failOpen = 0
	var ledger []contract.RunLedgerEntry
	if err := newRunner(f, dir, &ledger).Resume(context.Background(), "run1"); err != nil {
		t.Fatal(err)
	}
	if f.opens != 4 { // t1, t2 (killed), then t2, t3
		t.Fatalf("OpenPR called %d times, want 4", f.opens)
	}
	got, _ := loadStack(newRunner(f, dir, nil), "run1")
	for i, e := range got.Entries {
		if e.PR != 101+i {
			t.Fatalf("entry %d PR = %d", i, e.PR)
		}
	}
	if len(ledger) != 2 {
		t.Fatalf("resume wrote %d ledger lines, want 2 (t2, t3)", len(ledger))
	}
	// Resuming a finished run changes nothing.
	if err := newRunner(f, dir, nil).Resume(context.Background(), "run1"); err != nil || f.opens != 4 {
		t.Fatalf("second resume: %v, opens %d", err, f.opens)
	}
}

func TestResumeUnknownRun(t *testing.T) {
	err := newRunner(newFake(), t.TempDir(), nil).Resume(context.Background(), "ghost")
	if !errors.Is(err, runstate.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}
