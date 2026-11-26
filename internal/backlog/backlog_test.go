package backlog_test

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/KaaS-team/factory/internal/api"
	"github.com/KaaS-team/factory/internal/backlog"
)

// fake is an in-memory tracker.
type fake struct {
	issues []api.Ticket
	parent map[api.TicketID]api.TicketID
	rank   map[api.TicketID]int
}

func newFake() *fake {
	return &fake{parent: map[api.TicketID]api.TicketID{}, rank: map[api.TicketID]int{}}
}

func (f *fake) add(title, body string) api.TicketID {
	id, _ := f.Create(context.Background(), api.Ticket{Title: title, Body: body})
	return id
}

func (f *fake) Find(_ context.Context, title string) (api.TicketID, bool, error) {
	for _, t := range f.issues {
		if t.Title == title {
			return t.ID, true, nil
		}
	}
	return "", false, nil
}

func (f *fake) Create(_ context.Context, t api.Ticket) (api.TicketID, error) {
	t.ID = api.TicketID(strconv.Itoa(len(f.issues) + 1))
	f.issues = append(f.issues, t)
	return t.ID, nil
}

func (f *fake) Get(_ context.Context, id api.TicketID) (api.Ticket, error) {
	for _, t := range f.issues {
		if t.ID == id {
			return t, nil
		}
	}
	return api.Ticket{}, errors.New("not found")
}

func (f *fake) Ready(context.Context) ([]api.Ticket, error) { return f.issues, nil }

func (f *fake) LinkSub(_ context.Context, parent, child api.TicketID) error {
	f.parent[child] = parent
	return nil
}

func (f *fake) SetRank(_ context.Context, id api.TicketID, r int) error {
	f.rank[id] = r
	return nil
}

const two = `--- issue: Add the thing
## Problem
Nothing does it.
## Acceptance
- It does it.
## Out of scope
- The other thing.
--- issue: Add the other thing
## Problem
p
## Acceptance
- a
## Out of scope
- o
`

func TestSpecFilesIssuesInThreeSections(t *testing.T) {
	f := newFake()
	draft := func(_ context.Context, goal string) (string, error) {
		if goal != "do it" {
			t.Fatalf("goal = %q", goal)
		}
		return two, nil
	}
	ds, filed, err := backlog.Spec(context.Background(), f, draft, "do it", true)
	if err != nil || len(ds) != 2 || len(filed) != 2 || len(f.issues) != 2 {
		t.Fatalf("ds=%d filed=%d issues=%d err=%v", len(ds), len(filed), len(f.issues), err)
	}
	for _, sec := range backlog.Sections {
		if !strings.Contains(f.issues[0].Body, "## "+sec) {
			t.Fatalf("body lacks %s: %q", sec, f.issues[0].Body)
		}
	}
}

func TestSpecWithoutFileWritesNothing(t *testing.T) {
	f := newFake()
	_, filed, err := backlog.FromText(context.Background(), f, two, false)
	if err != nil || filed != nil || len(f.issues) != 0 {
		t.Fatalf("filed=%v issues=%d err=%v", filed, len(f.issues), err)
	}
}

func TestSpecNeedsADrafter(t *testing.T) {
	if _, _, err := backlog.Spec(context.Background(), newFake(), nil, "g", true); !errors.Is(err, backlog.ErrNoDrafter) {
		t.Fatalf("err = %v", err)
	}
}

func TestParseRejectsMissingSections(t *testing.T) {
	bad := "--- issue: T\n## Problem\np\n## Acceptance\n\n"
	_, err := backlog.Parse(bad)
	if err == nil || !strings.Contains(err.Error(), `"Acceptance"`) || !strings.Contains(err.Error(), `"Out of scope"`) {
		t.Fatalf("err = %v", err)
	}
	if _, err := backlog.Parse("no blocks"); err == nil {
		t.Fatal("want an error with no blocks")
	}
	if f := newFake(); func() bool {
		_, _, e := backlog.FromText(context.Background(), f, bad, true)
		return e == nil || len(f.issues) != 0
	}() {
		t.Fatal("an invalid draft must file nothing")
	}
}

func TestDecomposeLinksChildrenAsSubIssuesAndIsIdempotent(t *testing.T) {
	f := newFake()
	epic := f.add("Epic", "the epic")
	ctx := context.Background()
	_, filed, err := backlog.Decompose(ctx, f, epic, two, true)
	if err != nil || len(filed) != 2 {
		t.Fatalf("filed=%v err=%v", filed, err)
	}
	for _, c := range filed {
		if f.parent[c.ID] != epic {
			t.Fatalf("#%s parent = %q", c.ID, f.parent[c.ID])
		}
	}
	if !strings.Contains(f.issues[1].Body, "Part of #"+string(epic)) {
		t.Fatalf("child body = %q", f.issues[1].Body)
	}
	_, again, err := backlog.Decompose(ctx, f, epic, two, true)
	if err != nil || len(f.issues) != 3 || !again[0].Existed {
		t.Fatalf("rerun: issues=%d again=%v err=%v", len(f.issues), again, err)
	}
}

func TestDecomposeMissingEpicFilesNothing(t *testing.T) {
	f := newFake()
	if _, _, err := backlog.Decompose(context.Background(), f, "9", two, true); err == nil || len(f.issues) != 0 {
		t.Fatalf("issues=%d err=%v", len(f.issues), err)
	}
}

func TestScore(t *testing.T) {
	rice := "Reach: 10\n- Impact: 2\nConfidence: 0.5\nEffort: 5\n"
	if s, ok := backlog.Score(backlog.RICE, rice); !ok || s != 2 {
		t.Fatalf("rice = %v %v", s, ok)
	}
	if s, ok := backlog.Score(backlog.WSJF, "Value: 8\nUrgency: 5\nRisk: 3\nSize: 4"); !ok || s != 4 {
		t.Fatalf("wsjf = %v %v", s, ok)
	}
	for _, body := range []string{"Reach: 1", "Reach: 1\nImpact: 1\nConfidence: 1\nEffort: x", "Reach: 1\nImpact: 1\nConfidence: 1\nEffort: 0"} {
		if _, ok := backlog.Score(backlog.RICE, body); ok {
			t.Fatalf("%q should not score", body)
		}
	}
}

func TestPrioritizeRanksByMethodAndStoresRank(t *testing.T) {
	f := newFake()
	low := f.add("low", "Reach: 1\nImpact: 1\nConfidence: 1\nEffort: 1")
	none := f.add("unscored", "no fields")
	high := f.add("high", "Reach: 9\nImpact: 3\nConfidence: 1\nEffort: 1")
	wsjf := f.add("wsjf only", "Value: 100\nUrgency: 1\nRisk: 1\nSize: 1")
	ctx := context.Background()

	got, err := backlog.Prioritize(ctx, f, backlog.RICE, true)
	if err != nil {
		t.Fatal(err)
	}
	var order []api.TicketID
	for _, r := range got {
		order = append(order, r.ID)
	}
	want := []api.TicketID{high, low, none, wsjf}
	if strings.Join(ids(order), ",") != strings.Join(ids(want), ",") {
		t.Fatalf("rice order = %v, want %v", order, want)
	}
	if f.rank[high] != 1 || f.rank[low] != 2 || f.rank[none] != 3 || f.rank[wsjf] != 4 {
		t.Fatalf("stored ranks = %v", f.rank)
	}

	got, _ = backlog.Prioritize(ctx, f, backlog.WSJF, false)
	if got[0].ID != wsjf {
		t.Fatalf("wsjf first = %v", got[0])
	}
}

func TestPrioritizeWithoutApplyStoresNothing(t *testing.T) {
	f := newFake()
	f.add("a", "")
	if _, err := backlog.Prioritize(context.Background(), f, backlog.RICE, false); err != nil || len(f.rank) != 0 {
		t.Fatalf("rank=%v err=%v", f.rank, err)
	}
}

func TestParseMethod(t *testing.T) {
	if m, err := backlog.ParseMethod(" WSJF "); err != nil || m != backlog.WSJF {
		t.Fatalf("%v %v", m, err)
	}
	if _, err := backlog.ParseMethod("moscow"); err == nil {
		t.Fatal("want an error")
	}
}

func ids(a []api.TicketID) []string {
	var s []string
	for _, x := range a {
		s = append(s, string(x))
	}
	return s
}
