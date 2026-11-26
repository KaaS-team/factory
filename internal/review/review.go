// Package review runs review-only checks: each check gets its own disposable worktree seeded from
// HEAD plus the uncommitted changes, an agent edits it under a diff-only prompt, and the edits come
// back as a patch. An empty patch means the check passed.
package review

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/KaaS-team/factory/internal/reviewlearn"
)

// Status is how one check ended.
type Status string

// The statuses.
const (
	StatusPass    Status = "pass"    // the agent left the worktree unchanged
	StatusFail    Status = "fail"    // the agent edited files; Patch holds the edits
	StatusError   Status = "error"   // the agent or git failed; Output says why
	StatusTimeout Status = "timeout" // the check ran past Runner.Timeout
)

// Check is one review rule. Instructions is its body text.
type Check struct {
	Name         string
	Instructions string
}

// Source lists the checks to run. The check-pack loader implements it.
type Source interface {
	Checks(ctx context.Context) ([]Check, error)
}

// Agent runs one prompt with dir as its working directory and returns what it said.
type Agent interface {
	Run(ctx context.Context, dir, prompt string) (output string, err error)
}

// Result is the record of one check.
type Result struct {
	Name     string
	Status   Status
	Patch    string
	Output   string
	Duration time.Duration
	// Findings are the cited findings that survived the station; Dropped says why the rest did not.
	Findings []Finding
	Dropped  []Dropped
}

// DefaultDiffCap is the diff size in bytes put into a prompt when Runner.DiffCap is zero.
const DefaultDiffCap = 64 * 1024

// Runner runs every check from Source against the repository at Repo, one at a time.
type Runner struct {
	Repo    string
	Source  Source
	Agent   Agent
	Timeout time.Duration // per check; zero means no limit
	DiffCap int           // bytes of diff in the prompt; zero means DefaultDiffCap
	// Base is the ref the diff in the prompt starts from. Empty reviews only the uncommitted changes.
	Base string
	// Second is an optional reviewer from another model family, for example an ACP harness agent.
	// Its findings merge with the first reviewer's by citation. Its failure is not the check's.
	Second Agent
}

// Run returns one Result per check. A failing check does not stop the others; the error is for
// failures that stop the whole run, such as an unreadable source.
func (r Runner) Run(ctx context.Context) ([]Result, error) {
	checks, err := r.Source.Checks(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Result, 0, len(checks))
	for _, c := range checks {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		out = append(out, r.runOne(ctx, c))
	}
	return out, nil
}

func (r Runner) runOne(ctx context.Context, c Check) (res Result) {
	start := time.Now()
	res = Result{Name: c.Name, Status: StatusError}
	defer func() { res.Duration = time.Since(start) }()

	w, err := addWorktree(ctx, r.Repo)
	if err != nil {
		res.Output = err.Error()
		return res
	}
	defer w.remove() // runs on a panic in the agent too

	prompt, err := r.prompt(ctx, w, c)
	if err != nil {
		res.Output = err.Error()
		return res
	}
	actx := ctx
	if r.Timeout > 0 {
		var cancel context.CancelFunc
		actx, cancel = context.WithTimeout(ctx, r.Timeout)
		defer cancel()
	}
	res.Output, err = r.Agent.Run(actx, w.dir, prompt)
	switch {
	case errors.Is(actx.Err(), context.DeadlineExceeded):
		res.Status = StatusTimeout
		return res
	case err != nil:
		res.Output = strings.TrimSpace(res.Output + "\n" + err.Error())
		return res
	}
	if res.Patch, err = w.patch(context.WithoutCancel(ctx)); err != nil {
		res.Output = err.Error()
		return res
	}
	res.Status = StatusPass
	if res.Patch != "" {
		res.Status = StatusFail
	}
	r.station(actx, w, prompt, &res)
	return res
}

// station parses cited findings out of the agent's output and filters them. Output with no findings
// array is a plain verdict, so a parse failure is not an error.
func (r Runner) station(ctx context.Context, w *worktree, prompt string, res *Result) {
	base := r.Base
	if base == "" {
		base = "HEAD~1"
	}
	diff, err := git(ctx, w.dir, nil, "diff", base, "HEAD")
	if err != nil {
		return
	}
	st := &Station{}
	if rules, err := (reviewlearn.Learner{Root: r.Repo}).Rules(); err == nil {
		st.Rules = rules
	}
	var seconds []Reviewer
	if r.Second != nil {
		seconds = append(seconds, agentReviewer{name: "second", agent: r.Second, dir: w.dir, prompt: prompt})
	}
	res.Findings, res.Dropped, _ = st.Round(ctx, diff, outputReviewer{res.Output}, seconds...)
}

type outputReviewer struct{ out string }

func (outputReviewer) Name() string { return "primary" }
func (o outputReviewer) Review(context.Context, string) ([]Finding, error) {
	return ParseFindings(o.out)
}

type agentReviewer struct {
	name, dir, prompt string
	agent             Agent
}

func (a agentReviewer) Name() string { return a.name }
func (a agentReviewer) Review(ctx context.Context, _ string) ([]Finding, error) {
	out, err := a.agent.Run(ctx, a.dir, a.prompt)
	if err != nil {
		return nil, err
	}
	return ParseFindings(out)
}

// prompt builds the diff-only prompt: the check's rules, the changed files, the diff cut at the cap,
// and the four scope rules.
func (r Runner) prompt(ctx context.Context, w *worktree, c Check) (string, error) {
	base := r.Base
	if base == "" {
		base = "HEAD~1" // the seeded commit's parent: the uncommitted changes
	}
	diff, err := git(ctx, w.dir, nil, "diff", base, "HEAD")
	if err != nil {
		return "", err
	}
	names, err := git(ctx, w.dir, nil, "diff", "-z", "--name-only", base, "HEAD")
	if err != nil {
		return "", err
	}
	limit := r.DiffCap
	if limit <= 0 {
		limit = DefaultDiffCap
	}
	truncated := len(diff) > limit
	if truncated {
		diff = strings.ToValidUTF8(diff[:limit], "")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "You are a review agent. Check ONLY the changed lines in the diff below against this rule.\n\n## Rule: %s\n%s\n\n", c.Name, strings.TrimSpace(c.Instructions))
	b.WriteString("## Scope\n")
	b.WriteString("- Do NOT read, scan or report on files outside the diff.\n")
	b.WriteString("- Do NOT report pre-existing issues; in a changed file, only the changed lines are in scope.\n")
	b.WriteString("- You may read a changed file for context, but do not flag unchanged parts of it.\n")
	b.WriteString("- Edit only the changed files, and only to fix a violation of the rule above. If there is none, edit nothing and say so.\n\n")
	b.WriteString("## Findings\n")
	b.WriteString("Report each problem as an entry in a JSON array with the keys kind, file, line, quote, message and confidence (0 to 1). The line is the line number in the new file and the quote is that line's text. An entry without a valid line and quote is discarded.\n\n")
	b.WriteString("## Changed files\n")
	for _, f := range strings.FieldsFunc(names, func(r rune) bool { return r == 0 }) {
		fmt.Fprintf(&b, "- %s\n", f)
	}
	b.WriteString("\n## Diff\n```diff\n" + strings.TrimRight(diff, "\n") + "\n```\n")
	if truncated {
		fmt.Fprintf(&b, "\nNote: the diff was truncated at %d bytes. Read the changed files listed above for the rest; do not go beyond them.\n", limit)
	}
	return b.String(), nil
}
