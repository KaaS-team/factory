package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/spf13/cobra"

	"github.com/KaaS-team/factory/internal/audit"
	"github.com/KaaS-team/factory/internal/manager"
)

func init() {
	register("manage", manageCmd)
}

// auditPort writes manager actions to the factory audit log.
type auditPort struct{}

func (auditPort) Record(ctx context.Context, action, ticket, detail string, err error) {
	audit.Record(ctx, action, detail, ticket, err)
}

func manageCmd() *cobra.Command {
	var every, stall, restart time.Duration
	var once bool
	cmd := &cobra.Command{
		Use:   "manage",
		Short: "Watch active runs: nudge or restart stalled ones, handle failing CI, feed review comments back",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := control(nil)
			if err != nil {
				return err
			}
			p := manager.ControlPort{C: c, Out: cmd.OutOrStdout()}
			m := manager.New(manager.Config{Interval: every, StallAfter: stall, RestartAt: restart}, p, p, auditPort{})
			if once {
				return m.Pass(cmd.Context())
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
			defer stop()
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "factory manage: every %s\n", every)
			if err := m.Run(ctx); ctx.Err() == nil {
				return err
			}
			return nil
		},
	}
	cmd.Flags().DurationVar(&every, "every", 5*time.Minute, "time between passes")
	cmd.Flags().DurationVar(&stall, "stall-after", 30*time.Minute, "no progress for this long: send a nudge")
	cmd.Flags().DurationVar(&restart, "restart-after", 2*time.Hour, "no progress for this long: queue a new attempt (0 = never)")
	cmd.Flags().BoolVar(&once, "once", false, "run one pass and exit")
	return cmd
}
