package main

import (
	"github.com/KaaS-team/factory/internal/dispatch/shadow"
)

func init() {
	register("shadow", shadow.NewCommand)
}
