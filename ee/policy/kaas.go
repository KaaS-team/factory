//go:build ee

// SPDX-License-Identifier: LicenseRef-KaaS-EE

package policy

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/KaaS-team/factory/ee/contract"
	"github.com/KaaS-team/factory/internal/packs"
)

// Compiler implements contract.PolicyCompiler. Layers apply org, then team, then repo, and a lower
// layer may only narrow.
type Compiler struct{}

var _ contract.PolicyCompiler = Compiler{}

// list is one allowlist and the layer that set it.
type list struct {
	set     bool
	entries []string
	by      contract.Layer
}

// Effective is the compiled policy for one repo. It implements contract.Policy.
type Effective struct {
	lists    map[contract.Check]*list
	approval []approval
	budgets  []contract.BudgetLimit
}

type approval struct {
	action string
	by     contract.Layer
}

var _ contract.Policy = (*Effective)(nil)

var layerOrder = []contract.Layer{contract.LayerOrg, contract.LayerTeam, contract.LayerRepo}

// listOf returns the allowlist of f that check reads, and whether it is an egress list.
func listOf(f contract.PolicyFile, c contract.Check) []string {
	switch c {
	case contract.CheckModel:
		return f.Models.Allowed
	case contract.CheckHarness:
		return f.Harness.Allowed
	case contract.CheckMCPServer:
		return f.MCP.AllowedServers
	case contract.CheckEgress:
		return f.Egress.AllowedHosts
	}
	out := make([]string, len(f.Merge.AutoMergeRisk))
	for i, r := range f.Merge.AutoMergeRisk {
		out[i] = string(r)
	}
	if f.Merge.AutoMergeRisk == nil {
		return nil
	}
	return out
}

var listChecks = []contract.Check{contract.CheckModel, contract.CheckHarness, contract.CheckMCPServer, contract.CheckEgress, contract.CheckAutoMerge}

// Compile layers files as contract.PolicyCompiler.Compile specifies. Every file is validated first.
func (Compiler) Compile(files ...contract.PolicyFile) (contract.Policy, error) {
	if len(files) == 0 || files[0].Layer != contract.LayerOrg {
		return nil, fmt.Errorf("%w: the first layer must be org", contract.ErrPolicyLayerOrder)
	}
	e := &Effective{lists: map[contract.Check]*list{}}
	last := -1
	for i, f := range files {
		if f.Version != contract.PolicyVersion {
			return nil, fmt.Errorf("%w: version %d", contract.ErrPolicyInvalid, f.Version)
		}
		at := slices.Index(layerOrder, f.Layer)
		if at < 0 {
			return nil, fmt.Errorf("%w: unknown layer %q", contract.ErrPolicyInvalid, f.Layer)
		}
		if at <= last {
			return nil, fmt.Errorf("%w: %s after %s", contract.ErrPolicyLayerOrder, f.Layer, layerOrder[max(last, 0)])
		}
		last = at
		if err := Validate(f); err != nil {
			return nil, err
		}
		for _, c := range listChecks {
			src := listOf(f, c)
			if src == nil {
				continue
			}
			cur := e.lists[c]
			if cur == nil {
				cur = &list{}
				e.lists[c] = cur
			}
			if i > 0 {
				if miss, ok := covered(cur.entries, src, c == contract.CheckEgress); !ok {
					return nil, fmt.Errorf("%w: %s %s adds %q", contract.ErrPolicyWidening, f.Layer, listName(c), miss)
				}
			}
			cur.set, cur.entries, cur.by = true, slices.Clone(src), f.Layer
		}
		for _, a := range f.Approval.RequireHuman {
			if !slices.ContainsFunc(e.approval, func(x approval) bool { return x.action == string(a) }) {
				e.approval = append(e.approval, approval{string(a), f.Layer})
			}
		}
		for _, b := range f.Budgets {
			j := slices.IndexFunc(e.budgets, func(o contract.BudgetLimit) bool {
				return o.Scope == b.Scope && o.ID == b.ID && o.Period == b.Period
			})
			switch {
			case j < 0:
				e.budgets = append(e.budgets, b)
			case b.USD > e.budgets[j].USD:
				return nil, fmt.Errorf("%w: %s budget %s/%s", contract.ErrPolicyWidening, f.Layer, b.Scope, b.ID)
			default:
				e.budgets[j].USD = b.USD
			}
		}
	}
	return e, nil
}

func listName(c contract.Check) string {
	return map[contract.Check]string{
		contract.CheckModel: "models", contract.CheckHarness: "harnesses", contract.CheckMCPServer: "mcp",
		contract.CheckEgress: "egress", contract.CheckAutoMerge: "merge",
	}[c]
}

// covered reports the first entry of child that parent does not cover.
func covered(parent, child []string, egress bool) (string, bool) {
	for _, c := range child {
		ok := slices.Contains(parent, c)
		if egress && !ok {
			for _, p := range parent {
				if strings.HasPrefix(p, "*.") && strings.HasSuffix(strings.TrimPrefix(c, "*"), p[1:]) {
					ok = true
					break
				}
			}
		}
		if !ok {
			return c, false
		}
	}
	return "", true
}

func egressMatch(entry, host string) bool {
	if d, ok := strings.CutPrefix(entry, "*"); ok {
		return strings.HasPrefix(d, ".") && strings.HasSuffix(host, d)
	}
	return entry == host
}

// Evaluate answers one request as contract.Policy.Evaluate specifies.
func (e *Effective) Evaluate(_ context.Context, r contract.PolicyRequest) contract.Decision {
	d, _ := e.decide(r)
	return d
}

// decide is Evaluate plus the layer that decided: the one that set the list, or added the
// approval. A request no layer decided on (a list nobody set, an unknown check) has no layer.
func (e *Effective) decide(r contract.PolicyRequest) (contract.Decision, contract.Layer) {
	allow := contract.Decision{Allow: true, Reason: contract.ReasonPolicyAllowed}
	switch r.Check {
	case contract.CheckAction:
		for _, a := range e.approval {
			if a.action == r.Value {
				if r.HumanApproved {
					return allow, a.by
				}
				return contract.Decision{Reason: contract.ReasonPolicyApprovalRequired}, a.by
			}
		}
		return allow, ""
	case contract.CheckModel, contract.CheckHarness, contract.CheckMCPServer, contract.CheckEgress, contract.CheckAutoMerge:
		l := e.lists[r.Check]
		if l == nil {
			return contract.Decision{Reason: contract.ReasonPolicyNotAllowlisted}, ""
		}
		for _, entry := range l.entries {
			if (r.Check == contract.CheckEgress && egressMatch(entry, r.Value)) || entry == r.Value {
				return allow, l.by
			}
		}
		return contract.Decision{Reason: contract.ReasonPolicyNotAllowlisted}, l.by
	}
	return contract.Decision{Reason: contract.ReasonPolicyUnknownCheck}, ""
}

// Budgets returns the effective budget limits.
func (e *Effective) Budgets() []contract.BudgetLimit { return slices.Clone(e.budgets) }

// Explanation is a decision and the layer that made it, for `factory policy explain`.
type Explanation struct {
	Decision contract.Decision
	// Layer is the layer whose list or approval entry decided. Empty when no layer set the list
	// (default deny), or the request allows because no rule covers it.
	Layer contract.Layer
}

// Explain evaluates r and names the deciding layer. It is the same decision Evaluate returns.
func (e *Effective) Explain(r contract.PolicyRequest) Explanation {
	d, by := e.decide(r)
	return Explanation{Decision: d, Layer: by}
}

// Validate checks one layer file against the schema. It reports every problem, each wrapping
// contract.ErrPolicyInvalid.
func Validate(f contract.PolicyFile) error {
	var bad []error
	add := func(format string, a ...any) { bad = append(bad, fmt.Errorf(format, a...)) }
	if f.Version != contract.PolicyVersion {
		add("version must be %d, got %d", contract.PolicyVersion, f.Version)
	}
	if !slices.Contains(layerOrder, f.Layer) {
		add("layer must be org, team or repo, got %q", f.Layer)
	}
	if f.Name == "" {
		add("name is required")
	}
	for _, c := range []contract.Check{contract.CheckModel, contract.CheckHarness, contract.CheckMCPServer} {
		for _, v := range listOf(f, c) {
			if strings.TrimSpace(v) == "" || v != strings.TrimSpace(v) {
				add("%s: entry %q is empty or has spaces around it", listName(c), v)
			}
		}
		if d := dup(listOf(f, c)); d != "" {
			add("%s: %q is listed twice", listName(c), d)
		}
	}
	for _, h := range f.Egress.AllowedHosts {
		if !validHost(h) {
			add("egress: %q is not a host or a *.domain wildcard", h)
		}
	}
	if d := dup(f.Egress.AllowedHosts); d != "" {
		add("egress: %q is listed twice", d)
	}
	for _, r := range f.Merge.AutoMergeRisk {
		if !slices.Contains([]contract.RiskClass{contract.RiskLow, contract.RiskMedium, contract.RiskHigh}, r) {
			add("merge: unknown risk class %q", r)
		}
	}
	for _, a := range f.Approval.RequireHuman {
		if !slices.Contains(contract.Actions, a) {
			add("approval: unknown action %q", a)
		}
	}
	seen := map[[3]string]bool{}
	for _, b := range f.Budgets {
		if !slices.Contains([]contract.BudgetScope{contract.BudgetOrg, contract.BudgetTeam, contract.BudgetFactory, contract.BudgetBot, contract.BudgetAction}, b.Scope) {
			add("budget: unknown scope %q", b.Scope)
		}
		if b.ID == "" {
			add("budget: %s scope needs an id", b.Scope)
		}
		if !(b.USD > 0) {
			add("budget %s/%s: usd must be above 0", b.Scope, b.ID)
		}
		if !slices.Contains([]contract.Period{contract.PeriodDay, contract.PeriodWeek, contract.PeriodMonth}, b.Period) {
			add("budget %s/%s: unknown period %q", b.Scope, b.ID, b.Period)
		}
		k := [3]string{string(b.Scope), b.ID, string(b.Period)}
		if seen[k] {
			add("budget %s/%s per %s is set twice", b.Scope, b.ID, b.Period)
		}
		seen[k] = true
	}
	if len(bad) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %w", contract.ErrPolicyInvalid, errors.Join(bad...))
}

func dup(in []string) string {
	seen := map[string]bool{}
	for _, v := range in {
		if seen[v] {
			return v
		}
		seen[v] = true
	}
	return ""
}

// validHost accepts "host" and "*.domain": no scheme, port, path or stray wildcard.
func validHost(h string) bool {
	body := strings.TrimPrefix(h, "*.")
	if body == "" || strings.ContainsAny(body, "*:/@ \t?#") || strings.HasPrefix(body, ".") || strings.HasSuffix(body, ".") {
		return false
	}
	return !strings.Contains(body, "..")
}

// Decode parses one kaas-policy.toml. Unknown keys are an error, so a typo cannot silently loosen
// a policy.
func Decode(data []byte) (contract.PolicyFile, error) {
	var f contract.PolicyFile
	md, err := toml.Decode(string(data), &f)
	if err != nil {
		return f, fmt.Errorf("%w: %w", contract.ErrPolicyInvalid, err)
	}
	if u := md.Undecoded(); len(u) > 0 {
		keys := make([]string, len(u))
		for i, k := range u {
			keys[i] = k.String()
		}
		return f, fmt.Errorf("%w: unknown keys %s", contract.ErrPolicyInvalid, strings.Join(keys, ", "))
	}
	return f, nil
}

// Source is one layer's file on disk.
type Source struct {
	Layer contract.Layer
	Path  string
	// Key, when set, makes the file load only with a valid Path+".sig" under it (signed.go's
	// scheme, packs.ReadFileSigned). The org file must be signed when an operator sets a key, so a
	// repo cannot swap it.
	Key ed25519.PublicKey
}

// Load reads the files in order and compiles them. A Source with an empty Path is skipped, except
// the org layer, which is required.
func Load(src ...Source) (*Effective, []contract.PolicyFile, error) {
	var files []contract.PolicyFile
	for _, s := range src {
		if s.Path == "" {
			if s.Layer == contract.LayerOrg {
				return nil, nil, fmt.Errorf("%w: no org policy file is configured", contract.ErrPolicyInvalid)
			}
			continue
		}
		var data []byte
		var err error
		if s.Key != nil {
			data, err = packs.ReadFileSigned(s.Path, s.Key)
		} else {
			data, err = os.ReadFile(s.Path)
		}
		if err != nil {
			return nil, nil, fmt.Errorf("%w: %s: %w", contract.ErrPolicyInvalid, s.Layer, err)
		}
		f, err := Decode(data)
		if err != nil {
			return nil, files, fmt.Errorf("%s: %w", s.Path, err)
		}
		if f.Layer != s.Layer {
			return nil, files, fmt.Errorf("%w: %s declares layer %q, expected %q", contract.ErrPolicyLayerOrder, s.Path, f.Layer, s.Layer)
		}
		files = append(files, f)
	}
	p, err := Compiler{}.Compile(files...)
	if err != nil {
		return nil, files, err
	}
	return p.(*Effective), files, nil
}
