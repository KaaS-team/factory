package main

import (
	"github.com/KaaS-team/factory/internal/packs"
)

func init() {
	register("add", packs.NewAddCommand, noTool("pins third-party files into factory.lock; an operator decision, not an agent one"))
}
