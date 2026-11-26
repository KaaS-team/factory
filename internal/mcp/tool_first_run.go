package mcp

import (
	"github.com/KaaS-team/factory/internal/api"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func init() {
	registerTool("first_run", false, func(s *sdk.Server, _ api.Control, extra Handlers) {
		sdk.AddTool(s, &sdk.Tool{Name: "first_run", Description: "Walk through a first run"}, textTool("first_run", noArg(extra.FirstRun)))
	})
}
