package serve

import (
	"net/http"

	"github.com/KaaS-team/factory/ee/contract"
)

// Route is one entry of the route table: the only place a handler is registered. Every route
// names the action it needs, or says why it is open to anyone.
type Route struct {
	Pattern string
	Action  contract.Action // what the caller must be allowed to do; empty only when Open is set
	Open    string          // why the route needs no sign-in; empty for a guarded route
	Handler http.HandlerFunc
	// Scoped marks a route that authorizes itself, because the resource is named in its path (the
	// repo). The mux does not wrap it; its handler must call Server.authorize. A test walks these.
	Scoped bool
}

// Routes is the route table. A test walks it and fails on a route with no action and no reason.
func (s *Server) Routes() []Route {
	return []Route{
		{Pattern: "/healthz", Open: "liveness probe for the orchestrator", Handler: s.healthz},
		{Pattern: "/readyz", Open: "readiness probe for the orchestrator", Handler: s.readyz},
		{Pattern: "/events", Action: contract.ActionRunRead, Handler: s.events},
		{Pattern: "POST /api/runs/{repo}/{issue}/approve", Action: contract.ActionApprove, Scoped: true, Handler: s.write(contract.ActionApprove, s.approve)},
		{Pattern: "POST /api/runs/{repo}/{issue}/steer", Action: contract.ActionRunSteer, Scoped: true, Handler: s.write(contract.ActionRunSteer, s.steer)},
		{Pattern: "POST /api/runs/{repo}/{issue}/retry", Action: contract.ActionRunRetry, Scoped: true, Handler: s.write(contract.ActionRunRetry, s.retry)},
		{Pattern: "POST /api/runs/{repo}/{issue}/cancel", Action: contract.ActionRunCancel, Scoped: true, Handler: s.write(contract.ActionRunCancel, s.cancel)},
	}
}

// mux builds the router from the table. With Options.Access set, each guarded route first
// authenticates the caller and asks the authorizer for the route's action.
func (s *Server) mux() *http.ServeMux {
	mux := http.NewServeMux()
	for _, rt := range s.Routes() {
		var h http.Handler = rt.Handler
		if s.access != nil && rt.Open == "" && !rt.Scoped {
			h = s.access.guard(rt.Action, h)
		}
		mux.Handle(rt.Pattern, h)
	}
	return mux
}
