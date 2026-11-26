package main

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/KaaS-team/factory/internal/ledger"
)

func init() {
	register("metrics", metricsCmd, noTool("cost is operator detail and never a tool an agent or customer reads"))
}

// metricsCmd reports cost per accepted outcome from the run ledger, the same entries and the same
// cost sum the analytics routes use.
func metricsCmd() *cobra.Command {
	var by, workflow, team, since, until string
	var asJSON bool
	c := &cobra.Command{
		Use:   "metrics",
		Short: "Cost per accepted outcome per workflow or team, or per prompt variant of one workflow",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			f := ledger.Filter{Team: team}
			var err error
			if f.Since, err = day(since); err != nil {
				return fmt.Errorf("--since: %w", err)
			}
			if f.Until, err = day(until); err != nil {
				return fmt.Errorf("--until: %w", err)
			}
			key := ledger.ByWorkflow
			switch {
			case workflow != "":
				key = ledger.ByVariant
			case by == "team":
				key = ledger.ByTeam
			case by != "workflow":
				return fmt.Errorf("--by must be workflow or team")
			}
			all, err := ledger.Entries(".")
			if err != nil {
				return err
			}
			sel := ledger.Select(all, f)
			if workflow != "" {
				kept := sel[:0:0]
				for _, e := range sel {
					if e.Workflow == workflow {
						kept = append(kept, e)
					}
				}
				sel = kept
			}
			rows := ledger.Score(sel, key)
			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"groups": rows})
			}
			_, err = fmt.Fprint(cmd.OutOrStdout(), ledger.Text(rows))
			return err
		},
	}
	c.Flags().StringVar(&by, "by", "workflow", "group by workflow or team")
	c.Flags().StringVar(&workflow, "workflow", "", "compare the prompt variants of this workflow")
	c.Flags().StringVar(&team, "team", "", "only this team")
	c.Flags().StringVar(&since, "since", "", "window start, RFC 3339 or YYYY-MM-DD (inclusive)")
	c.Flags().StringVar(&until, "until", "", "window end, RFC 3339 or YYYY-MM-DD (exclusive)")
	c.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return c
}

// day reads an RFC 3339 time or a YYYY-MM-DD day (UTC); empty is the zero time.
func day(s string) (t time.Time, err error) {
	if s == "" {
		return t, nil
	}
	if t, err = time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	return time.Parse(time.DateOnly, s)
}
