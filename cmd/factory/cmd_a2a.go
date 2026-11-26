package main

import (
	"github.com/spf13/cobra"

	"github.com/KaaS-team/factory/internal/a2a"
)

func init() {
	register("a2a", func() *cobra.Command { return a2a.NewCommand(a2aDeps) }, noTool("serves another agent protocol; it is a peer of the MCP server, not a tool on it"))
}
