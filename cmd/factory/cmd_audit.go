package main

import (
	"github.com/KaaS-team/factory/internal/audit"
)

func init() {
	register("audit", audit.NewCommand, noTool("gap: an audit tool needs an api.Control method for the log, and api is W0; the CLI covers export, verify, search and forward"))
}
