package mcp

import (
	"context"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/KaaS-team/factory/internal/api"
)

func init() {
	registerTool("retry", true, func(s *sdk.Server, ctl api.Control, _ Handlers) {
		sdk.AddTool(s, &sdk.Tool{Name: "retry", Description: "Retry a ticket"},
			act("retry", func(ctx context.Context, in TicketInput) error { return ctl.Retry(ctx, api.TicketID(in.Ticket)) }))
	})
}
