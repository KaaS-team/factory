package ledger

import (
	"cmp"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"time"
)

// Filter selects entries. An empty field matches everything. Since is inclusive and Until is
// exclusive, both on started_at.
type Filter struct {
	Team    string
	Repo    string
	Harness string
	Since   time.Time
	Until   time.Time
}

// Match reports whether e passes f.
func (f Filter) Match(e Entry) bool {
	switch {
	case f.Team != "" && e.Team != f.Team,
		f.Repo != "" && e.Repo != f.Repo,
		f.Harness != "" && e.Agent.Harness != f.Harness,
		!f.Since.IsZero() && e.StartedAt.Before(f.Since),
		!f.Until.IsZero() && !e.StartedAt.Before(f.Until):
		return false
	}
	return true
}

// Select returns the entries that pass f, oldest first.
func Select(all []Entry, f Filter) []Entry {
	var out []Entry
	for _, e := range all {
		if f.Match(e) {
			out = append(out, e)
		}
	}
	slices.SortStableFunc(out, func(a, b Entry) int { return a.StartedAt.Compare(b.StartedAt) })
	return out
}

// Totals is the sum of a set of runs.
type Totals struct {
	Runs       int     `json:"runs"`
	Merged     int     `json:"merged"`
	Failed     int     `json:"failed"`
	CostUSD    float64 `json:"cost_usd"`
	Input      int64   `json:"input_tokens"`
	Output     int64   `json:"output_tokens"`
	CacheRead  int64   `json:"cache_read_tokens"`
	CacheWrite int64   `json:"cache_write_tokens"`
	// CostPerMergedUSD is the cost of every run in the set over its merged runs, 0 with none merged.
	CostPerMergedUSD float64 `json:"cost_per_merged_pr_usd"`
}

// Summary is the content of spend.json: totals per team and for the whole factory.
type Summary struct {
	Factory Totals            `json:"factory"`
	Teams   map[string]Totals `json:"teams"`
}

// Summarize derives the spend summary. It is the one place spend is added up. Runs with no team
// count under the team "" key, shown as "(none)" by callers that print it.
func Summarize(entries []Entry) Summary {
	s := Summary{Teams: map[string]Totals{}}
	for _, e := range entries {
		add(&s.Factory, e)
		t := s.Teams[e.Team]
		add(&t, e)
		s.Teams[e.Team] = t
	}
	finish(&s.Factory)
	for k, t := range s.Teams {
		finish(&t)
		s.Teams[k] = t
	}
	return s
}

func add(t *Totals, e Entry) {
	t.Runs++
	switch e.Status {
	case "merged":
		t.Merged++
	case "failed":
		t.Failed++
	}
	if e.CostUSD != nil {
		t.CostUSD += *e.CostUSD
	}
	if k := e.Tokens; k != nil {
		t.Input += deref(k.Input)
		t.Output += deref(k.Output)
		t.CacheRead += deref(k.CacheRead)
		t.CacheWrite += deref(k.CacheWrite)
	}
}

func finish(t *Totals) {
	if t.Merged > 0 {
		t.CostPerMergedUSD = t.CostUSD / float64(t.Merged)
	}
}

func deref(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

// TeamNames returns the team keys of s in order.
func (s Summary) TeamNames() []string {
	names := make([]string, 0, len(s.Teams))
	for k := range s.Teams {
		names = append(names, k)
	}
	slices.SortFunc(names, cmp.Compare[string])
	return names
}

func writeSpend(dir string, s Summary) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, SpendFile+".*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, SpendFile))
}
