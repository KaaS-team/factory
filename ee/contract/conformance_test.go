// SPDX-License-Identifier: Apache-2.0

package contract_test

import (
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/KaaS-team/factory/ee/contract"
)

// conformance mirrors testdata/conformance.json. Unknown fields fail the decode, so the file and
// this runner cannot drift apart silently.
type conformance struct {
	About   string `json:"about"`
	Version int    `json:"version"`
	Authz   []struct {
		Name      string             `json:"name"`
		Principal contract.Principal `json:"principal"`
		Action    contract.Action    `json:"action"`
		Resource  contract.Resource  `json:"resource"`
		Want      contract.Decision  `json:"want"`
	} `json:"authz"`
	Policy []struct {
		Name      string                 `json:"name"`
		Layers    []contract.PolicyFile  `json:"layers"`
		WantError *string                `json:"want_error"`
		Checks    []policyCheck          `json:"checks"`
		Budgets   []contract.BudgetLimit `json:"want_budgets"`
	} `json:"policy"`
	Audit struct {
		Genesis string                `json:"genesis_prev_hash"`
		Events  []contract.AuditEvent `json:"events"`
		Want    []struct {
			Seq       uint64 `json:"seq"`
			Canonical string `json:"canonical"`
			PrevHash  string `json:"prev_hash"`
			Hash      string `json:"hash"`
		} `json:"want"`
		Tamper struct {
			Seq    int    `json:"seq"`
			Field  string `json:"field"`
			Value  string `json:"value"`
			Broken int    `json:"want_broken_seq"`
		} `json:"tamper"`
	} `json:"audit"`
	Budget []struct {
		Name   string `json:"name"`
		Limits []struct {
			Key    contract.BudgetKey `json:"key"`
			Micros contract.Micros    `json:"micros"`
		} `json:"limits"`
		Steps []struct {
			Op        string               `json:"op"`
			Ref       string               `json:"ref"`
			Keys      []contract.BudgetKey `json:"keys"`
			Key       contract.BudgetKey   `json:"key"`
			Amount    contract.Micros      `json:"amount"`
			Want      contract.Micros      `json:"want"`
			WantOK    bool                 `json:"want_ok"`
			WantError string               `json:"want_error"`
		} `json:"steps"`
	} `json:"budget"`
}

type policyCheck struct {
	Request contract.PolicyRequest `json:"request"`
	Want    contract.Decision      `json:"want"`
}

func loadConformance(t *testing.T) conformance {
	t.Helper()
	f, err := os.Open("testdata/conformance.json")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	var c conformance
	if err := dec.Decode(&c); err != nil {
		t.Fatal(err)
	}
	if c.Version != 1 {
		t.Fatalf("conformance version %d", c.Version)
	}
	return c
}

// sentinels maps each error code in the file to the sentinel an implementation wraps.
var sentinels = map[string]error{
	string(contract.ReasonPolicyWidening):           contract.ErrPolicyWidening,
	string(contract.ReasonPolicyLayerOrder):         contract.ErrPolicyLayerOrder,
	string(contract.ReasonPolicyInvalid):            contract.ErrPolicyInvalid,
	string(contract.ReasonBudgetExceeded):           contract.ErrBudgetExceeded,
	string(contract.ReasonBudgetUnknownReservation): contract.ErrUnknownReservation,
}

func wantErr(t *testing.T, name, code string, err error) {
	t.Helper()
	switch {
	case code == "" && err != nil:
		t.Errorf("%s: unexpected error %v", name, err)
	case code != "" && !errors.Is(err, sentinels[code]):
		t.Errorf("%s: got error %v, want %s", name, err, code)
	case code != "" && sentinels[code] == nil:
		t.Errorf("%s: unknown error code %s", name, code)
	}
}

func TestConformanceAuthz(t *testing.T) {
	var a contract.Authorizer = refAuthorizer{}
	for _, c := range loadConformance(t).Authz {
		if got := a.Can(t.Context(), c.Principal, c.Action, c.Resource); got != c.Want {
			t.Errorf("%s: got %+v, want %+v", c.Name, got, c.Want)
		}
	}
}

func TestConformancePolicy(t *testing.T) {
	var comp contract.PolicyCompiler = refCompiler{}
	for _, c := range loadConformance(t).Policy {
		p, err := comp.Compile(c.Layers...)
		if c.WantError != nil {
			wantErr(t, c.Name, *c.WantError, err)
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", c.Name, err)
			continue
		}
		for _, ch := range c.Checks {
			if got := p.Evaluate(t.Context(), ch.Request); got != ch.Want {
				t.Errorf("%s: %+v: got %+v, want %+v", c.Name, ch.Request, got, ch.Want)
			}
		}
		if c.Budgets != nil && !sameBudgets(p.Budgets(), c.Budgets) {
			t.Errorf("%s: budgets %+v, want %+v", c.Name, p.Budgets(), c.Budgets)
		}
	}
}

// sameBudgets compares as sets: the contract does not fix their order.
func sameBudgets(a, b []contract.BudgetLimit) bool {
	key := func(l contract.BudgetLimit) string { return string(l.Scope) + "/" + l.ID + "/" + string(l.Period) }
	cmp := func(x, y contract.BudgetLimit) int { return strings.Compare(key(x), key(y)) }
	a, b = slices.Clone(a), slices.Clone(b)
	slices.SortFunc(a, cmp)
	slices.SortFunc(b, cmp)
	return slices.Equal(a, b)
}

func TestConformanceAudit(t *testing.T) {
	c := loadConformance(t).Audit
	a := &refAudit{}
	for i, e := range c.Events {
		got, err := a.Record(t.Context(), e)
		if err != nil {
			t.Fatal(err)
		}
		w := c.Want[i]
		if string(canonical(got)) != w.Canonical {
			t.Errorf("seq %d canonical:\n got %s\nwant %s", w.Seq, canonical(got), w.Canonical)
		}
		if got.Seq != w.Seq || got.PrevHash != w.PrevHash || got.Hash != w.Hash {
			t.Errorf("seq %d: got seq %d prev %s hash %s, want %+v", w.Seq, got.Seq, got.PrevHash, got.Hash, w)
		}
	}
	if c.Want[0].PrevHash != c.Genesis {
		t.Errorf("first prev_hash %s is not the genesis %s", c.Want[0].PrevHash, c.Genesis)
	}
	if err := a.Verify(t.Context()); err != nil {
		t.Fatalf("intact chain: %v", err)
	}
	if got, _ := a.Search(t.Context(), contract.AuditQuery{Outcome: contract.OutcomeAllow, Limit: 1}); len(got) != 1 || got[0].Seq != 1 {
		t.Errorf("search allow limit 1: %+v", got)
	}
	if got, _ := a.Search(t.Context(), contract.AuditQuery{Since: c.Events[1].Time, Until: c.Events[2].Time}); len(got) != 1 || got[0].Seq != 2 {
		t.Errorf("search [event 2, event 3): %+v", got)
	}
	if c.Tamper.Field != "actor" {
		t.Fatalf("this runner tampers with actor only, not %s", c.Tamper.Field)
	}
	a.events[c.Tamper.Seq-1].Actor = c.Tamper.Value
	err := a.Verify(t.Context())
	if !errors.Is(err, contract.ErrChainBroken) || !strings.HasSuffix(err.Error(), " seq "+strconv.Itoa(c.Tamper.Broken)) {
		t.Errorf("tampered chain: got %v, want %s at seq %d", err, contract.ErrChainBroken, c.Tamper.Broken)
	}
}

func TestConformanceBudget(t *testing.T) {
	for _, c := range loadConformance(t).Budget {
		limits := map[contract.BudgetKey]contract.Micros{}
		for _, l := range c.Limits {
			limits[l.Key] = l.Micros
		}
		var b contract.Budget = newRefBudget(limits)
		refs := map[string]string{}
		for i, s := range c.Steps {
			name := c.Name + " step " + strconv.Itoa(i+1) + " " + s.Op
			switch s.Op {
			case "reserve":
				r, err := b.Reserve(t.Context(), s.Keys, s.Amount)
				wantErr(t, name, s.WantError, err)
				if err == nil {
					refs[s.Ref] = r.ID
				}
			case "commit":
				wantErr(t, name, s.WantError, b.Commit(t.Context(), refOf(refs, s.Ref), s.Amount))
			case "release":
				wantErr(t, name, s.WantError, b.Release(t.Context(), refOf(refs, s.Ref)))
			case "remaining":
				got, ok, err := b.Remaining(t.Context(), s.Key)
				if err != nil || got != s.Want || ok != s.WantOK {
					t.Errorf("%s: got %d %v %v, want %d %v", name, got, ok, err, s.Want, s.WantOK)
				}
			default:
				t.Errorf("%s: unknown op", name)
			}
		}
	}
}

// refOf maps a case's reservation ref to the id Reserve returned; an unknown ref stays as given.
func refOf(refs map[string]string, ref string) string {
	if id, ok := refs[ref]; ok {
		return id
	}
	return ref
}

// TestConformanceCoverage: the cases reach every role and action pair, every reason code and
// every check, so a constant added without a case fails here.
func TestConformanceCoverage(t *testing.T) {
	c := loadConformance(t)
	pairs, reasons := map[string]bool{}, map[contract.ReasonCode]bool{}
	for _, a := range c.Authz {
		reasons[a.Want.Reason] = true
		if len(a.Principal.Roles) == 1 && a.Principal.Roles[0].Scope.Kind == contract.ScopeOrg && a.Principal.Kind == contract.KindUser {
			pairs[string(a.Principal.Roles[0].Role)+" "+string(a.Action)] = true
		}
	}
	checks := map[contract.Check]bool{}
	for _, p := range c.Policy {
		if p.WantError != nil {
			reasons[contract.ReasonCode(*p.WantError)] = true
		}
		for _, ch := range p.Checks {
			reasons[ch.Want.Reason] = true
			checks[ch.Request.Check] = true
		}
	}
	for _, b := range c.Budget {
		for _, s := range b.Steps {
			if s.WantError != "" {
				reasons[contract.ReasonCode(s.WantError)] = true
			}
		}
	}
	for _, r := range contract.Roles {
		for _, a := range contract.Actions {
			if !pairs[string(r)+" "+string(a)] {
				t.Errorf("no org-scope case for %s %s", r, a)
			}
		}
	}
	for _, ch := range []contract.Check{contract.CheckModel, contract.CheckHarness, contract.CheckMCPServer, contract.CheckEgress, contract.CheckAutoMerge, contract.CheckAction} {
		if !checks[ch] {
			t.Errorf("no policy case for check %s", ch)
		}
	}
	for _, r := range []contract.ReasonCode{contract.ReasonUnknownAction, contract.ReasonDeprovisioned, contract.ReasonCrossOrg,
		contract.ReasonHumanRequired, contract.ReasonNoBinding, contract.ReasonScopeTooNarrow, contract.ReasonRoleLacksAction,
		contract.ReasonGranted, contract.ReasonPolicyAllowed, contract.ReasonPolicyNotAllowlisted, contract.ReasonPolicyApprovalRequired,
		contract.ReasonPolicyUnknownCheck, contract.ReasonPolicyWidening, contract.ReasonPolicyLayerOrder, contract.ReasonPolicyInvalid,
		contract.ReasonBudgetExceeded, contract.ReasonBudgetUnknownReservation} {
		if !reasons[r] {
			t.Errorf("no case expects %s", r)
		}
	}
}
