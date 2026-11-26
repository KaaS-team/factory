package main

import (
	"github.com/KaaS-team/factory/internal/audit"
)

func init() {
	register("audit", audit.NewCommand, noTool("gap: no audit tool yet; E3 owns the audit read path"))
}
