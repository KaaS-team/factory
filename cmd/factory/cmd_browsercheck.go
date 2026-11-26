package main

import (
	"github.com/KaaS-team/factory/internal/browsercheck"
)

func init() {
	register("browsercheck", browsercheck.NewCommand, noTool("a gate step the browser pack runs as a subprocess"))
}
