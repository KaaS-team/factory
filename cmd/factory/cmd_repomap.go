package main

import (
	"github.com/KaaS-team/factory/internal/packs/repomap"
)

func init() {
	register("repomap", repomap.NewCommand)
}
