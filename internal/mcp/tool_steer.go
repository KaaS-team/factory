package mcp

import (
	"context"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/KaaS-team/factory/internal/api"
)

func init() {
	registerTool("steer", true, func(s *sdk.Server, ctl api.Control, _ Handlers) {
		sdk.AddTool(s, &sdk.Tool{Name: "steer", Description: "Send a message to a running ticket's session"},
			func(ctx context.Context, _ *sdk.CallToolRequest, in SteerInput) (*sdk.CallToolResult, OK, error) {
				if err := ctl.Steer(ctx, api.TicketID(in.Ticket), in.Message); err != nil {
					return nil, OK{}, safe("steer", err)
				}
				return nil, OK{Ticket: in.Ticket, Done: "steer"}, nil
			})
	})
}
