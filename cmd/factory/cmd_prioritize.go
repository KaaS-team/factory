package main

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/KaaS-team/factory/internal/backlog"
)

func init() { register("prioritize", prioritizeCmd) }

func prioritizeCmd() *cobra.Command {
	var method string
	var approve, asJSON bool
	c := &cobra.Command{
		Use:   "prioritize",
		Short: "Rank the ready issues by RICE or WSJF; pickup follows the stored rank",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if method == "" {
				l, err := loadConfig(nil)
				if err != nil {
					return err
				}
				method = l.Config.Prioritize.Method
			}
			m, err := backlog.ParseMethod(method)
			if err != nil {
				return err
			}
			p, err := backlogPort()
			if err != nil {
				return err
			}
			rs, err := backlog.Prioritize(cmd.Context(), p, m, approve)
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			if asJSON {
				return json.NewEncoder(w).Encode(rs)
			}
			for _, r := range rs {
				score := "unscored"
				if r.Scored {
					score = fmt.Sprintf("%.2f", r.Score)
				}
				_, _ = fmt.Fprintf(w, "%d. #%s %s (%s)\n", r.Rank, r.ID, r.Title, score)
			}
			if !approve {
				_, err = fmt.Fprintln(w, "nothing stored; pass --approve to store the ranks")
			}
			return err
		},
	}
	c.Flags().StringVar(&method, "method", "", "`rice` or `wsjf` (default: the prioritize.method setting)")
	c.Flags().BoolVar(&approve, "approve", false, "store the ranks; without it nothing is written")
	c.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return c
}
