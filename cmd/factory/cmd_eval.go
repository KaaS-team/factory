package main

import (
	"github.com/KaaS-team/factory/internal/eval"
	"github.com/spf13/cobra"
)

func init() {
	register("eval", func() *cobra.Command { return eval.NewCommand() }, noTool("gap: no eval tool yet; a matrix run is long and costs agent time"))
}
