// Package manager is the coordinator over the worker sessions. Each pass it looks at active runs and
// their PRs: it nudges or restarts a run with no progress, handles a PR with failing CI by class,
// and feeds review comments back into the task. Every action is an audit event.
package manager

import (
	"context"
	"fmt"
	"time"

	"github.com/KaaS-team/factory/internal/ciclass"
	"github.com/KaaS-team/factory/internal/plan"
	"github.com/KaaS-team/factory/internal/proof"
)

// Run is one active run the manager watches.
type Run struct {
	Ticket   string
	Attempt  int
	LastPush time.Time // last sign of progress
	PR       int       // 0 when no PR yet
}

// Comment is a review comment or a requested change on a PR.
type Comment struct {
	ID     string
	Author string
	Body   string
}

// Failure is one failing CI check with its log.
type Failure struct {
	Check string
	Log   string
}

// Source reads the outside world.
type Source interface {
	Runs(ctx context.Context) ([]Run, error)
	// Failures returns failing checks on the PR head. Empty means green or pending.
	Failures(ctx context.Context, pr int) ([]Failure, error)
	// NewComments returns review comments the manager has not yet delivered.
	NewComments(ctx context.Context, pr int) ([]Comment, error)
	// MarkDelivered records that the comments reached the task.
	MarkDelivered(ctx context.Context, pr int, ids []string) error
}

// ReportSource is an optional Source that can read a PR's build report. The manager checks the
// report with proof.CheckReport, the same check that blocks a merge, and steers the agent while it
// is incomplete.
type ReportSource interface {
	// Report returns the goal the build prompt carried and the report in the PR. ok is false when the
	// ticket had no goal checklist or the report is not there yet.
	Report(ctx context.Context, pr int) (g plan.Goal, report string, ok bool, err error)
}

// Actor changes things.
type Actor interface {
	Steer(ctx context.Context, ticket, message string) error
	Retry(ctx context.Context, ticket string) error
	RerunCI(ctx context.Context, pr int) error
	// Attention raises an item for a human.
	Attention(ctx context.Context, ticket, reason string) error
}

// Audit records every manager action.
type Audit interface {
	Record(ctx context.Context, action, ticket, detail string, err error)
}

// Config sets the thresholds.
type Config struct {
	Interval   time.Duration // between passes
	StallAfter time.Duration // no progress for this long: nudge
	RestartAt  time.Duration // no progress for this long: restart (0 = never)
	MaxReruns  int           // reruns per PR and head before the failure goes to a human
	LogBytes   int           // size of the log excerpt sent to the agent
	Now        func() time.Time
}

// Manager runs the loop.
type Manager struct {
	cfg    Config
	src    Source
	act    Actor
	audit  Audit
	nudged map[string]bool // ticket+attempt already nudged
	reruns map[int]int
	sent   map[string]bool // ticket+PR+failure digest already sent to the agent
}

// New returns a manager with defaults filled in.
func New(cfg Config, src Source, act Actor, audit Audit) *Manager {
	if cfg.Interval <= 0 {
		cfg.Interval = 5 * time.Minute
	}
	if cfg.StallAfter <= 0 {
		cfg.StallAfter = 30 * time.Minute
	}
	if cfg.MaxReruns <= 0 {
		cfg.MaxReruns = 2
	}
	if cfg.LogBytes <= 0 {
		cfg.LogBytes = 4000
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Manager{cfg: cfg, src: src, act: act, audit: audit,
		nudged: map[string]bool{}, reruns: map[int]int{}, sent: map[string]bool{}}
}

// Run passes every Interval until ctx ends.
func (m *Manager) Run(ctx context.Context) error {
	t := time.NewTicker(m.cfg.Interval)
	defer t.Stop()
	for {
		_ = m.Pass(ctx)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

// Pass looks at every active run once. It keeps going past a failing run and returns the first error.
func (m *Manager) Pass(ctx context.Context) error {
	runs, err := m.src.Runs(ctx)
	if err != nil {
		return fmt.Errorf("manager: list runs: %w", err)
	}
	var first error
	for _, r := range runs {
		if err := m.one(ctx, r); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (m *Manager) one(ctx context.Context, r Run) error {
	var first error
	keep := func(err error) {
		if err != nil && first == nil {
			first = err
		}
	}
	if r.PR > 0 {
		keep(m.report(ctx, r))
		keep(m.comments(ctx, r))
		keep(m.ci(ctx, r))
	}
	keep(m.stall(ctx, r))
	return first
}

func (m *Manager) stall(ctx context.Context, r Run) error {
	idle := m.cfg.Now().Sub(r.LastPush)
	key := fmt.Sprintf("%s#%d", r.Ticket, r.Attempt)
	switch {
	case m.cfg.RestartAt > 0 && idle >= m.cfg.RestartAt:
		err := m.act.Retry(ctx, r.Ticket)
		m.audit.Record(ctx, "manager.restart", r.Ticket, fmt.Sprintf("idle %s", idle.Round(time.Minute)), err)
		return err
	case idle >= m.cfg.StallAfter && !m.nudged[key]:
		m.nudged[key] = true
		err := m.act.Steer(ctx, r.Ticket, "No progress for a while. Say what is blocking you, or push what you have and continue.")
		m.audit.Record(ctx, "manager.nudge", r.Ticket, fmt.Sprintf("idle %s", idle.Round(time.Minute)), err)
		return err
	}
	return nil
}

// report steers the agent once per distinct list of gaps in its build report.
func (m *Manager) report(ctx context.Context, r Run) error {
	rs, ok := m.src.(ReportSource)
	if !ok {
		return nil
	}
	g, rep, ok, err := rs.Report(ctx, r.PR)
	if err != nil || !ok {
		return err
	}
	res := proof.CheckReport(g, rep)
	key := fmt.Sprintf("report|%s|%d|%s", r.Ticket, r.PR, res.Reason)
	if res.OK || m.sent[key] {
		return nil
	}
	m.sent[key] = true
	err = m.act.Steer(ctx, r.Ticket, "Verify will fail this PR: "+res.Reason+". Tick each goal row with the test that proves it, answer each checkpoint, update the report and push.")
	m.audit.Record(ctx, "manager.report_gap", r.Ticket, fmt.Sprintf("pr %d", r.PR), err)
	return err
}

func (m *Manager) comments(ctx context.Context, r Run) error {
	cs, err := m.src.NewComments(ctx, r.PR)
	if err != nil || len(cs) == 0 {
		return err
	}
	msg := "Review feedback on your PR. Address each point and push a fix.\n"
	ids := make([]string, 0, len(cs))
	for _, c := range cs {
		msg += fmt.Sprintf("\n- %s: %s", c.Author, c.Body)
		ids = append(ids, c.ID)
	}
	err = m.act.Steer(ctx, r.Ticket, msg)
	m.audit.Record(ctx, "manager.feed_review", r.Ticket, fmt.Sprintf("pr %d, %d comments", r.PR, len(cs)), err)
	if err != nil {
		return err // not marked delivered, so the next pass tries again
	}
	return m.src.MarkDelivered(ctx, r.PR, ids)
}

func (m *Manager) ci(ctx context.Context, r Run) error {
	fs, err := m.src.Failures(ctx, r.PR)
	if err != nil || len(fs) == 0 {
		return err
	}
	var first error
	for _, f := range fs {
		class := ciclass.Classify(f.Log)
		detail := fmt.Sprintf("pr %d, check %s, class %s", r.PR, f.Check, class)
		switch class {
		case ciclass.Retryable:
			if m.reruns[r.PR] >= m.cfg.MaxReruns {
				err = m.attend(ctx, r, f, class, fmt.Sprintf("CI check %s keeps failing after %d reruns", f.Check, m.reruns[r.PR]), detail+", rerun limit")
				break
			}
			m.reruns[r.PR]++
			err = m.act.RerunCI(ctx, r.PR)
			m.audit.Record(ctx, "manager.rerun_ci", r.Ticket, detail, err)
		case ciclass.Agent:
			ex := ciclass.Excerpt(f.Log, m.cfg.LogBytes)
			key := fmt.Sprintf("%s|%d|%s|%s", r.Ticket, r.PR, f.Check, ex)
			if m.sent[key] {
				continue
			}
			m.sent[key] = true
			err = m.act.Steer(ctx, r.Ticket, fmt.Sprintf("CI check %s failed on your PR. Fix it and push.\n\n%s", f.Check, ex))
			m.audit.Record(ctx, "manager.feed_ci", r.Ticket, detail, err)
		default:
			err = m.attend(ctx, r, f, class, fmt.Sprintf("CI check %s failed (%s)", f.Check, class), detail)
		}
		if err != nil && first == nil {
			first = err
		}
	}
	return first
}

// attend raises one attention item per ticket, PR, check and class, however many passes see it.
func (m *Manager) attend(ctx context.Context, r Run, f Failure, class ciclass.Class, reason, detail string) error {
	key := fmt.Sprintf("attn|%s|%d|%s|%s", r.Ticket, r.PR, f.Check, class)
	if m.sent[key] {
		return nil
	}
	m.sent[key] = true
	err := m.act.Attention(ctx, r.Ticket, reason)
	m.audit.Record(ctx, "manager.attention", r.Ticket, detail, err)
	return err
}
