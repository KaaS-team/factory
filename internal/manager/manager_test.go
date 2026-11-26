package manager

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/KaaS-team/factory/internal/plan"
)

type fake struct {
	runs      []Run
	failures  map[int][]Failure
	comments  map[int][]Comment
	delivered []string
	steers    []string // "ticket: message"
	retries   []string
	reruns    []int
	attention []string
	audits    []string
	steerErr  error
}

func (f *fake) Runs(context.Context) ([]Run, error) { return f.runs, nil }
func (f *fake) Failures(_ context.Context, pr int) ([]Failure, error) {
	return f.failures[pr], nil
}
func (f *fake) NewComments(_ context.Context, pr int) ([]Comment, error) {
	cs := f.comments[pr]
	f.comments[pr] = nil
	if f.steerErr != nil && len(cs) > 0 { // keep them for the next pass
		f.comments[pr] = cs
	}
	return cs, nil
}
func (f *fake) MarkDelivered(_ context.Context, _ int, ids []string) error {
	f.delivered = append(f.delivered, ids...)
	return nil
}
func (f *fake) Steer(_ context.Context, t, msg string) error {
	if f.steerErr != nil {
		return f.steerErr
	}
	f.steers = append(f.steers, t+": "+msg)
	return nil
}
func (f *fake) Retry(_ context.Context, t string) error { f.retries = append(f.retries, t); return nil }
func (f *fake) RerunCI(_ context.Context, pr int) error { f.reruns = append(f.reruns, pr); return nil }
func (f *fake) Attention(_ context.Context, t, r string) error {
	f.attention = append(f.attention, t+": "+r)
	return nil
}
func (f *fake) Record(_ context.Context, action, ticket, _ string, _ error) {
	f.audits = append(f.audits, action+" "+ticket)
}

var now = time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)

func mgr(f *fake) *Manager {
	return New(Config{StallAfter: 30 * time.Minute, RestartAt: 2 * time.Hour, MaxReruns: 1, Now: func() time.Time { return now }}, f, f, f)
}

func TestReviewCommentReachesTask(t *testing.T) {
	f := &fake{
		runs:     []Run{{Ticket: "42", Attempt: 1, LastPush: now, PR: 7}},
		comments: map[int][]Comment{7: {{ID: "c1", Author: "alice", Body: "rename Foo to Bar"}}},
	}
	m := mgr(f)
	if err := m.Pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.steers) != 1 || !strings.HasPrefix(f.steers[0], "42: ") || !strings.Contains(f.steers[0], "alice: rename Foo to Bar") {
		t.Fatalf("steers = %q", f.steers)
	}
	if len(f.delivered) != 1 || f.delivered[0] != "c1" {
		t.Fatalf("delivered = %v", f.delivered)
	}
	if len(f.audits) != 1 || f.audits[0] != "manager.feed_review 42" {
		t.Fatalf("audits = %v", f.audits)
	}
	_ = m.Pass(context.Background())
	if len(f.steers) != 1 {
		t.Fatal("comment delivered twice")
	}
}

func TestCommentKeptWhenSteerFails(t *testing.T) {
	f := &fake{
		runs:     []Run{{Ticket: "42", Attempt: 1, LastPush: now, PR: 7}},
		comments: map[int][]Comment{7: {{ID: "c1", Body: "x"}}},
		steerErr: errors.New("session gone"),
	}
	if err := mgr(f).Pass(context.Background()); err == nil {
		t.Fatal("want error")
	}
	if len(f.delivered) != 0 {
		t.Fatal("marked delivered after a failed steer")
	}
}

func TestStall(t *testing.T) {
	f := &fake{runs: []Run{{Ticket: "1", Attempt: 1, LastPush: now.Add(-40 * time.Minute)}, {Ticket: "2", Attempt: 1, LastPush: now.Add(-3 * time.Hour)}, {Ticket: "3", Attempt: 1, LastPush: now.Add(-5 * time.Minute)}}}
	m := mgr(f)
	_ = m.Pass(context.Background())
	_ = m.Pass(context.Background())
	if len(f.steers) != 1 || !strings.HasPrefix(f.steers[0], "1: ") {
		t.Fatalf("nudge once for ticket 1: %q", f.steers)
	}
	if len(f.retries) != 2 || f.retries[0] != "2" {
		t.Fatalf("retries = %v", f.retries)
	}
}

func TestCIByClass(t *testing.T) {
	f := &fake{
		runs: []Run{{Ticket: "a", Attempt: 1, LastPush: now, PR: 1}, {Ticket: "b", Attempt: 1, LastPush: now, PR: 2}, {Ticket: "c", Attempt: 1, LastPush: now, PR: 3}},
		failures: map[int][]Failure{
			1: {{Check: "test", Log: "dial tcp: i/o timeout"}},
			2: {{Check: "test", Log: "--- FAIL: TestX\nparse_test.go:9: got 1 want 2"}},
			3: {{Check: "deploy", Log: "Error: Input required and not supplied: token"}},
		},
		comments: map[int][]Comment{},
	}
	m := mgr(f)
	_ = m.Pass(context.Background())
	_ = m.Pass(context.Background())
	if len(f.reruns) != 1 || f.reruns[0] != 1 {
		t.Fatalf("reruns = %v (retryable reruns once, then limit)", f.reruns)
	}
	if len(f.steers) != 1 || !strings.Contains(f.steers[0], "got 1 want 2") {
		t.Fatalf("agent failure must reach the task once: %q", f.steers)
	}
	// ticket a hits the rerun limit on pass two; ticket c is repo_setup, raised once over two passes.
	if len(f.attention) != 2 {
		t.Fatalf("attention = %q", f.attention)
	}
}

// reportFake is a fake Source that also reads the build report.
type reportFake struct {
	*fake
	goal   plan.Goal
	report string
	has    bool
}

func (r *reportFake) Report(context.Context, int) (plan.Goal, string, bool, error) {
	return r.goal, r.report, r.has, nil
}

func completeReport(g plan.Goal) string {
	var s string
	for _, row := range g.Rows {
		s += "- [x] " + row.ID + ": " + row.Text + " | test: TestA\n"
	}
	for _, c := range plan.Checkpoints {
		s += "- [x] " + c.ID + ": none\n"
	}
	return s
}

func TestIncompleteBuildReportSteersOnce(t *testing.T) {
	g := plan.ParseGoal("## Acceptance\n- AC-1: a\n")
	f := &fake{runs: []Run{{Ticket: "42", Attempt: 1, LastPush: now, PR: 7}}, comments: map[int][]Comment{}}
	src := &reportFake{fake: f, goal: g, report: "- [x] AC-1: a | test: TestA\n", has: true}
	m := New(Config{Now: func() time.Time { return now }}, src, f, f)
	for i := 0; i < 2; i++ {
		if err := m.Pass(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.steers) != 1 || !strings.Contains(f.steers[0], "checkpoint files-outside-plan is unanswered") || !strings.Contains(f.steers[0], "Verify will fail") {
		t.Fatalf("steers = %q, want one naming the unanswered checkpoint", f.steers)
	}
	if len(f.audits) != 1 || f.audits[0] != "manager.report_gap 42" {
		t.Fatalf("audits = %v", f.audits)
	}
	src.report = completeReport(g) // fixed: the manager stays quiet
	_ = m.Pass(context.Background())
	if len(f.steers) != 1 {
		t.Fatalf("a complete report was steered: %q", f.steers)
	}
}

func TestBuildReportNotCheckedWithoutGoalOrSource(t *testing.T) {
	g := plan.ParseGoal("## Acceptance\n- AC-1: a\n")
	f := &fake{runs: []Run{{Ticket: "42", Attempt: 1, LastPush: now, PR: 7}}, comments: map[int][]Comment{}}
	_ = mgr(f).Pass(context.Background()) // f is not a ReportSource
	src := &reportFake{fake: f, goal: g, has: false}
	_ = New(Config{Now: func() time.Time { return now }}, src, f, f).Pass(context.Background())
	if len(f.steers) != 0 {
		t.Fatalf("steers = %q, want none", f.steers)
	}
}
