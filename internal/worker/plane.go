// Package worker is the self-hosted worker pool. The control plane side (Plane) serves a small
// HTTP API; the worker side (Worker) only ever dials out to it, so a worker needs no inbound port.
package worker

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/KaaS-team/factory/internal/dispatch/broker"
)

// maxWait caps one long-poll, so a proxy idle timeout never cuts a lease request.
const maxWait = 25 * time.Second

// Plane serves the worker API over a Leaser and the hashed pool tokens.
type Plane struct {
	L     *broker.Leaser
	Pools *broker.Pools
}

// Register is the body of POST /v1/workers.
type Register struct {
	Labels map[string]string `json:"labels"`
	Caps   []string          `json:"caps"`
}

// Complete is the body of POST /v1/leases/{id}/complete.
type Complete struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
}

// Handler returns the routes. Every route needs a bearer token from Pools.
func (p *Plane) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/workers", p.auth(false, p.register))
	mux.HandleFunc("POST /v1/workers/{id}/lease", p.auth(false, p.lease))
	mux.HandleFunc("POST /v1/workers/{id}/heartbeat", p.auth(false, p.heartbeat))
	mux.HandleFunc("POST /v1/workers/{id}/drain", p.auth(false, p.drain))
	mux.HandleFunc("POST /v1/leases/{id}/logs", p.auth(false, p.logs))
	mux.HandleFunc("POST /v1/leases/{id}/complete", p.auth(false, p.complete))
	mux.HandleFunc("POST /v1/jobs", p.auth(true, p.submit))
	mux.HandleFunc("GET /v1/jobs/{id}", p.auth(true, p.status))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	return mux
}

// auth checks the bearer token. A submit route needs the submit pool; a worker route needs any
// other pool, so a worker token cannot read or queue jobs.
func (p *Plane) auth(submit bool, h func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		pool, err := p.Pools.Authenticate(tok)
		if !ok || err != nil || (pool == broker.SubmitPool) != submit {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h(w, r, pool)
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, err error) {
	code := http.StatusBadRequest
	switch {
	case errors.Is(err, broker.ErrUnknownWorker), errors.Is(err, broker.ErrStaleLease):
		code = http.StatusNotFound
	case errors.Is(err, broker.ErrLostWorker):
		code = http.StatusGone
	}
	http.Error(w, err.Error(), code)
}

// own reports whether the worker belongs to the caller's pool.
func (p *Plane) own(w http.ResponseWriter, r *http.Request, pool string) bool {
	if wp, ok := p.L.WorkerPool(r.PathValue("id")); ok && wp != pool {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return false
	}
	return true
}

func (p *Plane) register(w http.ResponseWriter, r *http.Request, pool string) {
	var in Register
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	wk := p.L.Register(pool, in.Labels, in.Caps)
	writeJSON(w, http.StatusOK, map[string]any{"worker_id": wk.ID, "ttl_seconds": int(p.L.TTL().Seconds())})
}

func (p *Plane) lease(w http.ResponseWriter, r *http.Request, pool string) {
	if !p.own(w, r, pool) {
		return
	}
	wait := maxWait
	if d, err := time.ParseDuration(r.URL.Query().Get("wait")); err == nil && d < wait {
		wait = d
	}
	ls, err := p.L.Lease(r.Context(), r.PathValue("id"), wait)
	switch {
	case err != nil:
		fail(w, err)
	case ls == nil:
		w.WriteHeader(http.StatusNoContent)
	default:
		writeJSON(w, http.StatusOK, ls)
	}
}

func (p *Plane) heartbeat(w http.ResponseWriter, r *http.Request, pool string) {
	if !p.own(w, r, pool) {
		return
	}
	if err := p.L.Heartbeat(r.PathValue("id")); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (p *Plane) drain(w http.ResponseWriter, r *http.Request, pool string) {
	if !p.own(w, r, pool) {
		return
	}
	if err := p.L.Drain(r.PathValue("id")); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (p *Plane) logs(w http.ResponseWriter, r *http.Request, _ string) {
	var lines []string
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&lines); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := p.L.AppendLog(r.PathValue("id"), lines...); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (p *Plane) complete(w http.ResponseWriter, r *http.Request, _ string) {
	var in Complete
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := p.L.Complete(r.PathValue("id"), in.OK, in.Result); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (p *Plane) submit(w http.ResponseWriter, r *http.Request, _ string) {
	var j broker.Job
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&j); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	id, err := p.L.Submit(j)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id})
}

func (p *Plane) status(w http.ResponseWriter, r *http.Request, _ string) {
	st, ok := p.L.Status(r.PathValue("id"))
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, st)
}
