package main

import (
	"fmt"

	"github.com/KaaS-team/factory/internal/brand"

	"github.com/spf13/cobra"
)

func init() {
	register("version", func() *cobra.Command {
		return &cobra.Command{
			Use:   "version",
			Short: "Print the factory version",
			Run: func(cmd *cobra.Command, _ []string) {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), version+"\n"+brand.URL)
			},
		}
	})
}
