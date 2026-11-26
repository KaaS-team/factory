package main

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/KaaS-team/factory/internal/api"
	"github.com/KaaS-team/factory/internal/plancmd"
	"github.com/KaaS-team/factory/internal/tracker/github"
)

func init() {
	register("plan", func() *cobra.Command {
		return plancmd.NewCommand(
			plancmd.WithSpecFunc(plancmd.LocalSpec("claude", ".")),
			plancmd.WithSpecFactory(func(dir string) plancmd.SpecFunc { return plancmd.LocalSpec("claude", dir) }),
			plancmd.WithEpic(func(ctx context.Context, n int) (string, error) {
				tr, err := planTracker()
				if err != nil {
					return "", err
				}
				return github.Epic(tr)(ctx, n)
			}),
			plancmd.WithFiler(lazyFiler{}),
		)
	})
}

// lazyFiler builds the tracker on first use, so `factory plan` without --approve needs no token.
type lazyFiler struct{}

func (lazyFiler) Find(ctx context.Context, title string) (api.TicketID, bool, error) {
	tr, err := planTracker()
	if err != nil {
		return "", false, err
	}
	return tr.Find(ctx, title)
}

func (lazyFiler) Create(ctx context.Context, t api.Ticket) (api.TicketID, error) {
	tr, err := planTracker()
	if err != nil {
		return "", err
	}
	return tr.Create(ctx, t)
}
