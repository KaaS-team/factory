package cap

import (
	"context"
	"sync"

	"github.com/KaaS-team/factory/ee/contract"
)

// Watch stops a running session at its reservation. The session reports each cost increment to
// Add; the first Add that reaches the amount held calls Stop once and returns ErrOverCap. Done
// commits what was spent, or releases the hold when nothing was.
type Watch struct {
	Budget contract.Budget
	Res    contract.Reservation
	Stop   func() // cancels the session; called at most once
	// Stopped, when set, runs once with the reservation when the cap stops the session. The ee
	// build records the budget stop in the audit chain here.
	Stopped func(contract.Reservation)

	mu      sync.Mutex
	spent   contract.Micros
	stopped bool
}

// Add counts delta and stops the session when the total reaches the amount held.
func (w *Watch) Add(delta contract.Micros) error {
	w.mu.Lock()
	w.spent += delta
	hit := w.spent >= w.Res.Amount && !w.stopped
	if hit {
		w.stopped = true
	}
	over := w.stopped
	w.mu.Unlock()
	if hit {
		if w.Stop != nil {
			w.Stop()
		}
		if w.Stopped != nil {
			w.Stopped(w.Res)
		}
	}
	if over {
		return ErrOverCap
	}
	return nil
}

// Done closes the reservation with the amount spent.
func (w *Watch) Done(ctx context.Context) error {
	w.mu.Lock()
	spent := w.spent
	w.mu.Unlock()
	if spent == 0 {
		return w.Budget.Release(ctx, w.Res.ID)
	}
	return w.Budget.Commit(ctx, w.Res.ID, spent)
}
