package egress

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/KaaS-team/factory/ee/contract"
)

type allowOnly map[string]bool

func (a allowOnly) Evaluate(_ context.Context, r contract.PolicyRequest) contract.Decision {
	if r.Check == contract.CheckEgress && a[r.Value] {
		return contract.Decision{Allow: true, Reason: contract.ReasonPolicyAllowed}
	}
	return contract.Decision{Reason: contract.ReasonPolicyNotAllowlisted}
}
func (allowOnly) Budgets() []contract.BudgetLimit { return nil }

type recorder struct {
	mu sync.Mutex
	ev []contract.AuditEvent
}

func (r *recorder) Record(_ context.Context, e contract.AuditEvent) (contract.AuditEvent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ev = append(r.ev, e)
	return e, nil
}
func (*recorder) Verify(context.Context) error { return nil }
func (*recorder) Search(context.Context, contract.AuditQuery) ([]contract.AuditEvent, error) {
	return nil, nil
}

// connect sends CONNECT host to the proxy and returns the status code and the open conn.
func connect(t *testing.T, proxyAddr, host string) (int, net.Conn, *bufio.Reader) {
	t.Helper()
	c, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = c.Write([]byte("CONNECT " + host + " HTTP/1.1\r\nHost: " + host + "\r\n\r\n"))
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, c, br
}

func TestProxyAllowsAndDenies(t *testing.T) {
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = echo.Close() }()
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go func() { defer func() { _ = c.Close() }(); _, _ = c.Write([]byte("hello\n")) }()
		}
	}()

	var dialed []string
	var mu sync.Mutex
	rec := &recorder{}
	p := &Proxy{
		Policy: allowOnly{"ok.example.com": true}, Audit: rec, Actor: "svc:run", Resource: "acme/app",
		Dial: func(_ context.Context, _, addr string) (net.Conn, error) {
			mu.Lock()
			dialed = append(dialed, addr)
			mu.Unlock()
			return net.Dial("tcp", echo.Addr().String())
		},
	}
	srv := httptest.NewServer(p)
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")

	code, c, br := connect(t, addr, "ok.example.com:443")
	if code != http.StatusOK {
		t.Fatalf("allowed host: status %d", code)
	}
	if line, _ := br.ReadString('\n'); line != "hello\n" {
		t.Fatalf("tunnel carried %q", line)
	}
	_ = c.Close()

	code, c2, _ := connect(t, addr, "evil.example.net:443")
	_ = c2.Close()
	if code != http.StatusForbidden {
		t.Fatalf("denied host: status %d", code)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(dialed) != 1 || dialed[0] != "ok.example.com:443" {
		t.Fatalf("dialed %v, want only the allowed host", dialed)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.ev) != 1 {
		t.Fatalf("audit events: %d", len(rec.ev))
	}
	e := rec.ev[0]
	if e.Outcome != contract.OutcomeDeny || e.Action != ActionConnect || e.Reason != contract.ReasonPolicyNotAllowlisted ||
		e.Data["host"] != "evil.example.net" || e.Actor != "svc:run" || e.Resource != "acme/app" {
		t.Fatalf("event %+v", e)
	}
}

func TestProxyRefusesPlainHTTPAndPrivateAddresses(t *testing.T) {
	rec := &recorder{}
	// The default dialer: an allowlisted name that is a loopback address is still refused.
	p := &Proxy{Policy: allowOnly{"localhost": true}, Audit: rec}
	srv := httptest.NewServer(p)
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")

	code, c, _ := connect(t, addr, "localhost:443")
	_ = c.Close()
	if code != http.StatusForbidden {
		t.Fatalf("loopback: status %d", code)
	}
	resp, err := http.Get(srv.URL + "/x")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("plain GET: status %d", resp.StatusCode)
	}
	if len(rec.ev) != 2 {
		t.Fatalf("audit events: %d", len(rec.ev))
	}
}
