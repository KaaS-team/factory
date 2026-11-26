//go:build ee

// SPDX-License-Identifier: LicenseRef-KaaS-EE

package admin

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/KaaS-team/factory/ee/contract"
	"github.com/KaaS-team/ui"
)

// pageSize is how many events the search page shows, newest first.
const pageSize = 100

// anyValue is the select option that means no filter.
const anyValue = "any"

type auditData struct {
	Filter     ui.Form
	Table      ui.Table
	Shown      int
	Total      int
	Chain      ui.Chip
	ExportCSV  string
	ExportJSON string
	CanExport  bool
}

// auditQuery reads the filters. Dates are whole UTC days; Until includes its day.
func auditQuery(r *http.Request) (contract.AuditQuery, string) {
	v := r.URL.Query()
	q := contract.AuditQuery{Actor: strings.TrimSpace(v.Get("actor"))}
	if a := v.Get("action"); a != "" && a != anyValue {
		q.Action = contract.Action(a)
	}
	if o := v.Get("outcome"); o != "" && o != anyValue {
		q.Outcome = contract.Outcome(o)
	}
	var bad string
	if s := v.Get("since"); s != "" {
		t, err := time.Parse(time.DateOnly, s)
		if err != nil {
			bad = "From must be a date like 2026-11-26."
		}
		q.Since = t
	}
	if s := v.Get("until"); s != "" {
		t, err := time.Parse(time.DateOnly, s)
		if err != nil {
			bad = "To must be a date like 2026-11-26."
		}
		q.Until = t.AddDate(0, 0, 1)
	}
	return q, bad
}

func (c *Console) auditSearch(w http.ResponseWriter, r *http.Request, p contract.Principal) {
	q, bad := auditQuery(r)
	v := r.URL.Query()
	actions := []string{anyValue}
	for _, a := range contract.Actions {
		actions = append(actions, string(a))
	}
	d := auditData{CanExport: c.canEdit(r, p, contract.ActionAuditExport)}
	d.Filter = ui.Form{ID: "audit", Method: "get", Action: "/admin/audit", Error: bad, Submit: ui.Button{Label: "Search", Type: "submit"},
		Fields: []ui.Field{
			{Name: "actor", Label: "Actor", Value: q.Actor, Placeholder: "principal id"},
			{Name: "action", Label: "Action", Type: "select", Options: actions, Value: or(v.Get("action"), anyValue)},
			{Name: "outcome", Label: "Outcome", Type: "select", Options: []string{anyValue, "allow", "deny", "error"}, Value: or(v.Get("outcome"), anyValue)},
			{Name: "since", Label: "From", Type: "date", Value: v.Get("since")},
			{Name: "until", Label: "To", Type: "date", Value: v.Get("until")},
		}}
	d.Table = ui.Table{Caption: "Audit events, newest first", Columns: []string{"Seq", "Time (UTC)", "Actor", "Action", "Outcome", "Reason", "Detail"},
		Empty: ui.Empty{Title: "No events match", Body: "Every console change and every refused one lands here. Widen the filters to see more."}}
	if bad == "" {
		evs, err := c.cfg.Audit.Search(r.Context(), q)
		if err != nil {
			d.Table.Error = "The audit log could not be read."
		}
		d.Total = len(evs)
		slices.Reverse(evs)
		evs = evs[:min(len(evs), pageSize)]
		d.Shown = len(evs)
		for _, e := range evs {
			d.Table.Rows = append(d.Table.Rows, []string{strconv.FormatUint(e.Seq, 10), e.Time.UTC().Format("2006-01-02 15:04:05"),
				e.Actor, string(e.Action), string(e.Outcome), or(string(e.Reason), "–"), detail(e.Data)})
		}
	}
	d.Chain = ui.Chip{Label: "Chain intact", Status: ui.StatusPass}
	if err := c.cfg.Audit.Verify(r.Context()); err != nil {
		d.Chain = ui.Chip{Label: "Chain broken", Status: ui.StatusFail}
	}
	qs := r.URL.Query()
	qs.Set("format", "csv")
	d.ExportCSV = "/admin/audit/export?" + qs.Encode()
	qs.Set("format", "jsonl")
	d.ExportJSON = "/admin/audit/export?" + qs.Encode()
	c.page.render(w, http.StatusOK, "audit", "Audit log", notice(r), d)
}

// detail prints event data as "k=v" pairs in key order.
func detail(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + m[k]
	}
	return or(strings.Join(parts, " "), "–")
}

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// auditExport downloads the filtered events, oldest first, as CSV or JSON lines with their
// hashes, so the chain can be checked outside. The export is itself recorded first.
func (c *Console) auditExport(w http.ResponseWriter, r *http.Request, p contract.Principal) {
	q, bad := auditQuery(r)
	format := or(r.URL.Query().Get("format"), "csv")
	if bad != "" || (format != "csv" && format != "jsonl") {
		c.page.error(w, http.StatusUnprocessableEntity, "The export was not made", "Pick csv or jsonl and give dates like 2026-11-26.")
		return
	}
	evs, err := c.cfg.Audit.Search(r.Context(), q)
	if err != nil {
		c.fail(w, "The audit log could not be read")
		return
	}
	if err := c.record(r.Context(), p, contract.ActionAuditExport, contract.OutcomeAllow, "",
		map[string]string{"format": format, "events": strconv.Itoa(len(evs))}); err != nil {
		c.fail(w, "The export was not made")
		return
	}
	name := "audit-" + c.cfg.Now().UTC().Format("20060102-150405") + "." + format
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	if format == "jsonl" {
		w.Header().Set("Content-Type", "application/x-ndjson")
		enc := json.NewEncoder(w)
		for _, e := range evs {
			_ = enc.Encode(e)
		}
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"seq", "time", "actor", "action", "resource", "outcome", "reason", "data", "prev_hash", "hash"})
	for _, e := range evs {
		_ = cw.Write([]string{strconv.FormatUint(e.Seq, 10), e.Time.UTC().Format(time.RFC3339Nano), csvSafe(e.Actor), string(e.Action),
			csvSafe(e.Resource), string(e.Outcome), string(e.Reason), csvSafe(detail(e.Data)), e.PrevHash, e.Hash})
	}
	cw.Flush()
}

// csvSafe stops a spreadsheet from reading a cell as a formula.
func csvSafe(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}
