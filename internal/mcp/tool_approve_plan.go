package mcp

import (
	"github.com/KaaS-team/factory/internal/api"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func init() {
	registerTool("approve_plan", true, func(s *sdk.Server, _ api.Control, extra Handlers) {
		sdk.AddTool(s, &sdk.Tool{Name: "approve_plan", Description: "Plan a goal and approve it in one call, so its tickets are filed. The factory keeps no plan id between calls, so this takes the goal, not an id. Each call files tickets."}, textTool("approve_plan", extra.ApprovePlan))
	})
}
