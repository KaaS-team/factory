package ledger_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"go.opentelemetry.io/otel/attribute"
	sdklog "go.opentelemetry.io/otel/sdk/log"

	"github.com/KaaS-team/factory/ee/contract"
	"github.com/KaaS-team/factory/internal/ledger"
)

func schema(t *testing.T) *jsonschema.Resolved {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "contracts", "run-ledger.v1.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var s jsonschema.Schema
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatal(err)
	}
	r, err := s.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

var t0 = time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)

func i64(i int64) *int64 { return &i }

func entry(id, team, repo, harness, status string, cost float64) ledger.Entry {
	end := t0.Add(90 * time.Second)
	c := cost
	e := ledger.Entry{Schema: "kaas.run/v1", RunID: id, Repo: repo, Team: team, StartedAt: t0, EndedAt: &end, Status: status, CostUSD: &c,
		Tokens: &contract.LedgerTokens{Input: i64(100), Output: i64(50), CacheRead: i64(10), CacheWrite: i64(5)}}
	e.Ticket.Source, e.Ticket.ID = "github", "12"
	e.Agent.Harness, e.Agent.Model = harness, "m-1"
	return e
}

// TestWrittenLinesMatchSchema: every line Append writes validates against the schema file.
func TestWrittenLinesMatchSchema(t *testing.T) {
	root := t.TempDir()
	full := entry("r1", "core", "acme/api", "claude-code", "merged", 1.25)
	n := 7
	full.PR = &contract.LedgerPR{Number: &n, URL: "https://example.test/pr/7", OpenedAt: &t0}
	full.Review = &contract.LedgerReview{Rounds: &n}
	full.QA = &contract.LedgerQA{Verdict: "pass"}
	failed := entry("r2", "", "acme/web", "codex", "failed", 0)
	failed.FailureClass, failed.CostUSD, failed.Tokens, failed.EndedAt = "budget", nil, nil, nil
	for _, e := range []ledger.Entry{full, failed} {
		if err := ledger.Append(root, e); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(filepath.Join(root, ".factory", "ledger", "runs.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(data), []byte("\n"))
	if len(lines) != 2 {
		t.Fatalf("%d lines, want 2", len(lines))
	}
	res := schema(t)
	for i, l := range lines {
		var v map[string]any
		if err := json.Unmarshal(l, &v); err != nil {
			t.Fatal(err)
		}
		if err := res.Validate(v); err != nil {
			t.Errorf("line %d does not match the schema: %v\n%s", i+1, err, l)
		}
	}
}

// TestSchemaRejectsWhatValidateRejects: the schema file and Validate agree on bad entries.
func TestSchemaRejectsWhatValidateRejects(t *testing.T) {
	res := schema(t)
	bad := map[string]func(*ledger.Entry){
		"status": func(e *ledger.Entry) { e.Status = "done" },
		"source": func(e *ledger.Entry) { e.Ticket.Source = "email" },
		"class":  func(e *ledger.Entry) { e.FailureClass = "oops" },
		"qa":     func(e *ledger.Entry) { e.QA = &contract.LedgerQA{Verdict: "maybe"} },
		"cost":   func(e *ledger.Entry) { c := -1.0; e.CostUSD = &c },
	}
	for name, mut := range bad {
		e := entry("r", "t", "a/b", "claude-code", "merged", 1)
		mut(&e)
		if ledger.Validate(e) == nil {
			t.Errorf("%s: Validate accepted it", name)
		}
		b, _ := json.Marshal(e)
		var v map[string]any
		_ = json.Unmarshal(b, &v)
		if res.Validate(v) == nil {
			t.Errorf("%s: schema accepted it", name)
		}
	}
	// Validate is stricter than the schema here: an empty harness is useless to dx.
	e := entry("r", "t", "a/b", "", "merged", 1)
	if ledger.Validate(e) == nil {
		t.Error("Validate accepted an empty harness")
	}
	if err := ledger.Append(t.TempDir(), ledger.Entry{}); err == nil {
		t.Error("Append wrote an empty entry")
	}
}

func TestRunEndedTwiceKeepsLast(t *testing.T) {
	root := t.TempDir()
	_ = ledger.Append(root, entry("r1", "t", "a/b", "claude-code", "open", 0.5))
	_ = ledger.Append(root, entry("r1", "t", "a/b", "claude-code", "merged", 2))
	got, err := ledger.Entries(root)
	if err != nil || len(got) != 1 || got[0].Status != "merged" {
		t.Fatalf("%v %+v", err, got)
	}
}

func TestBadLineNamesItsNumber(t *testing.T) {
	root := t.TempDir()
	_ = ledger.Append(root, entry("r1", "t", "a/b", "claude-code", "merged", 1))
	f, _ := os.OpenFile(filepath.Join(root, ledger.Dir, ledger.RunsFile), os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.WriteString("{not json\n")
	_ = f.Close()
	if _, err := ledger.Entries(root); err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("got %v", err)
	}
}

// TestSpendJSONIsDerivedFromEntries: spend.json equals Summarize over the ledger file.
func TestSpendJSONIsDerivedFromEntries(t *testing.T) {
	root := t.TempDir()
	for _, e := range []ledger.Entry{
		entry("r1", "core", "acme/api", "claude-code", "merged", 1.5),
		entry("r2", "core", "acme/api", "codex", "failed", 0.5),
		entry("r3", "web", "acme/web", "claude-code", "merged", 2),
	} {
		if err := ledger.Append(root, e); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(filepath.Join(root, ledger.Dir, ledger.SpendFile))
	if err != nil {
		t.Fatal(err)
	}
	var got ledger.Summary
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	all, _ := ledger.Entries(root)
	want := ledger.Summarize(all)
	if a, b := mustJSON(got), mustJSON(want); a != b {
		t.Fatalf("spend.json %s\nwant %s", a, b)
	}
	if got.Factory.CostUSD != 4 || got.Factory.Merged != 2 || got.Factory.CostPerMergedUSD != 2 {
		t.Errorf("factory %+v", got.Factory)
	}
	if c := got.Teams["core"]; c.Runs != 2 || c.CostUSD != 2 || c.Failed != 1 || c.Input != 200 {
		t.Errorf("core %+v", c)
	}
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

func TestSelectFilters(t *testing.T) {
	a := entry("a", "core", "acme/api", "claude-code", "merged", 1)
	b := entry("b", "web", "acme/web", "codex", "merged", 1)
	b.StartedAt = t0.Add(48 * time.Hour)
	all := []ledger.Entry{b, a}
	ids := func(f ledger.Filter) string {
		var s []string
		for _, e := range ledger.Select(all, f) {
			s = append(s, e.RunID)
		}
		return strings.Join(s, ",")
	}
	for _, c := range []struct {
		f    ledger.Filter
		want string
	}{
		{ledger.Filter{}, "a,b"},
		{ledger.Filter{Team: "web"}, "b"},
		{ledger.Filter{Repo: "acme/api"}, "a"},
		{ledger.Filter{Harness: "codex"}, "b"},
		{ledger.Filter{Since: t0.Add(time.Hour)}, "b"},
		{ledger.Filter{Until: t0.Add(time.Hour)}, "a"},
		{ledger.Filter{Since: t0, Until: t0}, ""},
	} {
		if got := ids(c.f); got != c.want {
			t.Errorf("%+v: %q want %q", c.f, got, c.want)
		}
	}
}

type memExporter struct{ recs []sdklog.Record }

func (m *memExporter) Export(_ context.Context, r []sdklog.Record) error {
	for _, x := range r {
		m.recs = append(m.recs, x.Clone())
	}
	return nil
}
func (m *memExporter) Shutdown(context.Context) error   { return nil }
func (m *memExporter) ForceFlush(context.Context) error { return nil }

// TestExportCarriesCountDurationCostNotModel: the OTLP record holds the run metrics and no model or tokens.
func TestExportCarriesCountDurationCostNotModel(t *testing.T) {
	m := &memExporter{}
	x := ledger.NewExporter(m)
	x.Export(t.Context(), entry("r1", "core", "acme/api", "claude-code", "merged", 1.25))
	if err := x.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(m.recs) != 1 {
		t.Fatalf("%d records", len(m.recs))
	}
	got := map[string]string{}
	m.recs[0].WalkAttributes(func(kv attribute.KeyValue) bool { got[string(kv.Key)] = kv.Value.String(); return true })
	for k, v := range map[string]string{"kaas.run.count": "1", "kaas.run.duration_ms": "90000", "kaas.run.cost_usd": "1.25",
		"kaas.run.status": "merged", "kaas.run.harness": "claude-code"} {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	for k, v := range got {
		if strings.Contains(k, "model") || strings.Contains(k, "token") || v == "m-1" {
			t.Errorf("operator detail in export: %s=%s", k, v)
		}
	}
}

func TestNoEndpointNoExporter(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_LOGS_ENDPOINT", "")
	if x, err := ledger.NewExporterFromEnv(t.Context()); x != nil || err != nil {
		t.Fatalf("%v %v", x, err)
	}
}
