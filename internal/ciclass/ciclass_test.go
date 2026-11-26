package ciclass

import (
	"strings"
	"testing"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		name string
		log  string
		want Class
	}{
		{"network timeout", "go: downloading golang.org/x/net v0.30.0\ndial tcp 142.250.1.1:443: i/o timeout\nFAIL", Retryable},
		{"connection reset", "npm ERR! network read ECONNRESET\nnpm ERR! network This is a problem related to network connectivity.", Retryable},
		{"runner lost", "##[error]The runner has received a shutdown signal. This can happen when the runner service is stopped.", Retryable},
		{"disk full", "write /tmp/go-build: no space left on device", Retryable},
		{"missing secret", "Error: Input required and not supplied: token\n##[error]Process completed with exit code 1.", RepoSetup},
		{"secret with timeout noise", "DEPLOY_KEY secret is not set\nretrying: i/o timeout", RepoSetup},
		{"token perms", "RequestError: Resource not accessible by integration", RepoSetup},
		{"broken main", "main is broken: build fails on origin/main before this change", RepoSetup},
		{"upstream outage", "error fetching https://registry.npmjs.org/left-pad: 503 Service Unavailable\nSee githubstatus.com for the incident", External},
		{"test failure", "--- FAIL: TestParse (0.00s)\n    parse_test.go:41: got \"a\", want \"b\"\nFAIL\tgithub.com/x/y/parse\t0.004s", Agent},
		{"compile error", "internal/foo/foo.go:12:9: undefined: bar\nFAIL github.com/x/y/foo [build failed]", Agent},
		{"lint", "foo.go:3:1: exported function Foo should have comment (revive)", Agent},
		{"empty", "", Agent},
	}
	for _, c := range cases {
		if got := Classify(c.log); got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}

func TestExcerpt(t *testing.T) {
	log := strings.Repeat("noise line\n", 50) + "FAIL: the real error"
	got := Excerpt(log, 60)
	if !strings.HasSuffix(got, "FAIL: the real error") || len(got) > 70 {
		t.Fatalf("excerpt = %q", got)
	}
	if Excerpt("short\n", 100) != "short" {
		t.Fatal("short log changed")
	}
}
