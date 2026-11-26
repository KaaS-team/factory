package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// factoryBin is the built cmd/factory binary. TestMain builds it once; the tests below run it as a
// process, so they cover flag parsing, config loading and wiring that internal calls skip.
var factoryBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "factory-e2e-bin")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	factoryBin = filepath.Join(dir, "factory")
	build := exec.Command("go", "build", "-o", factoryBin, "github.com/KaaS-team/factory/cmd/factory")
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build factory: %v\n%s", err, out)
		_ = os.RemoveAll(dir)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// runBinary runs the built binary in a fresh repo directory against a fake GitHub API and returns
// its stdout. calls counts the requests the fake tracker served.
func runBinary(t *testing.T, calls *atomic.Int64, args ...string) string {
	t.Helper()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("[]"))
	}))
	t.Cleanup(api.Close)
	dir := t.TempDir()
	cmd := exec.Command(factoryBin, args...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "FACTORY_REPO=acme/widgets", "FACTORY_DISPATCHER=local", "GITHUB_TOKEN=fake", "GITHUB_API_URL=" + api.URL + "/"}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s %v: %v\n%s", factoryBin, args, err, errb.String())
	}
	t.Logf("ran binary %s %s", factoryBin, strings.Join(args, " "))
	return out.String()
}

func TestBinaryPrintsItsVersion(t *testing.T) {
	var n atomic.Int64
	if got := runBinary(t, &n, "version"); strings.TrimSpace(got) == "" {
		t.Fatal("version printed nothing")
	}
}

// status reads the tracker port: the fake GitHub API must have been asked for the ready tickets.
func TestBinaryStatusReadsTheTracker(t *testing.T) {
	var n atomic.Int64
	got := runBinary(t, &n, "status", "--json")
	var board struct {
		Slots []any `json:"slots"`
	}
	if err := json.Unmarshal([]byte(got), &board); err != nil {
		t.Fatalf("status --json is not JSON: %v\n%s", err, got)
	}
	if n.Load() == 0 {
		t.Error("status never called the tracker")
	}
}
