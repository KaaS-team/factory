//go:build ee

// SPDX-License-Identifier: LicenseRef-KaaS-EE

package budget

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/KaaS-team/factory/ee/audit"
	"github.com/KaaS-team/factory/ee/contract"
	"github.com/KaaS-team/factory/internal/api"
	ib "github.com/KaaS-team/factory/internal/budget"
	"github.com/KaaS-team/factory/internal/dispatch/spend"
	"github.com/KaaS-team/factory/internal/meter/cap"
)

type cases struct {
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

var sentinels = map[string]error{
	string(contract.ReasonBudgetExceeded):           contract.ErrBudgetExceeded,
	string(contract.ReasonBudgetUnknownReservation): contract.ErrUnknownReservation,
}

func TestConformance(t *testing.T) {
	data, err := os.ReadFile("../contract/testdata/conformance.json")
	if err != nil {
		t.Fatal(err)
	}
	var c cases
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatal(err)
	}
	for _, bc := range c.Budget {
		var limits []Limit
		for _, l := range bc.Limits {
			limits = append(limits, Limit{Key: l.Key, Micros: l.Micros})
		}
		b, err := New(Config{Limits: limits, Path: filepath.Join(t.TempDir(), "b.json")})
		if err != nil {
			t.Fatal(err)
		}
		refs := map[string]string{}
		id := func(ref string) string {
			if v, ok := refs[ref]; ok {
				return v
			}
			return ref
		}
		for i, s := range bc.Steps {
			name := bc.Name + " step " + strconv.Itoa(i+1) + " " + s.Op
			var err error
			switch s.Op {
			case "reserve":
				var r contract.Reservation
				r, err = b.Reserve(t.Context(), s.Keys, s.Amount)
				if err == nil {
					refs[s.Ref] = r.ID
				}
			case "commit":
				err = b.Commit(t.Context(), id(s.Ref), s.Amount)
			case "release":
				err = b.Release(t.Context(), id(s.Ref))
			case "remaining":
				got, ok, _ := b.Remaining(t.Context(), s.Key)
				if got != s.Want || ok != s.WantOK {
					t.Errorf("%s: got %d %v, want %d %v", name, got, ok, s.Want, s.WantOK)
				}
				continue
			}
			switch {
			case s.WantError == "" && err != nil:
				t.Errorf("%s: %v", name, err)
			case s.WantError != "" && !errors.Is(err, sentinels[s.WantError]):
				t.Errorf("%s: got %v, want %s", name, err, s.WantError)
			}
		}
	}
}

var team = contract.BudgetKey{Scope: contract.BudgetTeam, ID: "acme"}

func TestStateSurvivesRestartAndPeriodResets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s", "b.json")
	now := time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)
	cfg := Config{Limits: []Limit{{Key: team, Micros: 100, Period: contract.PeriodDay}}, Path: path, Now: func() time.Time { return now }}
	b, _ := New(cfg)
	r, err := b.Reserve(t.Context(), []contract.BudgetKey{team}, 60)
	if err != nil {
		t.Fatal(err)
	}
	b2, _ := New(cfg) // a restart: the open hold is still held
	if left, _, _ := b2.Remaining(t.Context(), team); left != 40 {
		t.Errorf("after restart left %d, want 40", left)
	}
	if err := b2.Commit(t.Context(), r.ID, 70); err != nil {
		t.Fatal(err)
	}
	if left, _, _ := b2.Remaining(t.Context(), team); left != 30 {
		t.Errorf("after commit left %d, want 30", left)
	}
	now = now.Add(24 * time.Hour)
	if left, _, _ := b2.Remaining(t.Context(), team); left != 100 {
		t.Errorf("next day left %d, want 100", left)
	}
}

func TestReserveRefusalIsAuditedAsDeny(t *testing.T) {
	log := audit.Open(filepath.Join(t.TempDir(), "c.jsonl"))
	b, _ := New(Config{Limits: []Limit{{Key: team, Micros: 10}}, Audit: log, Actor: "bot1"})
	if _, err := b.Reserve(t.Context(), []contract.BudgetKey{team}, 11); !errors.Is(err, contract.ErrBudgetExceeded) {
		t.Fatal(err)
	}
	got, _ := log.Search(t.Context(), contract.AuditQuery{Action: contract.ActionRunStart, Outcome: contract.OutcomeDeny})
	if len(got) != 1 || got[0].Reason != contract.ReasonBudgetExceeded || got[0].Actor != "bot1" {
		t.Errorf("got %+v", got)
	}
}

func TestFromPolicyRounds(t *testing.T) {
	got := FromPolicy([]contract.BudgetLimit{{Scope: contract.BudgetTeam, ID: "a", USD: 0.01, Period: contract.PeriodDay}})
	if got[0].Micros != 10000 || got[0].Key != (contract.BudgetKey{Scope: contract.BudgetTeam, ID: "a"}) {
		t.Errorf("%+v", got)
	}
}

type fakeDispatcher struct{ calls int }

func (f *fakeDispatcher) Dispatch(context.Context, api.Brief) (api.Session, error) {
	f.calls++
	return api.Session{}, nil
}
func (f *fakeDispatcher) Name() string                                     { return "fake" }
func (f *fakeDispatcher) Caps() api.Caps                                   { return api.Caps{} }
func (f *fakeDispatcher) Steer(context.Context, api.Session, string) error { return nil }
func (f *fakeDispatcher) Cancel(context.Context, api.Session) error        { return nil }
func (f *fakeDispatcher) Usage(context.Context, api.Session) (api.Usage, error) {
	return api.Usage{}, nil
}

// TestOneCentTeamBudgetRefusesRunBeforeStart is the "done when" case: a team with a 1-cent budget
// gets its run refused by the dispatch guard before the inner dispatcher is called.
func TestOneCentTeamBudgetRefusesRunBeforeStart(t *testing.T) {
	b, _ := New(Config{Limits: []Limit{{Key: team, Micros: Micros(0.01)}}})
	ib.SetEnforcer(&ib.Enforcer{Estimate: Micros(0.25), Open: func() (contract.Budget, error) { return b, nil }})
	defer ib.SetEnforcer(nil)
	inner := &fakeDispatcher{}
	_, err := spend.Wrap(inner).Dispatch(t.Context(), api.Brief{Repo: "acme/app"})
	if !errors.Is(err, contract.ErrBudgetExceeded) {
		t.Fatalf("got %v, want ErrBudgetExceeded", err)
	}
	if inner.calls != 0 {
		t.Error("the run started")
	}
	// A team with no limit still runs.
	if _, err := spend.Wrap(inner).Dispatch(t.Context(), api.Brief{Repo: "other/app"}); err != nil || inner.calls != 1 {
		t.Errorf("unlimited team: %v calls %d", err, inner.calls)
	}
}

func TestWatchStopsRunningSessionAtCapAndAudits(t *testing.T) {
	log := audit.Open(filepath.Join(t.TempDir(), "c.jsonl"))
	b, _ := New(Config{Limits: []Limit{{Key: team, Micros: 1000}}})
	res, err := b.Reserve(t.Context(), []contract.BudgetKey{team}, 500)
	if err != nil {
		t.Fatal(err)
	}
	stops := 0
	w := &cap.Watch{Budget: b, Res: res, Stop: func() { stops++ }, Stopped: StopRecorder(log, "bot1")}
	if err := w.Add(200); err != nil || stops != 0 {
		t.Fatalf("under cap: %v %d", err, stops)
	}
	if err := w.Add(400); !errors.Is(err, cap.ErrOverCap) || stops != 1 {
		t.Fatalf("at cap: %v %d", err, stops)
	}
	_ = w.Add(10)
	if stops != 1 {
		t.Errorf("stopped %d times", stops)
	}
	if err := w.Done(t.Context()); err != nil {
		t.Fatal(err)
	}
	if left, _, _ := b.Remaining(t.Context(), team); left != 390 {
		t.Errorf("left %d, want 390 after committing 610", left)
	}
	got, _ := log.Search(t.Context(), contract.AuditQuery{Action: contract.ActionRunCancel})
	if len(got) != 1 || got[0].Reason != contract.ReasonBudgetExceeded {
		t.Errorf("audit: %+v", got)
	}
}

func TestFromLegacyCountsSpendInWindow(t *testing.T) {
	now := time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)
	bs := []ib.Budget{{Scope: "team:acme", LimitUSD: 1, Period: ib.Day}, {Scope: "client:acme/app", LimitUSD: 1, Period: ib.Day}}
	rows := []ib.Row{{Team: "acme", At: now.Add(-time.Hour), CostUSD: 0.4}, {Team: "acme", At: now.Add(-48 * time.Hour), CostUSD: 9}}
	b, err := FromLegacy(bs, rows, now)
	if err != nil {
		t.Fatal(err)
	}
	if left, ok, _ := b.Remaining(t.Context(), team); !ok || left != Micros(0.6) {
		t.Errorf("left %d %v", left, ok)
	}
}
