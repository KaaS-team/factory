package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

func init() {
	register("resume", func() *cobra.Command {
		return &cobra.Command{
			Use:   "resume <run>",
			Short: "Continue a stack or program run from its state on disk after a crash",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				ctx := cmd.Context()
				st, err := runStore().Load(args[0])
				if err != nil {
					return err
				}
				switch st.Kind {
				case "stack":
					r, err := stackRunner()
					if err != nil {
						return err
					}
					if err := r.Resume(ctx, args[0]); err != nil {
						return err
					}
				case "program":
					r, err := programRunner(ctx, st.Epic)
					if err != nil {
						return err
					}
					if err := r.Resume(ctx, args[0]); err != nil {
						return err
					}
				default:
					return fmt.Errorf("resume: run %s has unknown kind %q", args[0], st.Kind)
				}
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "resumed %s (%s)\n", args[0], st.Kind)
				return nil
			},
		}
	}, noTool("it continues a local run from files in this checkout"))
}
