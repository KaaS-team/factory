//go:build ee

// SPDX-License-Identifier: LicenseRef-KaaS-EE

package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/KaaS-team/factory/ee/policy"
)

func init() {
	register("policy", policyCmd)
}

func policyCmd() *cobra.Command {
	var rules, role, repo string
	var usd float64
	cmd := &cobra.Command{
		Use:   "policy <action> | check [path] | explain <action>",
		Short: "Check actions and kaas-policy.toml files against policy (enterprise)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := loadPolicy(rules)
			if err != nil {
				return err
			}
			d := e.Check(cmd.Context(), policy.Actor{ID: actorID(), Role: role}, policy.Action(args[0]), policy.Resource{Repo: repo, USD: usd})
			verdict := "deny"
			if d.Allow {
				verdict = "allow"
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s %s: %s\n", verdict, d.RuleID, d.Reason)
			if !d.Allow {
				return fmt.Errorf("policy denied %s", args[0])
			}
			return nil
		},
	}
	cmd.AddCommand(policyCheckCmd(), policyExplainCmd())
	cmd.Flags().StringVar(&rules, "rules", "", "path to the YAML rules file")
	cmd.Flags().StringVar(&role, "role", "", "actor role")
	cmd.Flags().StringVar(&repo, "repo", "", "repository the action touches")
	cmd.Flags().Float64Var(&usd, "usd", 0, "spend the action would add")
	_ = cmd.MarkFlagRequired("rules")
	return cmd
}
