package mcp

import (
	"github.com/KaaS-team/factory/internal/api"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func init() {
	registerTool("report", false, func(s *sdk.Server, _ api.Control, extra Handlers) {
		sdk.AddTool(s, &sdk.Tool{Name: "report", Description: "Report on finished work"}, textTool("report", noArg(extra.Report)))
	})
}
