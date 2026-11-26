//go:build ee

// SPDX-License-Identifier: LicenseRef-KaaS-EE

package identity

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/KaaS-team/factory/ee/authz"
	"github.com/KaaS-team/factory/ee/contract"
	"github.com/KaaS-team/factory/ee/scim"
	"github.com/KaaS-team/factory/ee/sso"
)

// IDTokens verifies a bearer OIDC ID token. *sso.IDTokenVerifier is the production one.
type IDTokens interface {
	Verify(ctx context.Context, raw string) (sso.Session, error)
}

// DirectoryConfig wires a Directory. Org is the one org it serves and the SCIM tenant. Tokens and
// Agents are optional: a credential kind with no verifier is ErrUnsupportedCredential.
type DirectoryConfig struct {
	Org    string
	Tokens IDTokens          // for contract.CredOIDCIDToken
	Agents *Issuer           // for contract.CredAgentToken
	Groups authz.GroupMap    // IdP and SCIM group names to role bindings
	Gate   func(Claims) bool // optional extra check on an agent token, for example a run still open
}

// Directory implements contract.Identity. SCIM is the source of truth for who exists: a person
// signs in through OIDC or SAML, but only a SCIM-provisioned, active user is let in, and a user
// that SCIM or Deprovision turned off is refused on the very next call. Principal ids are SCIM
// resource ids, never emails.
type Directory struct {
	cfg   DirectoryConfig
	store scim.Store

	mu   sync.Mutex
	off  map[string]bool   // principal ids turned off by Deprovision
	gone map[string]string // SCIM user id -> user name, for users the store deleted
}

var _ contract.Identity = (*Directory)(nil)

// NewDirectory returns a Directory over store. Hand Store() to the SCIM handler, not store itself,
// so a SCIM delete is remembered as a deprovision.
func NewDirectory(store scim.Store, cfg DirectoryConfig) (*Directory, error) {
	if cfg.Org == "" {
		return nil, errors.New("identity: directory needs an org")
	}
	if err := cfg.Groups.Validate(); err != nil {
		return nil, err
	}
	return &Directory{cfg: cfg, store: store, off: map[string]bool{}, gone: map[string]string{}}, nil
}

// Store is the SCIM store to serve. It records deleted users so they stay deprovisioned.
func (d *Directory) Store() scim.Store { return tracked{d.store, d} }

type tracked struct {
	scim.Store
	d *Directory
}

func (t tracked) DeleteUser(id string) error {
	u, err := t.Store.GetUser(id)
	if err != nil {
		return err
	}
	if err := t.Store.DeleteUser(id); err != nil {
		return err
	}
	t.d.mu.Lock()
	t.d.gone[id] = u.UserName
	t.d.mu.Unlock()
	return nil
}

// Authenticate verifies c. Errors never carry the credential.
func (d *Directory) Authenticate(ctx context.Context, c contract.Credential) (contract.Principal, error) {
	switch c.Kind {
	case contract.CredOIDCIDToken:
		if d.cfg.Tokens == nil {
			return contract.Principal{}, fmt.Errorf("%w: no OIDC verifier configured", contract.ErrUnsupportedCredential)
		}
		s, err := d.cfg.Tokens.Verify(ctx, c.Value)
		if err != nil {
			return contract.Principal{}, fmt.Errorf("%w: ID token refused", contract.ErrUnauthenticated)
		}
		return d.Session(s)
	case contract.CredAgentToken:
		if d.cfg.Agents == nil {
			return contract.Principal{}, fmt.Errorf("%w: no agent issuer configured", contract.ErrUnsupportedCredential)
		}
		return d.agent(c.Value)
	}
	return contract.Principal{}, fmt.Errorf("%w: %s", contract.ErrUnsupportedCredential, c.Kind)
}

// Session turns a verified sign-in, from OIDC or SAML, into a Principal. The SSO packages prove
// who someone is; this decides whether they exist and what they hold. A person SCIM never
// provisioned is ErrUnauthenticated; a deprovisioned one is ErrDeprovisioned.
func (d *Directory) Session(s sso.Session) (contract.Principal, error) {
	email := strings.ToLower(strings.TrimSpace(s.Email))
	users, err := d.store.ListUsers(d.cfg.Org)
	if err != nil {
		return contract.Principal{}, fmt.Errorf("%w: directory unavailable", contract.ErrUnauthenticated)
	}
	for _, u := range users {
		if strings.EqualFold(u.UserName, email) {
			p := d.principal(u, s.Groups)
			if p.Disabled {
				return contract.Principal{}, fmt.Errorf("%w: user is deprovisioned", contract.ErrDeprovisioned)
			}
			return p, nil
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, name := range d.gone {
		if strings.EqualFold(name, email) {
			return contract.Principal{}, fmt.Errorf("%w: user is deprovisioned", contract.ErrDeprovisioned)
		}
	}
	return contract.Principal{}, fmt.Errorf("%w: user is not provisioned", contract.ErrUnauthenticated)
}

// principal builds the Principal of a SCIM user. Groups are the IdP's claim plus the user's SCIM
// group memberships; both feed the same map.
func (d *Directory) principal(u scim.User, claimed []string) contract.Principal {
	groups := slices.Clone(claimed)
	if all, err := d.store.ListGroups(d.cfg.Org); err == nil {
		for _, g := range all {
			if slices.Contains(g.Members, u.ID) {
				groups = append(groups, g.DisplayName)
			}
		}
	}
	roles, teams := d.cfg.Groups.Bindings(d.cfg.Org, groups)
	d.mu.Lock()
	off := d.off[u.ID]
	d.mu.Unlock()
	return contract.Principal{ID: u.ID, Kind: contract.KindUser, Org: d.cfg.Org, Teams: teams, Roles: roles, Disabled: off || !u.Active}
}

// agent resolves a per-run token to a bot. The bot holds the bot role at org scope and nothing
// more; it is refused when its token is revoked, expired, for another org, or when the person who
// delegated it was deprovisioned.
func (d *Directory) agent(tok string) (contract.Principal, error) {
	cl, err := d.cfg.Agents.Check(tok)
	if err != nil || cl.Tenant != d.cfg.Org || (d.cfg.Gate != nil && !d.cfg.Gate(cl)) {
		return contract.Principal{}, fmt.Errorf("%w: agent token refused", contract.ErrUnauthenticated)
	}
	d.mu.Lock()
	_, gone := d.gone[cl.Delegator]
	off := d.off[cl.Delegator]
	d.mu.Unlock()
	if u, err := d.store.GetUser(cl.Delegator); (err == nil && !u.Active) || gone || off {
		return contract.Principal{}, fmt.Errorf("%w: the delegating user is deprovisioned", contract.ErrDeprovisioned)
	}
	return contract.Principal{ID: cl.AgentID, Kind: contract.KindBot, Org: cl.Tenant,
		Roles: []contract.RoleBinding{{Role: contract.RoleBot, Scope: contract.Scope{Kind: contract.ScopeOrg, ID: cl.Tenant}}}}, nil
}

// LookupBySCIM returns the user with SCIM id scimID, a deprovisioned one included.
func (d *Directory) LookupBySCIM(_ context.Context, scimID string) (contract.Principal, error) {
	if u, err := d.store.GetUser(scimID); err == nil && u.Tenant == d.cfg.Org {
		return d.principal(u, nil), nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.gone[scimID]; ok {
		return contract.Principal{ID: scimID, Kind: contract.KindUser, Org: d.cfg.Org, Disabled: true}, nil
	}
	return contract.Principal{}, fmt.Errorf("%w: no such SCIM id", contract.ErrNotFound)
}

// Deprovision disables the user. It is idempotent and keeps the SCIM record, so history stays.
func (d *Directory) Deprovision(_ context.Context, principalID string) error {
	u, err := d.store.GetUser(principalID)
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, gone := d.gone[principalID]; (err != nil || u.Tenant != d.cfg.Org) && !gone {
		return fmt.Errorf("%w: no such principal", contract.ErrNotFound)
	}
	d.off[principalID] = true
	if err == nil && u.Active {
		u.Active = false
		return d.store.UpdateUser(u)
	}
	return nil
}
