package main

import (
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/KaaS-team/factory/internal/stack"
)

func init() {
	register("stack", func() *cobra.Command {
		var run, trunk, prefix string
		c := &cobra.Command{
			Use:   "stack <epic>",
			Short: "Open an epic's plan as a stack of branches and PRs, each based on the one below",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				n, err := strconv.Atoi(args[0])
				if err != nil || n <= 0 {
					return fmt.Errorf("stack: %q is not an epic issue number", args[0])
				}
				ctx := cmd.Context()
				d, err := epicPlan(ctx, n)
				if err != nil {
					return err
				}
				s, err := stack.New(dagPlan(d), trunk, prefix)
				if err != nil {
					return err
				}
				r, err := stackRunner()
				if err != nil {
					return err
				}
				if run == "" {
					run = "stack-" + args[0]
				}
				if err := r.Start(ctx, run, "#"+args[0], s); err != nil {
					return fmt.Errorf("%w (resume with: factory resume %s)", err, run)
				}
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "stack %s opened %d PRs\n", run, len(s.Entries))
				return nil
			},
		}
		c.Flags().StringVar(&run, "run", "", "the run id (default stack-<epic>)")
		c.Flags().StringVar(&trunk, "trunk", "main", "the branch the bottom PR targets")
		c.Flags().StringVar(&prefix, "prefix", "stack/", "the prefix of each branch name")
		return c
	}, noTool("it pushes branches from a local clone; an MCP client has no clone"))
}
