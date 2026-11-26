package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/KaaS-team/factory/internal/api"
	"github.com/KaaS-team/factory/internal/plan"
	"github.com/KaaS-team/factory/internal/plancmd"
	"github.com/KaaS-team/factory/internal/tracker/github"
)

func init() {
	register("plan", func() *cobra.Command {
		return withFromPRD(plancmd.NewCommand(
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
			plancmd.WithRanker(lazyFiler{}),
		))
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

// withFromPRD adds --from-prd <file>: the PRD becomes an epic and ordered tickets, which then take
// the same path as --spec (checks, waves, --approve).
func withFromPRD(c *cobra.Command) *cobra.Command {
	var prd string
	c.Flags().StringVar(&prd, "from-prd", "", "read a PRD from `file` and draft the epic and ordered tickets from it")
	inner := c.RunE
	c.RunE = func(cmd *cobra.Command, args []string) error {
		if prd == "" {
			return inner(cmd, args)
		}
		if cmd.Flags().Changed("spec") || cmd.Flags().Changed("epic") || len(args) > 0 {
			return errors.New("--from-prd takes no goal, --spec or --epic")
		}
		md, err := os.ReadFile(prd)
		if err != nil {
			return err
		}
		p, err := plan.FromPRD(string(md))
		if err != nil {
			return err
		}
		dir, err := os.MkdirTemp("", "factory-prd-")
		if err != nil {
			return err
		}
		defer func() { _ = os.RemoveAll(dir) }()
		spec := filepath.Join(dir, strings.TrimSuffix(filepath.Base(prd), filepath.Ext(prd))+".md")
		if err := os.WriteFile(spec, []byte(p.Text()), 0o600); err != nil {
			return err
		}
		if err := cmd.Flags().Set("spec", spec); err != nil {
			return err
		}
		return inner(cmd, args)
	}
	return c
}

func (lazyFiler) LinkSub(ctx context.Context, parent, child api.TicketID) error {
	tr, err := planTracker()
	if err != nil {
		return err
	}
	return tr.LinkSub(ctx, parent, child)
}

func (lazyFiler) Ready(ctx context.Context) ([]api.Ticket, error) {
	tr, err := planTracker()
	if err != nil {
		return nil, err
	}
	return tr.Ready(ctx)
}

func (lazyFiler) SetPriority(ctx context.Context, id api.TicketID, priority int) error {
	tr, err := planTracker()
	if err != nil {
		return err
	}
	return tr.SetPriority(ctx, id, priority)
}
