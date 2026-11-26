//go:build ee

// SPDX-License-Identifier: LicenseRef-KaaS-EE

package identity_test

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/KaaS-team/factory/ee/authz"
	"github.com/KaaS-team/factory/ee/contract"
	"github.com/KaaS-team/factory/ee/identity"
	"github.com/KaaS-team/factory/ee/scim"
	"github.com/KaaS-team/factory/ee/sso"
	"github.com/KaaS-team/factory/internal/api"
	"github.com/KaaS-team/factory/internal/api/porttest"
	"github.com/KaaS-team/factory/internal/serve"
)

const (
	org       = "acme"
	scimToken = "scim-secret"
	audience  = "factory"
)

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// fakeIdP is an OIDC provider that signs ID tokens on request.
type fakeIdP struct {
	srv *httptest.Server
	key *rsa.PrivateKey
}

func newFakeIdP(t *testing.T) *fakeIdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := &fakeIdP{key: key}
	mux := http.NewServeMux()
	p.srv = httptest.NewServer(mux)
	t.Cleanup(p.srv.Close)
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"issuer": p.srv.URL, "authorization_endpoint": p.srv.URL + "/a",
			"token_endpoint": p.srv.URL + "/t", "jwks_uri": p.srv.URL + "/jwks", "id_token_signing_alg_values_supported": []string{"RS256"}})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]any{{"kty": "RSA", "kid": "k1", "alg": "RS256", "use": "sig",
			"n": b64(key.N.Bytes()), "e": b64(big.NewInt(int64(key.E)).Bytes())}}})
	})
	return p
}

// token signs an ID token for email with groups.
func (p *fakeIdP) token(t *testing.T, email string, groups ...string) string {
	t.Helper()
	h, _ := json.Marshal(map[string]any{"alg": "RS256", "kid": "k1"})
	c, _ := json.Marshal(map[string]any{"iss": p.srv.URL, "aud": audience, "sub": "idp-" + email, "email": email, "email_verified": true,
		"groups": groups, "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix()})
	in := b64(h) + "." + b64(c)
	sum := sha256.Sum256([]byte(in))
	sig, err := rsa.SignPKCS1v15(rand.Reader, p.key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return in + "." + b64(sig)
}

type rig struct {
	idp  *fakeIdP
	dir  *identity.Directory
	scim *httptest.Server
	api  *httptest.Server
	iss  *identity.Issuer
}

type ledger struct{}

func (ledger) Append(context.Context, identity.Event) error { return nil }

func newRig(t *testing.T) *rig {
	t.Helper()
	idp := newFakeIdP(t)
	v, err := sso.NewIDTokenVerifier(t.Context(), idp.srv.URL, audience, "")
	if err != nil {
		t.Fatal(err)
	}
	iss, err := identity.NewIssuer([]byte(strings.Repeat("k", 32)), ledger{}, time.Hour, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := identity.NewDirectory(scim.NewMemoryStore(), identity.DirectoryConfig{Org: org, Tokens: v, Agents: iss,
		Groups: authz.GroupMap{{Group: "factory-admins", Role: contract.RoleAdmin}, {Group: "factory-viewers", Role: contract.RoleViewer}}})
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{idp: idp, dir: dir, iss: iss}
	r.scim = httptest.NewServer(scim.NewTenantHandler(dir.Store(), map[string]string{scimToken: org}, nil))
	r.api = httptest.NewServer(serve.New(serve.Core(&core{porttest.NewControl(api.Board{})}), serve.Options{
		Access: &serve.Access{Identity: dir, Authorizer: authz.New()}}).Handler())
	t.Cleanup(r.scim.Close)
	t.Cleanup(r.api.Close)
	return r
}

type core struct{ *porttest.Control }

func (core) Run(ctx context.Context) error { <-ctx.Done(); return nil }

// call sends a request and returns the status; for the SSE route it cancels once headers arrive.
func call(t *testing.T, method, url, token, body string) (int, string) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, method, url, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/scim+json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode >= 300 {
		var b strings.Builder
		buf := make([]byte, 512)
		n, _ := res.Body.Read(buf)
		b.Write(buf[:n])
		return res.StatusCode, b.String()
	}
	return res.StatusCode, ""
}

func (r *rig) provision(t *testing.T, email string) string {
	t.Helper()
	res, err := http.NewRequestWithContext(t.Context(), "POST", r.scim.URL+"/scim/v2/Users", strings.NewReader(`{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],"userName":"`+email+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	res.Header.Set("Authorization", "Bearer "+scimToken)
	res.Header.Set("Content-Type", "application/scim+json")
	resp, err := http.DefaultClient.Do(res)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var u struct{ ID string }
	if resp.StatusCode != 201 || json.NewDecoder(resp.Body).Decode(&u) != nil || u.ID == "" {
		t.Fatalf("provision: status %d", resp.StatusCode)
	}
	return u.ID
}

// TestLoginProvisionDeprovision is the E1 flow: sign in through a fake OIDC IdP, be provisioned by
// SCIM, get 200, be deprovisioned, get 403 on the next request.
func TestLoginProvisionDeprovision(t *testing.T) {
	for _, how := range []string{"scim patch", "scim delete", "directory"} {
		t.Run(how, func(t *testing.T) {
			r := newRig(t)
			tok := r.idp.token(t, "Bo@Acme.example", "factory-viewers")
			if code, _ := call(t, "GET", r.api.URL+"/events", tok, ""); code != 401 {
				t.Fatalf("before SCIM provisioning: status %d, want 401", code)
			}
			id := r.provision(t, "bo@acme.example")
			if code, _ := call(t, "GET", r.api.URL+"/events", tok, ""); code != 200 {
				t.Fatalf("provisioned: status %d, want 200", code)
			}
			switch how {
			case "scim patch":
				call(t, "PATCH", r.scim.URL+"/scim/v2/Users/"+id, scimToken, `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"replace","path":"active","value":false}]}`)
			case "scim delete":
				if code, _ := call(t, "DELETE", r.scim.URL+"/scim/v2/Users/"+id, scimToken, ""); code != 204 {
					t.Fatalf("delete: %d", code)
				}
			default:
				if err := r.dir.Deprovision(t.Context(), id); err != nil {
					t.Fatal(err)
				}
			}
			code, body := call(t, "GET", r.api.URL+"/events", tok, "")
			if code != 403 || !strings.Contains(body, string(contract.ReasonIdentityDeprovisioned)) {
				t.Fatalf("deprovisioned: status %d %q, want 403", code, body)
			}
		})
	}
}

// A viewer reads; the group map decides what an action needs, and a token for a person the IdP
// vouches for but with no mapped group is let in as a user with no authority.
func TestRolesFromGroups(t *testing.T) {
	r := newRig(t)
	r.provision(t, "bo@acme.example")
	p, err := r.dir.Authenticate(t.Context(), contract.Credential{Kind: contract.CredOIDCIDToken, Value: r.idp.token(t, "bo@acme.example", "factory-admins", "other")})
	if err != nil || len(p.Roles) != 1 || p.Roles[0].Role != contract.RoleAdmin || p.Roles[0].Scope != (contract.Scope{Kind: contract.ScopeOrg, ID: org}) || p.Kind != contract.KindUser {
		t.Fatalf("%+v %v", p, err)
	}
	p, err = r.dir.Authenticate(t.Context(), contract.Credential{Kind: contract.CredOIDCIDToken, Value: r.idp.token(t, "bo@acme.example")})
	if err != nil || len(p.Roles) != 0 {
		t.Fatalf("%+v %v", p, err)
	}
	if code, body := call(t, "GET", r.api.URL+"/events", r.idp.token(t, "bo@acme.example"), ""); code != 403 || !strings.Contains(body, string(contract.ReasonNoBinding)) {
		t.Fatalf("no role: %d %q", code, body)
	}
}

func TestAuthenticateRefusals(t *testing.T) {
	r := newRig(t)
	r.provision(t, "bo@acme.example")
	ctx := t.Context()
	for name, tc := range map[string]struct {
		c    contract.Credential
		want error
	}{
		"garbage token":  {contract.Credential{Kind: contract.CredOIDCIDToken, Value: "x.y.z"}, contract.ErrUnauthenticated},
		"api token":      {contract.Credential{Kind: contract.CredAPIToken, Value: "t"}, contract.ErrUnsupportedCredential},
		"saml assertion": {contract.Credential{Kind: contract.CredSAMLAssertion, Value: "t"}, contract.ErrUnsupportedCredential},
		"bad agent":      {contract.Credential{Kind: contract.CredAgentToken, Value: "x.y"}, contract.ErrUnauthenticated},
	} {
		_, err := r.dir.Authenticate(ctx, tc.c)
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", name, err, tc.want)
		}
		if err != nil && strings.Contains(err.Error(), tc.c.Value) && len(tc.c.Value) > 3 {
			t.Errorf("%s: error carries the credential", name)
		}
	}
}

func TestLookupAndDeprovision(t *testing.T) {
	r := newRig(t)
	ctx := t.Context()
	id := r.provision(t, "bo@acme.example")
	if p, err := r.dir.LookupBySCIM(ctx, id); err != nil || p.Disabled || p.ID != id {
		t.Fatalf("%+v %v", p, err)
	}
	if _, err := r.dir.LookupBySCIM(ctx, "nope"); !errors.Is(err, contract.ErrNotFound) {
		t.Fatal(err)
	}
	if err := r.dir.Deprovision(ctx, "nope"); !errors.Is(err, contract.ErrNotFound) {
		t.Fatal(err)
	}
	for range 2 { // idempotent
		if err := r.dir.Deprovision(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	if p, err := r.dir.LookupBySCIM(ctx, id); err != nil || !p.Disabled {
		t.Fatalf("%+v %v", p, err)
	}
	// SCIM turning the user back on does not undo a Deprovision.
	call(t, "PATCH", r.scim.URL+"/scim/v2/Users/"+id, scimToken, `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"replace","path":"active","value":true}]}`)
	if _, err := r.dir.Authenticate(ctx, contract.Credential{Kind: contract.CredOIDCIDToken, Value: r.idp.token(t, "bo@acme.example", "factory-admins")}); !errors.Is(err, contract.ErrDeprovisioned) {
		t.Fatal(err)
	}
}

// A deleted user is still found by SCIM id, as disabled.
func TestLookupDeletedUser(t *testing.T) {
	r := newRig(t)
	id := r.provision(t, "bo@acme.example")
	call(t, "DELETE", r.scim.URL+"/scim/v2/Users/"+id, scimToken, "")
	if p, err := r.dir.LookupBySCIM(t.Context(), id); err != nil || !p.Disabled {
		t.Fatalf("%+v %v", p, err)
	}
}

// A bot is a principal of kind bot with the bot role: it may start runs, never approve, and it
// stops when its token is revoked or the person who delegated it is deprovisioned.
func TestBotPrincipal(t *testing.T) {
	r := newRig(t)
	ctx := t.Context()
	id := r.provision(t, "bo@acme.example")
	who := identity.Principal{ID: id, Kind: identity.KindHuman}
	tok, err := r.iss.Mint(ctx, who, "agent-7", "run-1", org)
	if err != nil {
		t.Fatal(err)
	}
	p, err := r.dir.Authenticate(ctx, contract.Credential{Kind: contract.CredAgentToken, Value: tok})
	if err != nil || p.Kind != contract.KindBot || p.ID != "agent-7" || p.Org != org {
		t.Fatalf("%+v %v", p, err)
	}
	a := authz.New()
	res := contract.Resource{Org: org}
	if !a.Can(ctx, p, contract.ActionRunStart, res).Allow || a.Can(ctx, p, contract.ActionMerge, res).Allow || a.Can(ctx, p, contract.ActionApprove, res).Reason != contract.ReasonHumanRequired {
		t.Fatal("bot role does not match the contract")
	}
	other, _ := r.iss.Mint(ctx, who, "agent-8", "run-2", "other-org")
	if _, err := r.dir.Authenticate(ctx, contract.Credential{Kind: contract.CredAgentToken, Value: other}); !errors.Is(err, contract.ErrUnauthenticated) {
		t.Fatalf("other org: %v", err)
	}
	claims, _ := r.iss.Verify(tok)
	if err := r.iss.Revoke(ctx, who, claims.ID, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.dir.Authenticate(ctx, contract.Credential{Kind: contract.CredAgentToken, Value: tok}); !errors.Is(err, contract.ErrUnauthenticated) {
		t.Fatalf("revoked: %v", err)
	}
	tok2, _ := r.iss.Mint(ctx, who, "agent-9", "run-3", org)
	if err := r.dir.Deprovision(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := r.dir.Authenticate(ctx, contract.Credential{Kind: contract.CredAgentToken, Value: tok2}); !errors.Is(err, contract.ErrDeprovisioned) {
		t.Fatalf("delegator gone: %v", err)
	}
}

// A SAML sign-in reaches the same place as OIDC: Session turns it into a principal.
func TestSessionFromSAML(t *testing.T) {
	r := newRig(t)
	r.provision(t, "bo@acme.example")
	p, err := r.dir.Session(sso.Session{Email: "bo@acme.example", Groups: []string{"factory-viewers"}})
	if err != nil || len(p.Roles) != 1 || p.Roles[0].Role != contract.RoleViewer {
		t.Fatalf("%+v %v", p, err)
	}
	if _, err := r.dir.Session(sso.Session{Email: "stranger@acme.example"}); !errors.Is(err, contract.ErrUnauthenticated) {
		t.Fatal(err)
	}
}

func TestDirectoryNeedsAnOrg(t *testing.T) {
	if _, err := identity.NewDirectory(scim.NewMemoryStore(), identity.DirectoryConfig{}); err == nil {
		t.Fatal("want an error")
	}
	if _, err := identity.NewDirectory(scim.NewMemoryStore(), identity.DirectoryConfig{Org: "a", Groups: authz.GroupMap{{Group: "g", Role: "root"}}}); err == nil {
		t.Fatal("want an error for a bad group map")
	}
}
