package mcp

import (
	"github.com/KaaS-team/factory/internal/api"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func init() {
	registerTool("onboard", false, func(s *sdk.Server, _ api.Control, extra Handlers) {
		sdk.AddTool(s, &sdk.Tool{Name: "onboard", Description: "Detect the repo, preview the starter shadow config and list what is missing. Read-only; writes nothing."}, textTool("onboard", noArg(extra.Onboard)))
	})
}
