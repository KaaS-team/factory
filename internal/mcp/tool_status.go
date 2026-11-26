package mcp

import (
	"context"
	"fmt"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/KaaS-team/factory/internal/api"
)

func init() {
	registerTool("status", false, func(s *sdk.Server, ctl api.Control, _ Handlers) {
		sdk.AddTool(s, &sdk.Tool{Name: "status", Description: "Show the board, or one ticket with its history"},
			func(ctx context.Context, _ *sdk.CallToolRequest, in StatusInput) (*sdk.CallToolResult, StatusOutput, error) {
				b, err := ctl.Board(ctx)
				out := StatusOutput{BoardView: boardView(b)}
				if err != nil || in.Ticket == "" {
					return nil, out, safe("status", err)
				}
				v, ok, err := ticketView(ctx, ctl, b, in.Ticket)
				if err != nil {
					return nil, out, safe("status", err)
				}
				if !ok {
					return nil, out, fmt.Errorf("status: ticket %s is not on the board", in.Ticket)
				}
				out.Detail = &v
				return nil, out, nil
			})
	})
}
