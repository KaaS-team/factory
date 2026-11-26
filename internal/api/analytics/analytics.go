// Package analytics serves the run and spend analytics routes under /api/v1/analytics. It lives under
// internal/api but not in package api, so the clients that import api (the TUI, MCP) do not pull in
// the ledger.
package analytics

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/KaaS-team/factory/ee/contract"
	"github.com/KaaS-team/factory/internal/ledger"
)

// The routes. docs/contracts/analytics.openapi.yaml describes them; a test keeps the two equal.
const (
	RunsPath  = "/api/v1/analytics/runs"
	SpendPath = "/api/v1/analytics/spend"
)

// RunSource returns every run ledger entry the factory holds.
type RunSource func(ctx context.Context) ([]ledger.Entry, error)

// Handler serves the two analytics routes. A caller sends `Authorization: Bearer <token>`;
// the token goes to id as an api_token credential. Each entry is shown only when az allows the
// principal run.read on its team and repo (spend routes need budget.read). Model names, token counts
// and cost are operator detail: runs show them only to a principal who also has budget.read.
func Handler(src RunSource, id contract.Identity, az contract.Authorizer) http.Handler {
	h := handler{src: src, id: id, az: az}
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+RunsPath, h.runs)
	mux.HandleFunc("GET "+SpendPath, h.spend)
	return mux
}

type handler struct {
	src RunSource
	id  contract.Identity
	az  contract.Authorizer
}

// fail writes a plain message. It never carries the cause: errors can hold hostnames and paths.
func fail(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// load authenticates, parses the filter and returns the entries the principal may see for action.
func (a handler) load(w http.ResponseWriter, r *http.Request, action contract.Action) (p contract.Principal, entries []ledger.Entry, ok bool) {
	tok, found := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !found || tok == "" {
		fail(w, http.StatusUnauthorized, "sign in required")
		return p, nil, false
	}
	p, err := a.id.Authenticate(r.Context(), contract.Credential{Kind: contract.CredAPIToken, Value: tok})
	if err != nil || p.Disabled {
		fail(w, http.StatusUnauthorized, "sign in required")
		return p, nil, false
	}
	f, err := filterOf(r)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return p, nil, false
	}
	all, err := a.src(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "the run ledger could not be read")
		return p, nil, false
	}
	for _, e := range ledger.Select(all, f) {
		if a.az.Can(r.Context(), p, action, contract.Resource{Org: p.Org, Team: e.Team, Repo: e.Repo}).Allow {
			entries = append(entries, e)
		}
	}
	if len(entries) == 0 && !a.az.Can(r.Context(), p, action, contract.Resource{Org: p.Org, Team: f.Team, Repo: f.Repo}).Allow {
		fail(w, http.StatusForbidden, "not allowed")
		return p, nil, false
	}
	return p, entries, true
}

func filterOf(r *http.Request) (ledger.Filter, error) {
	q := r.URL.Query()
	f := ledger.Filter{Team: q.Get("team"), Repo: q.Get("repo"), Harness: q.Get("harness")}
	var err error
	if f.Since, err = when(q.Get("since")); err != nil {
		return f, fmt.Errorf("since: %w", err)
	}
	if f.Until, err = when(q.Get("until")); err != nil {
		return f, fmt.Errorf("until: %w", err)
	}
	if f := q.Get("format"); f != "" && f != "json" && f != "csv" {
		return ledger.Filter{}, fmt.Errorf("format must be json or csv")
	}
	return f, nil
}

// when reads an RFC 3339 time or a YYYY-MM-DD day (UTC). Empty is the zero time.
func when(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("use RFC 3339 or YYYY-MM-DD")
	}
	return t, nil
}

func (a handler) runs(w http.ResponseWriter, r *http.Request) {
	p, entries, ok := a.load(w, r, contract.ActionRunRead)
	if !ok {
		return
	}
	detail := a.az.Can(r.Context(), p, contract.ActionBudgetRead, contract.Resource{Org: p.Org}).Allow
	if !detail {
		for i := range entries {
			entries[i] = redact(entries[i])
		}
	}
	if r.URL.Query().Get("format") == "csv" {
		writeCSV(w, entries, detail)
		return
	}
	if entries == nil {
		entries = []ledger.Entry{}
	}
	writeJSON(w, map[string]any{"runs": entries})
}

func (a handler) spend(w http.ResponseWriter, r *http.Request) {
	_, entries, ok := a.load(w, r, contract.ActionBudgetRead)
	if !ok {
		return
	}
	s := ledger.Summarize(entries)
	if r.URL.Query().Get("format") == "csv" {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		cw := csv.NewWriter(w)
		_ = cw.Write([]string{"team", "runs", "merged", "failed", "cost_usd", "cost_per_merged_pr_usd"})
		row := func(name string, t ledger.Totals) {
			_ = cw.Write([]string{safeCell(name), strconv.Itoa(t.Runs), strconv.Itoa(t.Merged), strconv.Itoa(t.Failed),
				strconv.FormatFloat(t.CostUSD, 'f', -1, 64), strconv.FormatFloat(t.CostPerMergedUSD, 'f', -1, 64)})
		}
		row("(factory)", s.Factory)
		for _, n := range s.TeamNames() {
			row(n, s.Teams[n])
		}
		cw.Flush()
		return
	}
	writeJSON(w, s)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// redact removes operator detail: the model name, worker, tokens and cost.
func redact(e ledger.Entry) ledger.Entry {
	e.Agent.Model, e.Agent.Worker = "", ""
	e.Tokens, e.CostUSD = nil, nil
	return e
}

var runColumns = []string{"run_id", "repo", "team", "ticket_source", "ticket_id", "harness", "started_at", "ended_at",
	"duration_seconds", "status", "failure_class", "pr_number", "qa_verdict"}

var detailColumns = []string{"model", "input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens", "cost_usd"}

func writeCSV(w http.ResponseWriter, entries []ledger.Entry, detail bool) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="runs.csv"`)
	cw := csv.NewWriter(w)
	head := runColumns
	if detail {
		head = append(append([]string{}, head...), detailColumns...)
	}
	_ = cw.Write(head)
	for _, e := range entries {
		var ended, dur, pr, qa string
		if e.EndedAt != nil {
			ended = e.EndedAt.UTC().Format(time.RFC3339)
		}
		if d, ok := ledger.Duration(e); ok {
			dur = strconv.FormatFloat(d.Seconds(), 'f', -1, 64)
		}
		if e.PR != nil && e.PR.Number != nil {
			pr = strconv.Itoa(*e.PR.Number)
		}
		if e.QA != nil {
			qa = e.QA.Verdict
		}
		row := []string{e.RunID, e.Repo, e.Team, e.Ticket.Source, e.Ticket.ID, e.Agent.Harness, e.StartedAt.UTC().Format(time.RFC3339),
			ended, dur, e.Status, e.FailureClass, pr, qa}
		if detail {
			row = append(row, e.Agent.Model)
			t := e.Tokens
			if t == nil {
				t = &contract.LedgerTokens{}
			}
			row = append(row, num(t.Input), num(t.Output), num(t.CacheRead), num(t.CacheWrite))
			cost := ""
			if e.CostUSD != nil {
				cost = strconv.FormatFloat(*e.CostUSD, 'f', -1, 64)
			}
			row = append(row, cost)
		}
		for i := range row {
			row[i] = safeCell(row[i])
		}
		_ = cw.Write(row)
	}
	cw.Flush()
}

func num(p *int64) string {
	if p == nil {
		return ""
	}
	return strconv.FormatInt(*p, 10)
}

// safeCell stops a spreadsheet from running a cell as a formula: a value that starts with =, +, -
// or @ gets a leading quote. A negative number never occurs in these columns.
func safeCell(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}
