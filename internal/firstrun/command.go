package firstrun

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/KaaS-team/factory/internal/firstrun/sarif"
)

// Option configures NewCommand.
type Option func(*cmdOpts)

type cmdOpts struct {
	exec func(context.Context) (Exec, error)
	prot func(context.Context) (Protection, error)
}

// WithExec lets `--fix` dispatch the first fix and open its draft pull request. f builds the Exec
// only when --fix runs, so a plain report needs no token. Without it the command only reports.
func WithExec(f func(context.Context) (Exec, error)) Option { return func(o *cmdOpts) { o.exec = f } }

// WithProtection supplies the branch protection check of `--ready`. f builds it only when --ready
// runs, so a plain report needs no token. Without it the check reads "unknown".
func WithProtection(f func(context.Context) (Protection, error)) Option {
	return func(o *cmdOpts) { o.prot = f }
}

// NewCommand returns `factory first-run [--dry-run] [--gh owner/repo]`.
func NewCommand(opts ...Option) *cobra.Command {
	var co cmdOpts
	for _, f := range opts {
		f(&co)
	}
	var f runFlags
	c := &cobra.Command{
		Use:   "first-run",
		Short: "Audit this repo and show what the factory would fix, at no cost",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runFirstRun(cmd, &f, co)
		},
	}
	f.register(c, co.exec != nil)
	return c
}

// runFlags holds the parsed flag values of the first-run command.
type runFlags struct {
	opts               Options
	annPath, sarifPath string
	fix                bool
	ready              bool
	dir                string
}

func (f *runFlags) register(c *cobra.Command, canFix bool) {
	c.Flags().BoolVar(&f.opts.DryRun, "dry-run", false, "stop after the report; say what would be dispatched")
	usage := "with --ready, repair failed checks in place"
	if canFix {
		usage = "dispatch one build for the first covered finding and open a draft pull request; with --ready, repair failed checks in place instead"
	}
	c.Flags().BoolVar(&f.fix, "fix", false, usage)
	c.Flags().BoolVar(&f.ready, "ready", false, "check the repo is ready for agents: builds, tests, AGENTS.md, gate script, CI, branch protection; with --fix, repair what can be repaired in place")
	c.Flags().StringVar(&f.dir, "dir", ".", "the repository --ready checks")
	c.Flags().StringVar(&f.opts.GH, "gh", "", "also audit this GitHub repo (owner/repo)")
	c.Flags().StringVar(&f.annPath, "annotations", defaultAnnotations, "reason-tagged suppressions file; the default is optional")
	c.Flags().StringVar(&f.sarifPath, "sarif", "", "also write the findings to this file as SARIF 2.1.0")
}

// runFirstRun audits, prints the report, writes SARIF if asked, and with --fix opens the draft PR.
func runFirstRun(cmd *cobra.Command, f *runFlags, co cmdOpts) error {
	if f.ready {
		return runReady(cmd, f, co)
	}
	ann, err := loadAnnotations(f.annPath, cmd.Flags().Changed("annotations"))
	if err != nil {
		return err
	}
	f.opts.Annotations = ann
	r, err := Run(cmd.Context(), f.opts)
	if err != nil {
		return err
	}
	md, err := r.Markdown()
	if err != nil {
		return err
	}
	if err := writeSARIF(r, f.sarifPath); err != nil {
		return err
	}
	if _, err = fmt.Fprint(cmd.OutOrStdout(), md); err != nil || !f.fix {
		return err
	}
	if co.exec == nil {
		return errors.New("--fix without --ready needs a dispatcher, which is not wired here")
	}
	return runFix(cmd, r, co)
}

// writeSARIF writes the findings to path; an empty path writes nothing.
func writeSARIF(r Report, path string) error {
	if path == "" {
		return nil
	}
	js, err := r.SARIF("")
	if err != nil {
		return err
	}
	return os.WriteFile(path, js, 0o644)
}

// runFix dispatches the first fix and prints the plan, or the PR number and scorecard.
func runFix(cmd *cobra.Command, r Report, co cmdOpts) error {
	e, err := co.exec(cmd.Context())
	if err != nil {
		return err
	}
	res, err := Execute(cmd.Context(), r, e)
	if err != nil {
		return err
	}
	if res.Plan != "" {
		_, err = fmt.Fprintln(cmd.OutOrStdout(), res.Plan)
		return err
	}
	js, err := res.Scorecard.JSON()
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "\nDraft pull request #%d opened.\n\n%s\n", res.PR, js)
	return err
}

const defaultAnnotations = ".factory/annotations.yml"

// loadAnnotations reads the annotations file. A missing file is an error only when the user named it.
func loadAnnotations(path string, explicit bool) (sarif.Config, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) && !explicit {
			return sarif.Config{}, nil
		}
		return sarif.Config{}, err
	}
	defer func() { _ = f.Close() }()
	return sarif.ParseAnnotations(f)
}

// runReady prints the readiness report. With --fix it repairs in place first, prints what it did, and
// fails when a check is still red, so a script can tell.
func runReady(cmd *cobra.Command, f *runFlags, co cmdOpts) error {
	o := ReadyOptions{Dir: f.dir}
	if co.prot != nil {
		if p, err := co.prot(cmd.Context()); err == nil {
			o.Protected = p
		}
	}
	var fixed []Fixed
	var r Ready
	if f.fix {
		var err error
		if fixed, r, err = FixReady(cmd.Context(), o); err != nil {
			return err
		}
	} else {
		r = CheckReady(cmd.Context(), o)
	}
	if _, err := fmt.Fprint(cmd.OutOrStdout(), r.Markdown(fixed)); err != nil {
		return err
	}
	if !r.OK() {
		return errors.New("the repo is not ready for agents")
	}
	return nil
}
