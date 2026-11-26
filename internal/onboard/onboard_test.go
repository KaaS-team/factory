package onboard_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KaaS-team/factory/internal/api"
	"github.com/KaaS-team/factory/internal/config"
	"github.com/KaaS-team/factory/internal/dispatch/shadow"
	"github.com/KaaS-team/factory/internal/firstrun"
	"github.com/KaaS-team/factory/internal/onboard"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// repo makes a git repo with one commit, an origin remote and a go.mod.
func repo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-b", "main")
	git(t, dir, "config", "user.name", "t")
	git(t, dir, "config", "user.email", "t@example.com")
	git(t, dir, "remote", "add", "origin", "https://github.com/acme/widgets.git")
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module widgets\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-m", "init")
	return dir
}

func okRun(context.Context, string, string, ...string) (string, error) { return "", nil }

func TestRunDetectsWritesShadowConfigAndListsMissing(t *testing.T) {
	dir := repo(t)
	r, err := onboard.Run(t.Context(), onboard.Options{Dir: dir, Ready: runnerOpts()})
	if err != nil {
		t.Fatal(err)
	}
	if r.Detected.Repo != "acme/widgets" || r.Detected.Language != "Go" {
		t.Fatalf("detected %+v", r.Detected)
	}
	data, err := os.ReadFile(filepath.Join(dir, config.RepoFileYAML))
	if err != nil {
		t.Fatalf("config not written: %v", err)
	}
	if !strings.Contains(string(data), "shadow: true") || !strings.Contains(string(data), "repo: acme/widgets") {
		t.Fatalf("config:\n%s", data)
	}
	md := r.Markdown()
	for _, want := range []string{"AGENTS.md (fail)", "gate script (fail)", "CI exists (fail)", "branch protection (unknown)"} {
		if !strings.Contains(md, want) {
			t.Errorf("report lacks %q:\n%s", want, md)
		}
	}
	if strings.Contains(md, "builds (") {
		t.Errorf("a passing check is listed as missing:\n%s", md)
	}
}

func TestDryRunWritesNothing(t *testing.T) {
	dir := repo(t)
	r, err := onboard.Run(t.Context(), onboard.Options{Dir: dir, DryRun: true, Ready: runnerOpts()})
	if err != nil {
		t.Fatal(err)
	}
	if r.Path != "" || !strings.Contains(r.Config, "shadow: true") {
		t.Fatalf("dry run: path=%q config=%q", r.Path, r.Config)
	}
	if _, err := os.Stat(filepath.Join(dir, ".factory")); !os.IsNotExist(err) {
		t.Fatalf("dry run left .factory behind: %v", err)
	}
}

func TestRefusesToOverwriteWithoutForce(t *testing.T) {
	dir := repo(t)
	if _, err := onboard.Run(t.Context(), onboard.Options{Dir: dir, Ready: runnerOpts()}); err != nil {
		t.Fatal(err)
	}
	if _, err := onboard.Run(t.Context(), onboard.Options{Dir: dir, Ready: runnerOpts()}); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("second run err = %v, want a --force hint", err)
	}
	if _, err := onboard.Run(t.Context(), onboard.Options{Dir: dir, Force: true, Ready: runnerOpts()}); err != nil {
		t.Fatalf("forced run: %v", err)
	}
}

func TestCommandFailsWhileTheRepoIsNotReady(t *testing.T) {
	dir := repo(t)
	cmd := onboard.NewCommand(onboard.WithRunner(okRun))
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--dir", dir})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "not ready") {
		t.Fatalf("err = %v, want not ready", err)
	}
	if !strings.Contains(out.String(), "## Missing") {
		t.Fatalf("output:\n%s", out.String())
	}
	bad := onboard.NewCommand(onboard.WithRunner(func(context.Context, string, string, ...string) (string, error) {
		return "boom", errors.New("x")
	}))
	bad.SetOut(&out)
	bad.SetArgs([]string{"--dir", dir, "--dry-run"})
	if bad.Execute() == nil {
		t.Fatal("a failing build must fail the command")
	}
}

// fakeInner would create a branch if shadow mode let a call through.
type fakeInner struct {
	api.Dispatcher
	dir   string
	calls int
}

func (f *fakeInner) Name() string   { return "fake" }
func (f *fakeInner) Caps() api.Caps { return api.Caps{} }
func (f *fakeInner) Dispatch(context.Context, api.Brief) (api.Session, error) {
	f.calls++
	if err := exec.Command("git", "-C", f.dir, "branch", "factory/7").Run(); err != nil {
		return api.Session{}, err
	}
	return api.Session{}, nil
}

type memWriter struct{ n int }

func (m *memWriter) Write(shadow.Record) error { m.n++; return nil }

// TestShadowModeCreatesNoBranch loads the config onboard wrote, wraps a dispatcher that would cut a
// branch, dispatches, and proves the repo has the same branches and the inner dispatcher never ran.
func TestShadowModeCreatesNoBranch(t *testing.T) {
	dir := repo(t)
	if _, err := onboard.Run(t.Context(), onboard.Options{Dir: dir, Ready: runnerOpts()}); err != nil {
		t.Fatal(err)
	}
	src, err := config.ReadRepo(os.DirFS(dir))
	if err != nil {
		t.Fatal(err)
	}
	l, err := config.Load(config.Sources{Repo: src})
	if err != nil {
		t.Fatal(err)
	}
	if !l.Config.Shadow {
		t.Fatal("the written config does not turn shadow mode on")
	}
	before := git(t, dir, "for-each-ref", "refs/heads")
	inner := &fakeInner{dir: dir}
	w := &memWriter{}
	d := shadow.Wrap(inner, l.Config, w)
	if _, err := d.Dispatch(t.Context(), api.Brief{Ticket: "7"}); err != nil {
		t.Fatal(err)
	}
	if after := git(t, dir, "for-each-ref", "refs/heads"); after != before {
		t.Fatalf("shadow mode changed the branches:\nbefore %s\nafter  %s", before, after)
	}
	if inner.calls != 0 || w.n != 1 {
		t.Fatalf("inner calls=%d records=%d, want 0 and 1", inner.calls, w.n)
	}
	// The control: with shadow off the same dispatcher does cut the branch, so the test can fail.
	off := config.Config{}
	if _, err := shadow.Wrap(inner, off, w).Dispatch(t.Context(), api.Brief{Ticket: "7"}); err != nil {
		t.Fatal(err)
	}
	if git(t, dir, "for-each-ref", "refs/heads") == before {
		t.Fatal("control: the unwrapped dispatcher should have created a branch")
	}
}

func runnerOpts() firstrun.ReadyOptions { return firstrun.ReadyOptions{Run: okRun} }
