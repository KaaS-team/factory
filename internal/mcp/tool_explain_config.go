package mcp

import (
	"github.com/KaaS-team/factory/internal/api"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func init() {
	registerTool("explain_config", false, func(s *sdk.Server, _ api.Control, extra Handlers) {
		sdk.AddTool(s, &sdk.Tool{Name: "explain_config", Description: "Explain which layer set a config key and which layers it overrode. Read-only."}, textTool("explain_config", extra.ExplainConfig))
	})
}
