//go:build ee

// SPDX-License-Identifier: LicenseRef-KaaS-EE

package admin

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KaaS-team/factory/ee/audit"
	"github.com/KaaS-team/factory/ee/authz"
	"github.com/KaaS-team/factory/ee/budget"
	"github.com/KaaS-team/factory/ee/contract"
	"github.com/KaaS-team/factory/ee/identity"
	"github.com/KaaS-team/factory/ee/policy"
	"github.com/KaaS-team/factory/ee/scim"
	"github.com/KaaS-team/factory/ee/sso"
)

const org = "acme"

// now is the fixture clock.
var now = time.Date(2026, 11, 26, 21, 0, 0, 0, time.UTC)

// tokens maps a bearer token to the sign-in it stands for, as an IdP would.
type tokens map[string]sso.Session

func (t tokens) Verify(_ context.Context, raw string) (sso.Session, error) {
	if s, ok := t[raw]; ok {
		return s, nil
	}
	return sso.Session{}, errors.New("bad token")
}

// The people of the fixture, by role. Their token is "tok-" + name.
var people = []struct{ name, group string }{
	{"ana", "factory-owners"}, {"ben", "factory-admins"}, {"cy", "engineering"}, {"dee", "observers"},
	{"eli", "engineering"}, {"fay", "factory-admins"},
}

var groupMap = authz.GroupMap{
	{Group: "factory-owners", Role: contract.RoleOwner},
	{Group: "factory-admins", Role: contract.RoleAdmin},
	{Group: "engineering", Role: contract.RoleMember},
	{Group: "observers", Role: contract.RoleViewer},
}

const orgPolicy = `version = 1
layer = "org"
name = "acme"

[models]
allowed = ["provider-large-2026", "provider-small-2026"]

[harnesses]
allowed = ["claude-code", "codex"]

[mcp]
allowed_servers = ["github", "linear", "sentry"]

[egress]
allowed_hosts = ["github.com", "*.githubusercontent.com", "proxy.golang.org"]

[merge]
auto_merge_risk = ["low"]

[approval]
require_human = ["merge", "policy.edit"]

[[budgets]]
scope = "org"
id = "acme"
usd = 5000.0
period = "month"

[[budgets]]
scope = "factory"
id = "acme-prod"
usd = 300.0
period = "day"

[[budgets]]
scope = "bot"
id = "bot-claude-1"
usd = 50.0
period = "day"
`

const teamPolicy = `version = 1
layer = "team"
name = "payments"

[models]
allowed = ["provider-small-2026"]

[[budgets]]
scope = "team"
id = "payments"
usd = 1200.0
period = "month"
`

// fakeWorkers is a worker registry in memory.
type fakeWorkers struct {
	mu sync.Mutex
	ws []WorkerInfo
}

func (f *fakeWorkers) List() []WorkerInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]WorkerInfo(nil), f.ws...)
}

func (f *fakeWorkers) Drain(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.ws {
		if f.ws[i].ID == id {
			f.ws[i].Draining = true
			return nil
		}
	}
	return errors.New("no such worker")
}

func (f *fakeWorkers) TTL() time.Duration { return 30 * time.Second }

type fixture struct {
	c       *Console
	srv     *httptest.Server
	log     *audit.Store
	dir     *identity.Directory
	local   *LocalUsers
	workers *fakeWorkers
	orgFile string
	team    string
	svc     LocalUser
}

// newFixture builds a console over the real directory, authorizer, audit chain, policy engine and
// budget, with fake IdP tokens and a fake worker registry.
func newFixture(t testing.TB) *fixture {
	t.Helper()
	tmp := t.TempDir()
	store := scim.NewMemoryStore()
	toks := tokens{}
	for _, p := range people {
		id := "u-" + p.name
		must(t, store.CreateUser(scim.User{ID: id, Tenant: org, UserName: p.name + "@acme.example", Active: true}))
		toks["tok-"+p.name] = sso.Session{Subject: p.name, Email: p.name + "@acme.example"}
	}
	must(t, store.CreateUser(scim.User{ID: "u-gus", Tenant: org, UserName: "gus@acme.example", Active: false}))
	toks["tok-gus"] = sso.Session{Email: "gus@acme.example"}
	members := map[string][]string{}
	for _, p := range people {
		members[p.group] = append(members[p.group], "u-"+p.name)
	}
	for i, g := range []string{"factory-owners", "factory-admins", "engineering", "observers"} {
		must(t, store.CreateGroup(scim.Group{ID: "g" + string(rune('1'+i)), Tenant: org, DisplayName: g, Members: members[g]}))
	}
	dir, err := identity.NewDirectory(store, identity.DirectoryConfig{Org: org, Tokens: toks, Groups: groupMap})
	must(t, err)

	orgFile, teamFile := filepath.Join(tmp, "org.toml"), filepath.Join(tmp, "team.toml")
	must(t, os.WriteFile(orgFile, []byte(orgPolicy), 0o600))
	must(t, os.WriteFile(teamFile, []byte(teamPolicy), 0o600))
	paths := policy.Paths{Org: orgFile, Team: teamFile}
	eff, err := policy.Resolve(paths)
	must(t, err)
	log := audit.Open(filepath.Join(tmp, "audit.jsonl"))
	bud, err := budget.New(budget.Config{Limits: budget.FromPolicy(eff.Budgets()), Now: func() time.Time { return now }})
	must(t, err)
	spend := func(amount float64, keys ...contract.BudgetKey) {
		res, err := bud.Reserve(context.Background(), keys, budget.Micros(amount))
		must(t, err)
		must(t, bud.Commit(context.Background(), res.ID, budget.Micros(amount)))
	}
	spend(1190.25, contract.BudgetKey{Scope: contract.BudgetOrg, ID: org})
	spend(640, contract.BudgetKey{Scope: contract.BudgetOrg, ID: org}, contract.BudgetKey{Scope: contract.BudgetTeam, ID: "payments"})
	spend(255.10, contract.BudgetKey{Scope: contract.BudgetFactory, ID: "acme-prod"})
	spend(50, contract.BudgetKey{Scope: contract.BudgetBot, ID: "bot-claude-1"})

	local := &LocalUsers{Path: filepath.Join(tmp, "users.json")}
	svc, err := local.Add(LocalUser{Name: "ci-release", Kind: contract.KindService, Role: contract.RoleMember})
	must(t, err)
	_, err = local.Add(LocalUser{Name: "staging-factory", Kind: contract.KindService, Role: contract.RoleViewer, Disabled: true})
	must(t, err)

	wk := &fakeWorkers{ws: []WorkerInfo{
		{ID: "w-01", Pool: "linux-amd64", Labels: map[string]string{"os": "linux", "arch": "amd64", "pool": "linux-amd64"}, Caps: []string{"docker", "go"}, LastSeen: now.Add(-4 * time.Second), Job: "job-1841"},
		{ID: "w-02", Pool: "linux-amd64", Labels: map[string]string{"os": "linux", "arch": "amd64", "pool": "linux-amd64"}, Caps: []string{"docker", "go"}, LastSeen: now.Add(-9 * time.Second)},
		{ID: "w-03", Pool: "linux-amd64", Labels: map[string]string{"os": "linux", "arch": "amd64", "pool": "linux-amd64"}, Caps: []string{"docker"}, LastSeen: now.Add(-50 * time.Second)},
		{ID: "w-11", Pool: "gpu", Labels: map[string]string{"os": "linux", "arch": "arm64", "pool": "gpu"}, Caps: []string{"cuda"}, LastSeen: now.Add(-3 * time.Minute)},
		{ID: "w-12", Pool: "gpu", Labels: map[string]string{"os": "linux", "arch": "arm64", "pool": "gpu"}, Caps: []string{"cuda"}, LastSeen: now.Add(-2 * time.Second), Draining: true},
	}}
	c, err := New(Config{Org: org, Identity: dir, Authorizer: authz.New(), Audit: log, SCIM: store, Groups: groupMap,
		Local: local, Policy: paths, Budget: bud, Workers: wk, Now: func() time.Time { return now }})
	must(t, err)
	srv := httptest.NewServer(c.Handler())
	t.Cleanup(srv.Close)
	return &fixture{c: c, srv: srv, log: log, dir: dir, local: local, workers: wk, orgFile: orgFile, team: teamFile, svc: svc}
}

func must(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// do sends a request as who ("" for nobody). A POST carries form as a same-origin browser would.
func (f *fixture) do(t testing.TB, who, method, path string, form url.Values) (*http.Response, string) {
	t.Helper()
	var body *strings.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	} else {
		body = strings.NewReader("")
	}
	req, err := http.NewRequest(method, f.srv.URL+path, body)
	must(t, err)
	if who != "" {
		req.Header.Set("Authorization", "Bearer tok-"+who)
	}
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	cl := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := cl.Do(req)
	must(t, err)
	defer func() { _ = res.Body.Close() }()
	var sb strings.Builder
	_, _ = io.Copy(&sb, res.Body)
	return res, sb.String()
}

// events returns the whole audit log.
func (f *fixture) events(t testing.TB) []contract.AuditEvent {
	t.Helper()
	evs, err := f.log.Search(context.Background(), contract.AuditQuery{})
	must(t, err)
	return evs
}
