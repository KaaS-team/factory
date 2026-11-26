package main

import (
	"github.com/spf13/cobra"

	"github.com/KaaS-team/factory/internal/selfupdate"
	"github.com/KaaS-team/factory/internal/upgrade"
)

func init() {
	register("upgrade", func() *cobra.Command { return upgrade.NewSelfCommand(version, selfupdate.GitHubSource{}, "") },
		noTool("replaces the running binary; an agent must never do that"))
}
