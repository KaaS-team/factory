package mcp

import (
	"context"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/KaaS-team/factory/internal/api"
)

func init() {
	registerTool("hand_off_task", true, func(s *sdk.Server, ctl api.Control, _ Handlers) {
		sdk.AddTool(s, &sdk.Tool{Name: "hand_off_task", Annotations: handOffHint,
			Description: "Hand a task to another assignee. This replaces the current assignee, so the previous owner loses it. Repeating the same hand-off leaves the same end state."},
			func(ctx context.Context, _ *sdk.CallToolRequest, in HandOffInput) (*sdk.CallToolResult, OK, error) {
				if err := required("hand_off_task", [2]string{"id", in.ID}, [2]string{"to", in.To}); err != nil {
					return nil, OK{}, err
				}
				if err := ctl.HandOff(ctx, api.TicketID(in.ID), in.To, in.Note); err != nil {
					return nil, OK{}, safe("hand_off_task", err)
				}
				return nil, OK{Ticket: in.ID, Done: "hand_off_task"}, nil
			})
	})
}
