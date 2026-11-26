package k8s

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/KaaS-team/factory/internal/api"
	"github.com/KaaS-team/factory/internal/dispatch/brief"
	"github.com/KaaS-team/factory/internal/dispatch/local"
	"github.com/KaaS-team/factory/internal/sandbox"
)

const secretValue = "hunter2-very-secret"

// fake is a Kubernetes API server and a sandbox runtime in one.
type fake struct {
	mu       sync.Mutex
	calls    []string // "METHOD path"
	bodies   map[string]string
	execs    [][]string
	files    map[string][]byte
	crd      bool
	exitCode int
	hang     chan struct{} // when set, the agent exec signals it and blocks until cancelled
}

func (f *fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	f.bodies[r.Method+" "+r.URL.Path] = string(body)
	p := r.URL.Path
	switch {
	case p == "/apis/agents.x-k8s.io/v1alpha1":
		if !f.crd {
			http.NotFound(w, r)
		}
	case r.Method == http.MethodPost && strings.HasSuffix(p, "/exec"):
		var req struct{ Argv []string }
		_ = json.Unmarshal(body, &req)
		f.execs = append(f.execs, req.Argv)
		w.Header().Set("Content-Type", "application/x-ndjson")
		if f.hang != nil && strings.Contains(strings.Join(req.Argv, " "), "exec \"$@\"") {
			f.mu.Unlock()
			close(f.hang)
			<-r.Context().Done()
			f.mu.Lock()
			return
		}
		if strings.Contains(strings.Join(req.Argv, " "), "exec \"$@\"") {
			// The agent echoes its environment, as a careless one would.
			out, _ := json.Marshal(map[string]string{"stream": "stdout", "data": "token is " + secretValue + "\n"})
			_, _ = w.Write(append(out, '\n'))
		}
		_, _ = fmt.Fprintf(w, "{\"exit\":%d}\n", f.exitCode)
	case r.Method == http.MethodPut && strings.HasSuffix(p, "/files"):
		f.files[r.URL.Query().Get("path")] = body
	case r.Method == http.MethodGet && strings.Contains(p, "/sandboxes/"), r.Method == http.MethodGet && strings.Contains(p, "/pods/"):
		status := map[string]any{"phase": "Running", "conditions": []any{map[string]any{"type": "Ready", "status": "True"}}}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": status})
	case r.Method == http.MethodPost, r.Method == http.MethodDelete:
		_, _ = w.Write([]byte("{}"))
	default:
		http.NotFound(w, r)
	}
}

func (f *fake) count(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func (f *fake) runExecs() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, e := range f.execs {
		if strings.Contains(strings.Join(e, " "), `exec "$@"`) {
			n++
		}
	}
	return n
}

func repoDir(t *testing.T, env string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if env != "" {
		if err := os.MkdirAll(filepath.Join(dir, ".factory"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, sandbox.EnvironmentPath), []byte(env), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

type logBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) { l.mu.Lock(); defer l.mu.Unlock(); return l.b.Write(p) }
func (l *logBuf) String() string              { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

func setup(t *testing.T, f *fake, dir string, mod func(*Config)) (*Dispatcher, *logBuf) {
	t.Helper()
	f.bodies, f.files = map[string]string{}, map[string][]byte{}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	logs := &logBuf{}
	cfg := Config{
		APIURL: srv.URL, Token: "kube-token", HTTP: srv.Client(),
		Source:     func(context.Context, api.Brief) (string, error) { return dir, nil },
		RuntimeURL: func(string) string { return srv.URL }, RuntimeToken: "rt",
		DefaultImage: "base:1",
		Secrets: func(n string) (string, bool) {
			if n == "NPM_TOKEN" {
				return secretValue, true
			}
			return "", false
		},
		ProxyURL: "http://proxy.factory:3128", ProxyCIDR: "10.0.0.5/32", ProxyPort: 3128,
		Presets: map[string]local.Preset{"claude": {Argv: func(local.Run) ([]string, error) { return []string{"agent", "--go"}, nil }}},
		Logs:    func(string) io.Writer { return logs },
	}
	if mod != nil {
		mod(&cfg)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	d, err := New(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return d, logs
}

func briefN(n int) api.Brief {
	name, _ := brief.Name("T1", n)
	return api.Brief{Ticket: "T1", Attempt: n, Name: name, Text: "do it", Repo: "acme/app"}
}

func brief1() api.Brief { return briefN(1) }

func dispatchWait(t *testing.T, d *Dispatcher) error {
	t.Helper()
	s, err := d.Dispatch(context.Background(), brief1())
	if err != nil {
		t.Fatal(err)
	}
	return d.Wait(context.Background(), s)
}

const envToml = "version = 1\nimage = \"base:2\"\nsetup = [\"make deps\"]\nsecrets = [\"NPM_TOKEN\"]\n"

func TestRunCreatesExecsAndDeletes(t *testing.T) {
	for _, crd := range []bool{true, false} {
		t.Run(fmt.Sprintf("crd=%v", crd), func(t *testing.T) {
			f := &fake{crd: crd}
			d, logs := setup(t, f, repoDir(t, envToml), nil)
			if err := dispatchWait(t, d); err != nil {
				t.Fatal(err)
			}
			col := "/api/v1/namespaces/factory-runs/pods"
			if crd {
				col = "/apis/agents.x-k8s.io/v1alpha1/namespaces/factory-runs/sandboxes"
			}
			name := "factory-t1-a1"
			if f.count("POST "+col) != 1 || f.count("DELETE "+col+"/"+name) != 1 {
				t.Fatalf("want one create and one delete of %s, calls: %v", col, f.calls)
			}
			if f.count("POST /apis/networking.k8s.io/v1/namespaces/factory-runs/networkpolicies") != 1 ||
				f.count("DELETE /apis/networking.k8s.io/v1/namespaces/factory-runs/networkpolicies/"+name) != 1 {
				t.Fatalf("network policy not created and deleted: %v", f.calls)
			}
			if f.runExecs() != 1 || f.count("POST /sandboxes/"+name+"/exec") < 3 {
				t.Fatalf("execs: %v", f.execs)
			}
			if !strings.Contains(f.bodies["POST "+col], `"image":"base:2"`) {
				t.Fatalf("create body: %s", f.bodies["POST "+col])
			}
			// The repo went in as a tar over the file endpoint.
			tr := tar.NewReader(bytes.NewReader(f.files[tarFile]))
			h, err := tr.Next()
			if err != nil || h.Name == "" {
				t.Fatalf("copy-in is not a tar: %v", err)
			}
			if !bytes.Contains(f.files[tarFile], []byte("package main")) {
				t.Fatal("repo file missing from tar")
			}
			// The proxy is set in the env file.
			if env := string(f.files[envFile]); !strings.Contains(env, "HTTPS_PROXY='http://proxy.factory:3128'") {
				t.Fatalf("env file: %q", env)
			}
			if !strings.Contains(logs.String(), "[redacted]") {
				t.Fatalf("log: %q", logs.String())
			}
		})
	}
}

func TestSecretNeverInLogsOrCommandLines(t *testing.T) {
	f := &fake{crd: true}
	d, logs := setup(t, f, repoDir(t, envToml), nil)
	if err := dispatchWait(t, d); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(logs.String(), secretValue) {
		t.Fatalf("secret in logs: %q", logs.String())
	}
	if !strings.Contains(logs.String(), "token is [redacted]") {
		t.Fatalf("expected the masked line, got %q", logs.String())
	}
	if !strings.Contains(string(f.files[envFile]), secretValue) {
		t.Fatal("the env file must carry the secret")
	}
	for path, body := range f.bodies {
		if path != "PUT /sandboxes/factory-t1-a1/files" && strings.Contains(body, secretValue) {
			t.Fatalf("secret sent in %s: %s", path, body)
		}
	}
}

func TestMissingSecretFailsBeforeCreate(t *testing.T) {
	f := &fake{crd: true}
	d, _ := setup(t, f, repoDir(t, envToml), func(c *Config) { c.Secrets = func(string) (string, bool) { return "", false } })
	err := dispatchWait(t, d)
	if err == nil || !strings.Contains(err.Error(), "NPM_TOKEN") {
		t.Fatalf("err = %v", err)
	}
	if f.count("POST /apis/agents") != 0 {
		t.Fatal("a sandbox was created without its secret")
	}
}

func TestSnapshotIsReused(t *testing.T) {
	f := &fake{crd: true}
	commits := 0
	d, _ := setup(t, f, repoDir(t, envToml), func(c *Config) {
		c.Commit = func(context.Context, string, sandbox.Environment) (string, error) {
			commits++
			return "reg/env:abc", nil
		}
	})
	setupRuns := func() int {
		n := 0
		for _, e := range f.execs {
			if strings.Contains(strings.Join(e, " "), "make deps") {
				n++
			}
		}
		return n
	}
	if err := dispatchWait(t, d); err != nil {
		t.Fatal(err)
	}
	if setupRuns() != 1 || commits != 1 {
		t.Fatalf("first run: setup %d commits %d", setupRuns(), commits)
	}
	// Second attempt: the cache holds the image, so setup is skipped and the sandbox uses it.
	s, err := d.Dispatch(context.Background(), briefN(2))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Wait(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if setupRuns() != 1 || commits != 1 {
		t.Fatalf("second run repeated setup: setup %d commits %d", setupRuns(), commits)
	}
	if !strings.Contains(f.bodies["POST /apis/agents.x-k8s.io/v1alpha1/namespaces/factory-runs/sandboxes"], `"image":"reg/env:abc"`) {
		t.Fatal("second sandbox did not use the snapshot image")
	}
}

func TestFailedRunStillDeletes(t *testing.T) {
	f := &fake{crd: true, exitCode: 3}
	d, _ := setup(t, f, repoDir(t, ""), nil)
	if err := dispatchWait(t, d); err == nil {
		t.Fatal("want the exit error")
	}
	if f.count("DELETE /apis/agents.x-k8s.io/v1alpha1/namespaces/factory-runs/sandboxes/") != 1 {
		t.Fatalf("not deleted: %v", f.calls)
	}
}

func TestCancelDeletes(t *testing.T) {
	f := &fake{crd: true, hang: make(chan struct{})}
	d, _ := setup(t, f, repoDir(t, ""), nil)
	s, err := d.Dispatch(context.Background(), brief1())
	if err != nil {
		t.Fatal(err)
	}
	<-f.hang // the agent is running
	if err := d.Cancel(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	_ = d.Wait(context.Background(), s)
	if f.count("DELETE /apis/agents.x-k8s.io/v1alpha1/namespaces/factory-runs/sandboxes/") != 1 {
		t.Fatalf("not deleted: %v", f.calls)
	}
	if err := d.Steer(context.Background(), s, "x"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Steer = %v", err)
	}
}

func TestDispatchIsIdempotentAndCapped(t *testing.T) {
	f := &fake{crd: true}
	d, _ := setup(t, f, repoDir(t, ""), func(c *Config) { c.MaxConcurrent = 1 })
	s1, err := d.Dispatch(context.Background(), brief1())
	if err != nil {
		t.Fatal(err)
	}
	s2, err := d.Dispatch(context.Background(), brief1())
	if err != nil || s1.Key != s2.Key {
		t.Fatalf("second dispatch: %v %v", s2, err)
	}
	_ = d.Wait(context.Background(), s1)
	if f.count("POST /apis/agents.x-k8s.io/v1alpha1/namespaces/factory-runs/sandboxes") != 1 {
		t.Fatal("a repeated Dispatch created a second sandbox")
	}
	if d.Name() != "k8s" || !d.Caps().Remote || d.Caps().CanPushWorkflows {
		t.Fatalf("caps %+v", d.Caps())
	}
	if _, err := d.Usage(context.Background(), s1); err != nil {
		t.Fatal(err)
	}
}

func TestObjectName(t *testing.T) {
	if got := objectName("factory-ENG_12-a3"); got != "factory-eng-12-a3" {
		t.Fatal(got)
	}
	if got := objectName(strings.Repeat("a", 80)); len(got) != 63 {
		t.Fatal(len(got))
	}
}

func TestNewValidates(t *testing.T) {
	if _, err := New(context.Background(), Config{}); err == nil {
		t.Fatal("want an error")
	}
	if _, err := New(context.Background(), Config{APIURL: "x", Source: func(context.Context, api.Brief) (string, error) { return "", nil }, RuntimeURL: func(string) string { return "" }, ProxyURL: "p"}); err == nil {
		t.Fatal("a proxy without a CIDR must fail")
	}
}
