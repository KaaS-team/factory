//go:build ee

// SPDX-License-Identifier: LicenseRef-KaaS-EE

package enterprise

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KaaS-team/factory/ee/audit"
	"github.com/KaaS-team/factory/ee/authz"
	"github.com/KaaS-team/factory/ee/budget"
	"github.com/KaaS-team/factory/ee/contract"
	"github.com/KaaS-team/factory/ee/identity"
	"github.com/KaaS-team/factory/ee/scim"
	"github.com/KaaS-team/factory/ee/sso"
	coreaudit "github.com/KaaS-team/factory/internal/audit"
	"github.com/KaaS-team/factory/internal/serve"
)

const (
	org       = "acme"
	scimToken = "scim-secret"
)

type ledger struct{}

func (ledger) Append(context.Context, identity.Event) error { return nil }

// site is one factory: the real serve handler behind the enterprise identity, a SCIM endpoint and
// the SSO login routes, all on the one fake IdP.
type site struct {
	idp  *idp
	dir  *identity.Directory
	api  *httptest.Server
	scim *httptest.Server
	sso  *httptest.Server
	got  chan contract.Principal // the principal each completed login resolved to
}

func newSite(t *testing.T) *site {
	t.Helper()
	s := &site{idp: newIdP(t), got: make(chan contract.Principal, 4)}
	v, err := sso.NewIDTokenVerifier(t.Context(), s.idp.srv.URL, audience, "")
	if err != nil {
		t.Fatal(err)
	}
	iss, err := identity.NewIssuer([]byte(strings.Repeat("k", 32)), ledger{}, time.Hour, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	s.dir, err = identity.NewDirectory(scim.NewMemoryStore(), identity.DirectoryConfig{Org: org, Tokens: v, Agents: iss,
		Groups: authz.GroupMap{{Group: "factory-admins", Role: contract.RoleAdmin}, {Group: "factory-viewers", Role: contract.RoleViewer}}})
	if err != nil {
		t.Fatal(err)
	}
	s.scim = httptest.NewServer(scim.NewTenantHandler(s.dir.Store(), map[string]string{scimToken: org}, nil))
	s.api = httptest.NewServer(serve.New(serve.Core(newCore()), serve.Options{Access: &serve.Access{Identity: s.dir, Authorizer: authz.New()}}).Handler())
	mux := http.NewServeMux()
	s.sso = httptest.NewServer(mux)
	login, err := sso.NewOIDC(t.Context(), sso.OIDCConfig{Issuer: s.idp.srv.URL, ClientID: audience, ClientSecret: "secret",
		RedirectURL: s.sso.URL + "/callback", OnSession: func(w http.ResponseWriter, _ *http.Request, sess sso.Session) {
			p, err := s.dir.Session(sess)
			if err != nil {
				http.Error(w, "no session", http.StatusForbidden)
				return
			}
			s.got <- p
			http.SetCookie(w, &http.Cookie{Name: "factory_session", Value: p.ID, Path: "/", HttpOnly: true})
			w.WriteHeader(http.StatusNoContent)
		}})
	if err != nil {
		t.Fatal(err)
	}
	mux.HandleFunc("/login", login.Login)
	mux.HandleFunc("/callback", login.Callback)
	t.Cleanup(s.api.Close)
	t.Cleanup(s.scim.Close)
	t.Cleanup(s.sso.Close)
	return s
}

// login walks the browser through Login, the IdP and Callback, and returns the callback status.
func (s *site) login(t *testing.T, email string, groups ...string) (status int, jar http.CookieJar) {
	t.Helper()
	jar, _ = cookiejar.New(nil)
	c := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	get := func(u string) *http.Response {
		req, _ := http.NewRequestWithContext(t.Context(), "GET", u, nil)
		res, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		return res
	}
	res := get(s.sso.URL + "/login")
	if res.StatusCode != http.StatusFound {
		t.Fatalf("login: %d, want a redirect to the IdP", res.StatusCode)
	}
	return get(s.idp.signIn(t, res.Header.Get("Location"), email, groups...)).StatusCode, jar
}

// scimDo sends a SCIM request with the tenant token and returns the status and decoded body.
func (s *site) scimDo(t *testing.T, method, path, body string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequestWithContext(t.Context(), method, s.scim.URL+"/scim/v2"+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+scimToken)
	req.Header.Set("Content-Type", "application/scim+json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	out := map[string]any{}
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

func (s *site) provision(t *testing.T, email string) string {
	t.Helper()
	code, u := s.scimDo(t, "POST", "/Users", `{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],"userName":"`+email+`"}`)
	id, _ := u["id"].(string)
	if code != 201 || id == "" {
		t.Fatalf("provision %s: %d %v", email, code, u)
	}
	return id
}

func can(t *testing.T, s *site, tok string, a contract.Action) bool {
	t.Helper()
	p, err := s.dir.Authenticate(t.Context(), contract.Credential{Kind: contract.CredOIDCIDToken, Value: tok})
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	return authz.New().Can(t.Context(), p, a, contract.Resource{Org: org}).Allow
}

// TestLoginRoleDeprovision: an OIDC login through the fake IdP gets a session once SCIM has
// provisioned the user; the IdP group and a SCIM group each map to a role; after SCIM deprovisions
// the user the same token loses access on the real serve handler.
func TestLoginRoleDeprovision(t *testing.T) {
	s := newSite(t)

	// Not provisioned yet: the IdP vouches for the person, the directory does not know them.
	if st, _ := s.login(t, "bo@acme.example", "factory-viewers"); st != http.StatusForbidden {
		t.Fatalf("login before provisioning: %d, want 403", st)
	}

	boID := s.provision(t, "bo@acme.example")
	st, jar := s.login(t, "bo@acme.example", "factory-viewers")
	if st != http.StatusNoContent {
		t.Fatalf("login: %d, want 204", st)
	}
	su, _ := url.Parse(s.sso.URL)
	cs := jar.Cookies(su)
	if len(cs) == 0 {
		t.Fatal("login set no session cookie")
	}
	u := cs[0]
	if u.Name != "factory_session" || u.Value != boID {
		t.Fatalf("session cookie %v, want the SCIM id %s", u, boID)
	}
	boTok := s.idp.idToken()
	if code, _ := do(t, "GET", s.api.URL+"/events", boTok, ""); code != 200 {
		t.Fatalf("viewer on /events: %d, want 200", code)
	}
	if !can(t, s, boTok, contract.ActionRunRead) || can(t, s, boTok, contract.ActionBudgetEdit) {
		t.Fatal("viewer role: want run.read allowed and budget.edit denied")
	}

	// A user whose only group is a SCIM group gets that group's role.
	adaID := s.provision(t, "ada@acme.example")
	if code, g := s.scimDo(t, "POST", "/Groups", `{"schemas":["urn:ietf:params:scim:schemas:core:2.0:Group"],"displayName":"factory-admins","members":[{"value":"`+adaID+`"}]}`); code != 201 {
		t.Fatalf("create group: %d %v (ada=%q)", code, g, adaID)
	}
	if st, _ := s.login(t, "ada@acme.example"); st != http.StatusNoContent {
		t.Fatalf("ada login: %d", st)
	}
	adaTok := s.idp.idToken()
	if !can(t, s, adaTok, contract.ActionBudgetEdit) || can(t, s, adaTok, contract.ActionAdminUsers) {
		t.Fatal("SCIM group factory-admins: want admin: budget.edit allowed, admin.users (owner only) denied")
	}

	// Provisioned but in no mapped group: signed in, holds nothing.
	s.provision(t, "cy@acme.example")
	s.login(t, "cy@acme.example", "unmapped")
	if code, body := do(t, "GET", s.api.URL+"/events", s.idp.idToken(), ""); code != 403 || !strings.Contains(body, string(contract.ReasonNoBinding)) {
		t.Fatalf("user in no mapped group: %d %q, want 403 %s", code, body, contract.ReasonNoBinding)
	}

	// SCIM deprovisions Bo: the same token that worked is refused on the next request.
	if code, _ := s.scimDo(t, "PATCH", "/Users/"+boID, `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"replace","path":"active","value":false}]}`); code != 200 {
		t.Fatalf("scim deactivate: %d", code)
	}
	code, body := do(t, "GET", s.api.URL+"/events", boTok, "")
	if code != 403 || !strings.Contains(body, string(contract.ReasonIdentityDeprovisioned)) {
		t.Fatalf("after deprovision: %d %q, want 403 %s", code, body, contract.ReasonIdentityDeprovisioned)
	}
	if st, _ := s.login(t, "bo@acme.example", "factory-viewers"); st != http.StatusForbidden {
		t.Fatalf("login after deprovision: %d, want 403", st)
	}
	if code, _ := do(t, "GET", s.api.URL+"/events", adaTok, ""); code != 200 {
		t.Fatalf("deprovisioning bo cut off ada: %d", code)
	}
}

// TestAuditChainAndCollector: an audited action lands both in the hash chain and at the OTLP
// collector, and the chain verifies.
func TestAuditChainAndCollector(t *testing.T) {
	col := newCollector(t)
	chain := audit.Open(filepath.Join(t.TempDir(), "audit.jsonl"))
	flush := exportAudit(t, col, audit.Sink{Log: chain})
	recordRunCancel(t)
	flush()
	if !col.has("run.cancel", "acme/app") {
		t.Fatal("collector received no run.cancel record")
	}
	evs, err := chain.Search(t.Context(), contract.AuditQuery{})
	if err != nil || len(evs) != 1 || evs[0].Resource != "acme/app" {
		t.Fatalf("chain: %v %v", evs, err)
	}
	if err := chain.Verify(t.Context()); err != nil {
		t.Fatalf("chain verify: %v", err)
	}
}

// TestBudgetCapStopsRun: the enterprise budget stops a session at its hold, refuses the next run
// for the team with budget.exceeded, and chains the stop as run.cancel with that reason.
func TestBudgetCapStopsRun(t *testing.T) {
	chain := audit.Open(filepath.Join(t.TempDir(), "audit.jsonl"))
	b, err := budget.New(budget.Config{
		Limits: []budget.Limit{{Key: contract.BudgetKey{Scope: contract.BudgetTeam, ID: "acme"}, Micros: 1_000_000}},
		Path:   filepath.Join(t.TempDir(), "budget.json"), Audit: chain, Actor: "factory"})
	if err != nil {
		t.Fatal(err)
	}
	runBudgetStop(t, b, budget.StopRecorder(chain, "factory"))
	evs, _ := chain.Search(t.Context(), contract.AuditQuery{})
	if len(evs) != 1 || evs[0].Action != contract.ActionRunCancel || evs[0].Reason != contract.ReasonBudgetExceeded {
		t.Fatalf("chain: %+v, want one run.cancel with budget.exceeded", evs)
	}
	if err := chain.Verify(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// signIn plays the browser at the IdP: it takes the redirect Login produced, "authenticates" email
// with groups, and returns the callback URL the IdP would send the browser back to.
func (p *idp) signIn(t *testing.T, loginRedirect, email string, groups ...string) string {
	t.Helper()
	u, err := url.Parse(loginRedirect)
	if err != nil || u.Query().Get("nonce") == "" || u.Query().Get("code_challenge") == "" {
		t.Fatalf("login redirect %q carries no nonce or PKCE challenge: %v", loginRedirect, err)
	}
	p.mu.Lock()
	code := "code-" + b64(randomBytes())
	p.codes[code] = grant{email: email, nonce: u.Query().Get("nonce"), groups: groups}
	p.mu.Unlock()
	cb, _ := url.Parse(u.Query().Get("redirect_uri"))
	q := cb.Query()
	q.Set("code", code)
	q.Set("state", u.Query().Get("state"))
	cb.RawQuery = q.Encode()
	return cb.String()
}

// idToken returns the ID token the last sign-in earned.
func (p *idp) idToken() string { p.mu.Lock(); defer p.mu.Unlock(); return p.lastRaw }

// recordRunCancel audits one budget stop through the process sink.
func recordRunCancel(t *testing.T) {
	t.Helper()
	coreaudit.Record(t.Context(), "run.cancel", "acme/app", "T-7", contract.ErrBudgetExceeded)
}

func randomBytes() []byte {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return b
}
