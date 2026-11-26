// SPDX-License-Identifier: Apache-2.0

package contract

import "context"

// PrincipalKind says what sort of actor a Principal is.
type PrincipalKind string

// The three principal kinds. Only KindUser may take ActionApprove.
const (
	KindUser    PrincipalKind = "user"    // a person, signed in through SSO or SCIM-provisioned
	KindBot     PrincipalKind = "bot"     // a coding agent acting for a run
	KindService PrincipalKind = "service" // a machine client: CI, a webhook, another factory
)

// Principal is who is acting. It is a value, not an interface: it crosses process and language
// boundaries as JSON (see testdata/conformance.json), so every port shares one shape.
//
// Authority comes only from Roles. Teams is membership, used for routing and for team-scoped
// budgets; being in a team grants nothing without a RoleBinding at that team.
type Principal struct {
	ID       string        `json:"id"`              // stable and unique within Org; never an email
	Kind     PrincipalKind `json:"kind"`            // user, bot or service
	Org      string        `json:"org"`             // the one org the principal belongs to
	Teams    []string      `json:"teams,omitempty"` // team ids within Org
	Roles    []RoleBinding `json:"roles,omitempty"` // role grants; see Authorizer for how they combine
	Disabled bool          `json:"disabled,omitempty"`
}

// CredentialKind names the proof a client presents to Identity.Authenticate.
type CredentialKind string

// Credential kinds. An Identity may support a subset and returns ErrUnsupportedCredential for the rest.
const (
	CredOIDCIDToken   CredentialKind = "oidc_id_token"  // a signed OIDC ID token (ee/sso)
	CredSAMLAssertion CredentialKind = "saml_assertion" // a base64 SAML response (ee/sso)
	CredAPIToken      CredentialKind = "api_token"      // a long-lived service or user token
	CredAgentToken    CredentialKind = "agent_token"    // a per-run token minted for a bot (ee/identity)
)

// Credential is what a client presents. Value is secret: implementations never log it, never put
// it in an error, and compare it in constant time.
type Credential struct {
	Kind  CredentialKind
	Value string
}

// Identity resolves who a caller is. It answers identity only, never permission: what a
// Principal may do is Authorizer's job.
type Identity interface {
	// Authenticate verifies c and returns its Principal. It returns ErrUnauthenticated when c is
	// malformed, expired or does not verify, ErrUnsupportedCredential when c.Kind is not handled,
	// and ErrDeprovisioned when c verifies but its principal was deprovisioned. A deprovisioned
	// principal never authenticates again, even with a credential issued before Deprovision.
	Authenticate(ctx context.Context, c Credential) (Principal, error)

	// LookupBySCIM returns the principal the identity provider knows by SCIM resource id scimID,
	// including a deprovisioned one (Disabled set). It returns ErrNotFound for an unknown id.
	LookupBySCIM(ctx context.Context, scimID string) (Principal, error)

	// Deprovision disables the principal with this id: from its return, Authenticate fails with
	// ErrDeprovisioned and LookupBySCIM reports Disabled. It is idempotent. It returns ErrNotFound
	// for an unknown id. It must not delete audit history.
	Deprovision(ctx context.Context, principalID string) error
}
