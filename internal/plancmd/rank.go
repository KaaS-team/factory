package plancmd

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/KaaS-team/factory/internal/api"
	"github.com/KaaS-team/factory/internal/plan"
)

// Ranker is the tracker port `factory plan --rank` needs: the ready tickets and a write of one's priority.
// Pickup sorts by api.Ticket.Priority, lower first, so the rank written here is the pickup order.
type Ranker interface {
	Ready(ctx context.Context) ([]api.Ticket, error)
	SetPriority(ctx context.Context, id api.TicketID, priority int) error
}

// runRank ranks the ready tickets by method and, when write is set, stores rank n as priority n.
func runRank(ctx context.Context, w io.Writer, r Ranker, method string, write bool) error {
	m, err := plan.ParseMethod(method)
	if err != nil {
		return err
	}
	if r == nil {
		return errors.New("--rank needs a tracker to read ready tickets from")
	}
	ready, err := r.Ready(ctx)
	if err != nil {
		return err
	}
	items := make([]plan.Item, len(ready))
	by := map[string]api.Ticket{}
	for i, t := range ready {
		items[i] = plan.Item{ID: string(t.ID), Body: t.Body}
		by[string(t.ID)] = t
	}
	ranked := plan.Rank(items, m)
	for _, x := range ranked {
		t := by[x.ID]
		if x.Scored {
			say(w, "%d. #%s %s (%s %.2f)\\n", x.Rank+1, x.ID, t.Title, m, x.Score)
		} else {
			say(w, "%d. #%s %s (unscored: %s)\\n", x.Rank+1, x.ID, t.Title, x.Why)
		}
		if write && t.Priority != x.Rank {
			if err := r.SetPriority(ctx, t.ID, x.Rank); err != nil {
				return fmt.Errorf("ranking #%s: %w", x.ID, err)
			}
		}
	}
	return nil
}
