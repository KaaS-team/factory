package main

import (
	"github.com/spf13/cobra"

	"github.com/KaaS-team/factory/internal/api"
	"github.com/KaaS-team/factory/internal/backlog"
)

func init() { register("decompose", decomposeCmd) }

func decomposeCmd() *cobra.Command {
	var from string
	var approve, asJSON bool
	c := &cobra.Command{
		Use:   "decompose <epic>",
		Short: "Split an epic into child issues linked as sub-issues",
		Long:  "The children are the '--- issue: <title>' blocks in --from, or in the epic body when --from is not given.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := backlogPort()
			if err != nil {
				return err
			}
			var text string
			if from != "" {
				text, err = readFrom(cmd, from)
			} else {
				var t api.Ticket
				t, err = p.Get(cmd.Context(), api.TicketID(args[0]))
				text = t.Body
			}
			if err != nil {
				return err
			}
			ds, filed, err := backlog.Decompose(cmd.Context(), p, api.TicketID(args[0]), text, approve)
			if err != nil {
				return err
			}
			return printDrafts(cmd.OutOrStdout(), ds, filed, asJSON)
		},
	}
	c.Flags().StringVar(&from, "from", "", "read the child blocks from `file` (- for stdin)")
	c.Flags().BoolVar(&approve, "approve", false, "file the children and link them; without it nothing is written")
	c.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return c
}
