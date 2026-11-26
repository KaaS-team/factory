package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/KaaS-team/factory/internal/api"
	"github.com/KaaS-team/factory/internal/mcp"
)

func init() {
	register("mcp", func() *cobra.Command {
		return mcp.NewCommand(mcp.WithControl(func() (api.Control, mcp.Handlers, error) {
			c, err := control(nil)
			return c, mcpHandlers(), err
		}))
	}, noTool("it is the MCP server"))
}

// mcpHandlers backs the MCP tools that have a CLI command with that command, run in process, so a
// tool and its command share one code path. The CLI keeps no plan id between calls, so approve_plan
// takes the goal and runs `plan --approve` in one call.
func mcpHandlers() mcp.Handlers {
	return mcp.Handlers{
		FirstRun: func(ctx context.Context) (string, error) { return runSub(ctx, "first-run", "--dry-run") },
		Report:   func(ctx context.Context) (string, error) { return runSub(ctx, "report") },
		Plan: func(ctx context.Context, in mcp.PlanInput) (string, error) {
			return dataSub(ctx, "plan", in.Goal, "plan", "goal must be text")
		},
		ApprovePlan: func(ctx context.Context, in mcp.ApprovePlanInput) (string, error) {
			return dataSub(ctx, "approve_plan", in.Goal, "plan", "goal must be text", "--approve")
		},
		ExplainConfig: func(ctx context.Context, in mcp.ExplainConfigInput) (string, error) {
			return dataSub(ctx, "explain_config", in.Key, "config", "key must be a name", "explain")
		},
	}
}

// dataSub runs `factory <cmd> <args> -- <value>`. The value is data: empty or starting with "-" is
// refused, so a client can never turn it into a flag.
func dataSub(ctx context.Context, tool, value, cmd, want string, args ...string) (string, error) {
	if v := strings.TrimSpace(value); v == "" || strings.HasPrefix(v, "-") {
		return "", fmt.Errorf("%s: %s, not empty or a flag", tool, want)
	}
	return runSub(ctx, append(append([]string{cmd}, args...), "--", value)...)
}
