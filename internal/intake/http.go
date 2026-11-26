package intake

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/KaaS-team/factory/ee/contract"
)

// API serves POST /api/v1/tickets (docs/contracts/intake.openapi.yaml). The caller sends a bearer
// api token; Identity says who it is and Authorizer whether that principal may start runs in its
// org. The Idempotency-Key header is required: a repeat with the same body returns the first
// ticket, a repeat with another body is 409.
type API struct {
	Identity   contract.Identity
	Authorizer contract.Authorizer
	Store      Store
}

type apiRequest struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	URL   string `json:"url"`
}

type apiError struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// ServeHTTP implements http.Handler.
func (a *API) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, apiError{"method not allowed"})
		return
	}
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || tok == "" {
		writeJSON(w, http.StatusUnauthorized, apiError{"unauthorized"})
		return
	}
	p, err := a.Identity.Authenticate(r.Context(), contract.Credential{Kind: contract.CredAPIToken, Value: tok})
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, apiError{"unauthorized"}) // no detail: not why it failed
		return
	}
	if d := a.Authorizer.Can(r.Context(), p, contract.ActionRunStart, contract.Resource{Org: p.Org}); !d.Allow {
		writeJSON(w, http.StatusForbidden, apiError{"forbidden"})
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if key == "" || len(key) > 255 {
		writeJSON(w, http.StatusBadRequest, apiError{"Idempotency-Key header is required (1 to 255 characters)"})
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{"bad request"})
		return
	}
	var in apiRequest
	if json.Unmarshal(raw, &in) != nil || strings.TrimSpace(in.Title) == "" {
		writeJSON(w, http.StatusBadRequest, apiError{"title is required"})
		return
	}
	sum := sha256.Sum256(raw)
	c, err := a.Store.Create(r.Context(), NewTicket{
		Source: SourceAPI, Key: p.Org + "/" + key, Fingerprint: hex.EncodeToString(sum[:]),
		Title: in.Title, Body: in.Body, URL: in.URL,
	})
	switch {
	case errors.Is(err, ErrConflict):
		writeJSON(w, http.StatusConflict, apiError{"Idempotency-Key was used with a different request"})
	case err != nil:
		writeJSON(w, http.StatusInternalServerError, apiError{"could not create ticket"})
	default:
		code := http.StatusCreated
		if !c.New {
			code = http.StatusOK
			w.Header().Set("Idempotent-Replayed", "true")
		}
		writeJSON(w, code, map[string]any{"id": c.ID, "source": c.Source, "title": c.Title})
	}
}

// Mux mounts the sources under one prefix-free mux. A nil source is left out.
func Mux(api *API, slack *Slack, sentry *Sentry) *http.ServeMux {
	m := http.NewServeMux()
	if api != nil {
		m.Handle("/api/v1/tickets", api)
	}
	if slack != nil {
		m.Handle("/intake/slack/events", slack)
	}
	if sentry != nil {
		m.Handle("/intake/sentry", sentry)
	}
	return m
}
