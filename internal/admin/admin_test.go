//go:build ee

// SPDX-License-Identifier: LicenseRef-KaaS-EE

package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/KaaS-team/factory/ee/contract"
	"github.com/KaaS-team/factory/ee/policy"
)

// validForm is a request body each POST route accepts from an owner, so the walk below reaches
// the handler and not a validation error.
func validForm(f *fixture, pattern string) url.Values {
	switch pattern {
	case "/admin/users":
		return url.Values{"name": {"ci-nightly"}, "kind": {"service"}, "role": {"viewer"}}
	case "/admin/users/role":
		return url.Values{"id": {f.svc.ID}, "role": {"admin"}}
	case "/admin/users/status":
		return url.Values{"id": {f.svc.ID}, "disabled": {"true"}}
	case "/admin/policy":
		return url.Values{"layer": {"team"}, "text": {teamPolicy}}
	case "/admin/budgets":
		return url.Values{"scope": {"team"}, "id": {"payments"}, "period": {"month"}, "usd": {"1000"}}
	case "/admin/workers/drain":
		return url.Values{"id": {"w-02"}}
	}
	return nil
}

// TestMemberGets403OnEveryAdminRoute walks the route table: a member, a viewer and a
// deprovisioned user get 403 on every guarded route, nobody gets 401, and an owner gets through.
func TestMemberGets403OnEveryAdminRoute(t *testing.T) {
	f := newFixture(t)
	guarded := 0
	for _, rt := range f.c.Routes() {
		path := strings.TrimSuffix(rt.Pattern, "{$}")
		if rt.Open != "" {
			if res, _ := f.do(t, "", rt.Method, path, nil); res.StatusCode != http.StatusOK {
				t.Errorf("%s %s: open route answered %d", rt.Method, path, res.StatusCode)
			}
			continue
		}
		if rt.Action == "" {
			t.Errorf("%s %s: guarded route names no action", rt.Method, path)
		}
		guarded++
		for _, who := range []string{"cy", "eli", "dee", "gus"} {
			res, body := f.do(t, who, rt.Method, path, validForm(f, rt.Pattern))
			if res.StatusCode != http.StatusForbidden {
				t.Errorf("%s %s as %s: got %d, want 403", rt.Method, path, who, res.StatusCode)
			}
			if strings.Contains(body, "acme.example") {
				t.Errorf("%s %s as %s: refusal leaks the identity provider's detail", rt.Method, path, who)
			}
		}
		if res, _ := f.do(t, "", rt.Method, path, validForm(f, rt.Pattern)); res.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s %s with no credential: got %d, want 401", rt.Method, path, res.StatusCode)
		}
		if res, body := f.do(t, "ana", rt.Method, path, validForm(f, rt.Pattern)); res.StatusCode >= 400 {
			t.Errorf("%s %s as owner: got %d\n%s", rt.Method, path, res.StatusCode, body)
		}
	}
	if guarded < 13 {
		t.Fatalf("walked %d guarded routes, the table has shrunk", guarded)
	}
}

// TestAdminCannotManageUsers: admin.users is the owner's alone, so an admin reads the users page
// but every user change is refused.
func TestAdminCannotManageUsers(t *testing.T) {
	f := newFixture(t)
	if res, body := f.do(t, "ben", "GET", "/admin/users", nil); res.StatusCode != http.StatusOK || strings.Contains(body, "/admin/users/role") {
		t.Fatalf("admin users page: %d, edit forms shown: %v", res.StatusCode, strings.Contains(body, "/admin/users/role"))
	}
	for _, p := range []string{"/admin/users", "/admin/users/role", "/admin/users/status"} {
		if res, _ := f.do(t, "ben", "POST", p, validForm(f, p)); res.StatusCode != http.StatusForbidden {
			t.Errorf("POST %s as admin: got %d, want 403", p, res.StatusCode)
		}
	}
	if res, _ := f.do(t, "ben", "POST", "/admin/budgets", validForm(f, "/admin/budgets")); res.StatusCode != http.StatusSeeOther {
		t.Errorf("admin budget change: got %d, want 303", res.StatusCode)
	}
}

// TestEveryChangeIsAnAuditEvent: each POST route, done by an owner, adds an allow event for its
// action; refused by a member, a deny event with the authorizer's reason.
func TestEveryChangeIsAnAuditEvent(t *testing.T) {
	f := newFixture(t)
	for _, rt := range f.c.Routes() {
		if rt.Method != http.MethodPost {
			continue
		}
		before := len(f.events(t))
		if res, body := f.do(t, "ana", "POST", rt.Pattern, validForm(f, rt.Pattern)); res.StatusCode != http.StatusSeeOther {
			t.Fatalf("POST %s: %d\n%s", rt.Pattern, res.StatusCode, body)
		}
		evs := f.events(t)[before:]
		if len(evs) != 1 || evs[0].Action != rt.Action || evs[0].Actor != "u-ana" || evs[0].Outcome != contract.OutcomeAllow {
			t.Errorf("POST %s: events %+v, want one allow %s by u-ana", rt.Pattern, evs, rt.Action)
		}
		before = len(f.events(t))
		f.do(t, "cy", "POST", rt.Pattern, validForm(f, rt.Pattern))
		evs = f.events(t)[before:]
		if len(evs) != 1 || evs[0].Outcome != contract.OutcomeDeny || evs[0].Actor != "u-cy" || evs[0].Reason == "" {
			t.Errorf("refused POST %s: events %+v, want one deny by u-cy with a reason", rt.Pattern, evs)
		}
	}
	if err := f.log.Verify(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// TestPolicySaveChecksFirst: a file that fails `policy check`, or that would widen the layer
// above, is not written; the attempt is a deny event with the check's reason code.
func TestPolicySaveChecksFirst(t *testing.T) {
	f := newFixture(t)
	cases := []struct {
		name, layer, text string
		code              contract.ReasonCode
	}{
		{"unknown key", "team", teamPolicy + "\nfoo = 1\n", contract.ReasonPolicyInvalid},
		{"team widens models", "team", strings.Replace(teamPolicy, `["provider-small-2026"]`, `["provider-huge-2027"]`, 1), contract.ReasonPolicyWidening},
		{"org narrows under team", "org", strings.Replace(orgPolicy, `"provider-large-2026", "provider-small-2026"`, `"provider-large-2026"`, 1), contract.ReasonPolicyWidening},
	}
	for _, tc := range cases {
		path := map[string]string{"org": f.orgFile, "team": f.team}[tc.layer]
		was, _ := os.ReadFile(path)
		before := len(f.events(t))
		res, body := f.do(t, "ben", "POST", "/admin/policy", url.Values{"layer": {tc.layer}, "text": {tc.text}})
		if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, string(tc.code)) {
			t.Errorf("%s: got %d, want 422 naming %s", tc.name, res.StatusCode, tc.code)
		}
		if now, _ := os.ReadFile(path); !bytes.Equal(now, was) {
			t.Errorf("%s: the file changed although the check failed", tc.name)
		}
		evs := f.events(t)[before:]
		if len(evs) != 1 || evs[0].Outcome != contract.OutcomeDeny || evs[0].Reason != tc.code {
			t.Errorf("%s: events %+v, want one deny %s", tc.name, evs, tc.code)
		}
	}
	good := strings.Replace(teamPolicy, "usd = 1200.0", "usd = 900.0", 1)
	if res, _ := f.do(t, "ben", "POST", "/admin/policy", url.Values{"layer": {"team"}, "text": {good}}); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("clean save: got %d", res.StatusCode)
	}
	if now, _ := os.ReadFile(f.team); string(now) != good {
		t.Fatal("clean save did not write the file")
	}
	if ents, _ := os.ReadDir(f.srvDir()); slices.ContainsFunc(ents, func(e os.DirEntry) bool { return strings.HasPrefix(e.Name(), ".kaas-policy-") }) {
		t.Fatal("a staged check file was left behind")
	}
}

func (f *fixture) srvDir() string { return strings.TrimSuffix(f.orgFile, "/org.toml") }

// TestSignedLayerIsReadOnly: with a signing key set, the org and team layers are never written.
func TestSignedLayerIsReadOnly(t *testing.T) {
	f := newFixture(t)
	f.c.cfg.Policy.Key = "AAAA"
	res, body := f.do(t, "ana", "POST", "/admin/policy", url.Values{"layer": {"org"}, "text": {orgPolicy}})
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("signed save: got %d\n%s", res.StatusCode, body)
	}
}

// TestBudgetEditWritesTheLayerThatSetsIt: a team limit is changed in the team file, the usage
// page shows it, and raising it past nothing above is fine while widening an org key is refused.
func TestBudgetEditWritesTheLayerThatSetsIt(t *testing.T) {
	f := newFixture(t)
	res, body := f.do(t, "ben", "GET", "/admin/budgets", nil)
	if res.StatusCode != http.StatusOK || !strings.Contains(body, "$1,830.25") || !strings.Contains(body, `data-status="fail"`) {
		t.Fatalf("budgets page: %d, want the org spend and the bot at its limit", res.StatusCode)
	}
	if res, _ := f.do(t, "ben", "POST", "/admin/budgets", url.Values{"scope": {"team"}, "id": {"payments"}, "period": {"month"}, "usd": {"800"}}); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("team budget save: %d", res.StatusCode)
	}
	eff, err := policy.Resolve(f.c.cfg.Policy)
	must(t, err)
	if i := slices.IndexFunc(eff.Budgets(), func(b contract.BudgetLimit) bool { return b.ID == "payments" }); i < 0 || eff.Budgets()[i].USD != 800 {
		t.Fatalf("effective team budget after save: %+v", eff.Budgets())
	}
	org, _ := os.ReadFile(f.orgFile)
	if string(org) != orgPolicy {
		t.Fatal("a team budget edit touched the org file")
	}
	if res, _ := f.do(t, "ben", "POST", "/admin/budgets", url.Values{"scope": {"team"}, "id": {"payments"}, "period": {"month"}, "usd": {"-1"}}); res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("negative budget: %d, want 422", res.StatusCode)
	}
}

// TestPolicyFileSurvivesReencode: the budget editor rewrites a layer through the TOML encoder, so
// every key of the example file must come back the same.
func TestPolicyFileSurvivesReencode(t *testing.T) {
	data, err := os.ReadFile("../../ee/contract/testdata/kaas-policy.example.toml")
	must(t, err)
	f, err := policy.Decode(data)
	must(t, err)
	var buf bytes.Buffer
	must(t, toml.NewEncoder(&buf).Encode(f))
	g, err := policy.Decode(buf.Bytes())
	must(t, err)
	a, _ := json.Marshal(f)
	b, _ := json.Marshal(g)
	if !bytes.Equal(a, b) {
		t.Fatalf("re-encoded policy differs:\n%s\n%s", a, b)
	}
	empty := contract.PolicyFile{Version: 1, Layer: contract.LayerTeam, Name: "t", Models: contract.Allowlist{Allowed: []string{}}}
	buf.Reset()
	must(t, toml.NewEncoder(&buf).Encode(empty))
	h, err := policy.Decode(buf.Bytes())
	must(t, err)
	if h.Models.Allowed == nil || h.Harness.Allowed != nil {
		t.Fatalf("an empty allowlist must stay empty and an unset one unset: %+v\n%s", h, buf.String())
	}
}

// TestSCIMUsersAreReadOnly: a role change for a SCIM user is refused and changes nothing.
func TestSCIMUsersAreReadOnly(t *testing.T) {
	f := newFixture(t)
	res, body := f.do(t, "ana", "GET", "/admin/users", nil)
	if res.StatusCode != http.StatusOK || !strings.Contains(body, "u-cy") || !strings.Contains(body, "Read-only") {
		t.Fatalf("users page: %d", res.StatusCode)
	}
	before := len(f.events(t))
	if res, _ := f.do(t, "ana", "POST", "/admin/users/role", url.Values{"id": {"u-cy"}, "role": {"owner"}}); res.StatusCode != http.StatusNotFound {
		t.Fatalf("SCIM role change: got %d, want 404", res.StatusCode)
	}
	if len(f.events(t)) != before {
		t.Fatal("a refused SCIM edit must not record a change")
	}
	p, err := f.dir.LookupBySCIM(context.Background(), "u-cy")
	must(t, err)
	if p.Roles[0].Role != contract.RoleMember {
		t.Fatalf("cy's role changed: %+v", p.Roles)
	}
	if res, _ := f.do(t, "ana", "POST", "/admin/users/status", url.Values{"id": {"u-ana"}, "disabled": {"true"}}); res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("self-disable: got %d, want 422", res.StatusCode)
	}
}

// TestCrossSitePostRefused: a form posted from another site with the browser cookie is refused.
func TestCrossSitePostRefused(t *testing.T) {
	f := newFixture(t)
	for _, h := range []map[string]string{{"Sec-Fetch-Site": "cross-site"}, {"Origin": "https://evil.example"}, {}} {
		req, _ := http.NewRequest("POST", f.srv.URL+"/admin/workers/drain", strings.NewReader("id=w-01"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(&http.Cookie{Name: CookieName, Value: "tok-ana"})
		for k, v := range h {
			req.Header.Set(k, v)
		}
		res, err := http.DefaultClient.Do(req)
		must(t, err)
		_ = res.Body.Close()
		if res.StatusCode != http.StatusForbidden {
			t.Errorf("cookie POST with %v: got %d, want 403", h, res.StatusCode)
		}
	}
	if f.workers.List()[0].Draining {
		t.Fatal("a cross-site post drained a worker")
	}
}

// TestAuditSearchAndExport: filters narrow the table, and an export carries the hashes and is
// itself recorded.
func TestAuditSearchAndExport(t *testing.T) {
	f := newFixture(t)
	f.do(t, "ana", "POST", "/admin/workers/drain", url.Values{"id": {"w-01"}})
	f.do(t, "cy", "POST", "/admin/workers/drain", url.Values{"id": {"w-02"}})
	_, body := f.do(t, "ben", "GET", "/admin/audit?outcome=deny", nil)
	if !strings.Contains(body, "u-cy") || strings.Contains(body, "<td>u-ana</td>") {
		t.Fatal("outcome filter did not narrow the table")
	}
	if _, body := f.do(t, "ben", "GET", "/admin/audit?since=yesterday", nil); !strings.Contains(body, "From must be a date") {
		t.Fatal("a bad date is not reported")
	}
	res, body := f.do(t, "ben", "GET", "/admin/audit/export?format=jsonl", nil)
	if res.StatusCode != http.StatusOK || !strings.Contains(res.Header.Get("Content-Disposition"), "attachment") {
		t.Fatalf("export: %d", res.StatusCode)
	}
	lines := strings.Split(strings.TrimSpace(body), "\n")
	var first contract.AuditEvent
	must(t, json.Unmarshal([]byte(lines[0]), &first))
	if len(lines) != 2 || first.Hash == "" {
		t.Fatalf("export lines %d, first hash %q", len(lines), first.Hash)
	}
	evs := f.events(t)
	if last := evs[len(evs)-1]; last.Action != contract.ActionAuditExport || last.Data["events"] != "2" {
		t.Fatalf("export not recorded: %+v", last)
	}
	if _, body := f.do(t, "ben", "GET", "/admin/audit/export?format=csv&actor=u-cy", nil); !strings.HasPrefix(body, "seq,time,actor") || strings.Count(body, "\n") != 2 {
		t.Fatalf("csv export:\n%s", body)
	}
	if got := csvSafe("=HYPERLINK(1)"); got != "'=HYPERLINK(1)" {
		t.Fatalf("csvSafe: %q", got)
	}
}

func TestHealthOf(t *testing.T) {
	ttl := 30 * time.Second
	for _, tc := range []struct {
		age   time.Duration
		drain bool
		want  Health
	}{{time.Second, false, Healthy}, {ttl, false, Healthy}, {ttl + time.Second, false, Late}, {3*ttl + time.Second, false, Lost},
		{time.Second, true, Draining}, {4 * ttl, true, Lost}} {
		if got := HealthOf(WorkerInfo{LastSeen: now.Add(-tc.age), Draining: tc.drain}, now, ttl); got != tc.want {
			t.Errorf("age %v drain %v: %s, want %s", tc.age, tc.drain, got, tc.want)
		}
	}
}

// TestPagesRender: every page draws for an owner with its data, and with every source unset it
// shows an empty state instead of failing.
func TestPagesRender(t *testing.T) {
	f := newFixture(t)
	want := map[string]string{"/admin/users": "ci-release", "/admin/policy": "provider-small-2026", "/admin/budgets": "$5,000.00",
		"/admin/audit": "Chain intact", "/admin/workers": "2 / 3"}
	for p, s := range want {
		if res, body := f.do(t, "ana", "GET", p, nil); res.StatusCode != http.StatusOK || !strings.Contains(body, s) {
			t.Errorf("%s: %d, missing %q", p, res.StatusCode, s)
		}
	}
	bare, err := New(Config{Org: org, Identity: f.dir, Authorizer: f.c.cfg.Authorizer, Audit: f.log})
	must(t, err)
	f.c = bare
	f.srv.Config.Handler = bare.Handler()
	empty := map[string]string{"/admin/users": "Console users are off", "/admin/policy": "No policy is configured",
		"/admin/budgets": "No budget limits", "/admin/workers": "No worker plane is connected"}
	for p, s := range empty {
		if res, body := f.do(t, "ana", "GET", p, nil); res.StatusCode != http.StatusOK || !strings.Contains(body, s) {
			t.Errorf("bare %s: %d, missing %q", p, res.StatusCode, s)
		}
	}
	if _, err := New(Config{Org: org, Identity: f.dir, Authorizer: f.c.cfg.Authorizer}); err == nil {
		t.Fatal("a console with no audit log was built")
	}
}

func TestUSD(t *testing.T) {
	for m, want := range map[contract.Micros]string{0: "$0.00", 1_234_567_890: "$1,234.57", 5_000_000: "$5.00", -2_500_000: "-$2.50"} {
		if got := usd(m); got != want {
			t.Errorf("usd(%d) = %s, want %s", m, got, want)
		}
	}
}
