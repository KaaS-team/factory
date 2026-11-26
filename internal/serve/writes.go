package serve

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/KaaS-team/factory/ee/contract"
	"github.com/KaaS-team/factory/internal/api"
)

// ErrBadInput is what Options.ApprovePlan wraps when the goal is unusable, so the route answers 400.
var ErrBadInput = errors.New("bad input")

// maxBody caps a write request body.
const maxBody = 64 << 10

// reasonWritesOff is the reason a write is refused when no credential guard stands in front of the routes.
const reasonWritesOff = "serve.writes_need_auth"

// write is the one wrapper of every write route. It checks the route has a credential guard, asks the
// authorizer for action on the repo in the path, runs do, and records the attempt in the audit log.
// A write calls the same api.Control method as the CLI command and the MCP tool, so there is no
// second resolver. With no Access, the Wrap guard is the credential and the actor is "api-token".
func (s *Server) write(action contract.Action, do func(r *http.Request, id api.TicketID) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		repo, err := url.PathUnescape(r.PathValue("repo"))
		issue := r.PathValue("issue")
		rec := contract.AuditEvent{Action: action, Resource: repo, Actor: "api-token", Data: map[string]string{"ticket": issue}}
		deny := func(status int, why contract.ReasonCode) {
			rec.Outcome, rec.Reason = contract.OutcomeDeny, why
			s.record(r.Context(), rec)
			refuse(w, status, why)
		}
		switch {
		case !s.authed:
			deny(http.StatusForbidden, reasonWritesOff)
			return
		case s.access != nil:
			p, status, why := s.access.authorize(r, action, func(p contract.Principal) contract.Resource {
				return contract.Resource{Org: p.Org, Repo: repo}
			})
			if p.ID != "" {
				rec.Actor = p.ID
			} else {
				rec.Actor = "anonymous"
			}
			if status != 0 {
				deny(status, why)
				return
			}
		}
		if err != nil || issue == "" {
			http.Error(w, "bad request: repo and issue are required", http.StatusBadRequest)
			return
		}
		repos, err := s.core.Repos(r.Context())
		if err != nil || !slices.Contains(repos, repo) {
			rec.Outcome, rec.Reason = contract.OutcomeError, "serve.unknown_repo"
			s.record(r.Context(), rec)
			http.Error(w, "not found: no such repo", http.StatusNotFound)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		err = do(r, api.TicketID(issue))
		var se *api.StateError
		switch {
		case err == nil:
			rec.Outcome = contract.OutcomeAllow
			s.record(r.Context(), rec)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"ok":true}`+"\n")
		case errors.As(err, &se):
			rec.Outcome, rec.Reason = contract.OutcomeError, "serve.wrong_state"
			s.record(r.Context(), rec)
			http.Error(w, "conflict: "+se.Msg, http.StatusConflict)
		case errors.Is(err, ErrBadInput):
			rec.Outcome, rec.Reason = contract.OutcomeError, "serve.bad_input"
			s.record(r.Context(), rec)
			http.Error(w, "bad request: the body is not usable", http.StatusBadRequest)
		default:
			rec.Outcome, rec.Reason = contract.OutcomeError, "serve.failed"
			s.record(r.Context(), rec)
			http.Error(w, "failed: the action did not complete", http.StatusBadGateway) // detail stays in the operator's log
		}
	}
}

// record appends e to the audit log. A failed write never fails the action it describes.
func (s *Server) record(ctx context.Context, e contract.AuditEvent) {
	if s.audit != nil {
		_, _ = s.audit.Record(ctx, e)
	}
}

// field reads one string member from a JSON body. A message or goal is never put in the audit event.
func field(body io.Reader, name string) (string, error) {
	var m map[string]string
	if err := json.NewDecoder(body).Decode(&m); err != nil || strings.TrimSpace(m[name]) == "" {
		return "", ErrBadInput
	}
	return m[name], nil
}

// approve plans the goal and approves it: what `factory plan --approve` and the approve_plan tool do.
func (s *Server) approve(r *http.Request, _ api.TicketID) error {
	goal, err := field(r.Body, "goal")
	if err != nil {
		return err
	}
	if s.approveFn == nil {
		return errors.New("approve is not configured")
	}
	return s.approveFn(r.Context(), goal)
}

func (s *Server) steer(r *http.Request, id api.TicketID) error {
	msg, err := field(r.Body, "message")
	if err != nil {
		return err
	}
	return s.core.Steer(r.Context(), id, msg)
}

func (s *Server) retry(r *http.Request, id api.TicketID) error { return s.core.Retry(r.Context(), id) }
func (s *Server) cancel(r *http.Request, id api.TicketID) error {
	return s.core.Cancel(r.Context(), id)
}
