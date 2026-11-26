package main

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/KaaS-team/factory/internal/firstrun"
	"github.com/KaaS-team/factory/internal/tui"
)

func init() {
	register("ready", func() *cobra.Command {
		return tui.NewReadyCommand(readyFuncs)
	}, noTool("an interactive terminal screen; the first_run tool and `first-run --ready` run the same checks"))
}

// readyFuncs runs the first-run readiness checks for the ready screen, with branch protection read
// from the config when a token is there.
func readyFuncs(cmd *cobra.Command, dir string) tui.ReadyFuncs {
	opts := func(ctx context.Context) firstrun.ReadyOptions {
		o := firstrun.ReadyOptions{Dir: dir}
		if p, err := firstRunProtection(ctx); err == nil {
			o.Protected = p
		}
		return o
	}
	return tui.ReadyFuncs{
		Check: func(ctx context.Context) (tui.Readiness, error) {
			return readinessView(firstrun.CheckReady(ctx, opts(ctx)), nil), nil
		},
		Fix: func(ctx context.Context) (tui.Readiness, error) {
			fixed, r, err := firstrun.FixReady(ctx, opts(ctx))
			return readinessView(r, fixed), err
		},
	}
}

// readinessView turns a firstrun report into what the screen shows.
func readinessView(r firstrun.Ready, fixed []firstrun.Fixed) tui.Readiness {
	v := tui.Readiness{Dir: r.Dir}
	for _, c := range r.Checks {
		v.Checks = append(v.Checks, tui.ReadyCheck{Name: c.Name, Status: tui.CheckStatus(c.Status), Detail: c.Detail, Fix: c.Fix})
	}
	for _, f := range fixed {
		v.Fixed = append(v.Fixed, f.Name+": "+f.What)
	}
	return v
}
