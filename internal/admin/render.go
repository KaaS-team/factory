//go:build ee

// SPDX-License-Identifier: LicenseRef-KaaS-EE

package admin

import (
	"bytes"
	"context"
	"embed"
	"html/template"
	"net/http"
	"strings"

	"github.com/KaaS-team/factory/ee/contract"
	"github.com/KaaS-team/ui"
)

//go:embed templates/*.tmpl
var pageFiles embed.FS

//go:embed static/admin.css
var adminCSS []byte

// navItems is the console nav, in order. Page names are the template names.
var navItems = []struct{ Page, Label, Href string }{
	{"users", "Users and roles", "/admin/users"},
	{"policy", "Policy", "/admin/policy"},
	{"budgets", "Budgets", "/admin/budgets"},
	{"audit", "Audit log", "/admin/audit"},
	{"workers", "Workers", "/admin/workers"},
}

type pages struct{ t *template.Template }

// funcs build partial data inline in the page templates.
var funcs = template.FuncMap{
	"dict": func(kv ...any) map[string]any {
		m := map[string]any{}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i].(string)] = kv[i+1]
		}
		return m
	},
	"chip":      func(label, status string) ui.Chip { return ui.Chip{Label: label, Status: ui.Status(status)} },
	"empty":     func(title, body string) ui.Empty { return ui.Empty{Title: title, Body: body} },
	"attention": func(status, title, body string) ui.Attention { return ui.Attention{Status: ui.Status(status), Title: title, Body: body} },
	"title": func(s string) string {
		if s == "" {
			return s
		}
		return strings.ToUpper(s[:1]) + s[1:]
	},
}

func newPages() (*pages, error) {
	t, err := ui.Partials.Clone()
	if err != nil {
		return nil, err
	}
	t, err = t.Funcs(funcs).ParseFS(pageFiles, "templates/*.tmpl")
	if err != nil {
		return nil, err
	}
	return &pages{t: t}, nil
}

// view is what every page template gets: its own data plus the notice line.
type view struct {
	Title  string
	Notice string // a change that just went through
	Data   any
}

// render draws page inside the shell with status code.
func (p *pages) render(w http.ResponseWriter, status int, page, title, notice string, data any) {
	var body bytes.Buffer
	if err := p.t.ExecuteTemplate(&body, page, view{Title: title, Notice: notice, Data: data}); err != nil {
		http.Error(w, "admin: the page could not be drawn", http.StatusInternalServerError)
		return
	}
	nav := ui.Nav{Brand: "Factory admin", Label: "Admin"}
	for _, n := range navItems {
		nav.Items = append(nav.Items, ui.NavItem{Label: n.Label, Href: n.Href, Current: n.Page == page})
	}
	shell := ui.Shell{
		Head: ui.Head{Title: title + " · Factory admin", Styles: []string{"/admin/static/admin.css"}},
		Nav:  nav,
		Body: template.HTML(body.String()), //nolint:gosec // body is our own html/template output, already escaped
	}
	var out bytes.Buffer
	if err := p.t.ExecuteTemplate(&out, "shell", shell); err != nil {
		http.Error(w, "admin: the page could not be drawn", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(out.Bytes())
}

// error draws a refusal or failure page. It carries no operator detail.
func (p *pages) error(w http.ResponseWriter, status int, title, body string) {
	p.render(w, status, "error", title, "", struct{ Body string }{body})
}

func (c *Console) css(w http.ResponseWriter, _ *http.Request, _ contract.Principal) {
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	_, _ = w.Write(adminCSS)
}

// notices are the fixed messages a redirect after a change may show, by key.
var notices = map[string]string{
	"user-added":    "User added. The change is in the audit log.",
	"role-changed":  "Role changed. The change is in the audit log.",
	"status-change": "User status changed. The change is in the audit log.",
	"policy-saved":  "Policy checked and saved. The change is in the audit log.",
	"budget-saved":  "Budget checked and saved. The change is in the audit log.",
	"drained":       "Worker is draining. It finishes its job and takes no more.",
}

// done redirects to page after a change, with its notice.
func done(w http.ResponseWriter, r *http.Request, page, key string) {
	http.Redirect(w, r, page+"?done="+key, http.StatusSeeOther)
}

func notice(r *http.Request) string { return notices[r.URL.Query().Get("done")] }

// change records the change, then applies it. The event is written first, so a change never
// happens without one; if apply then fails, an error event follows.
func (c *Console) change(ctx context.Context, p contract.Principal, a contract.Action, data map[string]string, apply func() error) error {
	if err := c.record(ctx, p, a, contract.OutcomeAllow, "", data); err != nil {
		return err
	}
	if err := apply(); err != nil {
		_ = c.record(ctx, p, a, contract.OutcomeError, "", data)
		return err
	}
	return nil
}

// fail draws a failed change. The message is ours, not the error text, which may hold paths.
func (c *Console) fail(w http.ResponseWriter, title string) {
	c.page.error(w, http.StatusInternalServerError, title, "The change was not made. Try again, and check the factory log if it keeps failing.")
}
