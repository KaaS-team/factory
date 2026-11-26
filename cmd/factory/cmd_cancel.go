package main

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/KaaS-team/factory/internal/api"
)

func init() {
	register("cancel", func() *cobra.Command {
		return ticketCmd("cancel", "Cancel a ticket's running session", func(ctx context.Context, c api.Control, id api.TicketID) error { return c.Cancel(ctx, id) })
	})
}
