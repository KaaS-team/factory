package mergeq

import (
	"context"
	"slices"
	"testing"
)

// pinned is the world with approvals: a PR is approved at the head it was queued with.
type pinned struct {
	*world
	refused  []string
	ledgered map[string][2]string
	onPR     func(id string) // runs before each PR read
}

func (p *pinned) PR(ctx context.Context, id string) (PRState, error) {
	if p.onPR != nil {
		p.onPR(id)
	}
	st, err := p.world.PR(ctx, id)
	st.Approved = p.queued[id]
	return st, err
}

func (p *pinned) Refuse(_ context.Context, id, approved, head string) error {
	p.refused = append(p.refused, id+":"+approved+"->"+head)
	p.remove(id)
	return nil
}

func (p *pinned) record(_ context.Context, id, approved, merged string) error {
	p.ledgered[id] = [2]string{approved, merged}
	return nil
}

func (p *pinned) runner() *Runner {
	r := &Runner{Git: p.world, Gate: p.world, Store: p, Cfg: Config{BatchSize: 4, Pin: true}}
	r.Ledger = ledgerFunc(p.record)
	return r
}

type ledgerFunc func(ctx context.Context, id, approved, merged string) error

func (f ledgerFunc) Landed(ctx context.Context, id, approved, merged string) error {
	return f(ctx, id, approved, merged)
}

// The head moves between the green gate and the push: the PR is refused, the base does not move.
func TestPinRefusesPushAfterApproval(t *testing.T) {
	p := &pinned{world: newWorld(1), ledgered: map[string][2]string{}}
	reads := 0
	p.onPR = func(id string) {
		if reads++; reads == 2 { // the first read is the plan; the second is the recheck before landing
			p.heads[id] = "h1b"
		}
	}
	rep, err := p.runner().Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(p.ffs) != 0 || len(rep.Landed) != 0 || len(p.ledgered) != 0 {
		t.Fatalf("merged a PR whose head moved after approval: ffs=%v landed=%v", p.ffs, rep.Landed)
	}
	if !slices.Equal(p.refused, []string{"1:h1->h1b"}) || !slices.Equal(rep.Refused, []string{"1"}) {
		t.Fatalf("refused %v, report %v", p.refused, rep.Refused)
	}
}

// A push before the first plan is refused without a gate run.
func TestPinRefusesWithoutTesting(t *testing.T) {
	p := &pinned{world: newWorld(2), ledgered: map[string][2]string{}}
	p.heads["1"] = "h1b"
	rep, err := p.runner().Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(rep.Refused, []string{"1"}) || !slices.Equal(rep.Landed, []string{"2"}) {
		t.Fatalf("refused %v landed %v", rep.Refused, rep.Landed)
	}
}

// The ledger gets the approved head and the commit the base moved to, for every landed PR.
func TestLedgerRecordsApprovedAndMergedSHA(t *testing.T) {
	p := &pinned{world: newWorld(2), ledgered: map[string][2]string{}}
	if _, err := p.runner().Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{"1": "h1", "2": "h2"} {
		got := p.ledgered[id]
		if got[0] != want || got[1] == "" {
			t.Errorf("PR %s ledger %v, want approved %s and a merged sha", id, got, want)
		}
	}
}
