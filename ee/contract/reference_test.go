// SPDX-License-Identifier: Apache-2.0

package contract_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/KaaS-team/factory/ee/contract"
)

// This file is the in-memory reference implementation the conformance cases run against. It is
// test code: real implementations live in their owning packages and must pass the same cases.

type refAuthorizer struct{}

func (refAuthorizer) Can(_ context.Context, p contract.Principal, a contract.Action, r contract.Resource) contract.Decision {
	deny := func(c contract.ReasonCode) contract.Decision { return contract.Decision{Reason: c} }
	switch {
	case !slices.Contains(contract.Actions, a):
		return deny(contract.ReasonUnknownAction)
	case p.Disabled:
		return deny(contract.ReasonDeprovisioned)
	case r.Org != p.Org:
		return deny(contract.ReasonCrossOrg)
	case a == contract.ActionApprove && p.Kind != contract.KindUser:
		return deny(contract.ReasonHumanRequired)
	}
	covered, narrow := false, false
	for _, b := range p.Roles {
		s := b.Scope
		if !covers(s, r) {
			continue
		}
		covered = true
		if !slices.Contains(contract.Grants[b.Role], a) {
			continue
		}
		if !slices.Contains(contract.OrgOnlyActions, a) || s.Kind == contract.ScopeOrg {
			return contract.Decision{Allow: true, Reason: contract.ReasonGranted}
		}
		narrow = true
	}
	switch {
	case !covered:
		return deny(contract.ReasonNoBinding)
	case narrow:
		return deny(contract.ReasonScopeTooNarrow)
	}
	return deny(contract.ReasonRoleLacksAction)
}

// covers reports whether a binding at s applies to r (rule 5 of Authorizer.Can).
func covers(s contract.Scope, r contract.Resource) bool {
	switch s.Kind {
	case contract.ScopeOrg:
		return s.ID == r.Org
	case contract.ScopeTeam:
		return r.Team != "" && s.ID == r.Team
	case contract.ScopeRepo:
		return r.Repo != "" && s.ID == r.Repo
	}
	return false
}

type refCompiler struct{}

type refPolicy struct {
	models, harnesses, mcp, egress, merge []string
	approval                              []string
	budgets                               []contract.BudgetLimit
}

func strs[T ~string](in []T) []string {
	if in == nil {
		return nil
	}
	out := make([]string, len(in))
	for i, v := range in {
		out[i] = string(v)
	}
	return out
}

// egressMatch reports whether host is allowed by entry.
func egressMatch(entry, host string) bool {
	if d, ok := strings.CutPrefix(entry, "*"); ok {
		return strings.HasPrefix(d, ".") && strings.HasSuffix(host, d)
	}
	return entry == host
}

// covered reports whether every entry of child is covered by parent.
func covered(parent, child []string, egress bool) (string, bool) {
	for _, c := range child {
		ok := slices.Contains(parent, c)
		for _, p := range parent {
			if egress && !ok && strings.HasPrefix(p, "*.") {
				ok = strings.HasSuffix(strings.TrimPrefix(c, "*"), p[1:])
			}
		}
		if !ok {
			return c, false
		}
	}
	return "", true
}

func (refCompiler) Compile(files ...contract.PolicyFile) (contract.Policy, error) {
	order := []contract.Layer{contract.LayerOrg, contract.LayerTeam, contract.LayerRepo}
	if len(files) == 0 || files[0].Layer != contract.LayerOrg {
		return nil, fmt.Errorf("%w: the first layer must be org", contract.ErrPolicyLayerOrder)
	}
	p := &refPolicy{}
	last := -1
	for i, f := range files {
		if f.Version != contract.PolicyVersion {
			return nil, fmt.Errorf("%w: version %d", contract.ErrPolicyInvalid, f.Version)
		}
		at := slices.Index(order, f.Layer)
		if at <= last {
			return nil, fmt.Errorf("%w: %s after %s", contract.ErrPolicyLayerOrder, f.Layer, order[max(last, 0)])
		}
		last = at
		for _, l := range []struct {
			name   string
			dst    *[]string
			src    []string
			egress bool
		}{
			{"models", &p.models, f.Models.Allowed, false},
			{"harnesses", &p.harnesses, f.Harness.Allowed, false},
			{"mcp", &p.mcp, f.MCP.AllowedServers, false},
			{"egress", &p.egress, f.Egress.AllowedHosts, true},
			{"merge", &p.merge, strs(f.Merge.AutoMergeRisk), false},
		} {
			if l.src == nil {
				continue
			}
			if i > 0 {
				if e, ok := covered(*l.dst, l.src, l.egress); !ok {
					return nil, fmt.Errorf("%w: %s %s adds %q", contract.ErrPolicyWidening, f.Layer, l.name, e)
				}
			}
			*l.dst = slices.Clone(l.src)
		}
		for _, a := range strs(f.Approval.RequireHuman) {
			if !slices.Contains(p.approval, a) {
				p.approval = append(p.approval, a)
			}
		}
		for _, b := range f.Budgets {
			j := slices.IndexFunc(p.budgets, func(o contract.BudgetLimit) bool {
				return o.Scope == b.Scope && o.ID == b.ID && o.Period == b.Period
			})
			switch {
			case j < 0:
				p.budgets = append(p.budgets, b)
			case b.USD > p.budgets[j].USD:
				return nil, fmt.Errorf("%w: %s budget %s/%s", contract.ErrPolicyWidening, f.Layer, b.Scope, b.ID)
			default:
				p.budgets[j].USD = b.USD
			}
		}
	}
	return p, nil
}

func (p *refPolicy) Evaluate(_ context.Context, r contract.PolicyRequest) contract.Decision {
	allow := contract.Decision{Allow: true, Reason: contract.ReasonPolicyAllowed}
	in := func(list []string, match func(e string) bool) contract.Decision {
		if slices.ContainsFunc(list, match) {
			return allow
		}
		return contract.Decision{Reason: contract.ReasonPolicyNotAllowlisted}
	}
	eq := func(e string) bool { return e == r.Value }
	switch r.Check {
	case contract.CheckModel:
		return in(p.models, eq)
	case contract.CheckHarness:
		return in(p.harnesses, eq)
	case contract.CheckMCPServer:
		return in(p.mcp, eq)
	case contract.CheckAutoMerge:
		return in(p.merge, eq)
	case contract.CheckEgress:
		return in(p.egress, func(e string) bool { return egressMatch(e, r.Value) })
	case contract.CheckAction:
		if slices.Contains(p.approval, r.Value) && !r.HumanApproved {
			return contract.Decision{Reason: contract.ReasonPolicyApprovalRequired}
		}
		return allow
	}
	return contract.Decision{Reason: contract.ReasonPolicyUnknownCheck}
}

func (p *refPolicy) Budgets() []contract.BudgetLimit { return slices.Clone(p.budgets) }

// canonical returns the canonical form of e, per the AuditEvent doc comment.
func canonical(e contract.AuditEvent) []byte {
	data := e.Data
	if data == nil {
		data = map[string]string{}
	}
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var d strings.Builder
	d.WriteString("{")
	for i, k := range keys {
		if i > 0 {
			d.WriteString(",")
		}
		d.WriteString(jcsString(k) + ":" + jcsString(data[k]))
	}
	d.WriteString("}")
	t := e.Time.UTC().Truncate(time.Millisecond).Format("2006-01-02T15:04:05.000Z")
	return []byte(`{"action":` + jcsString(string(e.Action)) + `,"actor":` + jcsString(e.Actor) + `,"data":` + d.String() +
		`,"outcome":` + jcsString(string(e.Outcome)) + `,"reason":` + jcsString(string(e.Reason)) +
		`,"resource":` + jcsString(e.Resource) + `,"seq":` + strconv.FormatUint(e.Seq, 10) + `,"time":` + jcsString(t) + `}`)
}

func jcsString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\t':
			b.WriteString(`\t`)
		case '\n':
			b.WriteString(`\n`)
		case '\f':
			b.WriteString(`\f`)
		case '\r':
			b.WriteString(`\r`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

// genesis is the PrevHash of the first event: 32 zero bytes.
var genesis = strings.Repeat("0", 2*sha256.Size)

// chainHash is sha256(prev || canonical(e)) in lowercase hex.
func chainHash(prevHex string, e contract.AuditEvent) (string, error) {
	prev, err := hex.DecodeString(prevHex)
	if err != nil || len(prev) != sha256.Size {
		return "", fmt.Errorf("bad prev hash %q", prevHex)
	}
	sum := sha256.Sum256(append(prev, canonical(e)...))
	return hex.EncodeToString(sum[:]), nil
}

type refAudit struct {
	mu     sync.Mutex
	events []contract.AuditEvent
}

func (a *refAudit) Record(_ context.Context, e contract.AuditEvent) (contract.AuditEvent, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	e.Seq, e.PrevHash = uint64(len(a.events))+1, genesis
	if n := len(a.events); n > 0 {
		e.PrevHash = a.events[n-1].Hash
	}
	h, err := chainHash(e.PrevHash, e)
	if err != nil {
		return contract.AuditEvent{}, err
	}
	e.Hash = h
	a.events = append(a.events, e)
	return e, nil
}

func (a *refAudit) Verify(context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	prev := genesis
	for i, e := range a.events {
		h, err := chainHash(prev, e)
		if err != nil || e.Seq != uint64(i)+1 || h != e.Hash {
			return fmt.Errorf("%w at seq %d", contract.ErrChainBroken, i+1)
		}
		prev = e.Hash
	}
	return nil
}

func (a *refAudit) Search(_ context.Context, q contract.AuditQuery) ([]contract.AuditEvent, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []contract.AuditEvent
	for _, e := range a.events {
		if (q.Actor != "" && e.Actor != q.Actor) || (q.Action != "" && e.Action != q.Action) || (q.Outcome != "" && e.Outcome != q.Outcome) ||
			(!q.Since.IsZero() && e.Time.Before(q.Since)) || (!q.Until.IsZero() && !e.Time.Before(q.Until)) {
			continue
		}
		if out = append(out, e); q.Limit > 0 && len(out) == q.Limit {
			break
		}
	}
	return out, nil
}

type refBudget struct {
	mu     sync.Mutex
	limits map[contract.BudgetKey]contract.Micros
	spent  map[contract.BudgetKey]contract.Micros
	open   map[string]contract.Reservation
	next   int
}

func newRefBudget(limits map[contract.BudgetKey]contract.Micros) *refBudget {
	return &refBudget{limits: limits, spent: map[contract.BudgetKey]contract.Micros{}, open: map[string]contract.Reservation{}}
}

// microsOf converts a policy limit to Micros.
func microsOf(usd float64) contract.Micros { return contract.Micros(math.Round(usd * 1e6)) }

func (b *refBudget) remaining(k contract.BudgetKey) (contract.Micros, bool) {
	l, ok := b.limits[k]
	if !ok {
		return 0, false
	}
	l -= b.spent[k]
	for _, r := range b.open {
		if slices.Contains(r.Keys, k) {
			l -= r.Amount
		}
	}
	return l, true
}

func (b *refBudget) Reserve(_ context.Context, keys []contract.BudgetKey, amount contract.Micros) (contract.Reservation, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if amount <= 0 {
		return contract.Reservation{}, errors.New("amount must be > 0")
	}
	for _, k := range keys {
		if left, ok := b.remaining(k); ok && amount > left {
			return contract.Reservation{}, fmt.Errorf("%w: %s/%s", contract.ErrBudgetExceeded, k.Scope, k.ID)
		}
	}
	b.next++
	r := contract.Reservation{ID: strconv.Itoa(b.next), Keys: slices.Clone(keys), Amount: amount}
	b.open[r.ID] = r
	return r, nil
}

func (b *refBudget) close(id string) (contract.Reservation, error) {
	r, ok := b.open[id]
	if !ok {
		return r, fmt.Errorf("%w: %q", contract.ErrUnknownReservation, id)
	}
	delete(b.open, id)
	return r, nil
}

func (b *refBudget) Commit(_ context.Context, id string, actual contract.Micros) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	r, err := b.close(id)
	for _, k := range r.Keys {
		b.spent[k] += actual
	}
	return err
}

func (b *refBudget) Release(_ context.Context, id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, err := b.close(id)
	return err
}

func (b *refBudget) Remaining(_ context.Context, k contract.BudgetKey) (contract.Micros, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	l, ok := b.remaining(k)
	return l, ok, nil
}

// The reference types satisfy the contract interfaces.
var (
	_ contract.Authorizer     = refAuthorizer{}
	_ contract.PolicyCompiler = refCompiler{}
	_ contract.Audit          = (*refAudit)(nil)
	_ contract.Budget         = (*refBudget)(nil)
)
