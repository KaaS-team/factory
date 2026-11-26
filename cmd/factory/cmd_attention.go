package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/KaaS-team/factory/internal/attention"
)

func init() {
	register("attention", attentionCmd)
}

func attentionCmd() *cobra.Command {
	var file string
	c := &cobra.Command{
		Use:   "attention",
		Short: "List what needs a person: approvals, refusals, stops and missing proof",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := attention.Open(file)
			if err != nil {
				return err
			}
			items, _ := s.Open(cmd.Context())
			_, err = fmt.Fprint(cmd.OutOrStdout(), attention.Text(items))
			return err
		},
	}
	c.Flags().StringVar(&file, "file", ".factory/attention.json", "attention store file")
	resolve := &cobra.Command{
		Use:   "resolve <id> <approve|reject|retry|dismiss>",
		Short: "Close an attention item with one of its allowed actions",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := attention.Open(file)
			if err != nil {
				return err
			}
			by := os.Getenv("USER")
			if by == "" {
				by = "cli"
			}
			return s.Resolve(args[0], attention.Action(args[1]), by)
		},
	}
	c.AddCommand(resolve)
	return c
}
