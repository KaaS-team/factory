package mcp

import (
	"github.com/KaaS-team/factory/internal/api"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func init() {
	registerTool("doctor", false, func(s *sdk.Server, _ api.Control, extra Handlers) {
		sdk.AddTool(s, &sdk.Tool{Name: "doctor", Description: "Check the tools and settings the factory needs. Read-only."}, textTool("doctor", noArg(extra.Doctor)))
	})
}
