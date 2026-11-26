package manager

import (
	"context"
	"fmt"
	"io"

	"github.com/KaaS-team/factory/internal/api"
)

// ControlPort drives the manager from api.Control alone, as the CLI and MCP must. It sees runs and
// their last push from the board. It has no PR feed: Failures and NewComments return nothing until
// a GitHub-backed Source is supplied, so a PR with failing CI is handled only where one is wired.
type ControlPort struct {
	C   api.Control
	Out io.Writer // attention items are written here until the inbox exists (F8)
}

// Runs lists the running and stalled slots.
func (p ControlPort) Runs(ctx context.Context) ([]Run, error) {
	b, err := p.C.Board(ctx)
	if err != nil {
		return nil, err
	}
	var rs []Run
	for _, s := range b.Slots {
		if s.Phase != api.PhaseRunning && s.Phase != api.PhaseStalled {
			continue
		}
		r := Run{Ticket: string(s.Ticket.ID), LastPush: s.LastPush}
		if s.Session != nil {
			r.Attempt = s.Session.Attempt
			if r.LastPush.IsZero() {
				r.LastPush = s.Session.StartedAt
			}
		}
		rs = append(rs, r)
	}
	return rs, nil
}

// Failures returns nothing: Control has no CI read.
func (ControlPort) Failures(context.Context, int) ([]Failure, error) { return nil, nil }

// NewComments returns nothing: Control has no review read.
func (ControlPort) NewComments(context.Context, int) ([]Comment, error) { return nil, nil }

// MarkDelivered does nothing.
func (ControlPort) MarkDelivered(context.Context, int, []string) error { return nil }

// Steer sends the message to the ticket's session.
func (p ControlPort) Steer(ctx context.Context, t, m string) error {
	return p.C.Steer(ctx, api.TicketID(t), m)
}

// Retry queues a new attempt.
func (p ControlPort) Retry(ctx context.Context, t string) error {
	return p.C.Retry(ctx, api.TicketID(t))
}

// RerunCI is not available through Control.
func (ControlPort) RerunCI(context.Context, int) error {
	return fmt.Errorf("manager: no CI rerun without a GitHub source")
}

// Attention prints the item.
func (p ControlPort) Attention(_ context.Context, t, reason string) error {
	if p.Out == nil {
		return nil
	}
	_, err := fmt.Fprintf(p.Out, "attention %s: %s\n", t, reason)
	return err
}
