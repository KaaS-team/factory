package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/KaaS-team/factory/ee/contract"
	"github.com/KaaS-team/factory/internal/oidc"
)

// MetadataPath is where the server publishes its OAuth protected resource metadata (RFC 9728), as the
// MCP authorization spec requires. It needs no token.
const MetadataPath = "/.well-known/oauth-protected-resource"

// AuthConfig guards the HTTP transport with OAuth 2.1 bearer tokens.
type AuthConfig struct {
	// Identity verifies each token. Its audience check is what binds a token to this server, so it
	// must be built with Resource as the audience.
	Identity contract.Identity
	// Resource is the canonical URL of the MCP endpoint, for example https://factory.example.com/mcp.
	Resource string
	// AuthorizationServers lists the issuers a client may get a token from.
	AuthorizationServers []string
	// ReadOnly, when set, serves principals that hold no role above viewer. Without it they get Server.
	ReadOnly *sdk.Server
}

type principalKey struct{}

// PrincipalFrom returns the caller AuthHandler authenticated.
func PrincipalFrom(ctx context.Context) (contract.Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(contract.Principal)
	return p, ok
}

// WithPrincipal returns ctx carrying p, as AuthHandler does after it authenticates a caller.
func WithPrincipal(ctx context.Context, p contract.Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// AuthHandler serves srv at /mcp for callers with a valid bearer token and the metadata document at
// MetadataPath. A missing, bad or wrong-audience token gets 401 with a WWW-Authenticate header that
// points at the metadata (RFC 9728). A principal with no roles, or a deprovisioned one, gets 403.
// Tokens are never read from the query string and never logged.
func AuthHandler(srv *sdk.Server, cfg AuthConfig) (http.Handler, error) {
	if cfg.Identity == nil || cfg.Resource == "" || len(cfg.AuthorizationServers) == 0 {
		return nil, errors.New("mcp: auth needs an identity, a resource url and an authorization server")
	}
	meta, _ := json.Marshal(map[string]any{
		"resource":                 cfg.Resource,
		"authorization_servers":    cfg.AuthorizationServers,
		"bearer_methods_supported": []string{"header"},
	})
	challenge := `Bearer resource_metadata="` + strings.TrimSuffix(cfg.Resource, "/mcp") + MetadataPath + `"`
	pick := func(r *http.Request) *sdk.Server {
		if p, ok := PrincipalFrom(r.Context()); ok && cfg.ReadOnly != nil && viewerOnly(p) {
			return cfg.ReadOnly
		}
		return srv
	}
	mcp := sdk.NewStreamableHTTPHandler(pick, nil)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == MetadataPath {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(meta)
			return
		}
		if r.URL.Path != "/mcp" {
			http.NotFound(w, r)
			return
		}
		raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || raw == "" {
			w.Header().Set("WWW-Authenticate", challenge)
			http.Error(w, "bearer token required", http.StatusUnauthorized)
			return
		}
		p, err := cfg.Identity.Authenticate(r.Context(), contract.Credential{Kind: contract.CredOIDCIDToken, Value: raw})
		switch {
		case errors.Is(err, contract.ErrDeprovisioned):
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		case err != nil:
			w.Header().Set("WWW-Authenticate", challenge+`, error="invalid_token"`)
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		case p.Disabled || len(p.Roles) == 0:
			w.Header().Set("WWW-Authenticate", challenge+`, error="insufficient_scope"`)
			http.Error(w, "no role for your groups", http.StatusForbidden)
			return
		}
		mcp.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, p)))
	}), nil
}

func viewerOnly(p contract.Principal) bool {
	for _, b := range p.Roles {
		if b.Role != contract.RoleViewer {
			return false
		}
	}
	return true
}

// OIDCIdentity adapts the OIDC verifier to contract.Identity. It handles only CredOIDCIDToken. The
// factory roles admin, operator and viewer become the contract roles admin, member and viewer at the
// whole org; a token whose groups grant none gets no roles.
func OIDCIdentity(v *oidc.Verifier, org string) contract.Identity { return oidcIdentity{v, org} }

type oidcIdentity struct {
	v   *oidc.Verifier
	org string
}

func (o oidcIdentity) Authenticate(ctx context.Context, c contract.Credential) (contract.Principal, error) {
	if c.Kind != contract.CredOIDCIDToken {
		return contract.Principal{}, contract.ErrUnsupportedCredential
	}
	id, err := o.v.Verify(ctx, c.Value)
	if err != nil {
		return contract.Principal{}, contract.ErrUnauthenticated
	}
	p := contract.Principal{ID: id.Subject, Kind: contract.KindUser, Org: o.org}
	role := map[string]contract.Role{"admin": contract.RoleAdmin, "operator": contract.RoleMember, "viewer": contract.RoleViewer}[id.Role]
	if role != "" {
		p.Roles = []contract.RoleBinding{{Role: role, Scope: contract.Scope{Kind: contract.ScopeOrg, ID: o.org}}}
	}
	return p, nil
}

func (oidcIdentity) LookupBySCIM(context.Context, string) (contract.Principal, error) {
	return contract.Principal{}, contract.ErrNotFound
}

func (oidcIdentity) Deprovision(context.Context, string) error { return contract.ErrNotFound }
