package mcp

import (
	"context"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/KaaS-team/factory/internal/api"
)

func init() {
	registerTool("create_task", true, func(s *sdk.Server, ctl api.Control, _ Handlers) {
		sdk.AddTool(s, &sdk.Tool{Name: "create_task", Annotations: writeHint,
			Description: "Create a new task. Each call creates another task, so repeating it makes duplicates. Never removes or changes existing tasks."},
			func(ctx context.Context, _ *sdk.CallToolRequest, in CreateTaskInput) (*sdk.CallToolResult, CreatedOutput, error) {
				if err := required("create_task", [2]string{"title", in.Title}); err != nil {
					return nil, CreatedOutput{}, err
				}
				id, err := ctl.CreateTicket(ctx, in.Title, in.Body)
				return nil, CreatedOutput{ID: string(id)}, safe("create_task", err)
			})
	})
}
