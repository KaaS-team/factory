package main

import (
	"fmt"
	"strings"

	"github.com/KaaS-team/factory/internal/api"
	"github.com/spf13/cobra"
)

func init() {
	register("steer", steerCmd)
}

func steerCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "steer <ticket> [message...]",
		Short: "Send a message to a ticket's running session",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := control(nil)
			if err != nil {
				return err
			}
			if err := c.Steer(cmd.Context(), api.TicketID(args[0]), strings.Join(args[1:], " ")); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "steer %s\n", args[0])
			return nil
		},
	}
}
