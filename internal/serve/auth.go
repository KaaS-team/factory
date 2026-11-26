package serve

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/KaaS-team/factory/ee/contract"
	"github.com/KaaS-team/factory/internal/oidc"
)

// Environment names for OIDC sign-in.
const (
	EnvOIDCIssuer   = "FACTORY_OIDC_ISSUER"
	EnvOIDCAudience = "FACTORY_OIDC_AUDIENCE"
)

// AuthWrap picks the guard for the routes. With no FACTORY_OIDC_ISSUER it returns password unchanged, so
// the shared password path stays the default. With an issuer it returns the OIDC bearer middleware;
// /healthz and /readyz stay open. The audience defaults to "factory".
func AuthWrap(ctx context.Context, getenv func(string) string, password func(http.Handler) http.Handler) (func(http.Handler) http.Handler, error) {
	iss := getenv(EnvOIDCIssuer)
	if iss == "" {
		return password, nil
	}
	aud := getenv(EnvOIDCAudience)
	if aud == "" {
		aud = "factory"
	}
	v, err := oidc.New(ctx, oidc.Config{Issuer: iss, Audience: aud})
	if err != nil {
		return nil, err
	}
	return oidc.Middleware(v, func(r *http.Request) bool { return r.URL.Path == "/healthz" || r.URL.Path == "/readyz" }), nil
}

// Access authenticates a caller and decides what the caller may do. Both halves are the ee
// contract's interfaces, so any implementation plugs in.
type Access struct {
	Identity   contract.Identity
	Authorizer contract.Authorizer
	// Credential is what the bearer token is; the zero value means an OIDC ID token.
	Credential contract.CredentialKind
}

// authorize authenticates the caller and asks the authorizer for action on res. A missing or bad
// credential is a 401; a deprovisioned user or a denied action is a 403. On a refusal it returns the
// status and the contract's stable reason code, never the credential or the identity provider's text.
// The principal is returned whenever it was known, so a caller can audit a denial.
func (a *Access) authorize(r *http.Request, action contract.Action, res func(contract.Principal) contract.Resource) (contract.Principal, int, contract.ReasonCode) {
	kind := a.Credential
	if kind == "" {
		kind = contract.CredOIDCIDToken
	}
	raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || raw == "" {
		return contract.Principal{}, http.StatusUnauthorized, contract.ReasonIdentityUnauthenticated
	}
	p, err := a.Identity.Authenticate(r.Context(), contract.Credential{Kind: kind, Value: raw})
	switch {
	case errors.Is(err, contract.ErrDeprovisioned):
		return contract.Principal{}, http.StatusForbidden, contract.ReasonIdentityDeprovisioned
	case err != nil:
		return contract.Principal{}, http.StatusUnauthorized, contract.ReasonIdentityUnauthenticated
	}
	if d := a.Authorizer.Can(r.Context(), p, action, res(p)); !d.Allow {
		return p, http.StatusForbidden, d.Reason
	}
	return p, 0, contract.ReasonGranted
}

// guard wraps next so only a caller who may take action on the caller's org reaches it.
func (a *Access) guard(action contract.Action, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, status, why := a.authorize(r, action, func(p contract.Principal) contract.Resource { return contract.Resource{Org: p.Org} }); status != 0 {
			refuse(w, status, why)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func refuse(w http.ResponseWriter, status int, why contract.ReasonCode) {
	if status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", `Bearer realm="factory"`)
	}
	http.Error(w, "refused: "+string(why), status)
}
