package main

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/KaaS-team/factory/internal/api"
)

func init() {
	register("retry", func() *cobra.Command {
		return ticketCmd("retry", "Queue a new attempt for a ticket", func(ctx context.Context, c api.Control, id api.TicketID) error { return c.Retry(ctx, id) })
	})
}
