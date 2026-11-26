package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/KaaS-team/factory/ee/contract"
	"github.com/KaaS-team/factory/internal/api"
	"github.com/KaaS-team/factory/internal/plan"
	"github.com/KaaS-team/factory/internal/plancmd"
	"github.com/KaaS-team/factory/internal/program"
	"github.com/KaaS-team/factory/internal/runstate"
	"github.com/KaaS-team/factory/internal/stack"
)

const (
	runsDir    = ".factory/runs"
	ledgerFile = ".factory/ledger/runs.jsonl"
)

func runStore() runstate.Store { return runstate.Store{Root: runsDir} }

// epicPlan reads epic n through `factory plan --epic n --json`, the one resolver for epic plans.
func epicPlan(ctx context.Context, n int) (plancmd.DAG, error) {
	out, err := runSub(ctx, "plan", "--epic", strconv.Itoa(n), "--json")
	if err != nil {
		return plancmd.DAG{}, fmt.Errorf("read the plan of epic #%d: %w", n, err)
	}
	var d plancmd.DAG
	if err := json.Unmarshal([]byte(out), &d); err != nil {
		return d, fmt.Errorf("read the plan of epic #%d: %w", n, err)
	}
	if len(d.Problems) > 0 {
		return d, fmt.Errorf("the plan of epic #%d has problems: %v", n, d.Problems)
	}
	return d, nil
}

func dagPlan(d plancmd.DAG) plan.Plan {
	var p plan.Plan
	for _, t := range d.Tickets {
		p.Tickets = append(p.Tickets, plan.Ticket{ID: t.ID, Title: t.Title, Files: t.Files, Depends: t.Depends})
	}
	return p
}

// appendLedger adds one line to the run ledger.
func appendLedger(e contract.RunLedgerEntry) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(ledgerFile), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(ledgerFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// stackRunner builds the real stack runner for the configured repository.
func stackRunner() (stack.Runner, error) {
	l, err := loadConfig(nil)
	if err != nil {
		return stack.Runner{}, err
	}
	g, err := newGitHubConn(l.Config)
	if err != nil {
		return stack.Runner{}, err
	}
	c, err := g.client()
	if err != nil {
		return stack.Runner{}, err
	}
	return stack.Runner{Host: stack.GitHubHost{Client: c, Owner: g.owner, Repo: g.repo, Dir: "."}, Store: runStore(),
		Repo: g.owner + "/" + g.repo, Ledger: appendLedger}, nil
}

// controlWorker runs a task as a factory ticket: it files the ticket, dispatches it and waits for
// the board to show an end phase.
type controlWorker struct {
	ctl  api.Control
	epic string
	poll time.Duration
}

func (w controlWorker) Run(ctx context.Context, t program.Task) (program.Result, error) {
	id, err := w.ctl.CreateTicket(ctx, t.Title, "Part of epic "+w.epic+".\n\n"+t.Title)
	if err != nil {
		return program.Result{}, err
	}
	if err := w.ctl.Dispatch(ctx, id); err != nil {
		return program.Result{}, err
	}
	for {
		b, err := w.ctl.Board(ctx)
		if err != nil {
			return program.Result{}, err
		}
		for _, s := range b.Slots {
			if s.Ticket.ID != id {
				continue
			}
			switch s.Phase {
			case api.PhaseDone, api.PhaseQueued, api.PhaseMerged:
				return program.Result{OK: true, Summary: fmt.Sprintf("ticket %s reached %s", id, s.Phase)}, nil
			case api.PhaseNeedsHuman, api.PhaseCanceled:
				return program.Result{OK: false, Summary: fmt.Sprintf("ticket %s stopped at %s", id, s.Phase)}, nil
			}
		}
		select {
		case <-ctx.Done():
			return program.Result{}, ctx.Err()
		case <-time.After(w.poll):
		}
	}
}

// programRunner builds the runner for a program run.
func programRunner(ctx context.Context, epic string) (program.Runner, error) {
	c, err := control(nil)
	if err != nil {
		return program.Runner{}, err
	}
	return program.Runner{
		Worker: controlWorker{ctl: c, epic: epic, poll: 5 * time.Second},
		Lead:   program.ModelLead{Ask: program.ClaudeAsk("claude", ".")},
		Store:  runStore(),
	}, nil
}
