package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

func init() {
	register("version", func() *cobra.Command {
		return &cobra.Command{
			Use:   "version",
			Short: "Print the factory version",
			Run: func(cmd *cobra.Command, _ []string) {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), version)
			},
		}
	}, noTool("gap: no version tool yet; the MCP server reports its version in the handshake"))
}
