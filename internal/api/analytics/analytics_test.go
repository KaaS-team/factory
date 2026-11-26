package analytics_test

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/KaaS-team/factory/ee/contract"
	"github.com/KaaS-team/factory/internal/api/analytics"
	"github.com/KaaS-team/factory/internal/ledger"
)

var t0 = time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)

func run(id, team, repo, harness, status string, cost float64, day int) ledger.Entry {
	end := t0.AddDate(0, 0, day).Add(time.Minute)
	e := ledger.Entry{Schema: "kaas.run/v1", RunID: id, Repo: repo, Team: team, StartedAt: t0.AddDate(0, 0, day), EndedAt: &end,
		Status: status, CostUSD: &cost}
	in := int64(100)
	e.Tokens = &contract.LedgerTokens{Input: &in}
	e.Ticket.Source, e.Ticket.ID = "github", "1"
	e.Agent.Harness, e.Agent.Model = harness, "secret-model"
	return e
}

var data = []ledger.Entry{
	run("r1", "core", "acme/api", "claude-code", "merged", 1.5, 0),
	run("r2", "core", "acme/api", "codex", "failed", 0.5, 1),
	run("r3", "web", "acme/web", "claude-code", "merged", 2, 2),
	run("=cmd", "web", "acme/web", "claude-code", "open", 0, 3),
}

// fakeID maps a token to a principal.
type fakeID map[string]contract.Principal

func (f fakeID) Authenticate(_ context.Context, c contract.Credential) (contract.Principal, error) {
	if p, ok := f[c.Value]; ok && c.Kind == contract.CredAPIToken {
		return p, nil
	}
	return contract.Principal{}, errors.New("bad token at host-1.internal")
}
func (fakeID) LookupBySCIM(context.Context, string) (contract.Principal, error) {
	return contract.Principal{}, errors.New("unused")
}
func (fakeID) Deprovision(context.Context, string) error { return nil }

// fakeAz grants by role: an org binding covers everything, a team binding covers that team.
type fakeAz struct{}

func (fakeAz) Can(_ context.Context, p contract.Principal, a contract.Action, r contract.Resource) contract.Decision {
	for _, b := range p.Roles {
		if !slices.Contains(contract.Grants[b.Role], a) {
			continue
		}
		if b.Scope.Kind == contract.ScopeOrg || (b.Scope.Kind == contract.ScopeTeam && b.Scope.ID == r.Team) {
			return contract.Decision{Allow: true, Reason: contract.ReasonGranted}
		}
	}
	return contract.Decision{Reason: contract.ReasonNoBinding}
}

func principal(role contract.Role, scope contract.Scope) contract.Principal {
	return contract.Principal{ID: "u", Kind: contract.KindUser, Org: "acme", Roles: []contract.RoleBinding{{Role: role, Scope: scope}}}
}

func server(t *testing.T) *httptest.Server {
	t.Helper()
	org := contract.Scope{Kind: contract.ScopeOrg, ID: "acme"}
	ids := fakeID{
		"admin":  principal(contract.RoleAdmin, org),
		"viewer": principal(contract.RoleViewer, org),
		"web":    principal(contract.RoleViewer, contract.Scope{Kind: contract.ScopeTeam, ID: "web"}),
		"none":   {ID: "n", Org: "acme"},
	}
	h := analytics.Handler(func(context.Context) ([]ledger.Entry, error) { return data, nil }, ids, fakeAz{})
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	return s
}

func get(t *testing.T, s *httptest.Server, path, token string) (int, string, http.Header) {
	t.Helper()
	req, _ := http.NewRequest("GET", s.URL+path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	var b strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := res.Body.Read(buf)
		b.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return res.StatusCode, b.String(), res.Header
}

func runIDs(t *testing.T, body string) string {
	t.Helper()
	var v struct{ Runs []ledger.Entry }
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	var ids []string
	for _, e := range v.Runs {
		ids = append(ids, e.RunID)
	}
	return strings.Join(ids, ",")
}

func TestRunsFilters(t *testing.T) {
	s := server(t)
	for q, want := range map[string]string{
		"":                                   "r1,r2,r3,=cmd",
		"?team=core":                         "r1,r2",
		"?repo=acme/web":                     "r3,=cmd",
		"?harness=codex":                     "r2",
		"?since=2026-11-21":                  "r2,r3,=cmd",
		"?until=2026-11-22":                  "r1,r2",
		"?since=2026-11-21&until=2026-11-22": "r2",
		"?team=web&harness=claude-code&since=" + url.QueryEscape("2026-11-22T00:00:00Z"): "r3,=cmd",
		"?team=nope": "",
	} {
		code, body, _ := get(t, s, analytics.RunsPath+q, "admin")
		if code != 200 {
			t.Fatalf("%s: %d %s", q, code, body)
		}
		if got := runIDs(t, body); got != want {
			t.Errorf("%q: got %q want %q", q, got, want)
		}
	}
	if code, _, _ := get(t, s, analytics.RunsPath+"?since=yesterday", "admin"); code != 400 {
		t.Errorf("bad since: %d", code)
	}
	if code, _, _ := get(t, s, analytics.RunsPath+"?format=xml", "admin"); code != 400 {
		t.Errorf("bad format: %d", code)
	}
}

func TestRunsCSV(t *testing.T) {
	s := server(t)
	code, body, h := get(t, s, analytics.RunsPath+"?format=csv&team=web", "admin")
	if code != 200 || !strings.HasPrefix(h.Get("Content-Type"), "text/csv") {
		t.Fatalf("%d %v", code, h)
	}
	rows, err := csv.NewReader(strings.NewReader(body)).ReadAll()
	if err != nil || len(rows) != 3 {
		t.Fatalf("%v %v", err, rows)
	}
	col := func(name string) int { return slices.Index(rows[0], name) }
	if rows[1][col("run_id")] != "r3" || rows[1][col("duration_seconds")] != "60" || rows[1][col("cost_usd")] != "2" || rows[1][col("input_tokens")] != "100" {
		t.Errorf("row %v", rows[1])
	}
	if got := rows[2][col("run_id")]; got != "'=cmd" {
		t.Errorf("formula cell not neutralised: %q", got)
	}
}

func TestSpendAndCSV(t *testing.T) {
	s := server(t)
	code, body, _ := get(t, s, analytics.SpendPath+"?since=2026-11-21", "admin")
	var sum ledger.Summary
	if err := json.Unmarshal([]byte(body), &sum); code != 200 || err != nil {
		t.Fatalf("%d %v %s", code, err, body)
	}
	if sum.Factory.CostUSD != 2.5 || sum.Teams["core"].CostUSD != 0.5 || sum.Teams["web"].Merged != 1 {
		t.Errorf("%+v", sum)
	}
	_, body, h := get(t, s, analytics.SpendPath+"?format=csv", "admin")
	rows, _ := csv.NewReader(strings.NewReader(body)).ReadAll()
	if !strings.HasPrefix(h.Get("Content-Type"), "text/csv") || len(rows) != 4 || rows[1][0] != "(factory)" || rows[1][4] != "4" {
		t.Errorf("%v %v", h, rows)
	}
}

func TestAuth(t *testing.T) {
	s := server(t)
	p := analytics.RunsPath
	if code, body, _ := get(t, s, p, ""); code != 401 || strings.Contains(body, "host-1") {
		t.Errorf("no token: %d %s", code, body)
	}
	if code, body, _ := get(t, s, p, "wrong"); code != 401 || strings.Contains(body, "host-1") {
		t.Errorf("bad token leaks cause: %d %s", code, body)
	}
	if code, _, _ := get(t, s, p, "none"); code != 403 {
		t.Errorf("no grant: %d", code)
	}
	// A team-scoped viewer sees only its own team's runs and spend.
	_, body, _ := get(t, s, p, "web")
	if got := runIDs(t, body); got != "r3,=cmd" {
		t.Errorf("team viewer: %q", got)
	}
	_, body, _ = get(t, s, analytics.SpendPath, "web")
	var sum ledger.Summary
	_ = json.Unmarshal([]byte(body), &sum)
	if sum.Factory.Runs != 2 || len(sum.Teams) != 1 {
		t.Errorf("team viewer spend: %+v", sum)
	}
	// Asking for another team's runs is a 403, not an empty list that hides the refusal.
	if code, _, _ := get(t, s, p+"?team=core", "web"); code != 403 {
		t.Errorf("other team: %d", code)
	}
}

// TestRunsHideOperatorDetail: a caller without budget.read gets no model, tokens or cost.
func TestRunsHideOperatorDetail(t *testing.T) {
	s := server(t)
	// "web" and "viewer" both hold budget.read in Grants, so use a role without it.
	h := analytics.Handler(func(context.Context) ([]ledger.Entry, error) { return data, nil },
		fakeID{"nb": principal("runreader", contract.Scope{Kind: contract.ScopeOrg})}, runReadOnly{})
	srv := httptest.NewServer(h)
	defer srv.Close()
	_, body, _ := get(t, srv, analytics.RunsPath, "nb")
	for _, bad := range []string{"secret-model", "cost_usd", "tokens"} {
		if strings.Contains(body, bad) {
			t.Errorf("%q in %s", bad, body)
		}
	}
	_, body, _ = get(t, srv, analytics.RunsPath+"?format=csv", "nb")
	if strings.Contains(body, "secret-model") || strings.Contains(body, "cost_usd") {
		t.Errorf("csv: %s", body)
	}
	if code, _, _ := get(t, srv, analytics.SpendPath, "nb"); code != 403 {
		t.Errorf("spend: %d", code)
	}
	if _, body, _ = get(t, s, analytics.RunsPath, "admin"); !strings.Contains(body, "secret-model") {
		t.Error("admin lost the detail")
	}
}

type runReadOnly struct{}

func (runReadOnly) Can(_ context.Context, _ contract.Principal, a contract.Action, _ contract.Resource) contract.Decision {
	return contract.Decision{Allow: a == contract.ActionRunRead}
}

func TestLedgerErrorIsPlain(t *testing.T) {
	h := analytics.Handler(func(context.Context) ([]ledger.Entry, error) { return nil, errors.New("open /srv/x/runs.jsonl") },
		fakeID{"a": principal(contract.RoleAdmin, contract.Scope{Kind: contract.ScopeOrg})}, fakeAz{})
	s := httptest.NewServer(h)
	defer s.Close()
	code, body, _ := get(t, s, analytics.RunsPath, "a")
	if code != 500 || strings.Contains(body, "/srv") {
		t.Errorf("%d %s", code, body)
	}
}

// TestOpenAPIListsTheRoutes: the spec names exactly the routes the handler serves and the query
// parameters the handler reads.
func TestOpenAPIListsTheRoutes(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "contracts", "analytics.openapi.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	spec := string(b)
	paths := regexp.MustCompile(`(?m)^  (/\S+):$`).FindAllStringSubmatch(spec, -1)
	var got []string
	for _, m := range paths {
		got = append(got, m[1])
	}
	want := []string{analytics.RunsPath, analytics.SpendPath}
	if !slices.Equal(got, want) {
		t.Errorf("spec paths %v, handler %v", got, want)
	}
	for _, q := range []string{"team", "repo", "harness", "since", "until", "format"} {
		if !strings.Contains(spec, "name: "+q) {
			t.Errorf("spec lacks query parameter %s", q)
		}
	}
}
