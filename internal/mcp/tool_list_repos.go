package mcp

import (
	"context"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/KaaS-team/factory/internal/api"
)

func init() {
	registerTool("list_repos", false, func(s *sdk.Server, ctl api.Control, _ Handlers) {
		sdk.AddTool(s, &sdk.Tool{Name: "list_repos", Annotations: readHint,
			Description: "List the repositories the factory knows about. Read-only."},
			func(ctx context.Context, _ *sdk.CallToolRequest, _ Empty) (*sdk.CallToolResult, ReposOutput, error) {
				repos, err := ctl.Repos(ctx)
				return nil, ReposOutput{Repos: nonNil(repos)}, safe("list_repos", err)
			})
	})
}
