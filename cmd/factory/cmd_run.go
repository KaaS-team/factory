package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"

	"github.com/KaaS-team/factory/internal/config"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

func init() {
	register("run", runCmd, noTool("the factory loop itself; an agent starts work with dispatch"))
}

func runCmd() *cobra.Command {
	var headless bool
	var slots int
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run the factory; --headless streams one JSON event per line",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			fs := pflag.NewFlagSet("run", pflag.ContinueOnError)
			config.RegisterFlags(fs)
			if cmd.Flags().Changed("slots") {
				if err := fs.Set("slots", fmt.Sprint(slots)); err != nil {
					return err
				}
			}
			c, err := control(fs)
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
			defer stop()
			ev, err := c.Events(ctx)
			if err != nil {
				return err
			}
			// A Control that owns a loop (core.Core) is started here; the loop ends with ctx.
			if l, ok := c.(interface{ Run(context.Context) error }); ok {
				loopDone := make(chan struct{})
				go func() { defer close(loopDone); _ = l.Run(ctx) }()
				defer func() { stop(); <-loopDone }()
			}
			out, logs := cmd.OutOrStdout(), cmd.ErrOrStderr()
			if !headless {
				_, _ = fmt.Fprintln(logs, "factory run: streaming events (use --headless for NDJSON on stdout)")
			}
			enc := json.NewEncoder(out)
			for {
				select {
				case <-ctx.Done():
					return nil
				case e, ok := <-ev:
					if !ok {
						return nil
					}
					if headless {
						if err := enc.Encode(e); err != nil {
							return err
						}
						continue
					}
					_, _ = fmt.Fprintf(out, "%s\t%s\t%s\n", e.Time.Format("15:04:05"), e.Kind, e.Ticket)
				}
			}
		},
	}
	cmd.Flags().BoolVar(&headless, "headless", false, "print one api.Event JSON object per line and nothing else on stdout")
	cmd.Flags().IntVar(&slots, "slots", 0, "number of parallel slots (overrides config)")
	return cmd
}
