package mcp

import (
	"bytes"
	"context"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/KaaS-team/factory/internal/api"
	"github.com/KaaS-team/factory/internal/manager"
)

// ManageOutput is what manage returns.
type ManageOutput struct {
	Attention string `json:"attention,omitempty"`
}

type noInput struct{}

func init() {
	registerTool("manage", true, func(s *sdk.Server, ctl api.Control, _ Handlers) {
		sdk.AddTool(s, &sdk.Tool{Name: "manage", Description: "Run one manager pass: nudge or restart stalled runs and report items that need a person"},
			func(ctx context.Context, _ *sdk.CallToolRequest, _ noInput) (*sdk.CallToolResult, ManageOutput, error) {
				var out bytes.Buffer
				p := manager.ControlPort{C: ctl, Out: &out}
				m := manager.New(manager.Config{RestartAt: 2 * 60 * 60 * 1e9}, p, p, discard{})
				if err := m.Pass(ctx); err != nil {
					return nil, ManageOutput{}, safe("manage", err)
				}
				return nil, ManageOutput{Attention: out.String()}, nil
			})
	})
}

type discard struct{}

func (discard) Record(context.Context, string, string, string, error) {}
