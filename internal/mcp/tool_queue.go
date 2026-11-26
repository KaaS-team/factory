package mcp

import (
	"context"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/KaaS-team/factory/internal/api"
)

func init() {
	registerTool("queue", false, func(s *sdk.Server, ctl api.Control, _ Handlers) {
		sdk.AddTool(s, &sdk.Tool{Name: "queue", Description: "Show the merge queue"},
			func(ctx context.Context, _ *sdk.CallToolRequest, _ Empty) (*sdk.CallToolResult, QueueOutput, error) {
				b, err := ctl.Board(ctx)
				return nil, QueueOutput{MergeQueue: nonNil(b.MergeQueue)}, safe("queue", err)
			})
	})
}
