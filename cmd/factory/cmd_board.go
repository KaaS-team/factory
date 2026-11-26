package main

import (
	"github.com/spf13/cobra"

	"github.com/KaaS-team/factory/internal/tui"
)

func init() {
	register("board", func() *cobra.Command {
		board := tui.NewCommand()
		attach(board, tui.WithControl)
		return board
	}, noTool("an interactive terminal view; the status tool returns the same board"))
}
