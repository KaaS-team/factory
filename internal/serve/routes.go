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
}

// Routes is the route table. A test walks it and fails on a route with no action and no reason.
func (s *Server) Routes() []Route {
	return []Route{
		{Pattern: "/healthz", Open: "liveness probe for the orchestrator", Handler: s.healthz},
		{Pattern: "/readyz", Open: "readiness probe for the orchestrator", Handler: s.readyz},
		{Pattern: "/events", Action: contract.ActionRunRead, Handler: s.events},
	}
}

// mux builds the router from the table. With Options.Access set, each guarded route first
// authenticates the caller and asks the authorizer for the route's action.
func (s *Server) mux() *http.ServeMux {
	mux := http.NewServeMux()
	for _, rt := range s.Routes() {
		var h http.Handler = rt.Handler
		if s.access != nil && rt.Open == "" {
			h = s.access.guard(rt.Action, h)
		}
		mux.Handle(rt.Pattern, h)
	}
	return mux
}
