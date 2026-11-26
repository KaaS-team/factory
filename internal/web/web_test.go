package web

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/KaaS-team/factory/ee/contract"
	"github.com/KaaS-team/factory/internal/api"
	"github.com/KaaS-team/factory/internal/api/porttest"
	"github.com/KaaS-team/factory/internal/attention"
)

// fakeIdentity maps tokens to principals.
type fakeIdentity map[string]contract.Principal

func (f fakeIdentity) Authenticate(_ context.Context, c contract.Credential) (contract.Principal, error) {
	p, ok := f[c.Value]
	if !ok || c.Kind != contract.CredAPIToken {
		return contract.Principal{}, contract.ErrUnauthenticated
	}
	return p, nil
}
func (f fakeIdentity) LookupBySCIM(context.Context, string) (contract.Principal, error) {
	return contract.Principal{}, contract.ErrNotFound
}
func (f fakeIdentity) Deprovision(context.Context, string) error { return nil }

// grantsAuthz answers from contract.Grants: same org, a binding at org scope or on the repo, and a
// role that grants the action. It is the slice of the contract these tests need.
type grantsAuthz struct{}

func (grantsAuthz) Can(_ context.Context, p contract.Principal, a contract.Action, r contract.Resource) contract.Decision {
	if p.Org != r.Org || p.Disabled {
		return contract.Decision{Reason: contract.ReasonCrossOrg}
	}
	for _, b := range p.Roles {
		covers := b.Scope.Kind == contract.ScopeOrg && b.Scope.ID == r.Org ||
			b.Scope.Kind == contract.ScopeRepo && r.Repo != "" && b.Scope.ID == r.Repo
		if covers && slices.Contains(contract.Grants[b.Role], a) {
			return contract.Decision{Allow: true, Reason: contract.ReasonGranted}
		}
	}
	return contract.Decision{Reason: contract.ReasonNoBinding}
}

func role(id string, r contract.Role, scope contract.Scope) contract.Principal {
	return contract.Principal{ID: id, Kind: contract.KindUser, Org: "acme", Roles: []contract.RoleBinding{{Role: r, Scope: scope}}}
}

var (
	orgScope = contract.Scope{Kind: contract.ScopeOrg, ID: "acme"}
	people   = fakeIdentity{
		"member":   role("mia", contract.RoleMember, orgScope),
		"viewer":   role("vic", contract.RoleViewer, orgScope),
		"bot":      contract.Principal{ID: "bot1", Kind: contract.KindBot, Org: "acme", Roles: []contract.RoleBinding{{Role: contract.RoleBot, Scope: orgScope}}},
		"web-only": role("wes", contract.RoleMember, contract.Scope{Kind: contract.ScopeRepo, ID: "acme/web"}),
		"outsider": {ID: "olga", Kind: contract.KindUser, Org: "other", Roles: []contract.RoleBinding{{Role: contract.RoleOwner, Scope: contract.Scope{Kind: contract.ScopeOrg, ID: "other"}}}},
		"gone":     {ID: "gus", Kind: contract.KindUser, Org: "acme", Disabled: true, Roles: []contract.RoleBinding{{Role: contract.RoleOwner, Scope: orgScope}}},
	}
)

// fleet is a Control with an attention list: the porttest fake plus an in-memory store.
type fleet struct {
	*porttest.Control
	*attention.Store
}

func sampleBoard() api.Board {
	push := time.Date(2026, 11, 26, 20, 4, 5, 0, time.UTC)
	mk := func(id, title, repo string, p api.Phase, blocked ...api.TicketID) api.Slot {
		return api.Slot{
			Ticket:   api.Ticket{ID: api.TicketID(id), Title: title, Repo: repo, BlockedBy: blocked},
			Phase:    p,
			Session:  &api.Session{ID: "sess-" + id, Attempt: 2, Dispatcher: "cloud-runner.internal"},
			LastPush: push,
			Usage:    api.Usage{InputTokens: 123456, OutputTokens: 7654, CostUSD: 4.321},
			Budget:   api.Budget{Tokens: 999000},
		}
	}
	return api.Board{
		Slots: []api.Slot{
			mk("101", "Add retry to webhook", "acme/api", api.PhaseRunning),
			mk("102", "Beta <b>task</b>", "acme/api", api.PhaseStalled),
			mk("103", "Login page", "acme/web", api.PhaseQueued),
			mk("104", "Wire billing", "acme/api", api.PhaseClaimed, "101"),
			mk("105", "Old fix", "acme/web", api.PhaseMerged),
		},
		Ready:      []api.Ticket{{ID: "201", Title: "Waiting one", Repo: "acme/web", Priority: 1}},
		MergeQueue: []api.TicketID{"103"},
		FiresLeft:  5,
	}
}

func newFleet(t *testing.T) *fleet {
	t.Helper()
	st, err := attention.Open("")
	if err != nil {
		t.Fatal(err)
	}
	f := &fleet{Control: porttest.NewControl(sampleBoard()), Store: st}
	if _, err := st.Raise(attention.PlanApproval, "201", "The plan touches 4 files."); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Raise(attention.ExternalFailure, "102", "CI is down."); err != nil {
		t.Fatal(err)
	}
	f.Seed("101",
		api.Event{V: 1, Time: time.Date(2026, 11, 26, 20, 0, 0, 0, time.UTC), Kind: "dispatched", Ticket: "101", Data: map[string]any{"attempt": 2, "session": "sess-101"}},
		api.Event{V: 1, Time: time.Date(2026, 11, 26, 20, 1, 0, 0, time.UTC), Kind: "budget", Ticket: "101", Data: map[string]any{"verdict": "steer"}},
		api.Event{V: 1, Time: time.Date(2026, 11, 26, 20, 2, 0, 0, time.UTC), Kind: "pushed", Ticket: "101", Data: map[string]any{"branch": "factory/101-a2", "host": "runner-7.internal"}},
	)
	return f
}

func enterprise(ctl api.Control) http.Handler {
	return Handler(ctl, Options{Identity: people, Authorizer: grantsAuthz{}, Org: "acme"})
}

// call sends one request as the holder of token ("" for none). A POST carries a matching CSRF
// cookie and field.
func call(t *testing.T, h http.Handler, method, path, token string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	if form == nil {
		form = url.Values{}
	}
	var body io.Reader
	if method == http.MethodPost {
		form.Set("csrf", strings.Repeat("c", 32))
		body = strings.NewReader(form.Encode())
	}
	req := httptest.NewRequest(method, path, body)
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(&http.Cookie{Name: csrfCookie, Value: strings.Repeat("c", 32)})
	}
	if token != "" {
		req.AddCookie(&http.Cookie{Name: tokenCookie, Value: token})
	}
	rec := httptest.NewRecorder()
	if strings.HasSuffix(path, "/events") {
		ctx, cancel := context.WithTimeout(req.Context(), 50*time.Millisecond)
		defer cancel()
		req = req.WithContext(ctx)
	}
	h.ServeHTTP(rec, req)
	return rec
}

// routeCase is how one route answers each caller. TestEveryRouteWithAuthz fails when a route has no case.
type routeCase struct {
	method, path string
	form         url.Values
	signedOut    int // no credential
	denied       string
	deniedCode   int // the denied caller
	allowed      string
	allowedCode  int // the allowed caller
}

var routeCases = map[string]routeCase{
	"GET /_ui/":              {method: "GET", path: "/_ui/ui.css", signedOut: 200},
	"GET /static/":           {method: "GET", path: "/static/app.css", signedOut: 200},
	"GET /signin":            {method: "GET", path: "/signin", signedOut: 200},
	"POST /signin":           {method: "POST", path: "/signin", form: url.Values{"token": {"member"}}, signedOut: 303},
	"POST /signout":          {method: "POST", path: "/signout", signedOut: 303},
	"GET /{$}":               {method: "GET", path: "/", signedOut: 303, denied: "gone", deniedCode: 303, allowed: "viewer", allowedCode: 200},
	"GET /attention":         {method: "GET", path: "/attention", signedOut: 303, denied: "gone", deniedCode: 303, allowed: "viewer", allowedCode: 200},
	"POST /attention/{item}": {method: "POST", path: "/attention/a1", form: url.Values{"action": {"approve"}}, signedOut: 401, denied: "viewer", deniedCode: 403, allowed: "member", allowedCode: 303},
	"GET /runs/{id}":         {method: "GET", path: "/runs/101", signedOut: 303, denied: "web-only", deniedCode: 404, allowed: "viewer", allowedCode: 200},
	"GET /runs/{id}/events":  {method: "GET", path: "/runs/101/events", signedOut: 401, denied: "outsider", deniedCode: 404, allowed: "viewer", allowedCode: 200},
	"POST /runs/{id}/retry":  {method: "POST", path: "/runs/101/retry", signedOut: 401, denied: "viewer", deniedCode: 403, allowed: "bot", allowedCode: 303},
	"POST /runs/{id}/cancel": {method: "POST", path: "/runs/101/cancel", signedOut: 401, denied: "bot", deniedCode: 403, allowed: "member", allowedCode: 303},
	"POST /runs/{id}/steer":  {method: "POST", path: "/runs/101/steer", form: url.Values{"message": {"use the v2 client"}}, signedOut: 401, denied: "viewer", deniedCode: 403, allowed: "bot", allowedCode: 303},
	"GET /events":            {method: "GET", path: "/events", signedOut: 401, denied: "gone", deniedCode: 401, allowed: "viewer", allowedCode: 200},
	"GET /ticket/{id}":       {method: "GET", path: "/ticket/101", signedOut: 303, denied: "gone", deniedCode: 303, allowed: "viewer", allowedCode: 301},
}

// Every route answers a signed-out caller, a caller the Authorizer denies and one it allows, and
// a route added without a case here fails.
func TestEveryRouteWithAuthz(t *testing.T) {
	s := &server{}
	for _, r := range s.routes() {
		c, ok := routeCases[r.pattern]
		if !ok {
			t.Errorf("route %q has no authz case", r.pattern)
			continue
		}
		if !strings.HasPrefix(r.pattern, c.method+" ") {
			t.Errorf("route %q: case method %s", r.pattern, c.method)
		}
		if r.public != (c.allowed == "") {
			t.Errorf("route %q: public=%v but the case says otherwise", r.pattern, r.public)
		}
		t.Run(r.pattern, func(t *testing.T) {
			f := newFleet(t)
			h := enterprise(f)
			if rec := call(t, h, c.method, c.path, "", c.form); rec.Code != c.signedOut {
				t.Errorf("signed out: %d, want %d", rec.Code, c.signedOut)
			}
			if c.allowed == "" {
				return
			}
			if rec := call(t, h, c.method, c.path, c.denied, c.form); rec.Code != c.deniedCode {
				t.Errorf("%s: %d, want %d\n%s", c.denied, rec.Code, c.deniedCode, rec.Body)
			}
			if c.method == http.MethodPost && len(f.Calls()) != 0 {
				t.Errorf("a denied post wrote %v", f.Calls())
			}
			if rec := call(t, h, c.method, c.path, c.allowed, c.form); rec.Code != c.allowedCode {
				t.Errorf("%s: %d, want %d\n%s", c.allowed, rec.Code, c.allowedCode, rec.Body)
			}
		})
	}
	for p := range routeCases {
		if !slices.ContainsFunc(s.routes(), func(r route) bool { return r.pattern == p }) {
			t.Errorf("case %q has no route", p)
		}
	}
}

func TestActionsReachTheControl(t *testing.T) {
	f := newFleet(t)
	h := enterprise(f)
	call(t, h, "POST", "/runs/101/retry", "member", nil)
	call(t, h, "POST", "/runs/101/cancel", "member", nil)
	rec := call(t, h, "POST", "/runs/101/steer", "member", url.Values{"message": {"  use the v2 client "}})
	if loc := rec.Header().Get("Location"); loc != "/runs/101?done=steer" {
		t.Errorf("steer redirect %q", loc)
	}
	want := []string{"retry 101", "cancel 101", "steer 101: use the v2 client"}
	if got := f.Calls(); !slices.Equal(got, want) {
		t.Errorf("calls %v, want %v", got, want)
	}
	if rec := call(t, h, "POST", "/runs/101/steer", "member", url.Values{"message": {"  "}}); rec.Code != http.StatusBadRequest {
		t.Errorf("empty steer: %d", rec.Code)
	}
	if rec := call(t, h, "POST", "/runs/999/cancel", "member", nil); rec.Code != http.StatusNotFound {
		t.Errorf("unknown run: %d", rec.Code)
	}
	if len(f.Calls()) != 3 {
		t.Errorf("rejected posts wrote %v", f.Calls()[3:])
	}
}

// A repo-scoped member sees and changes only their repo; another org sees nothing.
func TestRepoScopeAndCrossOrg(t *testing.T) {
	h := enterprise(newFleet(t))
	body := call(t, h, "GET", "/", "web-only", nil).Body.String()
	if !strings.Contains(body, "Login page") || strings.Contains(body, "Add retry to webhook") {
		t.Errorf("web-only board shows the wrong runs:\n%s", body)
	}
	if rec := call(t, h, "GET", "/runs/103", "web-only", nil); rec.Code != http.StatusOK {
		t.Errorf("own repo run: %d", rec.Code)
	}
	if rec := call(t, h, "POST", "/runs/101/cancel", "web-only", nil); rec.Code != http.StatusNotFound {
		t.Errorf("other repo cancel: %d, want 404", rec.Code)
	}
	body = call(t, h, "GET", "/", "outsider", nil).Body.String()
	if !strings.Contains(body, "No runs yet") {
		t.Errorf("outsider sees runs:\n%s", body)
	}
	inbox := call(t, h, "GET", "/attention", "web-only", nil).Body.String()
	if !strings.Contains(inbox, "Plan to approve") || strings.Contains(inbox, "CI is down") {
		t.Errorf("web-only inbox shows the wrong items:\n%s", inbox)
	}
	if rec := call(t, h, "POST", "/attention/a2", "web-only", url.Values{"action": {"dismiss"}}); rec.Code != http.StatusNotFound {
		t.Errorf("resolving a hidden item: %d, want 404", rec.Code)
	}
}

func TestEveryPhaseHasOneColumn(t *testing.T) {
	phases := []api.Phase{api.PhaseReady, api.PhaseClaimed, api.PhaseRunning, api.PhaseStalled, api.PhaseRetryQueue,
		api.PhaseDone, api.PhaseQueued, api.PhaseMerged, api.PhaseNeedsHuman, api.PhaseCanceled}
	for _, p := range phases {
		n := 0
		for _, c := range columns {
			if slices.Contains(c.Phases, p) {
				n++
			}
		}
		if n != 1 {
			t.Errorf("phase %s is in %d columns", p, n)
		}
		if phaseLabel[p] == "" {
			t.Errorf("phase %s has no label", p)
		}
	}
}

func TestBoardColumnsWavesAndQueue(t *testing.T) {
	v := buildBoard(sampleBoard(), false)
	where := map[api.TicketID]string{}
	for _, c := range v.Columns {
		for _, cd := range c.Cards {
			where[cd.ID] = c.Key
		}
	}
	want := map[api.TicketID]string{"101": "running", "102": "needs", "103": "queue", "104": "running", "105": "done", "201": "ready"}
	for id, col := range want {
		if where[id] != col {
			t.Errorf("%s in %q, want %q", id, where[id], col)
		}
	}
	w := waveOf(sampleBoard())
	if w["101"] != 1 || w["104"] != 2 || v.Waves != 2 {
		t.Errorf("waves %v (max %d): 104 waits on 101", w, v.Waves)
	}
	cyc := api.Board{Slots: []api.Slot{{Ticket: api.Ticket{ID: "1", BlockedBy: []api.TicketID{"2"}}}, {Ticket: api.Ticket{ID: "2", BlockedBy: []api.TicketID{"1"}}}}}
	if w := waveOf(cyc); w["1"] != 1 || w["2"] != 1 {
		t.Errorf("cycle waves %v", w)
	}
	if v.Stats[1].Label != "Needs you" || v.Stats[1].Value != "1" || v.Stats[1].Status != "fail" {
		t.Errorf("needs-you stat %+v", v.Stats[1])
	}
}

// Customers never see token counts, spend, dispatcher hosts, session ids or budget events;
// operators see the first four.
func TestOperatorDetailStaysOut(t *testing.T) {
	pages := func(op bool) string {
		h := Handler(newFleet(t), Options{Operator: op})
		var all strings.Builder
		for _, p := range []string{"/", "/runs/101", "/attention"} {
			rec := call(t, h, "GET", p, "", nil)
			if rec.Code != http.StatusOK {
				t.Fatalf("%s: %d", p, rec.Code)
			}
			all.WriteString(rec.Body.String())
		}
		return all.String()
	}
	customer, operator := pages(false), pages(true)
	for _, secret := range []string{"123456", "4.32", "cloud-runner.internal", "Budget check", "runner-7.internal", "sess-101", "999000"} {
		if strings.Contains(customer, secret) {
			t.Errorf("customer view shows %q", secret)
		}
	}
	for _, detail := range []string{"123456", "4.32", "cloud-runner.internal", "Budget check", "999000"} {
		if !strings.Contains(operator, detail) {
			t.Errorf("operator view lacks %q", detail)
		}
	}
}

func TestRunPageTimelineAndLog(t *testing.T) {
	body := call(t, Handler(newFleet(t), Options{}), "GET", "/runs/101", "", nil).Body.String()
	for _, want := range []string{"Session started", "Pushed", "factory/101-a2", `role="log"`, "pushed branch=factory/101-a2", "Wave 1", "Attempt 2"} {
		if !strings.Contains(body, want) {
			t.Errorf("run page lacks %q", want)
		}
	}
	if strings.Contains(body, "Beta <b>") {
		t.Error("title not escaped")
	}
	ready := call(t, Handler(newFleet(t), Options{AllowActions: true}), "GET", "/runs/201", "", nil).Body.String()
	if !strings.Contains(ready, "The run has not started") || strings.Contains(ready, "Cancel run") || !strings.Contains(ready, "Retry") {
		t.Errorf("ready run page:\n%s", ready)
	}
	done := call(t, Handler(newFleet(t), Options{AllowActions: true}), "GET", "/runs/105", "", nil).Body.String()
	if !strings.Contains(done, "No actions apply to a finished run") || strings.Contains(done, "data-live") {
		t.Errorf("finished run page:\n%s", done)
	}
	if !strings.Contains(done, "Nothing has happened on this run yet.") {
		t.Error("empty timeline state missing")
	}
}

// brokenBoard fails Board; History fails too.
type brokenBoard struct{ *porttest.Control }

func (brokenBoard) Board(context.Context) (api.Board, error) {
	return api.Board{}, errors.New("dial tcp 10.0.0.7: refused")
}

type brokenHistory struct{ *porttest.Control }

func (brokenHistory) History(context.Context, api.TicketID) ([]api.Event, error) {
	return nil, errors.New("open /var/lib/factory/events.db: denied")
}

type brokenList struct{ *fleet }

func (brokenList) Open(context.Context) ([]attention.Item, error) {
	return nil, errors.New("disk full at /srv")
}

func TestErrorAndEmptyStates(t *testing.T) {
	cases := []struct {
		name, path string
		ctl        api.Control
		code       int
		want       string
	}{
		{"board error", "/", brokenBoard{porttest.NewControl(api.Board{})}, 502, "Board unavailable"},
		{"board empty", "/", porttest.NewControl(api.Board{}), 200, "No runs yet"},
		{"run error", "/runs/101", brokenBoard{porttest.NewControl(api.Board{})}, 502, "The board could not be loaded"},
		{"run missing", "/runs/404", porttest.NewControl(api.Board{}), 404, "Run not found"},
		{"history error", "/runs/101", brokenHistory{porttest.NewControl(sampleBoard())}, 200, "History unavailable"},
		{"inbox unkept", "/attention", porttest.NewControl(api.Board{}), 200, "No attention list"},
		{"inbox empty", "/attention", &fleet{porttest.NewControl(api.Board{}), mustStore(t)}, 200, "Nothing needs you"},
		{"inbox error", "/attention", brokenList{newFleet(t)}, 502, "Attention list unavailable"},
		{"column empty", "/", newFleet(t), 200, "Nothing is waiting to start."},
	}
	for _, c := range cases {
		if c.name == "column empty" {
			f := c.ctl.(*fleet)
			f.BoardVal.Ready = nil
		}
		rec := call(t, Handler(c.ctl, Options{}), "GET", c.path, "", nil)
		body := rec.Body.String()
		if rec.Code != c.code || !strings.Contains(body, c.want) {
			t.Errorf("%s: %d, want %d with %q\n%s", c.name, rec.Code, c.code, c.want, body)
		}
		for _, leak := range []string{"10.0.0.7", "/var/lib", "/srv"} {
			if strings.Contains(body, leak) {
				t.Errorf("%s leaks %q", c.name, leak)
			}
		}
	}
	// The loading state: live regions say they are connecting until the stream opens.
	if body := call(t, Handler(newFleet(t), Options{}), "GET", "/", "", nil).Body.String(); !strings.Contains(body, "Connecting…") {
		t.Error("board has no loading state for the live stream")
	}
}

func mustStore(t *testing.T) *attention.Store {
	st, err := attention.Open("")
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestInboxResolve(t *testing.T) {
	f := newFleet(t)
	h := enterprise(f)
	body := call(t, h, "GET", "/attention", "member", nil).Body.String()
	for _, want := range []string{"Plan to approve · 201", "The plan touches 4 files.", "Failure the factory cannot fix · 102", `value="approve"`} {
		if !strings.Contains(body, want) {
			t.Errorf("inbox lacks %q", want)
		}
	}
	if !strings.Contains(call(t, h, "GET", "/", "member", nil).Body.String(), `aria-label="2 open"`) {
		t.Error("nav does not count open items")
	}
	if rec := call(t, h, "POST", "/attention/a1", "member", url.Values{"action": {"retry"}}); rec.Code != http.StatusBadRequest {
		t.Errorf("an action the item lacks: %d", rec.Code)
	}
	if rec := call(t, h, "POST", "/attention/a1", "bot", url.Values{"action": {"approve"}}); rec.Code != http.StatusForbidden {
		t.Errorf("a bot approving: %d", rec.Code)
	}
	if rec := call(t, h, "POST", "/attention/a1", "member", url.Values{"action": {"approve"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("approve: %d", rec.Code)
	}
	if !f.Approved(attention.PlanApproval, "201") {
		t.Error("the store did not record the approval")
	}
	if rec := call(t, h, "POST", "/attention/a1", "member", url.Values{"action": {"approve"}}); rec.Code != http.StatusNotFound {
		t.Errorf("a closed item: %d", rec.Code)
	}
	if body := call(t, h, "GET", "/attention?done=approve", "member", nil).Body.String(); !strings.Contains(body, "Approved.") || strings.Contains(body, "Plan to approve") {
		t.Errorf("after approve:\n%s", body)
	}
}

func TestCSRFAndOrigin(t *testing.T) {
	f := newFleet(t)
	h := enterprise(f)
	post := func(mut func(*http.Request)) int {
		req := httptest.NewRequest("POST", "/runs/101/cancel", strings.NewReader("csrf="+strings.Repeat("c", 32)))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(&http.Cookie{Name: tokenCookie, Value: "member"})
		req.AddCookie(&http.Cookie{Name: csrfCookie, Value: strings.Repeat("c", 32)})
		mut(req)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if c := post(func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") }); c != http.StatusForbidden {
		t.Errorf("foreign origin: %d", c)
	}
	if c := post(func(r *http.Request) {
		r.Header.Del("Cookie")
		r.AddCookie(&http.Cookie{Name: tokenCookie, Value: "member"})
		r.AddCookie(&http.Cookie{Name: csrfCookie, Value: strings.Repeat("d", 32)})
	}); c != http.StatusForbidden {
		t.Errorf("mismatched token: %d", c)
	}
	if len(f.Calls()) != 0 {
		t.Fatalf("a forged post wrote %v", f.Calls())
	}
	if c := post(func(r *http.Request) { r.Header.Set("Origin", "http://example.com") }); c != http.StatusSeeOther {
		t.Errorf("same origin: %d", c)
	}
	// A bearer client is not a browser form and needs no CSRF token.
	req := httptest.NewRequest("POST", "/runs/101/retry", nil)
	req.Header.Set("Authorization", "Bearer member")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Errorf("bearer retry: %d", rec.Code)
	}
}

func TestSignIn(t *testing.T) {
	h := enterprise(newFleet(t))
	if rec := call(t, h, "GET", "/runs/101", "", nil); rec.Header().Get("Location") != "/signin?next=%2Fruns%2F101" {
		t.Errorf("signed-out redirect %q", rec.Header().Get("Location"))
	}
	if rec := call(t, h, "POST", "/signin?next=/runs/101", "", url.Values{"token": {"nope"}}); rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "That token did not work") {
		t.Errorf("bad token: %d", rec.Code)
	}
	rec := call(t, h, "POST", "/signin?next=/runs/101", "", url.Values{"token": {"viewer"}})
	if rec.Header().Get("Location") != "/runs/101" || !strings.Contains(rec.Header().Get("Set-Cookie"), tokenCookie+"=viewer") {
		t.Errorf("sign in: %d %v", rec.Code, rec.Header())
	}
	if rec := call(t, h, "POST", "/signin?next=//evil.example", "", url.Values{"token": {"viewer"}}); rec.Header().Get("Location") != "/" {
		t.Errorf("open redirect: %q", rec.Header().Get("Location"))
	}
	for in, want := range map[string]string{"/runs/1": "/runs/1", "//x": "/", "https://x": "/", `/\x`: "/", "": "/"} {
		if got := safeNext(in); got != want {
			t.Errorf("safeNext(%q) = %q", in, got)
		}
	}
	if rec := call(t, h, "POST", "/signout", "viewer", nil); !strings.Contains(rec.Header().Get("Set-Cookie"), "Max-Age=0") {
		t.Errorf("sign out kept the cookie: %v", rec.Header())
	}
	local := Handler(newFleet(t), Options{})
	if rec := call(t, local, "GET", "/signin", "", nil); rec.Code != http.StatusSeeOther {
		t.Errorf("local sign-in page: %d", rec.Code)
	}
}

var inlineScript = regexp.MustCompile(`<script(?:\s[^>]*)?>[^<]|\sstyle=|\son[a-z]+=`)

// Pages carry the CSP and no inline script, style or handler, so the CSP holds.
func TestPagesAreCSPClean(t *testing.T) {
	h := Handler(newFleet(t), Options{Operator: true, AllowActions: true})
	for _, p := range []string{"/", "/attention", "/runs/101", "/runs/201", "/runs/404"} {
		rec := call(t, h, "GET", p, "", nil)
		if !strings.Contains(rec.Header().Get("Content-Security-Policy"), "default-src 'self'") {
			t.Errorf("%s: no CSP", p)
		}
		if m := inlineScript.FindString(rec.Body.String()); m != "" {
			t.Errorf("%s: inline %q", p, m)
		}
	}
}

func TestHandlerRefusesIdentityWithoutAuthorizer(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("want a panic")
		}
	}()
	Handler(newFleet(t), Options{Identity: people, Org: "acme"})
}

func TestHandlerRefusesIdentityWithoutOrg(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("want a panic")
		}
	}()
	Handler(newFleet(t), Options{Identity: people, Authorizer: grantsAuthz{}})
}

// readSSE reads data lines from an event stream until n arrive or the stream ends.
func readSSE(t *testing.T, srv *httptest.Server, path, token string, f *fleet, push []api.Event, n int) []string {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s: %d", path, resp.StatusCode)
	}
	<-f.Subscribed()
	for _, e := range push {
		f.Push(e)
	}
	var out []string
	sc := bufio.NewScanner(resp.Body)
	for len(out) < n && sc.Scan() {
		if d, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
			out = append(out, d)
		}
	}
	return out
}

func TestRunStreamFiltersAndScrubs(t *testing.T) {
	f := newFleet(t)
	srv := httptest.NewServer(enterprise(f))
	defer srv.Close()
	now := time.Date(2026, 11, 26, 21, 0, 0, 0, time.UTC)
	push := []api.Event{
		{Time: now, Kind: "pushed", Ticket: "103", Data: map[string]any{"branch": "other"}},
		{Time: now, Kind: "budget", Ticket: "101", Data: map[string]any{"verdict": "stop"}},
		{Time: now, Kind: "steered", Ticket: "101", Data: map[string]any{"message": "go", "host": "runner-7.internal"}},
	}
	got := readSSE(t, srv, "/runs/101/events", "viewer", f, push, 1)
	if len(got) != 1 || !strings.Contains(got[0], `"Message sent to the run"`) || strings.Contains(got[0], "runner-7") {
		t.Errorf("run stream %v", got)
	}
}

func TestBoardStreamFollowsAuthz(t *testing.T) {
	f := newFleet(t)
	srv := httptest.NewServer(enterprise(f))
	defer srv.Close()
	now := time.Date(2026, 11, 26, 21, 0, 0, 0, time.UTC)
	push := []api.Event{
		{Time: now, Kind: "pushed", Ticket: "101", Data: map[string]any{"branch": "api-branch"}},
		{Time: now, Kind: "pushed", Ticket: "103", Data: map[string]any{"branch": "web-branch"}},
	}
	got := readSSE(t, srv, "/events", "web-only", f, push, 1)
	if len(got) != 1 || !strings.Contains(got[0], "web-branch") {
		t.Errorf("web-only stream %v", got)
	}
}
