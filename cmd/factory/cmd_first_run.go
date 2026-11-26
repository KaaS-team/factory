package main

import (
	"github.com/spf13/cobra"

	"github.com/KaaS-team/factory/internal/firstrun"
)

func init() {
	register("first-run", func() *cobra.Command {
		return firstrun.NewCommand(firstrun.WithExec(firstRunExec), firstrun.WithProtection(firstRunProtection))
	})
}
