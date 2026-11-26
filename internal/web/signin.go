package web

import (
	"net/http"
	"net/url"
	"strings"

	ui "github.com/KaaS-team/ui"

	"github.com/KaaS-team/factory/ee/contract"
)

// safeNext keeps a post-sign-in redirect on this site: a path, never a scheme or another host.
func safeNext(v string) string {
	if !strings.HasPrefix(v, "/") || strings.HasPrefix(v, "//") || strings.HasPrefix(v, "/\\") {
		return "/"
	}
	return v
}

func (s *server) signinForm(w http.ResponseWriter, r *http.Request, next, msg string) ui.Form {
	return ui.Form{
		ID: "signin", Action: "/signin?next=" + url.QueryEscape(next), CSRF: csrfToken(w, r), Error: msg,
		Fields: []ui.Field{{Name: "token", Label: "API token", Type: "password", Required: true,
			Help: "Your admin issues the token. It is kept in a cookie for this browser only."}},
		Submit: ui.Button{Label: "Sign in", Type: "submit", Variant: "primary"},
	}
}

func (s *server) signinPage(w http.ResponseWriter, r *http.Request) {
	if s.opts.Identity == nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	next := safeNext(r.URL.Query().Get("next"))
	s.render(w, r, http.StatusOK, "signin", page{Title: "Sign in", Nav: "signin", Data: s.signinForm(w, r, next, "")})
}

func (s *server) signin(w http.ResponseWriter, r *http.Request) {
	if s.opts.Identity == nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	next := safeNext(r.URL.Query().Get("next"))
	if !s.csrfOK(r) {
		s.render(w, r, http.StatusForbidden, "signin", page{Title: "Sign in", Nav: "signin", Data: s.signinForm(w, r, next, "This form expired. Try again.")})
		return
	}
	tok := r.PostFormValue("token")
	p, err := s.opts.Identity.Authenticate(r.Context(), contract.Credential{Kind: contract.CredAPIToken, Value: tok})
	if tok == "" || err != nil || p.Disabled {
		s.render(w, r, http.StatusUnauthorized, "signin", page{Title: "Sign in", Nav: "signin", Data: s.signinForm(w, r, next, "That token did not work. Check it and try again.")})
		return
	}
	http.SetCookie(w, &http.Cookie{Name: tokenCookie, Value: tok, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil})
	http.Redirect(w, r, next, http.StatusSeeOther)
}

func (s *server) signout(w http.ResponseWriter, r *http.Request) {
	if !s.csrfOK(r) {
		s.fail(w, r, http.StatusForbidden, "This form expired", "Reload the page and try again.")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: tokenCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	http.Redirect(w, r, "/signin", http.StatusSeeOther)
}
