//go:build ee

// SPDX-License-Identifier: LicenseRef-KaaS-EE

// Package admin is the admin console: users and roles, the policy editor, budgets, the audit log
// and the worker pools. Every route is in one table and needs an admin; every change, allowed or
// refused, is an audit event.
package admin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/KaaS-team/factory/ee/authz"
	"github.com/KaaS-team/factory/ee/contract"
	"github.com/KaaS-team/factory/ee/policy"
	"github.com/KaaS-team/factory/ee/scim"
	"github.com/KaaS-team/ui"
)

// Prefix is where the console is mounted.
const Prefix = "/admin/"

// ConsoleAction is the action every console route needs on top of its own. Owner and admin hold
// it; member, viewer and bot do not, so they get 403 on every route, reads included.
const ConsoleAction = contract.ActionSettingsWrite

// CookieName is the cookie a browser carries its credential in. A bearer header wins over it.
const CookieName = "factory_token"

// Config wires a Console. Org, Identity, Authorizer and Audit are required; a page whose source is
// unset shows an empty state saying what to configure.
type Config struct {
	Org        string
	Identity   contract.Identity
	Authorizer contract.Authorizer
	Credential contract.CredentialKind // what the token is; zero means an OIDC ID token
	Audit      contract.Audit          // every change is recorded here

	SCIM   scim.Store     // provisioned users, shown read-only
	Groups authz.GroupMap // shown beside SCIM users so an admin sees where a role comes from
	Local  *LocalUsers    // principals managed in the console

	Policy policy.Paths    // the layer files the editor reads and writes
	Budget contract.Budget // spend against the effective limits

	Workers Workers // the worker registry

	Now func() time.Time
}

// Console serves the admin pages.
type Console struct {
	cfg  Config
	page *pages
}

// New checks cfg and returns a Console.
func New(cfg Config) (*Console, error) {
	switch {
	case cfg.Org == "":
		return nil, errors.New("admin: an org is required")
	case cfg.Identity == nil || cfg.Authorizer == nil:
		return nil, errors.New("admin: an identity and an authorizer are required")
	case cfg.Audit == nil:
		return nil, errors.New("admin: an audit log is required, every change is recorded")
	}
	if cfg.Credential == "" {
		cfg.Credential = contract.CredOIDCIDToken
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	p, err := newPages()
	if err != nil {
		return nil, err
	}
	return &Console{cfg: cfg, page: p}, nil
}

// Route is one entry of the route table, the only place a console handler is registered.
type Route struct {
	Method  string
	Pattern string
	Action  contract.Action // needed on top of ConsoleAction; empty only when Open is set
	Open    string          // why the route needs no sign-in
	Handler func(w http.ResponseWriter, r *http.Request, p contract.Principal)
}

// Routes is the route table. A test walks it with a member and expects 403 on every guarded entry.
func (c *Console) Routes() []Route {
	return []Route{
		{Method: "GET", Pattern: "/admin/static/admin.css", Open: "the stylesheet holds no data", Handler: c.css},
		{Method: "GET", Pattern: "/admin/{$}", Action: ConsoleAction, Handler: c.home},
		{Method: "GET", Pattern: "/admin/users", Action: ConsoleAction, Handler: c.users},
		{Method: "POST", Pattern: "/admin/users", Action: contract.ActionAdminUsers, Handler: c.addUser},
		{Method: "POST", Pattern: "/admin/users/role", Action: contract.ActionAdminUsers, Handler: c.setRole},
		{Method: "POST", Pattern: "/admin/users/status", Action: contract.ActionAdminUsers, Handler: c.setStatus},
		{Method: "GET", Pattern: "/admin/policy", Action: contract.ActionPolicyRead, Handler: c.policyView},
		{Method: "POST", Pattern: "/admin/policy", Action: contract.ActionPolicyEdit, Handler: c.policySave},
		{Method: "GET", Pattern: "/admin/budgets", Action: contract.ActionBudgetRead, Handler: c.budgets},
		{Method: "POST", Pattern: "/admin/budgets", Action: contract.ActionBudgetEdit, Handler: c.budgetSave},
		{Method: "GET", Pattern: "/admin/audit", Action: contract.ActionAuditRead, Handler: c.auditSearch},
		{Method: "GET", Pattern: "/admin/audit/export", Action: contract.ActionAuditExport, Handler: c.auditExport},
		{Method: "GET", Pattern: "/admin/workers", Action: contract.ActionWorkerManage, Handler: c.workers},
		{Method: "POST", Pattern: "/admin/workers/drain", Action: contract.ActionWorkerManage, Handler: c.drain},
	}
}

// Handler serves the console under Prefix and the design system CSS under ui.Prefix.
func (c *Console) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle(ui.Prefix, ui.Handler())
	for _, rt := range c.Routes() {
		h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { rt.Handler(w, r, contract.Principal{}) })
		if rt.Open == "" {
			h = c.guard(rt)
		}
		mux.Handle(rt.Method+" "+rt.Pattern, h)
	}
	return secure(mux)
}

// secure sets the headers every console response carries.
func secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// guard lets a request through only for a principal allowed both ConsoleAction and the route's
// action. A missing or bad credential is 401, a refused one 403. A refused change is recorded.
func (c *Console) guard(rt Route) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw := credential(r)
		if raw == "" {
			c.refuse(w, r, http.StatusUnauthorized, contract.ReasonIdentityUnauthenticated)
			return
		}
		p, err := c.cfg.Identity.Authenticate(r.Context(), contract.Credential{Kind: c.cfg.Credential, Value: raw})
		switch {
		case errors.Is(err, contract.ErrDeprovisioned):
			c.refuse(w, r, http.StatusForbidden, contract.ReasonIdentityDeprovisioned)
			return
		case err != nil:
			c.refuse(w, r, http.StatusUnauthorized, contract.ReasonIdentityUnauthenticated)
			return
		}
		res := contract.Resource{Org: c.cfg.Org}
		for _, a := range []contract.Action{ConsoleAction, rt.Action} {
			if d := c.cfg.Authorizer.Can(r.Context(), p, a, res); !d.Allow {
				if r.Method == http.MethodPost {
					c.record(r.Context(), p, rt.Action, contract.OutcomeDeny, d.Reason, map[string]string{"route": rt.Pattern})
				}
				c.refuse(w, r, http.StatusForbidden, d.Reason)
				return
			}
		}
		if r.Method == http.MethodPost && !sameOrigin(r) {
			c.refuse(w, r, http.StatusForbidden, reasonCrossSite)
			return
		}
		rt.Handler(w, r, p)
	}
}

// reasonCrossSite marks a POST that did not come from a console page.
const reasonCrossSite contract.ReasonCode = "admin.cross_site"

// credential reads the bearer token, or the cookie a browser sends.
func credential(r *http.Request) string {
	if raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		return raw
	}
	if ck, err := r.Cookie(CookieName); err == nil {
		return ck.Value
	}
	return ""
}

// sameOrigin refuses a cross-site form post. A browser sends Sec-Fetch-Site or Origin; a client
// with a bearer header and neither is not a browser, so no cookie rode along.
func sameOrigin(r *http.Request) bool {
	if s := r.Header.Get("Sec-Fetch-Site"); s != "" {
		return s == "same-origin"
	}
	if o := r.Header.Get("Origin"); o != "" {
		u, err := url.Parse(o)
		return err == nil && u.Host == r.Host
	}
	return r.Header.Get("Authorization") != ""
}

// refuse writes a fixed message and the contract's reason code, never identity provider detail.
func (c *Console) refuse(w http.ResponseWriter, r *http.Request, status int, why contract.ReasonCode) {
	if status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", `Bearer realm="factory"`)
	}
	title := "Sign in to use the admin console"
	if status == http.StatusForbidden {
		title = "You do not have access to this page"
	}
	c.page.error(w, status, title, "Reason: "+string(why)+". Ask an owner of "+c.cfg.Org+" for the admin role.")
}

// record appends one audit event. A failed write is logged in the response as an error by the
// caller; it never silently drops the event.
func (c *Console) record(ctx context.Context, p contract.Principal, a contract.Action, o contract.Outcome, why contract.ReasonCode, data map[string]string) error {
	_, err := c.cfg.Audit.Record(ctx, contract.AuditEvent{
		Time: c.cfg.Now(), Actor: p.ID, Action: a, Resource: c.cfg.Org, Outcome: o, Reason: why, Data: data,
	})
	if err != nil {
		return fmt.Errorf("admin: audit write failed, the change was not made: %w", err)
	}
	return nil
}

// home sends the console root to the first page.
func (c *Console) home(w http.ResponseWriter, r *http.Request, _ contract.Principal) {
	http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
}
