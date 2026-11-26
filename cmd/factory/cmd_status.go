package main

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

func init() {
	register("status", statusCmd)
}

func statusCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show the board, one row per slot",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := control(nil)
			if err != nil {
				return err
			}
			b, err := c.Board(cmd.Context())
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if asJSON {
				return json.NewEncoder(out).Encode(b)
			}
			for _, s := range b.Slots {
				_, _ = fmt.Fprintf(out, "%s\t%s\t%s\n", s.Ticket.ID, s.Phase, s.Ticket.Title)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the board as one JSON object")
	return cmd
}
