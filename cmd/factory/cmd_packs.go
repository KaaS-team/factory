package main

import (
	"github.com/spf13/cobra"

	"github.com/KaaS-team/factory/internal/packs"
	"github.com/KaaS-team/factory/internal/upgrade"
)

func init() {
	register("packs", packsCommand)
}

// packsCommand is `factory packs`, with `packs upgrade` bumping pinned packs.
func packsCommand() *cobra.Command {
	c := packs.NewPacksCommand()
	c.AddCommand(upgrade.NewCommand())
	return c
}
