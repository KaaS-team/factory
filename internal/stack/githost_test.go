package stack

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	gogithub "github.com/google/go-github/v79/github"
)

func sh(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e")
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func commit(t *testing.T, dir, file string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(file), 0o644); err != nil {
		t.Fatal(err)
	}
	sh(t, dir, "add", file)
	sh(t, dir, "commit", "-q", "-m", file)
}

func TestGitHubHostRebaseOnRealGit(t *testing.T) {
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	sh(t, root, "init", "-q", "--bare", "-b", "main", remote)
	work := filepath.Join(root, "work")
	sh(t, root, "clone", "-q", remote, work)
	// GitHubHost runs git with the ambient environment, so the clone carries its own identity for the rebase.
	sh(t, work, "config", "user.name", "t")
	sh(t, work, "config", "user.email", "t@e")
	commit(t, work, "base")
	sh(t, work, "push", "-q", "origin", "HEAD:main")
	sh(t, work, "checkout", "-q", "-b", "b1")
	commit(t, work, "one")
	sh(t, work, "push", "-q", "origin", "b1")
	oldB1 := sh(t, work, "rev-parse", "HEAD")
	sh(t, work, "checkout", "-q", "-b", "b2")
	commit(t, work, "two")
	sh(t, work, "push", "-q", "origin", "b2")
	sh(t, work, "checkout", "-q", "b1")
	commit(t, work, "one-fix")
	sh(t, work, "push", "-q", "origin", "b1")

	h := GitHubHost{Dir: work}
	ctx := context.Background()
	tip, err := h.Head(ctx, "b1")
	if err != nil || tip == oldB1 {
		t.Fatalf("Head = %s, %v", tip, err)
	}
	got, err := h.Rebase(ctx, "b2", "b1", oldB1)
	if err != nil {
		t.Fatal(err)
	}
	if remoteTip := sh(t, remote, "rev-parse", "b2"); remoteTip != got {
		t.Fatalf("remote b2 = %s, Rebase returned %s", remoteTip, got)
	}
	files := sh(t, remote, "ls-tree", "--name-only", "b2")
	if !strings.Contains(files, "one-fix") || !strings.Contains(files, "two") {
		t.Fatalf("b2 tree: %s", files)
	}
}

func TestGitHubHostPullRequests(t *testing.T) {
	var calls []string
	var bases []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		switch {
		case r.Method == "POST" && r.URL.Path == "/repos/o/r/pulls":
			bases = append(bases, body["base"].(string))
			_, _ = w.Write([]byte(`{"number":7}`))
		case r.Method == "PATCH" && r.URL.Path == "/repos/o/r/pulls/7":
			bases = append(bases, body["base"].(string))
			_, _ = w.Write([]byte(`{"number":7}`))
		case r.Method == "GET" && r.URL.Path == "/repos/o/r/pulls/7":
			_, _ = w.Write([]byte(`{"number":7,"merged":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := gogithub.NewClient(nil)
	u, _ := url.Parse(srv.URL + "/")
	c.BaseURL = u
	h := GitHubHost{Client: c, Owner: "o", Repo: "r"}
	ctx := context.Background()
	n, err := h.OpenPR(ctx, "b", "main", "title")
	if err != nil || n != 7 {
		t.Fatalf("OpenPR = %d, %v", n, err)
	}
	if err := h.Retarget(ctx, 7, "other"); err != nil {
		t.Fatal(err)
	}
	if m, err := h.Merged(ctx, 7); err != nil || !m {
		t.Fatalf("Merged = %v, %v", m, err)
	}
	if strings.Join(bases, ",") != "main,other" {
		t.Fatalf("bases = %v (calls %v)", bases, calls)
	}
}
