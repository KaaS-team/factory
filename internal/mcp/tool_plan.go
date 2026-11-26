package mcp

import (
	"github.com/KaaS-team/factory/internal/api"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func init() {
	registerTool("plan", true, func(s *sdk.Server, _ api.Control, extra Handlers) {
		sdk.AddTool(s, &sdk.Tool{Name: "plan", Description: "Plan a goal into tickets"}, textTool("plan", extra.Plan))
	})
}
