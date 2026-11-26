package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

func init() {
	register("queue", queueCmd)
}

func queueCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "queue",
		Short: "Show tickets ready to run and the merge queue",
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
			for _, t := range b.Ready {
				_, _ = fmt.Fprintf(out, "ready\t%s\t%s\n", t.ID, t.Title)
			}
			for _, id := range b.MergeQueue {
				_, _ = fmt.Fprintf(out, "merge-queue\t%s\n", id)
			}
			return nil
		},
	}
}
