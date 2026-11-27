package enterprise

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/KaaS-team/factory/internal/serve"
)

// TestServeOIDCBearer: the real serve handler, guarded by the core's OIDC bearer middleware, lets a
// token from the fake IdP through and refuses a missing one; the probes stay open.
func TestServeOIDCBearer(t *testing.T) {
	p := newIdP(t)
	env := map[string]string{serve.EnvOIDCIssuer: p.srv.URL, serve.EnvOIDCAudience: audience}
	wrap, err := serve.AuthWrap(t.Context(), func(k string) string { return env[k] }, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Options.Wrap applies in Serve, not in Handler, so boot the real listener on a free port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	ctx, stop := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- serve.New(serve.Core(newCore()), serve.Options{Wrap: wrap}).Serve(ctx, addr, time.Second)
	}()
	t.Cleanup(func() { stop(); <-done })
	base := "http://" + addr
	deadline := time.Now().Add(5 * time.Second)
	for {
		if res, err := http.Get(base + "/healthz"); err == nil { //nolint:noctx // readiness poll
			_ = res.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("serve did not come up")
		}
		time.Sleep(20 * time.Millisecond)
	}
	tok := p.sign("bo@acme.example", "", []string{"factory-viewers"})
	if code, _ := do(t, "GET", base+"/events", tok, ""); code != 200 {
		t.Fatalf("valid token: %d, want 200", code)
	}
	if code, _ := do(t, "GET", base+"/events", "", ""); code != 401 {
		t.Fatalf("no token: %d, want 401", code)
	}
	if code, _ := do(t, "GET", base+"/readyz", "", ""); code != 200 {
		t.Fatalf("readyz: %d, want 200", code)
	}
}
