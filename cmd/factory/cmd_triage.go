package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/KaaS-team/factory/internal/api"
	"github.com/KaaS-team/factory/internal/triage"
)

func init() {
	register("triage", triageCmd, noTool("gap: an MCP triage tool is owed; it needs a tracker read for open tickets that api.Control does not expose yet"))
}

// ticketSink adapts the tracker's comment write to triage.Sink.
type ticketSink struct {
	c func(ctx context.Context, id api.TicketID, body string) error
}

func (s ticketSink) Comment(ctx context.Context, id, body string) error {
	return s.c(ctx, api.TicketID(id), body)
}

func triageCmd() *cobra.Command {
	var apply, asJSON bool
	var protected []string
	c := &cobra.Command{
		Use:   "triage <ticket>",
		Short: "Classify a ticket and decide: proceed, needs-info, duplicate or refused",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			tr, err := planTracker()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			t, err := tr.Get(ctx, api.TicketID(args[0]))
			if err != nil {
				return err
			}
			ready, err := tr.Ready(ctx)
			if err != nil {
				return err
			}
			var open []triage.Issue
			for _, o := range ready {
				open = append(open, triage.Issue{ID: string(o.ID), Title: o.Title, Body: o.Body, Labels: o.Labels})
			}
			res, err := triage.Run(ctx, triage.Issue{ID: string(t.ID), Title: t.Title, Body: t.Body, Labels: t.Labels},
				triage.Options{Open: open, Protected: protected})
			if err != nil {
				return err
			}
			if apply {
				if err := triage.Apply(ctx, ticketSink{tr.Comment}, args[0], res); err != nil {
					return err
				}
			}
			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(res)
			}
			_, err = fmt.Fprint(cmd.OutOrStdout(), res.Comment(true))
			return err
		},
	}
	c.Flags().BoolVar(&apply, "apply", false, "write the decision back to the ticket as a comment; without it nothing is written")
	c.Flags().BoolVar(&asJSON, "json", false, "print the decision as JSON")
	c.Flags().StringSliceVar(&protected, "protected", nil, "path `prefixes` agents may not edit; a ticket naming one is refused")
	return c
}
