package main

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/KaaS-team/factory/internal/api"
)

func init() {
	register("dispatch", func() *cobra.Command {
		return ticketCmd("dispatch", "Start a session for a ticket now", func(ctx context.Context, c api.Control, id api.TicketID) error { return c.Dispatch(ctx, id) })
	})
}
