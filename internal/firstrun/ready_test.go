package firstrun

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixtureRepo copies the Go fixture into a temp dir: a repo that builds and tests but has no
// AGENTS.md, no gate script and no CI.
func fixtureRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	ents, err := os.ReadDir("testdata/readyrepo")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		b, err := os.ReadFile(filepath.Join("testdata/readyrepo", e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, e.Name()), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func okRun(context.Context, string, string, ...string) (string, error) { return "", nil }

func statuses(r Ready) map[string]Status {
	m := map[string]Status{}
	for _, c := range r.Checks {
		m[c.Name] = c.Status
	}
	return m
}

func TestReadyFlagsWhatIsMissing(t *testing.T) {
	r := CheckReady(context.Background(), ReadyOptions{Dir: fixtureRepo(t), Run: okRun})
	got := statuses(r)
	want := map[string]Status{"builds": Pass, "tests run": Pass, "AGENTS.md": Fail, "gate script": Fail, "CI exists": Fail, "branch protection": Unknown}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if r.OK() {
		t.Error("OK() with three failures")
	}
}

func TestReadyFixMakesItPass(t *testing.T) {
	dir := fixtureRepo(t)
	fixed, r, err := FixReady(context.Background(), ReadyOptions{Dir: dir, Run: okRun})
	if err != nil {
		t.Fatal(err)
	}
	if len(fixed) != 3 || !r.OK() {
		t.Fatalf("fixed=%v ok=%v\n%s", fixed, r.OK(), r.Markdown(fixed))
	}
	for _, f := range []string{"AGENTS.md", "gate.sh", ".github/workflows/ci.yml"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("%s not written", f)
		}
	}
	if st, _ := os.Stat(filepath.Join(dir, "gate.sh")); st.Mode()&0o111 == 0 {
		t.Error("gate.sh is not executable")
	}
}

// TestReadyFixKeepsExistingFiles: a fix never overwrites; a repo with its own AGENTS.md keeps it.
func TestReadyFixKeepsExistingFiles(t *testing.T) {
	dir := fixtureRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := FixReady(context.Background(), ReadyOptions{Dir: dir, Run: okRun}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "AGENTS.md")); string(b) != "mine\n" {
		t.Errorf("AGENTS.md overwritten: %q", b)
	}
}

func TestReadyBuildFailureHasNoFix(t *testing.T) {
	dir := fixtureRepo(t)
	bad := func(_ context.Context, _, _ string, args ...string) (string, error) {
		if args[0] == "build" {
			return "x.go:1: syntax error\n", os.ErrInvalid
		}
		return "", nil
	}
	fixed, r, err := FixReady(context.Background(), ReadyOptions{Dir: dir, Run: bad})
	if err != nil {
		t.Fatal(err)
	}
	c := r.Checks[0]
	if c.Status != Fail || !strings.Contains(c.Detail, "syntax error") || r.OK() {
		t.Errorf("build check = %+v", c)
	}
	if len(fixed) != 3 {
		t.Errorf("fixed %d, want the 3 file fixes", len(fixed))
	}
}

func TestReadyProtection(t *testing.T) {
	for _, tc := range []struct {
		ok   bool
		want Status
	}{{true, Pass}, {false, Fail}} {
		r := CheckReady(context.Background(), ReadyOptions{Dir: fixtureRepo(t), Run: okRun,
			Protected: func(context.Context) (bool, error) { return tc.ok, nil }})
		if got := statuses(r)["branch protection"]; got != tc.want {
			t.Errorf("protected=%v: %q, want %q", tc.ok, got, tc.want)
		}
	}
}

// TestReadyCommand is the done-when run: --ready --fix on the fixture repo ends green, and a second
// plain --ready agrees.
func TestReadyCommand(t *testing.T) {
	dir := fixtureRepo(t)
	run := func(args ...string) (string, error) {
		c := NewCommand()
		var out bytes.Buffer
		c.SetOut(&out)
		c.SetArgs(append([]string{"--ready", "--dir", dir}, args...))
		err := c.Execute()
		return out.String(), err
	}
	// The real go toolchain runs on the fixture; the module has no dependencies.
	if _, err := run(); err == nil {
		t.Fatal("--ready on a bare repo succeeded")
	}
	out, err := run("--fix")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "Fixed AGENTS.md") {
		t.Errorf("no fix line:\n%s", out)
	}
	if _, err := run(); err != nil {
		t.Errorf("second check: %v", err)
	}
}
