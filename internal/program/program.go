// Package program runs an epic as a program: a tech-lead session owns the epic, worker tickets
// are dispatched in dependency order, and after each round the lead reads the results and may
// adjust the plan that remains. State lives in runstate, so a program resumes after a crash.
package program

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/KaaS-team/factory/internal/runstate"
)

// Task is one worker ticket.
type Task struct {
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	Depends []string `json:"depends,omitempty"`
}

// Result is what a worker reported.
type Result struct {
	Task    string `json:"task"`
	OK      bool   `json:"ok"`
	Summary string `json:"summary"`
}

// Decision is the lead's change to the plan that remains. Drop only reaches tasks that have not
// been dispatched. Add may depend on any existing task.
type Decision struct {
	Add  []Task   `json:"add,omitempty"`
	Drop []string `json:"drop,omitempty"`
	Note string   `json:"note,omitempty"`
}

// Worker runs one ticket to a result. The real one dispatches through api.Control.
type Worker interface {
	Run(ctx context.Context, t Task) (Result, error)
}

// Lead is the tech-lead session. Review sees the epic, every result so far and the tasks still
// to run, and returns how to change the remainder.
type Lead interface {
	Review(ctx context.Context, epic string, results []Result, remaining []Task) (Decision, error)
}

// State is the program's durable record.
type State struct {
	Tasks   []Task   `json:"tasks"`
	Results []Result `json:"results"`
	Dropped []string `json:"dropped,omitempty"`
	Notes   []string `json:"notes,omitempty"`
	Round   int      `json:"round"`
}

const doc = "program"

// Runner runs programs.
type Runner struct {
	Worker    Worker
	Lead      Lead
	Store     runstate.Store
	MaxRounds int // a ceiling on lead reviews; default 20
}

// ErrBlocked means tasks are left that cannot run because a task they need failed.
var ErrBlocked = errors.New("program: tasks are blocked by a failed task")

// Start records the program and runs it.
func (r Runner) Start(ctx context.Context, runID, epic string, tasks []Task) error {
	if err := check(tasks); err != nil {
		return err
	}
	st := runstate.State{ID: runID, Kind: "program", Epic: epic}
	if err := st.Put(doc, State{Tasks: tasks}); err != nil {
		return err
	}
	if err := r.Store.Create(st); err != nil {
		return err
	}
	return r.Resume(ctx, runID)
}

// Resume continues a program. A task with a recorded result is not run again.
func (r Runner) Resume(ctx context.Context, runID string) error {
	rs, err := r.Store.Load(runID)
	if err != nil {
		return err
	}
	var s State
	ok, err := rs.Get(doc, &s)
	if err != nil {
		return fmt.Errorf("program: read run %s: %w", runID, err)
	}
	if !ok {
		return fmt.Errorf("program: run %s has no program state", runID)
	}
	max := r.MaxRounds
	if max <= 0 {
		max = 20
	}
	save := func() error {
		if err := rs.Put(doc, s); err != nil {
			return err
		}
		return r.Store.Save(rs)
	}
	for s.Round < max {
		if err := ctx.Err(); err != nil {
			return err
		}
		ready := s.ready()
		if len(ready) == 0 {
			break
		}
		results := make([]Result, len(ready))
		errs := make([]error, len(ready))
		var wg sync.WaitGroup
		for i, t := range ready {
			wg.Add(1)
			go func() {
				defer wg.Done()
				res, err := r.Worker.Run(ctx, t)
				res.Task = t.ID
				results[i], errs[i] = res, err
			}()
		}
		wg.Wait()
		if err := ctx.Err(); err != nil {
			return err
		}
		for i, err := range errs {
			if err != nil {
				results[i] = Result{Task: ready[i].ID, OK: false, Summary: "the worker could not run this task"}
			}
		}
		s.Results = append(s.Results, results...)
		s.Round++
		if err := save(); err != nil {
			return err
		}
		d, err := r.Lead.Review(ctx, rs.Epic, s.Results, s.remaining())
		if err != nil {
			return fmt.Errorf("program: the lead could not review round %d: %w", s.Round, err)
		}
		if err := check(append(append([]Task{}, s.Tasks...), d.Add...)); err != nil {
			// A bad decision is recorded and ignored. The program keeps the plan it had.
			s.Notes = append(s.Notes, "the lead's change was refused: "+err.Error())
		} else if err := s.apply(d); err != nil {
			s.Notes = append(s.Notes, "the lead's change was refused: "+err.Error())
		}
		if d.Note != "" {
			s.Notes = append(s.Notes, d.Note)
		}
		if err := save(); err != nil {
			return err
		}
	}
	if s.Round >= max && len(s.ready()) > 0 {
		return fmt.Errorf("program: stopped after %d rounds with tasks left", max)
	}
	if b := s.blocked(); len(b) > 0 {
		return fmt.Errorf("%w: %s", ErrBlocked, strings.Join(b, ", "))
	}
	return nil
}

func (s State) dispatched() map[string]bool {
	m := map[string]bool{}
	for _, r := range s.Results {
		m[r.Task] = true
	}
	return m
}

func (s State) dropped() map[string]bool {
	m := map[string]bool{}
	for _, d := range s.Dropped {
		m[d] = true
	}
	return m
}

// satisfied is the set of tasks that count as finished for their dependents: passed or dropped.
func (s State) satisfied() map[string]bool {
	m := s.dropped()
	for _, r := range s.Results {
		if r.OK {
			m[r.Task] = true
		}
	}
	return m
}

// remaining lists tasks that have no result and are not dropped.
func (s State) remaining() []Task {
	done, drop := s.dispatched(), s.dropped()
	var out []Task
	for _, t := range s.Tasks {
		if !done[t.ID] && !drop[t.ID] {
			out = append(out, t)
		}
	}
	return out
}

// ready lists remaining tasks whose dependencies are all satisfied, in plan order.
func (s State) ready() []Task {
	sat := s.satisfied()
	var out []Task
	for _, t := range s.remaining() {
		ok := true
		for _, d := range t.Depends {
			ok = ok && sat[d]
		}
		if ok {
			out = append(out, t)
		}
	}
	return out
}

func (s State) blocked() []string {
	var out []string
	for _, t := range s.remaining() {
		out = append(out, t.ID)
	}
	sort.Strings(out)
	return out
}

func (s *State) apply(d Decision) error {
	done := s.dispatched()
	for _, id := range d.Drop {
		if done[id] {
			return fmt.Errorf("task %s already ran and cannot be dropped", id)
		}
	}
	s.Dropped = append(s.Dropped, d.Drop...)
	s.Tasks = append(s.Tasks, d.Add...)
	return nil
}

// check validates a task set: unique ids, known dependencies and no cycle. An empty id or title
// is refused.
func check(tasks []Task) error {
	seen := map[string]bool{}
	for _, t := range tasks {
		if strings.TrimSpace(t.ID) == "" || strings.TrimSpace(t.Title) == "" {
			return errors.New("every task needs an id and a title")
		}
		if seen[t.ID] {
			return fmt.Errorf("task %s is listed twice", t.ID)
		}
		seen[t.ID] = true
	}
	for _, t := range tasks {
		for _, d := range t.Depends {
			if !seen[d] {
				return fmt.Errorf("task %s needs %s, which is no task", t.ID, d)
			}
		}
	}
	state := map[string]int{}
	by := map[string]Task{}
	for _, t := range tasks {
		by[t.ID] = t
	}
	var visit func(id string) bool
	visit = func(id string) bool {
		switch state[id] {
		case 1:
			return true
		case 2:
			return false
		}
		state[id] = 1
		for _, d := range by[id].Depends {
			if visit(d) {
				return true
			}
		}
		state[id] = 2
		return false
	}
	for _, t := range tasks {
		if visit(t.ID) {
			return fmt.Errorf("task %s is in a dependency cycle", t.ID)
		}
	}
	return nil
}
