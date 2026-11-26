package spend

import (
	"context"
	"strings"
	"time"

	"github.com/KaaS-team/factory/ee/contract"
	"github.com/KaaS-team/factory/internal/api"
	"github.com/KaaS-team/factory/internal/ledger"
)

type (
	ledgerTicketT = contract.LedgerTicket
	ledgerAgentT  = contract.LedgerAgent
	ledgerPR      = contract.LedgerPR
	ledgerTokens  = contract.LedgerTokens
)

// RunEnd is what the core knows when a run ends.
type RunEnd struct {
	Session api.Session
	Brief   api.Brief
	Team    string
	Source  string // ticket source: github, linear, jira ...; empty means github
	Harness string // empty means claude-code
	Status  string // open, merged, failed, cancelled or abandoned
	Class   string // failure class, empty on success
	Usage   api.Usage
	PR      int
	EndedAt time.Time
}

// Entry builds the ledger line for r. A zero Usage.CostUSD is unknown, so cost_usd is left out
// rather than written as 0 (api.Usage: "0 = unknown, never guessed").
func (r RunEnd) Entry() ledger.Entry {
	e := ledger.Entry{
		Schema:       "kaas.run/v1",
		RunID:        r.Session.Key,
		Repo:         r.Brief.Repo,
		Team:         r.Team,
		Ticket:       ledgerTicket(r),
		Agent:        ledgerAgent(r),
		StartedAt:    r.Session.StartedAt,
		Status:       r.Status,
		FailureClass: r.Class,
	}
	if e.Team == "" {
		e.Team, _, _ = strings.Cut(r.Brief.Repo, "/")
	}
	if !r.EndedAt.IsZero() {
		end := r.EndedAt
		e.EndedAt = &end
	}
	if r.PR > 0 {
		e.PR = &ledgerPR{Number: &r.PR}
	}
	if u := r.Usage; u.InputTokens+u.OutputTokens+u.CacheReadTokens+u.CacheWriteTokens > 0 {
		e.Tokens = &ledgerTokens{Input: &u.InputTokens, Output: &u.OutputTokens, CacheRead: &u.CacheReadTokens, CacheWrite: &u.CacheWriteTokens}
	}
	if r.Usage.CostUSD > 0 {
		c := r.Usage.CostUSD
		e.CostUSD = &c
	}
	return e
}

// EndRun writes the ledger line for a finished run and, when exp is not nil, exports its metrics.
// This is the one call that ends a run in the ledger.
func EndRun(ctx context.Context, root string, exp *ledger.Exporter, r RunEnd) error {
	e := r.Entry()
	if err := ledger.Append(root, e); err != nil {
		return err
	}
	if exp != nil {
		exp.Export(ctx, e)
	}
	return nil
}

func ledgerTicket(r RunEnd) ledgerTicketT {
	src := r.Source
	if src == "" {
		src = "github"
	}
	return ledgerTicketT{Source: src, ID: string(r.Session.Ticket)}
}

func ledgerAgent(r RunEnd) ledgerAgentT {
	h := r.Harness
	if h == "" {
		h = "claude-code"
	}
	return ledgerAgentT{Harness: h, Model: r.Brief.Model, Worker: r.Session.Dispatcher}
}
