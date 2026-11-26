//go:build ee

// SPDX-License-Identifier: LicenseRef-KaaS-EE

package admin

import (
	"net/http"
	"net/url"
	"os"
	"testing"
)

// TestDemoServer serves the fixture console for screenshots and axe at ADMIN_DEMO_ADDR, with a
// few changes already in the audit log. It runs only when that variable is set:
//
//	ADMIN_DEMO_ADDR=127.0.0.1:8099 go test -tags ee -run TestDemoServer ./internal/admin
func TestDemoServer(t *testing.T) {
	addr := os.Getenv("ADMIN_DEMO_ADDR")
	if addr == "" {
		t.Skip("set ADMIN_DEMO_ADDR to serve the demo console")
	}
	f := newFixture(t)
	f.do(t, "ana", "POST", "/admin/users/role", url.Values{"id": {f.svc.ID}, "role": {"admin"}})
	f.do(t, "ben", "POST", "/admin/workers/drain", url.Values{"id": {"w-03"}})
	f.do(t, "cy", "POST", "/admin/policy", url.Values{"layer": {"org"}, "text": {orgPolicy}})
	f.do(t, "ben", "POST", "/admin/policy", url.Values{"layer": {"team"}, "text": {teamPolicy + "\nfoo = 1\n"}})
	f.do(t, "ben", "POST", "/admin/budgets", url.Values{"scope": {"team"}, "id": {"payments"}, "period": {"month"}, "usd": {"1100"}})
	f.do(t, "ana", "GET", "/admin/audit/export?format=csv", nil)
	t.Logf("serving the demo console at http://%s/admin/", addr)
	if err := http.ListenAndServe(addr, f.c.Handler()); err != nil { //nolint:gosec // a local demo server, run by hand
		t.Fatal(err)
	}
}
