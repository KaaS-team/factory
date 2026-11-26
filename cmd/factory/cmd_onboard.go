package main

import (
	"github.com/spf13/cobra"

	"github.com/KaaS-team/factory/internal/onboard"
)

func init() {
	register("onboard", func() *cobra.Command {
		return onboard.NewCommand(onboard.WithProtection(firstRunProtection))
	})
}
