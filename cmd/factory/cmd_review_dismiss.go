package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/KaaS-team/factory/internal/reviewlearn"
)

func init() {
	register("review-dismiss", reviewDismissCmd, noTool("gap: dismissals are recorded by people at the terminal; an MCP tool is owed"))
}

// reviewDismissCmd records a review finding a human judged wrong. Repeated dismissals of one kind
// become a rule in .factory/review-rules.yml.
func reviewDismissCmd() *cobra.Command {
	var file, reason, reword string
	c := &cobra.Command{
		Use:   "review-dismiss <kind>",
		Short: "Record a review finding as wrong so the review stops repeating it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			l := reviewlearn.Learner{Root: "."}
			if err := l.Record(reviewlearn.Dismissal{Kind: args[0], File: file, Reason: reason, Reword: reword}); err != nil {
				return err
			}
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "recorded; rules live in %s\n", reviewlearn.DefaultPath)
			return err
		},
	}
	c.Flags().StringVar(&file, "file", "", "file the finding was on")
	c.Flags().StringVar(&reason, "reason", "", "why the finding was wrong")
	c.Flags().StringVar(&reword, "reword", "", "the wording you wanted instead")
	return c
}
