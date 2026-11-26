package main

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/KaaS-team/factory/ee/contract"
	"github.com/KaaS-team/factory/internal/ledger"
)

func TestMetricsCommand(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	day0 := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	add := func(id, variant, state string, cost float64, d int) {
		e := ledger.Entry{RunID: id, Repo: "acme/api", Team: "core", Workflow: "fix-bug", Variant: variant, Status: "open",
			StartedAt: day0.AddDate(0, 0, d), CostUSD: &cost, Outcome: &contract.LedgerOutcome{Kind: "pr", State: state}}
		e.Ticket.Source, e.Ticket.ID, e.Agent.Harness = "github", id, "claude-code"
		if err := ledger.Append(root, e); err != nil {
			t.Fatal(err)
		}
	}
	add("1", "a", "merged", 4, 0)
	add("2", "b", "merged", 1, 0)
	add("3", "b", "closed", 1, 0)
	add("4", "b", "merged", 1, 10) // outside the window below

	run := func(args ...string) (string, error) {
		c := metricsCmd()
		var b bytes.Buffer
		c.SetOut(&b)
		c.SetArgs(args)
		err := c.Execute()
		return b.String(), err
	}
	out, err := run("--workflow", "fix-bug", "--json", "--until", "2026-11-25")
	if err != nil {
		t.Fatal(err)
	}
	var got struct{ Groups []ledger.Scored }
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Groups) != 2 || got.Groups[0].CostPerAcceptedUSD != 4 || got.Groups[1].CostPerAcceptedUSD != 2 {
		t.Errorf("variant comparison %+v", got.Groups)
	}
	if _, err := run("--by", "nonsense"); err == nil {
		t.Error("a bad --by was accepted")
	}
	if _, err := run("--since", "yesterday"); err == nil {
		t.Error("a bad --since was accepted")
	}
}
