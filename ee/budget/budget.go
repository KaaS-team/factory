//go:build ee

// SPDX-License-Identifier: LicenseRef-KaaS-EE

// Package budget is contract.Budget. It holds money before a run, records the real cost after, and
// refuses a run whose hold would pass any limit that names it. State is one JSON file, so a hold
// and the spend of the period survive a restart.
package budget

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/KaaS-team/factory/ee/contract"
	"github.com/KaaS-team/factory/internal/budget"
)

// Limit is the cap of one key. Period empty means the cap never resets.
type Limit struct {
	Key    contract.BudgetKey
	Micros contract.Micros
	Period contract.Period
}

// FromPolicy turns the effective policy limits into Limits, rounding USD to the nearest micro.
func FromPolicy(ls []contract.BudgetLimit) []Limit {
	out := make([]Limit, len(ls))
	for i, l := range ls {
		out[i] = Limit{Key: contract.BudgetKey{Scope: l.Scope, ID: l.ID}, Micros: Micros(l.USD), Period: l.Period}
	}
	return out
}

// Micros converts US dollars to contract.Micros.
func Micros(usd float64) contract.Micros { return contract.Micros(math.Round(usd * 1e6)) }

// Config builds a Budget.
type Config struct {
	Limits []Limit
	Path   string           // state file; empty keeps state in memory
	Now    func() time.Time // default time.Now
	Audit  contract.Audit   // when set, a refused reserve is recorded as a run.start deny
	Actor  string           // the Principal.ID named on that event
}

type counter struct {
	Spent contract.Micros `json:"spent"`
	Since string          `json:"since,omitempty"` // start of the window Spent counts
}

type state struct {
	Spent map[string]counter              `json:"spent"`
	Open  map[string]contract.Reservation `json:"open"`
	Next  int                             `json:"next"`
}

// Budget implements contract.Budget.
type Budget struct {
	cfg    Config
	limits map[contract.BudgetKey]Limit
	mu     sync.Mutex
	st     state
}

var _ contract.Budget = (*Budget)(nil)

// New returns a Budget and loads its state file when there is one.
func New(cfg Config) (*Budget, error) {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	b := &Budget{cfg: cfg, limits: map[contract.BudgetKey]Limit{},
		st: state{Spent: map[string]counter{}, Open: map[string]contract.Reservation{}}}
	for _, l := range cfg.Limits {
		b.limits[l.Key] = l
	}
	if cfg.Path == "" {
		return b, nil
	}
	data, err := os.ReadFile(filepath.Clean(cfg.Path))
	if errors.Is(err, os.ErrNotExist) {
		return b, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &b.st); err != nil {
		return nil, fmt.Errorf("budget: state file: %w", err)
	}
	if b.st.Spent == nil {
		b.st.Spent = map[string]counter{}
	}
	if b.st.Open == nil {
		b.st.Open = map[string]contract.Reservation{}
	}
	return b, nil
}

func keyName(k contract.BudgetKey) string { return string(k.Scope) + "/" + k.ID }

// window returns the start of the current window of k, "" for a key that never resets.
func (b *Budget) window(k contract.BudgetKey) string {
	p := b.limits[k].Period
	if p == "" {
		return ""
	}
	return budget.Start(budget.Period(p), b.cfg.Now()).Format(time.RFC3339)
}

// spent is the committed amount of k in the current window.
func (b *Budget) spent(k contract.BudgetKey) contract.Micros {
	c := b.st.Spent[keyName(k)]
	if c.Since != b.window(k) {
		return 0
	}
	return c.Spent
}

func (b *Budget) remaining(k contract.BudgetKey) (contract.Micros, bool) {
	l, ok := b.limits[k]
	if !ok {
		return 0, false
	}
	left := l.Micros - b.spent(k)
	for _, r := range b.st.Open {
		if slices.Contains(r.Keys, k) {
			left -= r.Amount
		}
	}
	return left, true
}

func (b *Budget) save() error {
	if b.cfg.Path == "" {
		return nil
	}
	data, err := json.Marshal(b.st)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(b.cfg.Path), 0o750); err != nil {
		return err
	}
	tmp := b.cfg.Path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, b.cfg.Path)
}

// Reserve holds amount on every key, or on none.
func (b *Budget) Reserve(ctx context.Context, keys []contract.BudgetKey, amount contract.Micros) (contract.Reservation, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if amount <= 0 {
		return contract.Reservation{}, errors.New("budget: amount must be above 0")
	}
	for _, k := range keys {
		if left, ok := b.remaining(k); ok && amount > left {
			b.refused(ctx, k)
			return contract.Reservation{}, fmt.Errorf("%w: %s/%s", contract.ErrBudgetExceeded, k.Scope, k.ID)
		}
	}
	b.st.Next++
	r := contract.Reservation{ID: "r" + strconv.Itoa(b.st.Next), Keys: slices.Clone(keys), Amount: amount}
	b.st.Open[r.ID] = r
	if err := b.save(); err != nil {
		delete(b.st.Open, r.ID)
		return contract.Reservation{}, err
	}
	return r, nil
}

// refused records the run that did not start. The event names the key, never an amount.
func (b *Budget) refused(ctx context.Context, k contract.BudgetKey) {
	if b.cfg.Audit == nil {
		return
	}
	actor := b.cfg.Actor
	if actor == "" {
		actor = "factory"
	}
	_, _ = b.cfg.Audit.Record(ctx, contract.AuditEvent{Actor: actor, Action: contract.ActionRunStart, Resource: k.ID,
		Outcome: contract.OutcomeDeny, Reason: contract.ReasonBudgetExceeded, Data: map[string]string{"scope": string(k.Scope)}})
}

func (b *Budget) close(id string) (contract.Reservation, error) {
	r, ok := b.st.Open[id]
	if !ok {
		return r, fmt.Errorf("%w: %q", contract.ErrUnknownReservation, id)
	}
	delete(b.st.Open, id)
	return r, nil
}

// Commit records actual on the reservation's keys and closes it.
func (b *Budget) Commit(_ context.Context, id string, actual contract.Micros) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	r, err := b.close(id)
	if err != nil {
		return err
	}
	for _, k := range r.Keys {
		c := counter{Spent: b.spent(k) + actual, Since: b.window(k)}
		b.st.Spent[keyName(k)] = c
	}
	return b.save()
}

// Release closes the reservation with nothing spent.
func (b *Budget) Release(_ context.Context, id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, err := b.close(id); err != nil {
		return err
	}
	return b.save()
}

// Remaining returns what is left under k's limit.
func (b *Budget) Remaining(_ context.Context, k contract.BudgetKey) (contract.Micros, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	l, ok := b.remaining(k)
	return l, ok, nil
}

// StopRecorder returns the function to give cap.Watch as Stopped. It records that a running
// session was stopped at its cap, as run.cancel with reason budget.exceeded.
func StopRecorder(log contract.Audit, actor string) func(contract.Reservation) {
	return func(r contract.Reservation) {
		if log == nil {
			return
		}
		res := ""
		if len(r.Keys) > 0 {
			res = r.Keys[0].ID
		}
		_, _ = log.Record(context.Background(), contract.AuditEvent{Actor: actor, Action: contract.ActionRunCancel, Resource: res,
			Outcome: contract.OutcomeAllow, Reason: contract.ReasonBudgetExceeded, Data: map[string]string{"cause": "budget.stop"}})
	}
}
