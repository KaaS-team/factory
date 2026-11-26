package cidrift

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func write(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCheck(t *testing.T) {
	root := t.TempDir()
	write(t, root, "ci/same.yml", "a\n")
	write(t, root, ".github/workflows/same.yml", "a\n")
	write(t, root, "ci/changed.yml", "new\n")
	write(t, root, ".github/workflows/changed.yml", "old\n")
	write(t, root, "ci/fresh.yaml", "x\n")
	write(t, root, "ci/README.md", "not a workflow\n")
	write(t, root, ".github/workflows/unstaged.yml", "live only\n")
	got, err := Check(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []Drift{{"changed.yml", Differs}, {"fresh.yaml", NotLive}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if s := got[0].String(); s != "ci/changed.yml differs from .github/workflows/changed.yml" {
		t.Fatalf("String = %q", s)
	}
}

func TestNoStagedDirIsClean(t *testing.T) {
	got, err := Check(t.TempDir())
	if err != nil || got != nil {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestUnreadableStagedDirErrors(t *testing.T) {
	root := t.TempDir()
	write(t, root, "ci", "a file, not a directory")
	if _, err := Check(root); err == nil {
		t.Fatal("want an error when ci is not a directory")
	}
}

// The repo itself must be readable by the check; drift is reported by the gate step, not failed.
func TestRepoIsComparable(t *testing.T) {
	if _, err := Check(filepath.Join("..", "..")); err != nil {
		t.Fatal(err)
	}
}
