package main

import (
	"context"
	"fmt"

	"github.com/KaaS-team/factory/internal/api"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// control loads the config and builds the Control through the newControl seam.
func control(fs *pflag.FlagSet) (api.Control, error) {
	l, err := loadConfig(fs)
	if err != nil {
		return nil, err
	}
	return newControl(l.Config)
}

func ticketCmd(name, short string, call func(context.Context, api.Control, api.TicketID) error) *cobra.Command {
	return &cobra.Command{
		Use:   name + " <ticket>",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := control(nil)
			if err != nil {
				return err
			}
			if err := call(cmd.Context(), c, api.TicketID(args[0])); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s %s\n", name, args[0])
			return nil
		},
	}
}
