package mcp

import (
	"context"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/KaaS-team/factory/internal/api"
)

func init() {
	registerTool("dispatch", true, func(s *sdk.Server, ctl api.Control, _ Handlers) {
		sdk.AddTool(s, &sdk.Tool{Name: "dispatch", Description: "Dispatch a ready ticket"},
			act("dispatch", func(ctx context.Context, in TicketInput) error { return ctl.Dispatch(ctx, api.TicketID(in.Ticket)) }))
	})
}
