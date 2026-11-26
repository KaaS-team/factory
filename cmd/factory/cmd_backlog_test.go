package main

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/KaaS-team/factory/internal/api"
	"github.com/KaaS-team/factory/internal/backlog"
	"github.com/KaaS-team/factory/internal/mcp"
)

type memTracker struct {
	issues []api.Ticket
	parent map[api.TicketID]api.TicketID
	rank   map[api.TicketID]int
}

func (m *memTracker) Find(_ context.Context, title string) (api.TicketID, bool, error) {
	for _, t := range m.issues {
		if t.Title == title {
			return t.ID, true, nil
		}
	}
	return "", false, nil
}

func (m *memTracker) Create(_ context.Context, t api.Ticket) (api.TicketID, error) {
	t.ID = api.TicketID(strconv.Itoa(len(m.issues) + 1))
	m.issues = append(m.issues, t)
	return t.ID, nil
}

func (m *memTracker) Get(_ context.Context, id api.TicketID) (api.Ticket, error) {
	for _, t := range m.issues {
		if t.ID == id {
			return t, nil
		}
	}
	return api.Ticket{}, errors.New("not found")
}
func (m *memTracker) Ready(context.Context) ([]api.Ticket, error) { return m.issues, nil }
func (m *memTracker) LinkSub(_ context.Context, p, c api.TicketID) error {
	m.parent[c] = p
	return nil
}
func (m *memTracker) SetRank(_ context.Context, id api.TicketID, r int) error {
	m.rank[id] = r
	return nil
}

func useFake(t *testing.T) *memTracker {
	t.Helper()
	m := &memTracker{parent: map[api.TicketID]api.TicketID{}, rank: map[api.TicketID]int{}}
	old := backlogPort
	backlogPort = func() (backlog.Port, error) { return m, nil }
	t.Cleanup(func() { backlogPort = old })
	return m
}

const blocks = "--- issue: Child one\n## Problem\np\n## Acceptance\n- a\n## Out of scope\n- o\n"

func TestSpecCommandFilesOnlyWithApprove(t *testing.T) {
	m := useFake(t)
	out, err := mcp.BacklogRun(context.Background(), blocks, "spec", "--from", "-")
	if err != nil || len(m.issues) != 0 || !strings.Contains(out, "nothing filed") {
		t.Fatalf("out=%q issues=%d err=%v", out, len(m.issues), err)
	}
	out, err = mcp.BacklogRun(context.Background(), blocks, "spec", "--from", "-", "--approve")
	if err != nil || len(m.issues) != 1 || !strings.Contains(out, "filed #1 Child one") {
		t.Fatalf("out=%q issues=%d err=%v", out, len(m.issues), err)
	}
}

func TestSpecGoalWithoutRunnerSaysSo(t *testing.T) {
	useFake(t)
	_, err := mcp.BacklogRun(context.Background(), "", "spec", "build it")
	if !errors.Is(err, backlog.ErrNoDrafter) {
		t.Fatalf("err = %v", err)
	}
}

func TestDecomposeCommandReadsEpicBodyAndLinks(t *testing.T) {
	m := useFake(t)
	m.issues = []api.Ticket{{ID: "1", Title: "Epic", Body: "intro\n" + blocks}}
	out, err := mcp.BacklogRun(context.Background(), "", "decompose", "1", "--approve")
	if err != nil || m.parent["2"] != "1" || !strings.Contains(out, "filed #2 Child one") {
		t.Fatalf("out=%q parent=%v err=%v", out, m.parent, err)
	}
}

func TestPrioritizeCommandUsesConfiguredMethod(t *testing.T) {
	t.Chdir(t.TempDir())
	m := useFake(t)
	m.issues = []api.Ticket{
		{ID: "1", Title: "a", Body: "Reach: 1\nImpact: 1\nConfidence: 1\nEffort: 1\nValue: 1\nUrgency: 1\nRisk: 1\nSize: 9"},
		{ID: "2", Title: "b", Body: "Reach: 1\nImpact: 1\nConfidence: 1\nEffort: 9\nValue: 9\nUrgency: 9\nRisk: 9\nSize: 1"},
	}
	// The builtin method is rice, so #1 leads.
	if _, err := mcp.BacklogRun(context.Background(), "", "prioritize", "--approve"); err != nil || m.rank["1"] != 1 {
		t.Fatalf("rice: rank=%v err=%v", m.rank, err)
	}
	if _, err := mcp.BacklogRun(context.Background(), "", "prioritize", "--method", "wsjf", "--approve"); err != nil || m.rank["2"] != 1 {
		t.Fatalf("wsjf: rank=%v err=%v", m.rank, err)
	}
	if _, err := mcp.BacklogRun(context.Background(), "", "prioritize", "--method", "moscow"); err == nil {
		t.Fatal("want an error for an unknown method")
	}
}
