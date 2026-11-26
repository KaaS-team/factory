package serve

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/KaaS-team/factory/ee/contract"
	"github.com/KaaS-team/factory/internal/api"
)

type auditLog struct {
	mu sync.Mutex
	ev []contract.AuditEvent
}

func (a *auditLog) Record(_ context.Context, e contract.AuditEvent) (contract.AuditEvent, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.ev = append(a.ev, e)
	return e, nil
}
func (*auditLog) Search(context.Context, contract.AuditQuery) ([]contract.AuditEvent, error) {
	return nil, nil
}
func (*auditLog) Verify(context.Context) error { return nil }

// stateCore refuses every write as the real core does for a state that does not fit.
type stateCore struct{ *fakeCore }

func (stateCore) Steer(context.Context, api.TicketID, string) error {
	return &api.StateError{Msg: "ticket 7 has no running session"}
}
func (stateCore) Retry(context.Context, api.TicketID) error {
	return errors.New("tracker said: token ghp_secret rejected")
}

func writeServer(t *testing.T, core Core, opt Options) (*Server, *auditLog) {
	t.Helper()
	log := &auditLog{}
	opt.Audit = log
	return New(core, opt), log
}

func repoCore() *fakeCore {
	c := newFakeCore()
	c.ReposVal = []string{"acme/web"}
	return c
}

func post(s *Server, path, tok, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", path, strings.NewReader(body))
	if tok != "" {
		r.Header.Set("Authorization", "Bearer "+tok)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func access() *Access {
	return &Access{
		Identity: fakeIdentity{byToken: map[string]contract.Principal{
			"member": {ID: "u1", Org: "acme", Kind: contract.KindUser},
			"viewer": {ID: "u2", Org: "acme", Kind: contract.KindUser},
		}},
		Authorizer: roleTable{"u1": contract.RoleMember, "u2": contract.RoleViewer},
	}
}

// roleTable authorizes by the contract's Grants table, one role per principal id.
type roleTable map[string]contract.Role

func (r roleTable) Can(_ context.Context, p contract.Principal, a contract.Action, _ contract.Resource) contract.Decision {
	for _, g := range contract.Grants[r[p.ID]] {
		if g == a {
			return contract.Decision{Allow: true, Reason: contract.ReasonGranted}
		}
	}
	return contract.Decision{Reason: contract.ReasonRoleLacksAction}
}

const base = "/api/runs/acme%2Fweb/7/"

var writeBodies = map[string]string{"approve": `{"goal":"ship it"}`, "steer": `{"message":"hi"}`, "retry": ``, "cancel": ``}

// TestWriteRoutesRefuseAnonymous walks every Scoped write route: with no token, a viewer's token or
// no guard at all it is refused, nothing reaches Control, and each refusal is audited as a deny.
func TestWriteRoutesRefuseAnonymous(t *testing.T) {
	var scoped []Route
	for _, rt := range New(newFakeCore(), Options{}).Routes() {
		if rt.Scoped {
			scoped = append(scoped, rt)
		}
	}
	if len(scoped) != 4 {
		t.Fatalf("want 4 write routes, got %d", len(scoped))
	}
	for _, rt := range scoped {
		verb := rt.Pattern[strings.LastIndex(rt.Pattern, "/")+1:]
		for _, tc := range []struct {
			name, tok string
			acc       bool
			want      int
		}{
			{"no guard", "", false, 403},
			{"no token", "", true, 401},
			{"bad token", "nope", true, 401},
			{"viewer lacks role", "viewer", true, 403},
		} {
			t.Run(verb+"/"+tc.name, func(t *testing.T) {
				core := repoCore()
				opt := Options{ApprovePlan: func(context.Context, string) error { t.Error("approve ran"); return nil }}
				if tc.acc {
					opt.Access = access()
				}
				s, log := writeServer(t, core, opt)
				w := post(s, base+verb, tc.tok, writeBodies[verb])
				if w.Code != tc.want {
					t.Fatalf("status %d, want %d: %s", w.Code, tc.want, w.Body)
				}
				if len(core.Calls()) != 0 {
					t.Fatalf("control was called: %v", core.Calls())
				}
				if len(log.ev) != 1 || log.ev[0].Outcome != contract.OutcomeDeny || log.ev[0].Action != rt.Action || log.ev[0].Resource != "acme/web" {
					t.Fatalf("audit = %+v", log.ev)
				}
			})
		}
	}
}

// TestWriteRoutesCallControlAndAudit: an allowed member reaches the same Control method the CLI and
// the MCP tool call, and the allow row names the actor, the action and the repo, never the message.
func TestWriteRoutesCallControlAndAudit(t *testing.T) {
	for verb, want := range map[string]string{"steer": "steer 7: hi", "retry": "retry 7", "cancel": "cancel 7"} {
		core := repoCore()
		s, log := writeServer(t, core, Options{Access: access()})
		if w := post(s, base+verb, "member", writeBodies[verb]); w.Code != 200 {
			t.Fatalf("%s: %d %s", verb, w.Code, w.Body)
		}
		if got := core.Calls(); len(got) != 1 || got[0] != want {
			t.Fatalf("%s: calls %v, want %q", verb, got, want)
		}
		e := log.ev[0]
		if e.Outcome != contract.OutcomeAllow || e.Actor != "u1" || e.Data["ticket"] != "7" || strings.Contains(e.Data["ticket"]+e.Resource, "hi") {
			t.Fatalf("%s: audit %+v", verb, e)
		}
	}
	var got string
	s, log := writeServer(t, repoCore(), Options{Access: access(), ApprovePlan: func(_ context.Context, g string) error { got = g; return nil }})
	if w := post(s, base+"approve", "member", `{"goal":"ship it"}`); w.Code != 200 || got != "ship it" || log.ev[0].Action != contract.ActionApprove {
		t.Fatalf("approve: %d %q %+v", w.Code, got, log.ev)
	}
}

// TestWriteStateAndErrors: a wrong state is 409 with its text; any other failure is a fixed 502 with
// no detail; an unknown repo is 404; a bad body is 400. Each is audited as an error.
func TestWriteStateAndErrors(t *testing.T) {
	s, log := writeServer(t, stateCore{repoCore()}, Options{Access: access()})
	if w := post(s, base+"steer", "member", `{"message":"x"}`); w.Code != 409 || !strings.Contains(w.Body.String(), "no running session") {
		t.Fatalf("steer: %d %s", w.Code, w.Body)
	}
	if w := post(s, base+"retry", "member", ""); w.Code != 502 || strings.Contains(w.Body.String(), "ghp_") {
		t.Fatalf("retry: %d %s", w.Code, w.Body)
	}
	if w := post(s, "/api/runs/other%2Frepo/7/cancel", "member", ""); w.Code != 404 {
		t.Fatalf("repo: %d", w.Code)
	}
	if w := post(s, base+"steer", "member", `{}`); w.Code != 400 {
		t.Fatalf("body: %d", w.Code)
	}
	if len(log.ev) != 4 {
		t.Fatalf("audit rows %d, want 4", len(log.ev))
	}
	for _, e := range log.ev {
		if e.Outcome != contract.OutcomeError {
			t.Errorf("outcome %s, want error: %+v", e.Outcome, e)
		}
	}
}

// With only the Wrap guard (the password or token path) a write goes through and is audited as the api token.
func TestWriteWithWrapOnly(t *testing.T) {
	core := repoCore()
	wrap := func(h http.Handler) http.Handler { return h }
	s, log := writeServer(t, core, Options{Wrap: wrap})
	if w := post(s, base+"cancel", "", ""); w.Code != 200 || log.ev[0].Actor != "api-token" {
		t.Fatalf("%d %+v", w.Code, log.ev)
	}
}

// TestOpenAPIListsEveryWriteRoute keeps docs/contracts/runs.openapi.yaml equal to the route table.
func TestOpenAPIListsEveryWriteRoute(t *testing.T) {
	b, err := os.ReadFile("../../docs/contracts/runs.openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, rt := range New(newFakeCore(), Options{}).Routes() {
		if rt.Scoped && !strings.Contains(string(b), strings.TrimPrefix(rt.Pattern, "POST ")+":") {
			t.Errorf("%s is not in runs.openapi.yaml", rt.Pattern)
		}
	}
}
