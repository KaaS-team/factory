package firstrun

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Status is the outcome of one readiness check.
type Status string

// The three outcomes. Unknown means the check could not run, for example branch protection with no
// GitHub repo given. Unknown does not fail the readiness check.
const (
	Pass    Status = "pass"
	Fail    Status = "fail"
	Unknown Status = "unknown"
)

// Check is one readiness result.
type Check struct {
	Name   string
	Status Status
	Detail string
	// Fix says what --fix does; empty means the failure needs a human, and Detail says what to do.
	Fix string
	fix func(dir string) error
}

// Ready is the readiness report: can an agent work in this repo without failing for a reason the
// repo could have fixed first.
type Ready struct {
	Dir    string
	Checks []Check
}

// OK is true when no check failed.
func (r Ready) OK() bool {
	for _, c := range r.Checks {
		if c.Status == Fail {
			return false
		}
	}
	return true
}

// Runner runs a command in dir and returns its combined output.
type Runner func(ctx context.Context, dir, name string, args ...string) (string, error)

// Protection reports whether the default branch of a GitHub repo is protected.
type Protection func(ctx context.Context) (bool, error)

// ReadyOptions configures CheckReady.
type ReadyOptions struct {
	Dir string
	// Run runs the build and test commands; nil runs them for real.
	Run Runner
	// Protected answers the branch protection check; nil leaves it unknown.
	Protected Protection
}

func execRunner(ctx context.Context, dir, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func exists(dir, rel string) bool {
	_, err := os.Stat(filepath.Join(dir, rel))
	return err == nil
}

func existsAny(dir string, rels ...string) bool {
	for _, r := range rels {
		if exists(dir, r) {
			return true
		}
	}
	return false
}

// CheckReady runs every readiness check and changes nothing.
func CheckReady(ctx context.Context, o ReadyOptions) Ready {
	dir := o.Dir
	if dir == "" {
		dir = "."
	}
	run := o.Run
	if run == nil {
		run = execRunner
	}
	r := Ready{Dir: dir}
	r.Checks = append(r.Checks,
		stepCheck(ctx, dir, run, "builds", "go", "build", "./..."),
		stepCheck(ctx, dir, run, "tests run", "go", "test", "./..."),
		agentsCheck(dir),
		gateCheck(dir),
		ciCheck(dir),
		protectionCheck(ctx, o.Protected),
	)
	return r
}

// stepCheck runs a Go step. A repo without go.mod has no step the factory knows, so it is unknown.
func stepCheck(ctx context.Context, dir string, run Runner, name, bin string, args ...string) Check {
	if !exists(dir, "go.mod") {
		return Check{Name: name, Status: Unknown, Detail: "no go.mod; only Go repos are built and tested here"}
	}
	out, err := run(ctx, dir, bin, args...)
	if err != nil {
		return Check{Name: name, Status: Fail, Detail: "`" + bin + " " + strings.Join(args, " ") + "` failed: " + lastLine(out, err)}
	}
	return Check{Name: name, Status: Pass}
}

func lastLine(out string, err error) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if l := strings.TrimSpace(lines[len(lines)-1]); l != "" {
		return l
	}
	return err.Error()
}

func agentsCheck(dir string) Check {
	if exists(dir, "AGENTS.md") {
		return Check{Name: "AGENTS.md", Status: Pass}
	}
	return Check{Name: "AGENTS.md", Status: Fail, Detail: "no AGENTS.md; agents start with no rules for this repo",
		Fix: "write an AGENTS.md skeleton", fix: writeAgents}
}

func gateCheck(dir string) Check {
	if existsAny(dir, "gate.sh", "justfile", "Justfile", "Makefile") {
		return Check{Name: "gate script", Status: Pass}
	}
	return Check{Name: "gate script", Status: Fail, Detail: "no gate.sh, justfile or Makefile; agents cannot tell when work is done",
		Fix: "add a gate.sh that builds, vets and tests", fix: writeGate}
}

func ciCheck(dir string) Check {
	ws, _ := filepath.Glob(filepath.Join(dir, ".github", "workflows", "*.y*ml"))
	if len(ws) > 0 || existsAny(dir, ".gitlab-ci.yml", ".circleci/config.yml", "Jenkinsfile", "azure-pipelines.yml") {
		return Check{Name: "CI exists", Status: Pass}
	}
	return Check{Name: "CI exists", Status: Fail, Detail: "no CI config; nothing checks a pull request",
		Fix: "add .github/workflows/ci.yml that runs gate.sh", fix: writeCI}
}

func protectionCheck(ctx context.Context, p Protection) Check {
	const name = "branch protection"
	if p == nil {
		return Check{Name: name, Status: Unknown, Detail: "pass --gh owner/repo to check it"}
	}
	ok, err := p(ctx)
	switch {
	case err != nil:
		return Check{Name: name, Status: Unknown, Detail: "could not read it: " + err.Error()}
	case ok:
		return Check{Name: name, Status: Pass}
	}
	return Check{Name: name, Status: Fail, Detail: "the default branch is not protected; turn on required checks in the repo settings (needs an admin)"}
}

// Fixed names one check that --fix repaired.
type Fixed struct{ Name, What string }

// FixReady applies the in-place fix of every failed check that has one, then checks again. It
// returns what it fixed and the new report. A check without a fix is left for a human.
func FixReady(ctx context.Context, o ReadyOptions) ([]Fixed, Ready, error) {
	r := CheckReady(ctx, o)
	var done []Fixed
	for _, c := range r.Checks {
		if c.Status != Fail || c.fix == nil {
			continue
		}
		if err := c.fix(r.Dir); err != nil {
			return done, r, fmt.Errorf("fix %s: %w", c.Name, err)
		}
		done = append(done, Fixed{c.Name, c.Fix})
	}
	return done, CheckReady(ctx, o), nil
}

// Markdown renders the report.
func (r Ready) Markdown(fixed []Fixed) string {
	var b strings.Builder
	b.WriteString("## Readiness\n\n")
	for _, c := range r.Checks {
		fmt.Fprintf(&b, "- %s: %s", c.Name, c.Status)
		if c.Detail != "" {
			b.WriteString(". " + c.Detail)
		}
		if c.Status == Fail && c.Fix != "" {
			b.WriteString(". `--fix` will " + c.Fix)
		}
		b.WriteString("\n")
	}
	for _, f := range fixed {
		fmt.Fprintf(&b, "\nFixed %s: %s.\n", f.Name, f.What)
	}
	return b.String()
}

func writeNew(dir, rel, text string, mode os.FileMode) error {
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	_, werr := io.WriteString(f, text)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}

func writeAgents(dir string) error {
	name := filepath.Base(mustAbs(dir))
	return writeNew(dir, "AGENTS.md", fmt.Sprintf(`# %s

Describe what this repo does in one sentence.

## Gate

`+"`./gate.sh`"+` is the gate. It must end with zero errors and zero warnings. Run it before every push.

## Boundaries

- List the paths an agent must not edit.
- List the packages and who owns them.

## Package map

| Path | Holds |
|---|---|
| `+"`.`"+` | fill in |
`, name), 0o644)
}

func mustAbs(p string) string {
	if a, err := filepath.Abs(p); err == nil {
		return a
	}
	return p
}

func writeGate(dir string) error {
	return writeNew(dir, "gate.sh", `#!/bin/sh
# The gate: the same steps CI runs. It must end with zero errors and zero warnings.
set -eu
cd "$(dirname "$0")"
test -z "$(gofmt -l .)" || { echo "gofmt needed:"; gofmt -l .; exit 1; }
go vet ./...
go test ./...
go build ./...
`, 0o755)
}

func writeCI(dir string) error {
	return writeNew(dir, ".github/workflows/ci.yml", `name: ci
on:
  pull_request:
  push:
    branches: [main]
jobs:
  gate:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      - run: ./gate.sh
`, 0o644)
}
