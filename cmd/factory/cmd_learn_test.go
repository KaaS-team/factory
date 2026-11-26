package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KaaS-team/factory/internal/api"
	"github.com/KaaS-team/factory/internal/api/porttest"
	"github.com/KaaS-team/factory/internal/config"
	"github.com/KaaS-team/factory/internal/learn"
)

// TestLearnExportAllWritesOneJSONLAndNoDatabase: --export --all prints every registered repo's
// lessons as one stream and creates no file, so no state database is opened or written.
func TestLearnExportAllWritesOneJSONLAndNoDatabase(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	s := learn.Store{Dir: learn.DefaultDir}
	for _, l := range []learn.Lesson{{ID: "1", Repo: "o/a", Text: "from a"}, {ID: "2", Repo: "o/b", Text: "from b"}, {ID: "3", Repo: "o/unregistered", Text: "skip"}} {
		if err := s.Add(l); err != nil {
			t.Fatal(err)
		}
	}
	before := listFiles(t, dir)
	old := newControl
	newControl = func(config.Config) (api.Control, error) {
		c := porttest.NewControl(api.Board{})
		c.ReposVal = []string{"o/a", "o/b"}
		return c, nil
	}
	t.Cleanup(func() { newControl = old })
	root := newRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"learn", "--export", "--all"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], `"repo":"o/a"`) || !strings.Contains(lines[1], `"repo":"o/b"`) {
		t.Fatalf("export = %q, want one line per registered repo", out.String())
	}
	if after := listFiles(t, dir); after != before {
		t.Fatalf("export changed the tree:\n%s\n--\n%s", before, after)
	}
}

func listFiles(t *testing.T, dir string) string {
	t.Helper()
	var sb strings.Builder
	_ = filepath.Walk(dir, func(p string, i os.FileInfo, err error) error {
		if err == nil && !i.IsDir() {
			sb.WriteString(p + " " + i.ModTime().String() + "\n")
		}
		return nil
	})
	return sb.String()
}

// TestMCPSinkFailureIsAnError: a sink that cannot start reports an error, which the opener logs
// and carries on from (see internal/learn).
func TestMCPSinkFailureIsAnError(t *testing.T) {
	if err := (mcpSink{command: "", tool: "x"}).Send(context.Background(), learn.Lesson{}); err == nil {
		t.Fatal("empty command accepted")
	}
	if err := (mcpSink{command: "/nonexistent/mcp-server", tool: "x"}).Send(context.Background(), learn.Lesson{}); err == nil {
		t.Fatal("missing server accepted")
	}
}
