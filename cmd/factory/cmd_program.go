package main

import (
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/KaaS-team/factory/internal/program"
)

func init() {
	register("program", func() *cobra.Command {
		var run string
		c := &cobra.Command{
			Use:   "program <epic>",
			Short: "Run an epic as a program: a tech lead dispatches worker tickets and adjusts the plan",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				n, err := strconv.Atoi(args[0])
				if err != nil || n <= 0 {
					return fmt.Errorf("program: %q is not an epic issue number", args[0])
				}
				ctx := cmd.Context()
				d, err := epicPlan(ctx, n)
				if err != nil {
					return err
				}
				var tasks []program.Task
				for _, t := range d.Tickets {
					tasks = append(tasks, program.Task{ID: t.ID, Title: t.Title, Depends: t.Depends})
				}
				if run == "" {
					run = "program-" + args[0]
				}
				r, err := programRunner(ctx, "#"+args[0])
				if err != nil {
					return err
				}
				if err := r.Start(ctx, run, "#"+args[0], tasks); err != nil {
					return fmt.Errorf("%w (resume with: factory resume %s)", err, run)
				}
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "program %s finished\n", run)
				return nil
			},
		}
		c.Flags().StringVar(&run, "run", "", "the run id (default program-<epic>)")
		return c
	}, noTool("a program is a long local session that files and dispatches tickets itself; an MCP client uses create_task and dispatch"))
}
