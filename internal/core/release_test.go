package core

import (
	"context"
	"strings"
	"testing"

	"github.com/KaaS-team/factory/internal/api"
	"github.com/KaaS-team/factory/internal/meter"
)

// staleRig is a core restarted over a ticket labelled building whose session the dispatcher does
// not know, with one more ticket ready and a single slot (issue #719).
func staleRig(t *testing.T, attempt int) *rig {
	t.Helper()
	r := newRig(t, 1, meter.Budgets{}, tkt("stale", 0), tkt("next", 1))
	r.tr.Phases["stale"] = api.PhaseRunning
	r.obs.Set(api.Observation{Ticket: "stale", Attempt: attempt, Branch: "factory/stale", LastPush: t0})
	r.disp.Lost = map[string]bool{"stale-a" + string(rune('0'+attempt)): true}
	return r
}

func TestLostSessionReleasesSlot(t *testing.T) {
	r := staleRig(t, 1)
	ctx := context.Background()
	r.c.Tick(ctx)
	r.c.Tick(ctx)
	if _, ok := r.disp.Sessions["next-a1"]; !ok {
		t.Fatalf("the second ticket was not dispatched within two ticks, sessions = %v", r.disp.Sessions)
	}
	n := 0
	for _, c := range r.tr.Comments["stale"] {
		if strings.Contains(c, "Session lost") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("comments = %q, want one saying the session was lost", r.tr.Comments["stale"])
	}
}

func TestLostSessionAtAttemptCapNeedsHuman(t *testing.T) {
	r := staleRig(t, 3)
	ctx := context.Background()
	r.c.Tick(ctx)
	if got := r.tr.Phases["stale"]; got != api.PhaseNeedsHuman {
		t.Fatalf("phase = %s, want needs-human", got)
	}
	r.c.Tick(ctx)
	if _, ok := r.disp.Sessions["next-a1"]; !ok {
		t.Fatal("the slot was not freed")
	}
}
