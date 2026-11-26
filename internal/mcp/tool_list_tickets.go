package mcp

import (
	"context"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/KaaS-team/factory/internal/api"
)

func init() {
	registerTool("list_tickets", false, func(s *sdk.Server, ctl api.Control, _ Handlers) {
		sdk.AddTool(s, &sdk.Tool{Name: "list_tickets", Description: "List running and ready tickets"},
			func(ctx context.Context, _ *sdk.CallToolRequest, _ Empty) (*sdk.CallToolResult, TicketsOutput, error) {
				b, err := ctl.Board(ctx)
				out := TicketsOutput{Tickets: []api.Ticket{}}
				for _, sl := range b.Slots {
					out.Tickets = append(out.Tickets, sl.Ticket)
				}
				out.Tickets = append(out.Tickets, b.Ready...)
				return nil, out, safe("list_tickets", err)
			})
	})
}
