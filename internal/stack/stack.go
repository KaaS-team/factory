// Package stack turns an ordered plan into a stack of branches and pull requests, each based on
// the one below. When a lower branch moves, the ones above are rebased. When the bottom merges,
// the next one is retargeted to trunk. Progress lives in a runstate directory, so a run that
// crashed continues with Resume.
package stack

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/KaaS-team/factory/ee/contract"
	"github.com/KaaS-team/factory/internal/plan"
	"github.com/KaaS-team/factory/internal/runstate"
)

// Entry is one branch and pull request in the stack. Position counts from 1 at the bottom.
type Entry struct {
	Ticket   string `json:"ticket"`
	Title    string `json:"title"`
	Branch   string `json:"branch"`
	Base     string `json:"base"`      // the branch the PR targets now
	BaseHead string `json:"base_head"` // the tip of Base when this branch was last built on it
	Head     string `json:"head"`
	Position int    `json:"position"`
	PR       int    `json:"pr,omitempty"`
	Merged   bool   `json:"merged,omitempty"`
}

// Stack is the ordered entries, bottom first, and the trunk the bottom targets.
type Stack struct {
	Trunk   string  `json:"trunk"`
	Entries []Entry `json:"entries"`
}

// New orders the plan into one stack. Dependencies come first. Tickets in the same wave keep
// plan order. Branches are named prefix + ticket id.
func New(p plan.Plan, trunk, prefix string) (Stack, error) {
	if trunk == "" {
		return Stack{}, errors.New("stack: no trunk branch")
	}
	if len(p.Tickets) == 0 {
		return Stack{}, errors.New("stack: the plan has no tickets")
	}
	waves, err := p.Waves()
	if err != nil {
		return Stack{}, err
	}
	title := map[string]string{}
	for _, t := range p.Tickets {
		title[t.ID] = t.Title
	}
	s := Stack{Trunk: trunk}
	base := trunk
	for _, w := range waves {
		for _, id := range w {
			e := Entry{Ticket: id, Title: title[id], Branch: prefix + id, Base: base, Position: len(s.Entries) + 1}
			s.Entries = append(s.Entries, e)
			base = e.Branch
		}
	}
	return s, nil
}

// Host is what the stack needs from GitHub and git. A real one is GitHubHost; tests use a fake.
type Host interface {
	// OpenPR opens a pull request from head onto base and returns its number.
	OpenPR(ctx context.Context, head, base, title string) (int, error)
	// Retarget changes the base of an open pull request.
	Retarget(ctx context.Context, pr int, base string) error
	// Merged reports whether the pull request has merged.
	Merged(ctx context.Context, pr int) (bool, error)
	// Head returns the tip commit of a branch as the remote has it.
	Head(ctx context.Context, branch string) (string, error)
	// Rebase moves branch, whose commits start after oldBase, onto onto, and returns the new tip.
	Rebase(ctx context.Context, branch, onto, oldBase string) (string, error)
}

// Runner drives a stack and keeps its state in Store.
type Runner struct {
	Host   Host
	Store  runstate.Store
	Repo   string // owner/name, for the ledger
	Ledger func(contract.RunLedgerEntry) error
	Now    func() time.Time
}

const stackDoc = "stack"

func (r Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now().UTC()
}

// Start records a new run and opens every PR in order.
func (r Runner) Start(ctx context.Context, runID, epic string, s Stack) error {
	st := runstate.State{ID: runID, Kind: "stack", Epic: epic}
	if err := st.Put(stackDoc, s); err != nil {
		return err
	}
	if err := r.Store.Create(st); err != nil {
		return err
	}
	return r.Resume(ctx, runID)
}

// Resume does the steps of the run that are not done. It is safe to call after a crash: a PR
// that was already opened is not opened again.
func (r Runner) Resume(ctx context.Context, runID string) error {
	st, s, err := r.load(runID)
	if err != nil {
		return err
	}
	for i := range s.Entries {
		e := &s.Entries[i]
		step := "open:" + e.Ticket
		if st.Done(step) {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if e.BaseHead == "" {
			h, err := r.Host.Head(ctx, e.Base)
			if err != nil {
				return fmt.Errorf("stack: read %s: %w", e.Base, err)
			}
			e.BaseHead = h
		}
		if e.Head == "" {
			h, err := r.Host.Head(ctx, e.Branch)
			if err != nil {
				return fmt.Errorf("stack: read %s: %w", e.Branch, err)
			}
			e.Head = h
		}
		if e.PR == 0 {
			n, err := r.Host.OpenPR(ctx, e.Branch, e.Base, e.Title)
			if err != nil {
				return fmt.Errorf("stack: open PR for %s: %w", e.Ticket, err)
			}
			e.PR = n
		}
		st.MarkDone(step)
		if err := r.save(&st, s); err != nil {
			return err
		}
		if err := r.ledger(runID, *e, "open"); err != nil {
			return err
		}
	}
	return r.Sync(ctx, runID)
}

// Sync reads the stack's PRs and branches and repairs it: a merged entry leaves the stack, an
// entry whose base changed is retargeted, and an entry whose base moved is rebased.
func (r Runner) Sync(ctx context.Context, runID string) error {
	st, s, err := r.load(runID)
	if err != nil {
		return err
	}
	for i := range s.Entries {
		e := &s.Entries[i]
		if e.Merged || e.PR == 0 {
			continue
		}
		ok, err := r.Host.Merged(ctx, e.PR)
		if err != nil {
			return fmt.Errorf("stack: read PR %d: %w", e.PR, err)
		}
		if !ok {
			continue
		}
		e.Merged = true
		if err := r.save(&st, s); err != nil {
			return err
		}
		if err := r.ledger(runID, *e, "merged"); err != nil {
			return err
		}
	}
	// below is the nearest live branch under the entry being fixed; trunk when none is left.
	below := s.Trunk
	for i := range s.Entries {
		e := &s.Entries[i]
		if e.Merged {
			continue
		}
		if e.Base != below {
			if err := r.Host.Retarget(ctx, e.PR, below); err != nil {
				return fmt.Errorf("stack: retarget PR %d to %s: %w", e.PR, below, err)
			}
			e.Base = below
			if err := r.save(&st, s); err != nil {
				return err
			}
		}
		tip, err := r.Host.Head(ctx, below)
		if err != nil {
			return fmt.Errorf("stack: read %s: %w", below, err)
		}
		if tip != e.BaseHead {
			h, err := r.Host.Rebase(ctx, e.Branch, below, e.BaseHead)
			if err != nil {
				return fmt.Errorf("stack: rebase %s onto %s: %w", e.Branch, below, err)
			}
			e.Head, e.BaseHead = h, tip
			if err := r.save(&st, s); err != nil {
				return err
			}
			if err := r.ledger(runID, *e, "open"); err != nil {
				return err
			}
		}
		below = e.Branch
	}
	return nil
}

func (r Runner) load(runID string) (runstate.State, Stack, error) {
	st, err := r.Store.Load(runID)
	if err != nil {
		return st, Stack{}, err
	}
	var s Stack
	ok, err := st.Get(stackDoc, &s)
	if err != nil {
		return st, s, err
	}
	if !ok {
		return st, s, fmt.Errorf("stack: run %s has no stack", runID)
	}
	return st, s, nil
}

func (r Runner) save(st *runstate.State, s Stack) error {
	if err := st.Put(stackDoc, s); err != nil {
		return err
	}
	st.Heads = map[string]string{}
	for _, e := range s.Entries {
		st.Heads[e.Branch] = e.Head
	}
	return r.Store.Save(*st)
}

// ledger writes the entry's line, with its stack_position. Status is open or merged.
func (r Runner) ledger(runID string, e Entry, status string) error {
	if r.Ledger == nil {
		return nil
	}
	pos, n := e.Position, e.PR
	now := r.now()
	le := contract.RunLedgerEntry{
		Schema:    contract.RunLedgerSchema,
		RunID:     runID + "/" + e.Ticket,
		Repo:      r.Repo,
		Ticket:    contract.LedgerTicket{Source: "github", ID: e.Ticket},
		Agent:     contract.LedgerAgent{Harness: "factory"},
		StartedAt: now,
		Status:    status,
		PR:        &contract.LedgerPR{Number: &n, HeadSHA: e.Head, StackPosition: &pos},
	}
	if status == "merged" {
		le.EndedAt = &now
		le.PR.MergedAt = &now
	}
	return r.Ledger(le)
}
