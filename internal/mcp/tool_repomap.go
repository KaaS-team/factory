package mcp

import (
	"github.com/KaaS-team/factory/internal/api"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func init() {
	registerTool("repomap", false, func(s *sdk.Server, _ api.Control, extra Handlers) {
		sdk.AddTool(s, &sdk.Tool{Name: "repomap", Description: "Print a ranked, token-budgeted symbol map of the repository. Read-only."}, textTool("repomap", extra.Repomap))
	})
}
