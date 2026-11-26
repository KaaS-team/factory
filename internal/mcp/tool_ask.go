package mcp

import (
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/KaaS-team/factory/internal/api"
)

// AskInput is a read-only question about one ticket's run.
type AskInput struct {
	Ticket   string `json:"ticket" jsonschema:"the ticket to ask about"`
	Question string `json:"question" jsonschema:"what to ask; answered from the run's ledger and transcript"`
}

func init() {
	// Not a write tool: it reads the run's records and changes nothing, so --read-only keeps it.
	registerTool("ask", false, func(s *sdk.Server, _ api.Control, extra Handlers) {
		sdk.AddTool(s, &sdk.Tool{Name: "ask", Description: "Ask a read-only question about a running or finished ticket, answered from its ledger and transcript. Read-only."}, textTool("ask", extra.Ask))
	})
}
