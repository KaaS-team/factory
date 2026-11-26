package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

func init() {
	register("config", configCmd, withTool("explain_config"))
}

func configCmd() *cobra.Command {
	cfg := &cobra.Command{Use: "config", Short: "Read resolved configuration"}
	cfg.AddCommand(&cobra.Command{
		Use:   "get <key>",
		Short: "Print the resolved value of a key",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			l, err := loadConfig(nil)
			if err != nil {
				return err
			}
			v, ok := l.Config.Get(args[0])
			if !ok {
				return fmt.Errorf("unknown key %q", args[0])
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), v)
			return nil
		},
	}, &cobra.Command{
		Use:   "explain <key>",
		Short: "Print which layer set a key and which layers it overrode",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			l, err := loadConfig(nil)
			if err != nil {
				return err
			}
			ex, err := l.Explain(args[0])
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			_, _ = fmt.Fprintln(out, ex)
			for _, a := range ex.Tried {
				src := a.Source
				if src == "" {
					src = "-"
				}
				_, _ = fmt.Fprintf(out, "  %s\t%v\t%s\n", a.Layer, a.Value, src)
			}
			return nil
		},
	})
	return cfg
}
