//go:build ee

// SPDX-License-Identifier: LicenseRef-KaaS-EE

package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/KaaS-team/factory/ee/policy"
	"github.com/KaaS-team/factory/internal/api"
	"github.com/KaaS-team/factory/internal/config"
)

// eeBuild reports whether the enterprise commands are compiled in.
const eeBuild = true

func init() { rootHooks = append(rootHooks, guardControl) }

// guardControl runs on every new root. When FACTORY_POLICY names a rules file it puts the policy
// check in front of dispatch and retry. With the variable unset the check is off.
func guardControl(*cobra.Command) {
	next := newControl
	newControl = func(cfg config.Config) (api.Control, error) {
		c, err := next(cfg)
		path := os.Getenv("FACTORY_POLICY")
		if err != nil || path == "" {
			return c, err
		}
		e, err := loadPolicy(path)
		if err != nil {
			return nil, err
		}
		role := os.Getenv("FACTORY_ROLE")
		return policy.Guard(c, e, policy.Actor{ID: actorID(), Role: role}, os.Stderr), nil
	}
}

func actorID() string {
	if id := os.Getenv("FACTORY_ACTOR"); id != "" {
		return id
	}
	return "cli"
}

func loadPolicy(path string) (*policy.Engine, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("policy: %w", err)
	}
	return policy.Parse(data)
}
