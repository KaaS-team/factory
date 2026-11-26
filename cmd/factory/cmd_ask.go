package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/KaaS-team/factory/internal/api"
	"github.com/KaaS-team/factory/internal/ask"
	"github.com/KaaS-team/factory/internal/ledger"
	"github.com/KaaS-team/factory/internal/logs"
)

func init() {
	register("ask", askCmd)
}

func askCmd() *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   "ask <ticket> <question...>",
		Short: "Ask a read-only question about a running or finished ticket, answered from its ledger and transcript",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := askTicket(cmd.Context(), api.TicketID(args[0]), strings.Join(args[1:], " "))
			if err != nil {
				return err
			}
			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(a)
			}
			_, err = fmt.Fprint(cmd.OutOrStdout(), ask.Text(a))
			return err
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "print the answer as JSON")
	return c
}

// askTicket gathers what the run left behind and answers from it. It only reads: the ledger file,
// the control's past events and the run log. A source that is missing is skipped.
func askTicket(ctx context.Context, id api.TicketID, question string) (ask.Answer, error) {
	s := ask.Sources{Ticket: id}
	var err error
	if s.Ledger, err = ledger.Entries("."); err != nil {
		return ask.Answer{}, err
	}
	if c, cerr := control(nil); cerr == nil {
		if h, ok := c.(api.Historian); ok {
			s.Events, _ = h.History(ctx, id)
		}
	}
	if src, serr := logBackends().Source(ctx, id); serr == nil {
		s.Transcript, err = readTranscript(ctx, src)
		if err != nil && !errors.Is(err, context.Canceled) {
			return ask.Answer{}, err
		}
	}
	return ask.Ask(question, s), nil
}

// readTranscript reads every page that exists now, once, without following.
func readTranscript(ctx context.Context, src logs.Source) ([]string, error) {
	var out []string
	cursor := ""
	for {
		p, err := src.Poll(ctx, cursor)
		if err != nil {
			return out, err
		}
		for _, e := range p.Events {
			out = append(out, e.Text)
		}
		if !p.More || p.Next == cursor {
			return out, nil
		}
		cursor = p.Next
	}
}
