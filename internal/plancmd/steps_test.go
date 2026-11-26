package plancmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/KaaS-team/factory/internal/api"
	"github.com/KaaS-team/factory/internal/pick"
)

// fakeTracker is the fake tracker the three steps run against: filing, sub-issue links and priorities.
type fakeTracker struct {
	fakeFiler
	links   [][2]api.TicketID
	ready   []api.Ticket
	setErr  error
	written map[api.TicketID]int
}

func (f *fakeTracker) LinkSub(_ context.Context, parent, child api.TicketID) error {
	f.links = append(f.links, [2]api.TicketID{parent, child})
	return nil
}

func (f *fakeTracker) Ready(context.Context) ([]api.Ticket, error) { return f.ready, nil }

func (f *fakeTracker) SetPriority(_ context.Context, id api.TicketID, p int) error {
	if f.setErr != nil {
		return f.setErr
	}
	if f.written == nil {
		f.written = map[api.TicketID]int{}
	}
	f.written[id] = p
	return nil
}

func runWith(t *testing.T, ft *fakeTracker, args ...string) (string, error) {
	t.Helper()
	c := NewCommand(WithFiler(ft), WithRanker(ft),
		WithEpic(func(context.Context, int) (string, error) { return mustRead(t, "testdata/spec5.md"), nil }),
		WithSpecFunc(func(context.Context, string) (string, error) { return mustRead(t, "testdata/spec5.md"), nil }))
	var out bytes.Buffer
	c.SetOut(&out)
	c.SetErr(&out)
	c.SetArgs(args)
	err := c.ExecuteContext(context.Background())
	return out.String(), err
}

// Spec step: a goal becomes issues in the Problem (Why), Acceptance and Out of scope form.
func TestSpecStepFilesIssuesInIssueForm(t *testing.T) {
	ft := &fakeTracker{}
	if _, err := runWith(t, ft, "ship it", "--approve"); err != nil {
		t.Fatal(err)
	}
	if len(ft.filed) != 5 {
		t.Fatalf("filed %d", len(ft.filed))
	}
	for _, f := range ft.filed {
		for _, h := range []string{"## Why", "## Acceptance", "## Out of scope"} {
			if !strings.Contains(f.Body, h) {
				t.Errorf("%s lacks %s:\n%s", f.Title, h, f.Body)
			}
		}
		if strings.Contains(f.Body, "- "+f.Title) {
			t.Errorf("%s lists itself as out of scope", f.Title)
		}
	}
	if !strings.Contains(ft.filed[0].Body, "T2: Add store") {
		t.Errorf("out of scope does not name the other tickets:\n%s", ft.filed[0].Body)
	}
}

// Decompose: an epic's tickets are filed and each is linked as a sub-issue of the epic, once.
func TestDecomposeLinksChildrenToTheEpic(t *testing.T) {
	ft := &fakeTracker{}
	if _, err := runWith(t, ft, "--epic", "7", "--approve"); err != nil {
		t.Fatal(err)
	}
	if len(ft.links) != 5 {
		t.Fatalf("links = %v", ft.links)
	}
	for i, l := range ft.links {
		if l[0] != "7" || l[1] != api.TicketID(fmt.Sprint(i+1)) {
			t.Errorf("link %d = %v", i, l)
		}
	}
	// A rerun finds every child and links nothing new to the filer's tickets, but repeats the idempotent link.
	if _, err := runWith(t, ft, "--epic", "7", "--approve"); err != nil || len(ft.filed) != 5 {
		t.Fatalf("rerun filed %d err %v", len(ft.filed), err)
	}
}

func TestSpecFromGoalLinksNothing(t *testing.T) {
	ft := &fakeTracker{}
	if _, err := runWith(t, ft, "ship it", "--approve"); err != nil || len(ft.links) != 0 {
		t.Fatalf("links %v err %v", ft.links, err)
	}
}

// Prioritize: the rank is written as priority, and pickup (pick.Rank) then follows it.
func TestRankWritesPriorityAndPickupFollowsIt(t *testing.T) {
	ft := &fakeTracker{ready: []api.Ticket{
		{ID: "1", Title: "low", Body: "Score: reach=1 impact=1 confidence=1 effort=1"},
		{ID: "2", Title: "unscored", Body: "no score"},
		{ID: "3", Title: "high", Body: "Score: reach=9 impact=1 confidence=1 effort=1"},
		{ID: "4", Title: "mid", Body: "Score: reach=4 impact=1 confidence=1 effort=1"},
	}}
	out, err := runWith(t, ft, "--rank", "rice", "--approve")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "unscored: no Score") {
		t.Errorf("unscored ticket not reported:\n%s", out)
	}
	for i := range ft.ready {
		ft.ready[i].Priority = ft.written[ft.ready[i].ID]
	}
	var order []string
	for _, tk := range pick.Rank(ft.ready) {
		order = append(order, string(tk.ID))
	}
	if got := strings.Join(order, " "); got != "3 4 1 2" {
		t.Errorf("pickup order = %s, want 3 4 1 2", got)
	}
}

func TestRankWithoutApproveWritesNothing(t *testing.T) {
	ft := &fakeTracker{ready: []api.Ticket{{ID: "1", Body: "Score: value=1 time=1 risk=1 size=1"}}}
	if _, err := runWith(t, ft, "--rank", "wsjf"); err != nil || len(ft.written) != 0 {
		t.Fatalf("err %v written %v", err, ft.written)
	}
}

func TestRankRefusals(t *testing.T) {
	ft := &fakeTracker{}
	for _, args := range [][]string{{"--rank", "moscow"}, {"--rank", "rice", "--spec", "testdata/spec5.md"}, {"--rank", "rice", "goal"}} {
		if _, err := runWith(t, ft, args...); err == nil {
			t.Errorf("%v: want error", args)
		}
	}
	if _, err := run(t, nil, "--rank", "rice"); err == nil {
		t.Error("--rank without a tracker must fail")
	}
}

func TestRankWriteErrorSurfaces(t *testing.T) {
	ft := &fakeTracker{ready: []api.Ticket{{ID: "1", Priority: 5, Body: "Score: value=1 time=1 risk=1 size=1"}}, setErr: fmt.Errorf("boom")}
	if _, err := runWith(t, ft, "--rank", "wsjf", "--approve"); err == nil {
		t.Fatal("want error")
	}
}

// A ticket already at its rank is not rewritten.
func TestRankSkipsUnchangedPriority(t *testing.T) {
	ft := &fakeTracker{ready: []api.Ticket{{ID: "1", Priority: 0, Body: "Score: value=1 time=1 risk=1 size=1"}}}
	if _, err := runWith(t, ft, "--rank", "wsjf", "--approve"); err != nil || len(ft.written) != 0 {
		t.Fatalf("err %v written %v", err, ft.written)
	}
}

// --slice folds the 3-deep chain of spec5 (T1 -> T3 -> T4/T5 fan-out) and reports before and after.
func TestSliceFlagReportsChainAndFilesUnits(t *testing.T) {
	out, err := run(t, nil, "--spec", "testdata/chain4.md", "--slice", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var d DAG
	if err := json.Unmarshal([]byte(out), &d); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(d.Units) != 2 || len(d.Units[0].Tasks) != 3 || d.LongestChain != 2 || len(d.Warnings) != 0 {
		t.Errorf("%+v", d)
	}
	ft := &fakeTracker{}
	if _, err := runWith(t, ft, "--spec", "testdata/chain4.md", "--slice", "--approve"); err != nil {
		t.Fatal(err)
	}
	if len(ft.filed) != 2 || !strings.Contains(ft.filed[0].Body, "## Tasks") || len(ft.filed[1].BlockedBy) != 1 {
		t.Errorf("filed %+v", ft.filed)
	}
}

func TestUnslicedChainWarns(t *testing.T) {
	out, err := run(t, nil, "--spec", "testdata/chain4.md")
	if err != nil || !strings.Contains(out, "Warning: the longest dependency chain is 4") {
		t.Fatalf("err %v\n%s", err, out)
	}
	j, _ := run(t, nil, "--spec", "testdata/chain4.md", "--json")
	var d DAG
	if err := json.Unmarshal([]byte(j), &d); err != nil || d.LongestChain != 4 || len(d.Warnings) != 1 {
		t.Errorf("%v %+v", err, d)
	}
}

func TestSliceDoesNotRunOnAPlanWithProblems(t *testing.T) {
	out, err := run(t, nil, "--spec", "testdata/loop.md", "--slice")
	if err != nil || !strings.Contains(out, "Problems:") {
		t.Fatalf("err %v\n%s", err, out)
	}
}
