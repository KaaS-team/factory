package mcp

import (
	"github.com/KaaS-team/factory/internal/api"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func init() {
	registerTool("shadow", false, func(s *sdk.Server, _ api.Control, extra Handlers) {
		sdk.AddTool(s, &sdk.Tool{Name: "shadow", Description: "Summarize what shadow mode would have dispatched. Read-only."}, textTool("shadow", noArg(extra.Shadow)))
	})
}
