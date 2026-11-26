package intake_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KaaS-team/factory/ee/contract"
	"github.com/KaaS-team/factory/internal/intake"
)

type replies struct {
	mu   sync.Mutex
	sent []string
}

func (r *replies) Reply(_ context.Context, ch, thread, text string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, ch+"|"+thread+"|"+text)
	return nil
}

func do(h http.Handler, method, path, body string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

const slackBody = `{"type":"event_callback","event_id":"Ev1","event":{"type":"app_mention","channel":"C1","ts":"1700000000.000100","text":"<@U9> fix the login bug\nsteps below"}}`

func slackSetup(t *testing.T) (http.Handler, *intake.FileStore, *replies, func(body string, sig string) map[string]string) {
	t.Helper()
	st, _ := intake.OpenFileStore("")
	rp := &replies{}
	now := time.Unix(1700000000, 0)
	h := &intake.Slack{Secret: "shh", Store: st, Replier: rp, Now: func() time.Time { return now }}
	hdr := func(body, sig string) map[string]string {
		if sig == "" {
			sig = intake.SignSlack("shh", now, []byte(body))
		}
		return map[string]string{"X-Slack-Request-Timestamp": "1700000000", "X-Slack-Signature": sig}
	}
	return h, st, rp, hdr
}

func TestSlackSignedEventCreatesTicket(t *testing.T) {
	h, st, rp, hdr := slackSetup(t)
	if w := do(h, "POST", "/", slackBody, hdr(slackBody, "")); w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}
	c, _ := st.Create(context.Background(), intake.NewTicket{Source: "slack", Key: "C1/1700000000.000100", Title: "x"})
	if c.New || c.ID != "slack-1" || c.Title != "fix the login bug" || c.Source != "slack" {
		t.Fatalf("ticket %+v", c)
	}
	if len(rp.sent) != 1 || !strings.HasPrefix(rp.sent[0], "C1|1700000000.000100|Created slack-1") {
		t.Fatalf("reply %v", rp.sent)
	}
	// Slack retries: same event, no second ticket and no second reply.
	do(h, "POST", "/", slackBody, hdr(slackBody, ""))
	if len(rp.sent) != 1 {
		t.Fatalf("retry replied again: %v", rp.sent)
	}
}

func TestSlackBadSignatureIs401(t *testing.T) {
	h, st, rp, hdr := slackSetup(t)
	for name, hd := range map[string]map[string]string{
		"wrong sig":  hdr(slackBody, "v0=00"),
		"no headers": {},
		"stale":      {"X-Slack-Request-Timestamp": "1600000000", "X-Slack-Signature": intake.SignSlack("shh", time.Unix(1600000000, 0), []byte(slackBody))},
	} {
		if w := do(h, "POST", "/", slackBody, hd); w.Code != 401 {
			t.Errorf("%s: status %d, want 401", name, w.Code)
		}
	}
	if c, _ := st.Create(context.Background(), intake.NewTicket{Source: "slack", Key: "k", Title: "t"}); c.ID != "slack-1" || len(rp.sent) != 0 {
		t.Fatalf("a rejected request left state: %+v %v", c, rp.sent)
	}
}

func TestSlackURLVerificationAndOwnMessages(t *testing.T) {
	h, _, rp, hdr := slackSetup(t)
	b := `{"type":"url_verification","challenge":"abc"}`
	if w := do(h, "POST", "/", b, hdr(b, "")); !strings.Contains(w.Body.String(), `"abc"`) {
		t.Fatalf("challenge: %s", w.Body)
	}
	b = `{"type":"event_callback","event":{"type":"app_mention","bot_id":"B1","channel":"C1","ts":"1","text":"hi"}}`
	do(h, "POST", "/", b, hdr(b, ""))
	if len(rp.sent) != 0 {
		t.Fatal("a bot message made a ticket")
	}
}

func sentryBody(issue string) string {
	return `{"action":"triggered","data":{"event":{"issue_id":"` + issue + `","title":"TypeError: x is undefined","culprit":"app/login","web_url":"https://sentry.example/e/1","issue_url":"https://sentry.example/i/` + issue + `","level":"error",` +
		`"exception":{"values":[{"type":"TypeError","value":"x is undefined","stacktrace":{"frames":[` +
		`{"filename":"lib.js","function":"vendor","lineno":1,"in_app":false},{"filename":"app.js","function":"outer","lineno":10,"in_app":true},{"filename":"login.js","function":"inner","lineno":42,"in_app":true}]}}]}}}}`
}

func TestSentryTwiceCreatesOneTicket(t *testing.T) {
	st, _ := intake.OpenFileStore("")
	h := &intake.Sentry{Secret: "client-secret", Store: st}
	sig := func(b string) map[string]string {
		return map[string]string{"Sentry-Hook-Signature": intake.SignSentry("client-secret", []byte(b))}
	}
	b := sentryBody("77")
	var ids []string
	var created []bool
	for range 2 {
		w := do(h, "POST", "/", b, sig(b))
		if w.Code != 200 {
			t.Fatalf("status %d: %s", w.Code, w.Body)
		}
		var out struct {
			Ticket  string `json:"ticket"`
			Created bool   `json:"created"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		ids, created = append(ids, out.Ticket), append(created, out.Created)
	}
	if ids[0] != "sentry-1" || ids[1] != "sentry-1" || !created[0] || created[1] {
		t.Fatalf("ids %v created %v", ids, created)
	}
	if w := do(h, "POST", "/", sentryBody("78"), sig(sentryBody("78"))); !strings.Contains(w.Body.String(), "sentry-2") {
		t.Fatalf("a different issue must be a new ticket: %s", w.Body)
	}
	c, _ := st.Create(context.Background(), intake.NewTicket{Source: "sentry", Key: "77", Title: "t"})
	for _, want := range []string{"TypeError: x is undefined", "at inner (login.js:42)", "https://sentry.example/i/77", "Culprit: app/login"} {
		if !strings.Contains(c.Body, want) {
			t.Errorf("body lacks %q:\n%s", want, c.Body)
		}
	}
	if strings.Contains(c.Body, "vendor") {
		t.Errorf("a library frame was listed when in-app frames exist:\n%s", c.Body)
	}
	if w := do(h, "POST", "/", b, map[string]string{"Sentry-Hook-Signature": "bad"}); w.Code != 401 {
		t.Fatalf("bad signature: %d", w.Code)
	}
}

type fakeID struct{}

func (fakeID) Authenticate(_ context.Context, c contract.Credential) (contract.Principal, error) {
	switch c.Value {
	case "good":
		return contract.Principal{ID: "svc", Kind: contract.KindService, Org: "acme", Roles: []contract.RoleBinding{{Role: contract.RoleBot, Scope: contract.Scope{Kind: contract.ScopeOrg, ID: "acme"}}}}, nil
	case "viewer":
		return contract.Principal{ID: "v", Kind: contract.KindUser, Org: "acme", Roles: []contract.RoleBinding{{Role: contract.RoleViewer, Scope: contract.Scope{Kind: contract.ScopeOrg, ID: "acme"}}}}, nil
	}
	return contract.Principal{}, contract.ErrUnauthenticated
}
func (fakeID) LookupBySCIM(context.Context, string) (contract.Principal, error) {
	return contract.Principal{}, contract.ErrNotFound
}
func (fakeID) Deprovision(context.Context, string) error { return nil }

// fakeAuthz applies the contract's Grants table for org-scope bindings.
type fakeAuthz struct{}

func (fakeAuthz) Can(_ context.Context, p contract.Principal, a contract.Action, _ contract.Resource) contract.Decision {
	for _, b := range p.Roles {
		for _, g := range contract.Grants[b.Role] {
			if g == a {
				return contract.Decision{Allow: true, Reason: contract.ReasonGranted}
			}
		}
	}
	return contract.Decision{Reason: contract.ReasonRoleLacksAction}
}

func TestAPIIdempotency(t *testing.T) {
	st, _ := intake.OpenFileStore(filepath.Join(t.TempDir(), "tickets.jsonl"))
	h := &intake.API{Identity: fakeID{}, Authorizer: fakeAuthz{}, Store: st}
	post := func(tok, key, body string) *httptest.ResponseRecorder {
		hd := map[string]string{"Authorization": "Bearer " + tok}
		if key != "" {
			hd["Idempotency-Key"] = key
		}
		return do(h, "POST", "/api/v1/tickets", body, hd)
	}
	id := func(w *httptest.ResponseRecorder) string {
		var o struct{ ID string }
		_ = json.Unmarshal(w.Body.Bytes(), &o)
		return o.ID
	}
	first := post("good", "k1", `{"title":"Add dark mode"}`)
	again := post("good", "k1", `{"title":"Add dark mode"}`)
	if first.Code != 201 || again.Code != 200 || id(first) != "api-1" || id(again) != "api-1" || again.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("first %d %s, again %d %s", first.Code, first.Body, again.Code, again.Body)
	}
	if w := post("good", "k1", `{"title":"Something else"}`); w.Code != 409 {
		t.Fatalf("same key, new body: %d", w.Code)
	}
	if w := post("good", "k2", `{"title":"Add dark mode"}`); w.Code != 201 || id(w) != "api-2" {
		t.Fatalf("new key: %d %s", w.Code, w.Body)
	}
	if post("nope", "k3", `{"title":"t"}`).Code != 401 || post("good", "", `{"title":"t"}`).Code != 400 ||
		post("good", "k4", `{}`).Code != 400 || post("viewer", "k5", `{"title":"t"}`).Code != 403 {
		t.Fatal("auth or validation status wrong")
	}
}

func TestLedgerSource(t *testing.T) {
	for _, src := range []string{intake.SourceSlack, intake.SourceSentry, intake.SourceAPI} {
		st, _ := intake.OpenFileStore("")
		c, _ := st.Create(context.Background(), intake.NewTicket{Source: src, Key: "k", Title: "t", URL: "https://x"})
		lt := intake.LedgerTicket(c)
		if c.Ledger != lt {
			t.Errorf("%s: stored ledger block %+v", src, c.Ledger)
		}
		if lt.Source != src || lt.ID != src+"-1" || lt.URL != "https://x" {
			t.Errorf("%s: %+v", src, lt)
		}
	}
}

func TestFileStoreSurvivesRestart(t *testing.T) {
	p := filepath.Join(t.TempDir(), "t.jsonl")
	a, _ := intake.OpenFileStore(p)
	if _, err := a.Create(context.Background(), intake.NewTicket{Source: "api", Key: "k", Fingerprint: "f", Title: "t"}); err != nil {
		t.Fatal(err)
	}
	b, err := intake.OpenFileStore(p)
	if err != nil {
		t.Fatal(err)
	}
	c, err := b.Create(context.Background(), intake.NewTicket{Source: "api", Key: "k", Fingerprint: "f", Title: "t"})
	if err != nil || c.New || c.ID != "api-1" {
		t.Fatalf("%+v %v", c, err)
	}
}
