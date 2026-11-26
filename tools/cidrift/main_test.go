package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	root := t.TempDir()
	for rel, body := range map[string]string{"ci/ci.yml": "new\n", ".github/workflows/ci.yml": "old\n"} {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		args []string
		code int
	}{{nil, 0}, {[]string{"-strict"}, 1}, {[]string{"-nope"}, 2}} {
		out, err := os.CreateTemp(t.TempDir(), "out")
		if err != nil {
			t.Fatal(err)
		}
		if got := run(tc.args, root, out, out); got != tc.code {
			t.Errorf("run(%v) = %d, want %d", tc.args, got, tc.code)
		}
		b, _ := os.ReadFile(out.Name())
		if tc.code != 2 && !strings.Contains(string(b), "drift: ci/ci.yml differs") {
			t.Errorf("run(%v) output lacks the drift line:\n%s", tc.args, b)
		}
	}
	if got := run(nil, t.TempDir(), os.Stdout, os.Stderr); got != 0 {
		t.Errorf("clean root = %d", got)
	}
}
