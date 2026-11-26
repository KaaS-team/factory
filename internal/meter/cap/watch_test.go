package cap

import (
	"context"
	"errors"
	"testing"

	"github.com/KaaS-team/factory/ee/contract"
)

type fakeBudget struct{ committed, released contract.Micros }

func (f *fakeBudget) Reserve(_ context.Context, _ []contract.BudgetKey, _ contract.Micros) (contract.Reservation, error) {
	return contract.Reservation{}, nil
}
func (f *fakeBudget) Commit(_ context.Context, _ string, a contract.Micros) error {
	f.committed = a
	return nil
}
func (f *fakeBudget) Release(context.Context, string) error { f.released = 1; return nil }
func (f *fakeBudget) Remaining(context.Context, contract.BudgetKey) (contract.Micros, bool, error) {
	return 0, false, nil
}

func TestWatchStopsOnceAtTheHold(t *testing.T) {
	stops := 0
	w := &Watch{Budget: &fakeBudget{}, Res: contract.Reservation{ID: "r1", Amount: 100}, Stop: func() { stops++ }}
	if err := w.Add(99); err != nil || stops != 0 {
		t.Fatalf("below the hold: %v %d", err, stops)
	}
	if err := w.Add(1); !errors.Is(err, ErrOverCap) || stops != 1 {
		t.Fatalf("at the hold: %v %d", err, stops)
	}
	_ = w.Add(5)
	if stops != 1 {
		t.Errorf("Stop ran %d times", stops)
	}
}

func TestWatchDoneCommitsOrReleases(t *testing.T) {
	fb := &fakeBudget{}
	w := &Watch{Budget: fb, Res: contract.Reservation{ID: "r1", Amount: 100}}
	_ = w.Done(context.Background())
	if fb.released != 1 || fb.committed != 0 {
		t.Errorf("nothing spent: %+v", fb)
	}
	_ = w.Add(40)
	_ = w.Done(context.Background())
	if fb.committed != 40 {
		t.Errorf("committed %d", fb.committed)
	}
}
