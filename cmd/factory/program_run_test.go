package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/KaaS-team/factory/internal/api"
	"github.com/KaaS-team/factory/internal/program"
)

type boardCtl struct {
	api.Control
	phases    []api.Phase
	n         int
	dispached []api.TicketID
}

func (b *boardCtl) CreateTicket(_ context.Context, title, body string) (api.TicketID, error) {
	return "42", nil
}
func (b *boardCtl) Dispatch(_ context.Context, id api.TicketID) error {
	b.dispached = append(b.dispached, id)
	return nil
}
func (b *boardCtl) Board(context.Context) (api.Board, error) {
	p := b.phases[min(b.n, len(b.phases)-1)]
	b.n++
	return api.Board{Slots: []api.Slot{{Ticket: api.Ticket{ID: "7"}, Phase: api.PhaseMerged}, {Ticket: api.Ticket{ID: "42"}, Phase: p}}}, nil
}

func TestControlWorkerWaitsForAnEndPhase(t *testing.T) {
	c := &boardCtl{phases: []api.Phase{api.PhaseRunning, api.PhaseRunning, api.PhaseDone}}
	w := controlWorker{ctl: c, epic: "#9", poll: time.Millisecond}
	res, err := w.Run(context.Background(), program.Task{ID: "t1", Title: "T"})
	if err != nil || !res.OK || c.n != 3 || len(c.dispached) != 1 {
		t.Fatalf("%+v %v polls=%d", res, err, c.n)
	}
	c = &boardCtl{phases: []api.Phase{api.PhaseNeedsHuman}}
	res, err = controlWorker{ctl: c, poll: time.Millisecond}.Run(context.Background(), program.Task{ID: "t1", Title: "T"})
	if err != nil || res.OK {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestResumeUnknownRun(t *testing.T) {
	t.Chdir(t.TempDir())
	_, err := runSub(context.Background(), "resume", "ghost")
	if err == nil || !strings.Contains(err.Error(), "no such run") {
		t.Fatalf("err = %v", err)
	}
}
