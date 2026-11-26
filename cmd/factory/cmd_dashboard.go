package main

import (
	"github.com/spf13/cobra"

	"github.com/KaaS-team/factory/internal/dashboard"
)

func init() {
	register("dashboard", func() *cobra.Command {
		dash := dashboard.NewCommand()
		attach(dash, dashboard.WithControl)
		return dash
	}, noTool("a long-running web server; the status tool returns the same board"))
}
