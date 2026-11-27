// Package enterprise boots the real factory serve handler and drives the enterprise paths through
// it: OIDC sign-in, SCIM provisioning, audit export and budget stops. The identity provider and the
// OTLP collector are in-process fakes; nothing leaves the test binary.
package enterprise

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"
)

const audience = "factory"

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// grant is what the fake IdP remembers about one authorization code.
type grant struct {
	email, nonce string
	groups       []string
}

// idp is an OIDC provider with discovery, JWKS, authorize and token endpoints.
type idp struct {
	srv *httptest.Server
	key *rsa.PrivateKey

	mu      sync.Mutex
	codes   map[string]grant
	lastRaw string // the ID token the token endpoint issued last
	n       int
}

func newIdP(t *testing.T) *idp {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := &idp{key: key, codes: map[string]grant{}}
	mux := http.NewServeMux()
	p.srv = httptest.NewServer(mux)
	t.Cleanup(p.srv.Close)
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"issuer": p.srv.URL, "authorization_endpoint": p.srv.URL + "/authorize",
			"token_endpoint": p.srv.URL + "/token", "jwks_uri": p.srv.URL + "/jwks", "id_token_signing_alg_values_supported": []string{"RS256"}})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]any{{"kty": "RSA", "kid": "k1", "alg": "RS256", "use": "sig",
			"n": b64(key.N.Bytes()), "e": b64(big.NewInt(int64(key.E)).Bytes())}}})
	})
	mux.HandleFunc("/token", p.tokenEndpoint)
	return p
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
	p.n++
	code := "code-" + string(rune('a'+p.n))
	p.codes[code] = grant{email: email, nonce: u.Query().Get("nonce"), groups: groups}
	p.mu.Unlock()
	cb, _ := url.Parse(u.Query().Get("redirect_uri"))
	q := cb.Query()
	q.Set("code", code)
	q.Set("state", u.Query().Get("state"))
	cb.RawQuery = q.Encode()
	return cb.String()
}

func (p *idp) tokenEndpoint(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	p.mu.Lock()
	g, ok := p.codes[r.PostForm.Get("code")]
	delete(p.codes, r.PostForm.Get("code"))
	p.mu.Unlock()
	if !ok || r.PostForm.Get("code_verifier") == "" {
		http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
		return
	}
	raw := p.sign(g.email, g.nonce, g.groups)
	p.mu.Lock()
	p.lastRaw = raw
	p.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "token_type": "Bearer", "expires_in": 3600, "id_token": raw})
}

func (p *idp) sign(email, nonce string, groups []string) string {
	h, _ := json.Marshal(map[string]any{"alg": "RS256", "kid": "k1"})
	c, _ := json.Marshal(map[string]any{"iss": p.srv.URL, "aud": audience, "sub": "idp-" + email, "email": email, "email_verified": true,
		"nonce": nonce, "groups": groups, "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix()})
	in := b64(h) + "." + b64(c)
	sum := sha256.Sum256([]byte(in))
	sig, err := rsa.SignPKCS1v15(rand.Reader, p.key, crypto.SHA256, sum[:])
	if err != nil {
		panic(err)
	}
	return in + "." + b64(sig)
}

// idToken returns the ID token the last sign-in earned.
func (p *idp) idToken() string { p.mu.Lock(); defer p.mu.Unlock(); return p.lastRaw }

// do sends one request with an optional bearer token and returns the status and the start of the body.
func do(t *testing.T, method, target, bearer, body string) (int, string) {
	t.Helper()
	ctx := t.Context()
	req, err := http.NewRequestWithContext(ctx, method, target, stringsReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	req.Header.Set("Content-Type", "application/scim+json")
	// /events is a stream: stop reading once the headers say 200.
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		return res.StatusCode, string(b)
	}
	return res.StatusCode, ""
}
