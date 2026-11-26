package mcp

import (
	"context"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/KaaS-team/factory/internal/api"
	"github.com/KaaS-team/factory/internal/attention"
)

// AttentionOutput lists what needs a person.
type AttentionOutput struct {
	Items []attention.Item `json:"items"`
}

func init() {
	registerTool("attention", false, func(s *sdk.Server, ctl api.Control, _ Handlers) {
		sdk.AddTool(s, &sdk.Tool{Name: "attention", Description: "List what needs a person: plan approvals, policy refusals, budget stops, missing proof and merge approvals"},
			func(ctx context.Context, _ *sdk.CallToolRequest, _ Empty) (*sdk.CallToolResult, AttentionOutput, error) {
				l, ok := ctl.(attention.Lister)
				if !ok {
					return nil, AttentionOutput{Items: []attention.Item{}}, nil
				}
				items, err := l.Open(ctx)
				return nil, AttentionOutput{Items: nonNil(items)}, safe("attention", err)
			})
	})
}
