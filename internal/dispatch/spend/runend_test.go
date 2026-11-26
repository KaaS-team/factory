package spend

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/KaaS-team/factory/internal/api"
	"github.com/KaaS-team/factory/internal/ledger"
)

func TestEndRunWritesOneLedgerLine(t *testing.T) {
	root := t.TempDir()
	start := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	r := RunEnd{
		Session: api.Session{Key: "acme-7-a1", Ticket: "7", Dispatcher: "local", StartedAt: start},
		Brief:   api.Brief{Repo: "acme/api", Model: "m-1"},
		Status:  "merged", PR: 42, EndedAt: start.Add(time.Minute),
		Usage: api.Usage{InputTokens: 10, OutputTokens: 5, CostUSD: 0.25},
	}
	if err := EndRun(t.Context(), root, nil, r); err != nil {
		t.Fatal(err)
	}
	got, err := ledger.Entries(root)
	if err != nil || len(got) != 1 {
		t.Fatalf("%v %v", err, got)
	}
	e := got[0]
	if e.RunID != "acme-7-a1" || e.Team != "acme" || e.Agent.Harness != "claude-code" || e.Ticket.Source != "github" ||
		e.PR == nil || *e.PR.Number != 42 || *e.CostUSD != 0.25 || *e.Tokens.Input != 10 {
		t.Errorf("%+v", e)
	}
	if _, err := os.Stat(filepath.Join(root, ledger.Dir, ledger.SpendFile)); err != nil {
		t.Error("spend.json missing")
	}
}

// TestUnknownCostIsOmitted: a zero cost means unknown (api.Usage), so the line has no cost_usd.
func TestUnknownCostIsOmitted(t *testing.T) {
	r := RunEnd{Session: api.Session{Key: "k", Ticket: "1", StartedAt: time.Now()}, Brief: api.Brief{Repo: "a/b"}, Status: "failed", Class: "agent"}
	b, _ := json.Marshal(r.Entry())
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if _, has := m["cost_usd"]; has {
		t.Errorf("cost_usd written for unknown cost: %s", b)
	}
	if _, has := m["tokens"]; has {
		t.Errorf("tokens written for none: %s", b)
	}
}
