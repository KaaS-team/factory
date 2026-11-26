package tui

import (
	"context"
	"errors"
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"

	"github.com/KaaS-team/factory/internal/api"
)

type controlKey struct{}

// WithControl returns a context carrying the Control the board command talks to.
func WithControl(ctx context.Context, ctl api.Control) context.Context {
	return context.WithValue(ctx, controlKey{}, ctl)
}

// NewCommand returns the `board` command. The caller attaches a Control with WithControl on the
// command's context before executing it.
func NewCommand() *cobra.Command {
	var (
		replay   string
		speed    float64
		operator bool
	)
	c := &cobra.Command{
		Use:   "board",
		Short: "Open the terminal board",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var ctl api.Control
			opts := []Option{WithOperator(operator)}
			if replay != "" {
				f, err := os.Open(replay)
				if err != nil {
					return fmt.Errorf("board: %w", err)
				}
				defer func() { _ = f.Close() }()
				rp, err := NewReplay(f, speed)
				if err != nil {
					return fmt.Errorf("board: read %s: %w", replay, err)
				}
				ctl = rp
				opts = append(opts, WithClock(rp.Now))
			} else {
				var ok bool
				ctl, ok = cmd.Context().Value(controlKey{}).(api.Control)
				if !ok || ctl == nil {
					return errors.New("board: no control attached to the command context")
				}
			}
			return runScreen(cmd, New(ctl, opts...))
		},
	}
	c.Flags().StringVar(&replay, "replay", "", "play a recorded headless event stream (NDJSON) instead of a live fleet")
	c.Flags().Float64Var(&speed, "speed", 1, "replay speed: 10 plays ten times faster than recorded")
	c.Flags().BoolVar(&operator, "operator", false, "show tokens against budget on each card")
	return c
}

// runScreen runs one screen on the command's terminal until it quits.
func runScreen(cmd *cobra.Command, m tea.Model) error {
	_, err := tea.NewProgram(m,
		tea.WithContext(cmd.Context()),
		tea.WithInput(cmd.InOrStdin()),
		tea.WithOutput(cmd.OutOrStdout())).Run()
	return err
}

// ReviewDeps is what the plan-review command needs from the plan packages, which the TUI does not import.
type ReviewDeps struct {
	// Load reads a PRD file into the review.
	Load func(path string) (PlanReview, error)
	// Approve files the approved plan, as `factory plan --from-prd <path> --approve` does.
	Approve func(cmd *cobra.Command, path string) error
}

// NewReviewCommand returns the `plan-review` command: open a PRD's plan, then approve it or ask for a
// revision.
func NewReviewCommand(d ReviewDeps) *cobra.Command {
	var prd string
	c := &cobra.Command{
		Use:   "plan-review",
		Short: "Review a plan in the terminal: its epic, tickets and acceptance criteria; approve or revise",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if prd == "" {
				return errors.New("plan-review: give --from-prd <file>")
			}
			r, err := d.Load(prd)
			if err != nil {
				return fmt.Errorf("plan-review: %w", err)
			}
			m := NewReview(r)
			if err := runScreen(cmd, m); err != nil {
				return err
			}
			return finishReview(cmd, m.Decision(), func() error { return d.Approve(cmd, prd) })
		},
	}
	c.Flags().StringVar(&prd, "from-prd", "", "review the epic and tickets drafted from the PRD in `file`")
	return c
}

// finishReview acts on the decision once the screen has closed.
func finishReview(cmd *cobra.Command, d Decision, approve func() error) error {
	out := cmd.OutOrStdout()
	switch d.Verdict {
	case VerdictApprove:
		return approve()
	case VerdictRevise:
		_, err := fmt.Fprintf(out, "Revision requested:\n%s\n", d.Note)
		return err
	}
	_, err := fmt.Fprintln(out, "No decision; nothing was filed.")
	return err
}

// ErrNotReady is returned by the ready command when a check still fails as the screen closes.
var ErrNotReady = errors.New("the repo is not ready for agents")

// NewReadyCommand returns the `ready` command: the first-run readiness report as a screen, with a key
// that applies the in-place fixes. funcs builds the checks for a directory.
func NewReadyCommand(funcs func(cmd *cobra.Command, dir string) ReadyFuncs) *cobra.Command {
	var dir string
	c := &cobra.Command{
		Use:   "ready",
		Short: "Open the first-run readiness report: checks, fixes, and a key to apply them",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			m := NewReport(cmd.Context(), funcs(cmd, dir))
			if err := runScreen(cmd, m); err != nil {
				return err
			}
			return readyResult(m)
		},
	}
	c.Flags().StringVar(&dir, "dir", ".", "the repository to check")
	return c
}

// readyResult is the exit status of the ready command: an error when a check failed or could not run.
func readyResult(m *ReportModel) error {
	if err := m.Err(); err != nil {
		return err
	}
	if len(m.Readiness().Checks) == 0 {
		return errors.New("ready: closed before the checks finished")
	}
	if !m.Readiness().OK() {
		return ErrNotReady
	}
	return nil
}
