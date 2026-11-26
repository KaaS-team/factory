package main

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/KaaS-team/factory/internal/meter"
	"github.com/KaaS-team/factory/internal/report"
)

func init() {
	register("report", func() *cobra.Command {
		return report.NewCommand(report.WithSource(func(context.Context) (report.Source, error) {
			st, err := openStore()
			if err != nil {
				return nil, err
			}
			src := report.StoreSource{Store: st}
			if tr, err := planTracker(); err == nil {
				src.Ledger = meter.Ledger{Thread: tr}
			}
			return src, nil
		}))
	})
}
