package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/KaaS-team/factory/internal/api/porttest"
)

func TestCommandWithoutControlFails(t *testing.T) {
	c := NewCommand()
	if c.Use != "board" {
		t.Fatalf("Use = %q", c.Use)
	}
	c.SetContext(context.Background())
	c.SetArgs(nil)
	if err := c.Execute(); err == nil || !strings.Contains(err.Error(), "no control") {
		t.Fatalf("err = %v", err)
	}
}

func TestCommandRunsAndQuits(t *testing.T) {
	f := porttest.NewControl(fixedBoard())
	c := NewCommand()
	c.SetContext(WithControl(context.Background(), f))
	c.SetIn(strings.NewReader("q"))
	var out strings.Builder
	c.SetOut(&out)
	c.SetArgs(nil)
	if err := c.Execute(); err != nil {
		t.Fatal(err)
	}
}

func TestCommandReplayRunsAndQuits(t *testing.T) {
	c := NewCommand()
	c.SetContext(context.Background())
	c.SetIn(strings.NewReader("q"))
	var out strings.Builder
	c.SetOut(&out)
	c.SetArgs([]string{"--replay", "../../docs/demo/fleet.ndjson", "--speed", "1000"})
	if err := c.Execute(); err != nil {
		t.Fatal(err)
	}
	c.SetArgs([]string{"--replay", "missing.ndjson"})
	if err := c.Execute(); err == nil {
		t.Fatal("a missing replay file must fail")
	}
}

// gated is stdin that sends its keys only once open is closed, so a screen sees them after it loaded.
type gated struct {
	open <-chan struct{}
	keys *strings.Reader
}

func (g gated) Read(p []byte) (int, error) {
	<-g.open
	return g.keys.Read(p)
}

func runReview(t *testing.T, keys string, args ...string) (out string, approved int, err error) {
	t.Helper()
	c := NewReviewCommand(ReviewDeps{
		Load: func(path string) (PlanReview, error) {
			if path != "prd.md" {
				return PlanReview{}, errors.New("no such file")
			}
			return fixedReview(), nil
		},
		Approve: func(_ *cobra.Command, path string) error { approved++; return nil },
	})
	c.SetContext(context.Background())
	c.SetIn(strings.NewReader(keys))
	var b strings.Builder
	c.SetOut(&b)
	c.SetErr(&b)
	c.SetArgs(args)
	err = c.Execute()
	return b.String(), approved, err
}

func TestReviewCommandApproveFiles(t *testing.T) {
	if _, n, err := runReview(t, "ay", "--from-prd", "prd.md"); err != nil || n != 1 {
		t.Fatalf("approve: filed %d times, err %v", n, err)
	}
}

func TestReviewCommandReviseFilesNothing(t *testing.T) {
	out, n, err := runReview(t, "rsplit T3\r", "--from-prd", "prd.md")
	if err != nil || n != 0 {
		t.Fatalf("revise: filed %d times, err %v", n, err)
	}
	if !strings.Contains(out, "Revision requested:\nsplit T3\n") {
		t.Fatalf("output lacks the note: %q", out)
	}
}

func TestReviewCommandQuitFilesNothing(t *testing.T) {
	out, n, err := runReview(t, "q", "--from-prd", "prd.md")
	if err != nil || n != 0 || !strings.Contains(out, "nothing was filed") {
		t.Fatalf("quit: filed %d, err %v, out %q", n, err, out)
	}
}

func TestReviewCommandNeedsAPRD(t *testing.T) {
	if _, _, err := runReview(t, "q"); err == nil || !strings.Contains(err.Error(), "--from-prd") {
		t.Fatalf("err = %v", err)
	}
	if _, _, err := runReview(t, "q", "--from-prd", "missing.md"); err == nil || !strings.Contains(err.Error(), "no such file") {
		t.Fatalf("err = %v", err)
	}
}

func runReady(t *testing.T, r Readiness) error {
	t.Helper()
	open := make(chan struct{})
	c := NewReadyCommand(func(_ *cobra.Command, dir string) ReadyFuncs {
		if dir != "repo" {
			t.Errorf("dir = %q", dir)
		}
		return ReadyFuncs{Check: func(context.Context) (Readiness, error) { close(open); return r, nil }}
	})
	c.SetContext(context.Background())
	c.SetIn(gated{open: open, keys: strings.NewReader("q")})
	var b strings.Builder
	c.SetOut(&b)
	c.SetArgs([]string{"--dir", "repo"})
	return c.Execute()
}

// The ready command exits non-zero while a check fails, as `first-run --ready` does.
func TestReadyCommandExitStatus(t *testing.T) {
	if err := runReady(t, mixedReadiness()); !errors.Is(err, ErrNotReady) {
		t.Fatalf("failing repo: err = %v", err)
	}
	if err := runReady(t, passingReadiness()); err != nil {
		t.Fatalf("ready repo: err = %v", err)
	}
}

func TestReadyResultBeforeChecks(t *testing.T) {
	m := NewReport(context.Background(), ReadyFuncs{})
	if err := readyResult(m); err == nil {
		t.Fatal("a report with no checks passed")
	}
}
