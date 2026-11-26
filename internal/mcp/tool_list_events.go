package mcp

import (
	"context"
	"errors"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/KaaS-team/factory/internal/api"
)

func init() {
	registerTool("list_events", false, func(s *sdk.Server, ctl api.Control, _ Handlers) {
		sdk.AddTool(s, &sdk.Tool{Name: "list_events", Annotations: readHint,
			Description: "List the events of one run, as the factory returns them. Read-only."},
			func(ctx context.Context, _ *sdk.CallToolRequest, in IDInput) (*sdk.CallToolResult, EventsOutput, error) {
				out := EventsOutput{Events: []EventView{}}
				if err := required("list_events", [2]string{"id", in.ID}); err != nil {
					return nil, out, err
				}
				h, ok := ctl.(api.Historian)
				if !ok {
					return nil, out, errors.New("list_events: history is unavailable: this control plane does not keep past events")
				}
				evs, err := h.History(ctx, api.TicketID(in.ID))
				for _, e := range evs {
					out.Events = append(out.Events, EventView{Time: e.Time, Kind: e.Kind, Ticket: e.Ticket})
				}
				return nil, out, safe("list_events", err)
			})
	})
}
