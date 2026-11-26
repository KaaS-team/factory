package main

import (
	"errors"

	"github.com/spf13/cobra"

	"github.com/KaaS-team/factory/internal/backlog"
)

func init() { register("spec", specCmd) }

func specCmd() *cobra.Command {
	var from string
	var approve, asJSON bool
	c := &cobra.Command{
		Use:   "spec [goal]",
		Short: "Turn a goal into issues with Problem, Acceptance and Out of scope sections",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if (from == "") == (len(args) == 0) {
				return errors.New("give exactly one of a goal or --from")
			}
			p, err := backlogPort()
			if err != nil {
				return err
			}
			var ds []backlog.Draft
			var filed []backlog.Filed
			if from != "" {
				text, rerr := readFrom(cmd, from)
				if rerr != nil {
					return rerr
				}
				ds, filed, err = backlog.FromText(cmd.Context(), p, text, approve)
			} else {
				ds, filed, err = backlog.Spec(cmd.Context(), p, nil, args[0], approve)
			}
			if err != nil {
				return err
			}
			return printDrafts(cmd.OutOrStdout(), ds, filed, asJSON)
		},
	}
	c.Flags().StringVar(&from, "from", "", "read issue blocks from `file` (- for stdin)")
	c.Flags().BoolVar(&approve, "approve", false, "file the issues; without it nothing is written")
	c.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return c
}
