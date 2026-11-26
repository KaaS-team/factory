package mcp

import (
	"context"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/KaaS-team/factory/internal/api"
)

func init() {
	registerTool("queue_followup", true, func(s *sdk.Server, ctl api.Control, _ Handlers) {
		sdk.AddTool(s, &sdk.Tool{Name: "queue_followup", Annotations: writeHint,
			Description: "Queue a follow-up message for a run. Each call queues another message. Does not remove or edit earlier ones."},
			func(ctx context.Context, _ *sdk.CallToolRequest, in FollowupInput) (*sdk.CallToolResult, OK, error) {
				if err := required("queue_followup", [2]string{"id", in.ID}, [2]string{"message", in.Message}); err != nil {
					return nil, OK{}, err
				}
				if err := ctl.QueueFollowup(ctx, api.TicketID(in.ID), in.Message); err != nil {
					return nil, OK{}, safe("queue_followup", err)
				}
				return nil, OK{Ticket: in.ID, Done: "queue_followup"}, nil
			})
	})
}
