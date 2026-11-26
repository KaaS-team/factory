package main

import (
	"github.com/KaaS-team/factory/internal/initwiz"
)

func init() {
	register("init", initwiz.NewCommand, noTool("an interactive wizard that writes the local config file"))
}
