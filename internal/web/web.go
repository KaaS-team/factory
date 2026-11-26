// Package web is the fleet app: a board of runs by state and wave, the attention inbox, and a run page
// with a live timeline and log that can retry, cancel and steer the run. Every page is built on the
// KaaS design system partials (github.com/KaaS-team/ui). It reaches the core only through api.Control
// and api.Event, and every route asks a contract.Authorizer before it reads or writes.
package web

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"errors"
	"html/template"
	"io/fs"
	"net/http"
	"net/url"
	"strings"

	ui "github.com/KaaS-team/ui"

	"github.com/KaaS-team/factory/ee/contract"
	"github.com/KaaS-team/factory/internal/api"
)

//go:embed templates/*.tmpl
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

// LocalOrg is the org of the local principal used when Options.Identity is nil.
const LocalOrg = "local"

// Cookie names. The token cookie holds the API token a person signed in with; the CSRF cookie
// holds the double-submit token every form sends back.
const (
	tokenCookie = "kaas_token"
	csrfCookie  = "kaas_csrf"
)

// Options configures Handler.
type Options struct {
	// Identity resolves the bearer token or sign-in cookie to a Principal. Nil means local mode:
	// every request acts as one local user, and the page is meant for a loopback listener.
	Identity contract.Identity
	// Authorizer decides every read and write. Required with Identity. In local mode it may be nil,
	// and then the app is read-only unless AllowActions is set.
	Authorizer contract.Authorizer
	// AllowActions lets the local user retry, cancel, steer and resolve attention items when
	// Identity is nil. It has no effect when Identity is set.
	AllowActions bool
	// Org is the org this factory's runs belong to: the Resource.Org every check names, so a
	// principal of another org is denied. Required with Identity; local mode uses LocalOrg.
	Org string
	// Operator shows usage, budget and dispatcher detail. False by default; customers never see them.
	Operator bool
}

type server struct {
	ctl  api.Control
	opts Options
	tpl  *template.Template
}

// Handler returns the fleet app. It panics when Identity is set without an Authorizer or Org, so a
// misconfigured server never starts with authz off.
func Handler(ctl api.Control, opts Options) http.Handler {
	if opts.Identity != nil && (opts.Authorizer == nil || opts.Org == "") {
		panic("web: an Identity needs an Authorizer and an Org")
	}
	if opts.Identity == nil && opts.Org == "" {
		opts.Org = LocalOrg
	}
	t := template.Must(ui.Partials.Clone())
	template.Must(t.Funcs(funcs).ParseFS(templateFS, "templates/*.tmpl"))
	s := &server{ctl: ctl, opts: opts, tpl: t}
	mux := http.NewServeMux()
	for _, r := range s.routes() {
		mux.Handle(r.pattern, s.wrap(r))
	}
	return secureHeaders(mux)
}

// route is one registered pattern. Public routes skip sign-in; every other route authenticates
// and checks its action (see authed).
type route struct {
	pattern string
	public  bool
	handler http.Handler
}

// routes is every route the handler serves, and the one list the tests walk.
func (s *server) routes() []route {
	static, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err) // unreachable: static is embedded at build time
	}
	pub := func(p string, h http.Handler) route { return route{pattern: p, public: true, handler: h} }
	auth := func(p string, h http.HandlerFunc) route { return route{pattern: p, handler: h} }
	return []route{
		pub("GET "+ui.Prefix, ui.Handler()),
		pub("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static))),
		pub("GET /signin", http.HandlerFunc(s.signinPage)),
		pub("POST /signin", http.HandlerFunc(s.signin)),
		pub("POST /signout", http.HandlerFunc(s.signout)),
		auth("GET /{$}", s.board),
		auth("GET /attention", s.inbox),
		auth("POST /attention/{item}", s.resolve),
		auth("GET /runs/{id}", s.run),
		auth("GET /runs/{id}/events", s.runEvents),
		auth("POST /runs/{id}/retry", s.act(contract.ActionRunRetry)),
		auth("POST /runs/{id}/cancel", s.act(contract.ActionRunCancel)),
		auth("POST /runs/{id}/steer", s.act(contract.ActionRunSteer)),
		auth("GET /events", s.events),
		auth("GET /ticket/{id}", s.legacyTicket),
	}
}

// secureHeaders sets the headers every response carries. The CSP allows no inline script or style.
func secureHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		hd.Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("Referrer-Policy", "same-origin")
		h.ServeHTTP(w, r)
	})
}

type principalKey struct{}

// principal returns the Principal authed stored on the request context.
func principal(r *http.Request) contract.Principal {
	p, _ := r.Context().Value(principalKey{}).(contract.Principal)
	return p
}

// localPrincipal is who acts in local mode.
var localPrincipal = contract.Principal{ID: "local", Kind: contract.KindUser, Org: LocalOrg}

// errSignIn means the request carries no valid credential.
var errSignIn = errors.New("sign in required")

// authenticate resolves the caller. Local mode always answers localPrincipal.
func (s *server) authenticate(r *http.Request) (contract.Principal, error) {
	if s.opts.Identity == nil {
		return localPrincipal, nil
	}
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		if c, err := r.Cookie(tokenCookie); err == nil {
			tok = c.Value
		}
	}
	if tok == "" {
		return contract.Principal{}, errSignIn
	}
	p, err := s.opts.Identity.Authenticate(r.Context(), contract.Credential{Kind: contract.CredAPIToken, Value: tok})
	if err != nil || p.Disabled {
		return contract.Principal{}, errSignIn
	}
	return p, nil
}

// can answers whether p may take a on repo. In local mode reads are always allowed and writes
// follow AllowActions, unless an Authorizer was given, which then decides.
func (s *server) can(ctx context.Context, p contract.Principal, a contract.Action, repo string) bool {
	if s.opts.Authorizer != nil {
		return s.opts.Authorizer.Can(ctx, p, a, contract.Resource{Org: s.opts.Org, Repo: repo}).Allow
	}
	return a == contract.ActionRunRead || s.opts.AllowActions
}

// wrap authenticates every non-public route. A page sends a person to /signin; a stream or a
// form post answers 401.
func (s *server) wrap(rt route) http.Handler {
	if rt.public {
		return rt.handler
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, err := s.authenticate(r)
		if err != nil {
			if r.Method == http.MethodGet && !strings.HasSuffix(r.URL.Path, "/events") {
				http.Redirect(w, r, "/signin?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
				return
			}
			s.fail(w, r, http.StatusUnauthorized, "Sign in required", "Sign in again to continue.")
			return
		}
		if r.Method == http.MethodPost && !s.csrfOK(r) {
			s.fail(w, r, http.StatusForbidden, "This form expired", "Reload the page and try again.")
			return
		}
		rt.handler.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, p)))
	})
}

// csrfToken returns the request's CSRF token, setting a new cookie when it has none.
func csrfToken(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie(csrfCookie); err == nil && len(c.Value) >= 32 {
		return c.Value
	}
	for _, sc := range w.Header().Values("Set-Cookie") { // already minted for this response
		if v, ok := strings.CutPrefix(sc, csrfCookie+"="); ok {
			v, _, _ = strings.Cut(v, ";")
			return v
		}
	}
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	v := base64.RawURLEncoding.EncodeToString(b)
	http.SetCookie(w, &http.Cookie{Name: csrfCookie, Value: v, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil})
	return v
}

// csrfOK checks the double-submit token and, when the browser sends one, the Origin header.
// A request with a bearer token is not a browser form and needs neither.
func (s *server) csrfOK(r *http.Request) bool {
	if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		return true
	}
	if o := r.Header.Get("Origin"); o != "" {
		u, err := url.Parse(o)
		if err != nil || u.Host != r.Host {
			return false
		}
	}
	c, err := r.Cookie(csrfCookie)
	if err != nil || c.Value == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(c.Value), []byte(r.PostFormValue("csrf"))) == 1
}

// page is what every screen template gets.
type page struct {
	Title    string
	Nav      string // the current nav item: board or attention
	CSRF     string
	Operator bool
	Local    bool // local mode: no sign-out button
	Inbox    int  // open attention items, shown as the nav count
	Data     any
}

// render executes name into the shell. A render failure answers a plain 500 with no detail.
func (s *server) render(w http.ResponseWriter, r *http.Request, code int, name string, p page) {
	p.CSRF = csrfToken(w, r)
	p.Operator = s.opts.Operator
	p.Local = s.opts.Identity == nil
	if p.Nav != "signin" && p.Nav != "error" {
		p.Inbox = s.openCount(r)
	}
	var body bytes.Buffer
	if err := s.tpl.ExecuteTemplate(&body, name, p); err != nil {
		http.Error(w, "the page could not be drawn", http.StatusInternalServerError)
		return
	}
	var out bytes.Buffer
	if err := s.tpl.ExecuteTemplate(&out, "app", appShell{Page: p, Body: template.HTML(body.String())}); err != nil { //nolint:gosec // body is our own executed template
		http.Error(w, "the page could not be drawn", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_, _ = w.Write(out.Bytes())
}

// appShell is the data for the "app" template: the page around a rendered body.
type appShell struct {
	Page page
	Body template.HTML
}

// failure is the data for the error screen.
type failure struct {
	Heading string
	Body    string
}

// fail renders the error screen with a plain message. It never carries the cause.
func (s *server) fail(w http.ResponseWriter, r *http.Request, code int, heading, body string) {
	s.render(w, r, code, "error", page{Title: heading, Nav: "error", Data: failure{Heading: heading, Body: body}})
}
