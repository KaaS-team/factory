//go:build ee

// SPDX-License-Identifier: LicenseRef-KaaS-EE

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	eeaudit "github.com/KaaS-team/factory/ee/audit"
	"github.com/KaaS-team/factory/ee/contract"
	"github.com/KaaS-team/factory/internal/audit"
)

// chainPath is the hash-chained log: FACTORY_AUDIT_CHAIN, or chain.jsonl in the audit directory.
func chainPath() string {
	if p := os.Getenv("FACTORY_AUDIT_CHAIN"); p != "" {
		return p
	}
	return filepath.Join(audit.Dir(), "chain.jsonl")
}

func init() {
	audit.RegisterSink(func() (audit.Sink, error) {
		return eeaudit.Sink{Log: eeaudit.Open(chainPath())}, nil
	})
	audit.RegisterSubcommand(auditVerifyCmd)
	audit.RegisterSubcommand(auditSearchCmd)
	audit.RegisterSubcommand(auditForwardCmd)
}

func auditVerifyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "verify",
		Short: "Check the hash chain of the audit log; fails on a changed or deleted event",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if err := eeaudit.Open(chainPath()).Verify(c.Context()); err != nil {
				return err
			}
			_, err := fmt.Fprintln(c.OutOrStdout(), "audit chain intact")
			return err
		},
	}
}

func auditSearchCmd() *cobra.Command {
	var q contract.AuditQuery
	var action, outcome, resource, since, until string
	var limit int
	cmd := &cobra.Command{
		Use:   "search",
		Short: "Print chained audit events that match the filters, as jsonl",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			var err error
			if q.Since, err = parseTime(since); err != nil {
				return err
			}
			if q.Until, err = parseTime(until); err != nil {
				return err
			}
			q.Action, q.Outcome = contract.Action(action), contract.Outcome(outcome)
			log := eeaudit.Open(chainPath())
			evs, err := log.Search(c.Context(), q)
			if err != nil {
				return err
			}
			enc := json.NewEncoder(c.OutOrStdout())
			n := 0
			for _, e := range evs {
				if resource != "" && !strings.HasPrefix(e.Resource, resource) {
					continue
				}
				if limit > 0 && n == limit {
					break
				}
				n++
				if err := enc.Encode(e); err != nil {
					return err
				}
			}
			eeaudit.Emit(c.Context(), log, contract.AuditEvent{Actor: actorID(), Action: contract.ActionAuditRead, Resource: "org",
				Outcome: contract.OutcomeAllow, Data: map[string]string{"hits": fmt.Sprint(n)}})
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&q.Actor, "actor", "", "only events by this actor")
	f.StringVar(&action, "action", "", "only this action, such as run.start")
	f.StringVar(&outcome, "outcome", "", "allow, deny or error")
	f.StringVar(&resource, "resource", "", "only resources that start with this")
	f.StringVar(&since, "since", "", "from this RFC 3339 time, inclusive")
	f.StringVar(&until, "until", "", "before this RFC 3339 time")
	f.IntVar(&limit, "limit", 0, "at most this many events; 0 is all")
	return cmd
}

func parseTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return t, fmt.Errorf("audit: %q is not an RFC 3339 time", s)
	}
	return t, nil
}

func auditForwardCmd() *cobra.Command {
	var to string
	var after uint64
	cmd := &cobra.Command{
		Use:   "forward",
		Short: "Send chained events to a SIEM: file:<path>, splunk:<url> or webhook:<url>",
		Long: "Send chained events to a SIEM. The token for splunk and webhook is read from " +
			"FACTORY_AUDIT_TOKEN, never from a flag. Use --after to send only newer events.",
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			x, err := exporterFor(to, os.Getenv("FACTORY_AUDIT_TOKEN"))
			if err != nil {
				return err
			}
			log := eeaudit.Open(chainPath())
			evs, err := log.Search(c.Context(), contract.AuditQuery{})
			if err != nil {
				return err
			}
			var send []contract.AuditEvent
			for _, e := range evs {
				if e.Seq > after {
					send = append(send, e)
				}
			}
			out := contract.OutcomeAllow
			if err = exportAll(c.Context(), x, send); err != nil {
				out = contract.OutcomeError
			}
			eeaudit.Emit(c.Context(), log, contract.AuditEvent{Actor: actorID(), Action: contract.ActionAuditExport, Resource: "org",
				Outcome: out, Data: map[string]string{"events": fmt.Sprint(len(send))}})
			if err == nil {
				_, err = fmt.Fprintf(c.OutOrStdout(), "sent %d events\n", len(send))
			}
			return err
		},
	}
	cmd.Flags().StringVar(&to, "to", "", "file:<path>, splunk:<url> or webhook:<url>")
	cmd.Flags().Uint64Var(&after, "after", 0, "send only events with a higher seq than this")
	return cmd
}

func exportAll(ctx context.Context, x eeaudit.Exporter, evs []contract.AuditEvent) error {
	if len(evs) == 0 {
		return nil
	}
	return x.Export(ctx, evs)
}

func exporterFor(to, token string) (eeaudit.Exporter, error) {
	kind, target, _ := strings.Cut(to, ":")
	switch {
	case kind == "file" && target != "":
		return eeaudit.JSONLFile{Path: target}, nil
	case kind == "splunk" && target != "":
		return eeaudit.SplunkHEC{URL: target, Token: token}, nil
	case kind == "webhook" && target != "":
		return eeaudit.Webhook{URL: target, Token: token}, nil
	}
	return nil, fmt.Errorf("audit: --to must be file:<path>, splunk:<url> or webhook:<url>")
}
