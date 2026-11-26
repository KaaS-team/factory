package mcp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/KaaS-team/factory/ee/contract"
	factorymcp "github.com/KaaS-team/factory/internal/mcp"
)

// fakeIdentity accepts three tokens: admin, viewer and norole verify; gone is deprovisioned.
type fakeIdentity struct{}

func (fakeIdentity) Authenticate(_ context.Context, c contract.Credential) (contract.Principal, error) {
	if c.Kind != contract.CredOIDCIDToken {
		return contract.Principal{}, contract.ErrUnsupportedCredential
	}
	bind := func(r contract.Role) []contract.RoleBinding {
		return []contract.RoleBinding{{Role: r, Scope: contract.Scope{Kind: contract.ScopeOrg, ID: "o"}}}
	}
	switch c.Value {
	case "admin":
		return contract.Principal{ID: "a", Kind: contract.KindUser, Org: "o", Roles: bind(contract.RoleAdmin)}, nil
	case "viewer":
		return contract.Principal{ID: "v", Kind: contract.KindUser, Org: "o", Roles: bind(contract.RoleViewer)}, nil
	case "norole":
		return contract.Principal{ID: "n", Kind: contract.KindUser, Org: "o"}, nil
	case "gone":
		return contract.Principal{}, contract.ErrDeprovisioned
	}
	return contract.Principal{}, contract.ErrUnauthenticated
}
func (fakeIdentity) LookupBySCIM(context.Context, string) (contract.Principal, error) {
	return contract.Principal{}, contract.ErrNotFound
}
func (fakeIdentity) Deprovision(context.Context, string) error { return nil }

func authServer(t *testing.T) *httptest.Server {
	t.Helper()
	ctl := newFake()
	h, err := factorymcp.AuthHandler(factorymcp.NewServer(ctl, factorymcp.Handlers{}), factorymcp.AuthConfig{
		Identity: fakeIdentity{}, Resource: "https://f.example/mcp", AuthorizationServers: []string{"https://idp.example"},
		ReadOnly: factorymcp.NewServer(ctl, factorymcp.Handlers{ReadOnly: true}),
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts
}

type bearer struct{ tok string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.tok)
	return http.DefaultTransport.RoundTrip(r)
}

func TestAuthRejectsWithoutAValidToken(t *testing.T) {
	ts := authServer(t)
	for _, tc := range []struct {
		name, header string
		code         int
		www          string
	}{
		{"no token", "", 401, `Bearer resource_metadata="https://f.example/.well-known/oauth-protected-resource"`},
		{"bad token", "Bearer nope", 401, `error="invalid_token"`},
		{"wrong scheme", "Basic abc", 401, "resource_metadata="},
		{"no role", "Bearer norole", 403, `error="insufficient_scope"`},
		{"deprovisioned", "Bearer gone", 403, ""},
	} {
		req, _ := http.NewRequest("POST", ts.URL+"/mcp", strings.NewReader("{}"))
		if tc.header != "" {
			req.Header.Set("Authorization", tc.header)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if res.StatusCode != tc.code {
			t.Errorf("%s: status %d, want %d", tc.name, res.StatusCode, tc.code)
		}
		if got := res.Header.Get("WWW-Authenticate"); tc.code == 401 && !strings.Contains(got, tc.www) {
			t.Errorf("%s: WWW-Authenticate %q lacks %q", tc.name, got, tc.www)
		}
	}
}

func TestAuthPublishesProtectedResourceMetadata(t *testing.T) {
	res, err := http.Get(authServer(t).URL + factorymcp.MetadataPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	var m struct {
		Resource string   `json:"resource"`
		Servers  []string `json:"authorization_servers"`
	}
	if err := json.NewDecoder(res.Body).Decode(&m); err != nil || res.StatusCode != 200 {
		t.Fatalf("status %d err %v", res.StatusCode, err)
	}
	if m.Resource != "https://f.example/mcp" || len(m.Servers) != 1 || m.Servers[0] != "https://idp.example" {
		t.Errorf("metadata %+v", m)
	}
}

func tools(t *testing.T, ts *httptest.Server, tok string) map[string]bool {
	t.Helper()
	tr := &sdk.StreamableClientTransport{Endpoint: ts.URL + "/mcp", HTTPClient: &http.Client{Transport: bearer{tok}}}
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "t", Version: "0"}, nil).Connect(t.Context(), tr, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cs.Close() }()
	got := map[string]bool{}
	for tool, err := range cs.Tools(t.Context(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		got[tool.Name] = true
	}
	return got
}

func TestAuthValidTokenSucceedsAndViewerIsReadOnly(t *testing.T) {
	ts := authServer(t)
	admin, viewer := tools(t, ts, "admin"), tools(t, ts, "viewer")
	if !admin["status"] || !admin["dispatch"] {
		t.Errorf("admin tools %v", admin)
	}
	if !viewer["status"] || viewer["dispatch"] {
		t.Errorf("viewer must see reads only, got %v", viewer)
	}
}

func TestAuthConfigNeedsEveryField(t *testing.T) {
	if _, err := factorymcp.AuthHandler(factorymcp.NewServer(newFake(), factorymcp.Handlers{}), factorymcp.AuthConfig{}); err == nil {
		t.Error("empty config must be refused")
	}
	srv := factorymcp.NewServer(newFake(), factorymcp.Handlers{})
	if _, err := factorymcp.AuthHandler(srv, factorymcp.AuthConfig{
		Identity: fakeIdentity{}, Resource: "https://factory.example/mcp", AuthorizationServers: []string{"https://idp.example"},
	}); err == nil {
		t.Error("a config without a read-only server must be refused, or viewers would get every write tool")
	}
}

func newTS(t *testing.T, h http.Handler) string {
	t.Helper()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts.URL
}
