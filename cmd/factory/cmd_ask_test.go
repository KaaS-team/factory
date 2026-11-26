package main

import (
	"strings"
	"testing"
	"time"

	"github.com/KaaS-team/factory/internal/ledger"
)

// TestAskAnswersFromTheLedgerAndCallsNoControlWrite: a question about a finished ticket is answered
// from its ledger line, cites it, and neither steers, dispatches nor queues anything.
func TestAskAnswersFromTheLedgerAndCallsNoControlWrite(t *testing.T) {
	f := &fakeControl{}
	use(t, f)
	e := ledger.Entry{Schema: "kaas.run/v1", RunID: "r1", Repo: "o/r", StartedAt: time.Unix(0, 0), Status: "failed", FailureClass: "budget"}
	e.Ticket.Source, e.Ticket.ID = "github", "ENG-1"
	e.Agent.Harness = "claude"
	if err := ledger.Append(".", e); err != nil {
		t.Fatal(err)
	}
	out, _, err := runCLI(t, "ask", "ENG-1", "why", "did", "it", "fail", "with", "budget")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "[ledger] run r1 status failed failure budget") {
		t.Fatalf("out = %q", out)
	}
	if len(f.calls) != 0 {
		t.Fatalf("ask called control: %q", f.calls)
	}
}

func TestAskNeedsAQuestion(t *testing.T) {
	use(t, &fakeControl{})
	if _, _, err := runCLI(t, "ask", "ENG-1"); err == nil {
		t.Fatal("want an error without a question")
	}
}
