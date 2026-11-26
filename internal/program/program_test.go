package program

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/KaaS-team/factory/internal/runstate"
)

type fakeWorker struct {
	mu   sync.Mutex
	ran  []string
	fail map[string]bool
	hook func(id string) error
}

func (w *fakeWorker) Run(_ context.Context, t Task) (Result, error) {
	w.mu.Lock()
	w.ran = append(w.ran, t.ID)
	w.mu.Unlock()
	if w.hook != nil {
		if err := w.hook(t.ID); err != nil {
			return Result{}, err
		}
	}
	return Result{OK: !w.fail[t.ID], Summary: "did " + t.ID}, nil
}

type fakeLead struct {
	decide func(round int, results []Result, remaining []Task) Decision
	calls  int
}

func (l *fakeLead) Review(_ context.Context, _ string, results []Result, remaining []Task) (Decision, error) {
	l.calls++
	if l.decide == nil {
		return Decision{}, nil
	}
	return l.decide(l.calls, results, remaining), nil
}

func load(r Runner, id string) (State, error) {
	rs, err := r.Store.Load(id)
	if err != nil {
		return State{}, err
	}
	var s State
	_, err = rs.Get(doc, &s)
	return s, err
}

func tasks() []Task {
	return []Task{{ID: "a", Title: "A"}, {ID: "b", Title: "B", Depends: []string{"a"}}, {ID: "c", Title: "C", Depends: []string{"b"}}}
}

func TestRunsInDependencyOrder(t *testing.T) {
	w, l := &fakeWorker{}, &fakeLead{}
	r := Runner{Worker: w, Lead: l, Store: runstate.Store{Root: t.TempDir()}}
	if err := r.Start(context.Background(), "p1", "epic", tasks()); err != nil {
		t.Fatal(err)
	}
	if strings.Join(w.ran, ",") != "a,b,c" || l.calls != 3 {
		t.Fatalf("ran %v, lead reviewed %d times", w.ran, l.calls)
	}
}

func TestLeadAdjustsRemainingPlan(t *testing.T) {
	w := &fakeWorker{}
	l := &fakeLead{decide: func(round int, res []Result, rem []Task) Decision {
		if round == 1 {
			return Decision{Add: []Task{{ID: "d", Title: "D", Depends: []string{"a"}}}, Drop: []string{"c"}, Note: "c is not needed"}
		}
		return Decision{}
	}}
	r := Runner{Worker: w, Lead: l, Store: runstate.Store{Root: t.TempDir()}}
	if err := r.Start(context.Background(), "p1", "epic", tasks()); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(w.ran, ","); got != "a,b,d" && got != "a,d,b" {
		t.Fatalf("ran %v", w.ran)
	}
	s, _ := load(r, "p1")
	if len(s.Notes) != 1 || len(s.Dropped) != 1 {
		t.Fatalf("state %+v", s)
	}
}

func TestBadDecisionIsRefusedNotApplied(t *testing.T) {
	w := &fakeWorker{}
	l := &fakeLead{decide: func(round int, res []Result, rem []Task) Decision {
		if round == 1 {
			return Decision{Drop: []string{"a"}, Add: []Task{{ID: "x", Title: "X", Depends: []string{"ghost"}}}}
		}
		return Decision{}
	}}
	r := Runner{Worker: w, Lead: l, Store: runstate.Store{Root: t.TempDir()}}
	if err := r.Start(context.Background(), "p1", "epic", tasks()); err != nil {
		t.Fatal(err)
	}
	s, _ := load(r, "p1")
	if len(s.Dropped) != 0 || len(w.ran) != 3 || len(s.Notes) != 1 || !strings.Contains(s.Notes[0], "refused") {
		t.Fatalf("ran %v state %+v", w.ran, s)
	}
}

func TestFailedTaskBlocksDependents(t *testing.T) {
	w := &fakeWorker{fail: map[string]bool{"a": true}}
	r := Runner{Worker: w, Lead: &fakeLead{}, Store: runstate.Store{Root: t.TempDir()}}
	err := r.Start(context.Background(), "p1", "epic", tasks())
	if !errors.Is(err, ErrBlocked) || !strings.Contains(err.Error(), "b, c") {
		t.Fatalf("err = %v", err)
	}
}

func TestLeadCanDropAFailedTasksDependents(t *testing.T) {
	w := &fakeWorker{fail: map[string]bool{"a": true}}
	l := &fakeLead{decide: func(round int, res []Result, rem []Task) Decision { return Decision{Drop: []string{"b", "c"}} }}
	r := Runner{Worker: w, Lead: l, Store: runstate.Store{Root: t.TempDir()}}
	if err := r.Start(context.Background(), "p1", "epic", tasks()); err != nil {
		t.Fatal(err)
	}
}

// The program dies in round 2. Resume does not run round 1 again.
func TestCrashResume(t *testing.T) {
	dir := t.TempDir()
	w := &fakeWorker{}
	ctx, cancel := context.WithCancel(context.Background())
	w.hook = func(id string) error {
		if id == "b" {
			cancel() // the process is killed while b runs
		}
		return nil
	}
	err := Runner{Worker: w, Lead: &fakeLead{}, Store: runstate.Store{Root: dir}}.Start(ctx, "p1", "epic", tasks())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	w2 := &fakeWorker{}
	if err := (Runner{Worker: w2, Lead: &fakeLead{}, Store: runstate.Store{Root: dir}}).Resume(context.Background(), "p1"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(w2.ran, ",") != "b,c" {
		t.Fatalf("resume ran %v, want b,c", w2.ran)
	}
}

func TestStartRefusesBadTasks(t *testing.T) {
	r := Runner{Worker: &fakeWorker{}, Lead: &fakeLead{}, Store: runstate.Store{Root: t.TempDir()}}
	for name, ts := range map[string][]Task{
		"cycle":   {{ID: "a", Title: "A", Depends: []string{"b"}}, {ID: "b", Title: "B", Depends: []string{"a"}}},
		"unknown": {{ID: "a", Title: "A", Depends: []string{"z"}}},
		"dup":     {{ID: "a", Title: "A"}, {ID: "a", Title: "A2"}},
		"empty":   {{ID: "", Title: "A"}},
	} {
		if err := r.Start(context.Background(), "p-"+name, "e", ts); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestParseDecision(t *testing.T) {
	d, err := ParseDecision("```json\n{\"add\":[{\"id\":\"x\",\"title\":\"X\"}],\"drop\":[\"c\"],\"note\":\"n\"}\n```")
	if err != nil || len(d.Add) != 1 || d.Drop[0] != "c" || d.Note != "n" {
		t.Fatalf("%+v %v", d, err)
	}
	for _, bad := range []string{"", "no json", `{"add": 5}`, `{"surprise":1}`} {
		if _, err := ParseDecision(bad); err == nil {
			t.Errorf("%q should fail", bad)
		}
	}
}

func TestModelLeadUsesAsk(t *testing.T) {
	var prompt string
	l := ModelLead{Ask: func(_ context.Context, p string) (string, error) { prompt = p; return `{"note":"ok"}`, nil }}
	d, err := l.Review(context.Background(), "the epic", []Result{{Task: "a", OK: true}}, nil)
	if err != nil || d.Note != "ok" || !strings.Contains(prompt, "the epic") || !strings.Contains(prompt, `"task":"a"`) {
		t.Fatalf("%+v %v %q", d, err, prompt)
	}
	if _, err := (ModelLead{}).Review(context.Background(), "", nil, nil); err == nil {
		t.Fatal("no client must fail")
	}
}
