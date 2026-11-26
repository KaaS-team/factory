package dashboard

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/KaaS-team/factory/internal/api"
	"github.com/KaaS-team/factory/internal/api/porttest"
)

func sampleBoard() api.Board {
	return api.Board{Slots: []api.Slot{{
		Ticket:  api.Ticket{ID: "101", Title: "Alpha task"},
		Phase:   api.PhaseRunning,
		Session: &api.Session{ID: "sess-101", Attempt: 1},
	}}}
}

// start runs the command with args and returns the base URL it prints and a stop function.
func start(t *testing.T, f *porttest.Control, args ...string) (string, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(WithControl(context.Background(), f))
	pr, pw := io.Pipe()
	cmd := NewCommand()
	cmd.SetOut(pw)
	cmd.SetArgs(append([]string{"--addr", "127.0.0.1:0"}, args...))
	done := make(chan error, 1)
	go func() { done <- cmd.ExecuteContext(ctx) }()
	line, err := bufio.NewReader(pr).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(strings.TrimPrefix(line, "dashboard on ")), func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}
}

func TestDefaultAddrIsLoopback(t *testing.T) {
	cmd := NewCommand()
	if cmd.Use != "dashboard" {
		t.Errorf("use %q", cmd.Use)
	}
	addr := cmd.Flags().Lookup("addr")
	if addr == nil || !strings.HasPrefix(addr.DefValue, "127.0.0.1:") {
		t.Fatalf("addr flag %+v", addr)
	}
	for _, name := range []string{"operator", "allow-actions"} {
		if fl := cmd.Flags().Lookup(name); fl == nil || fl.DefValue != "false" {
			t.Errorf("%s flag %+v", name, fl)
		}
	}
}

func TestCommandNeedsControl(t *testing.T) {
	cmd := NewCommand()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(nil)
	if err := cmd.ExecuteContext(context.Background()); err == nil {
		t.Error("want an error without a Control in the context")
	}
}

func TestCommandServesAndStops(t *testing.T) {
	f := porttest.NewControl(sampleBoard())
	base, stop := start(t, f)
	resp, err := http.Get(base + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "Alpha task") {
		t.Errorf("GET /: %d\n%s", resp.StatusCode, body)
	}
	stopped := make(chan struct{})
	go func() { stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("command did not stop on context cancel")
	}
}

func TestCommandRejectsRebindingHost(t *testing.T) {
	base, stop := start(t, porttest.NewControl(sampleBoard()))
	defer stop()
	for host, want := range map[string]int{"": http.StatusOK, "evil.example.com": http.StatusForbidden} {
		req, _ := http.NewRequest(http.MethodGet, base+"/", nil)
		if host != "" {
			req.Host = host
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("host %q: status %d, want %d", host, resp.StatusCode, want)
		}
	}
}

// cancelThroughBrowser loads the run page like a browser, then posts the cancel form with its CSRF cookie.
func cancelThroughBrowser(t *testing.T, base string) int {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Get(base + "/runs/101")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	u, _ := url.Parse(base)
	tok := ""
	for _, ck := range jar.Cookies(u) {
		if ck.Name == "kaas_csrf" {
			tok = ck.Value
		}
	}
	if tok == "" {
		t.Fatal("the run page set no csrf cookie")
	}
	resp, err = c.PostForm(base+"/runs/101/cancel", url.Values{"csrf": {tok}})
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// The command is read-only unless --allow-actions is passed.
func TestAllowActionsFlag(t *testing.T) {
	f := porttest.NewControl(sampleBoard())
	base, stop := start(t, f)
	if code := cancelThroughBrowser(t, base); code != http.StatusForbidden {
		t.Errorf("without --allow-actions: cancel answered %d, want 403", code)
	}
	stop()
	if calls := f.Calls(); len(calls) != 0 {
		t.Fatalf("read-only dashboard wrote %v", calls)
	}
	base, stop = start(t, f, "--allow-actions")
	defer stop()
	if code := cancelThroughBrowser(t, base); code != http.StatusSeeOther {
		t.Errorf("with --allow-actions: cancel answered %d, want 303", code)
	}
	if calls := f.Calls(); len(calls) != 1 || calls[0] != "cancel 101" {
		t.Errorf("calls %v, want [cancel 101]", calls)
	}
}
