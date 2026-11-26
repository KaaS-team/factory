package mcp

import (
	"context"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/KaaS-team/factory/internal/api"
)

// BacklogRun runs one backlog CLI command (spec, decompose or prioritize) in process with stdin and
// returns what it printed. cmd/factory sets it from init, so the tools and the commands share one
// code path. Nil means the build has no backlog commands.
var BacklogRun func(ctx context.Context, stdin string, args ...string) (string, error)

// backlogAdd builds the add func of a tool that runs a backlog command. Each tool_<name>.go passes it
// to registerTool under its own name.
func backlogAdd[In any](name, desc string, args func(In) ([]string, string, error)) func(*sdk.Server, api.Control, Handlers) {
	return func(s *sdk.Server, _ api.Control, _ Handlers) {
		sdk.AddTool(s, &sdk.Tool{Name: name, Description: desc},
			func(ctx context.Context, _ *sdk.CallToolRequest, in In) (*sdk.CallToolResult, TextOutput, error) {
				if BacklogRun == nil {
					return nil, TextOutput{}, unavailable(name)
				}
				argv, stdin, err := args(in)
				if err != nil {
					return nil, TextOutput{}, err
				}
				out, err := BacklogRun(ctx, stdin, argv...)
				return nil, TextOutput{Text: out}, safe(name, err)
			})
	}
}

func digits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}
