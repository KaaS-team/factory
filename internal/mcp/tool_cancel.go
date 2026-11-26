package mcp

import (
	"context"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/KaaS-team/factory/internal/api"
)

func init() {
	registerTool("cancel", true, func(s *sdk.Server, ctl api.Control, _ Handlers) {
		sdk.AddTool(s, &sdk.Tool{Name: "cancel", Description: "Cancel a ticket's session"},
			act("cancel", func(ctx context.Context, in TicketInput) error { return ctl.Cancel(ctx, api.TicketID(in.Ticket)) }))
	})
}
