package ledger

import (
	"cmp"
	"fmt"
	"hash/fnv"
	"slices"
	"strings"
)

// outcomeStates maps an outcome kind to its states; the first is accepted, the second is rejected.
var outcomeStates = map[string][2]string{
	"pr":      {"merged", "closed"},
	"issue":   {"kept", "dismissed"},
	"comment": {"acted", "ignored"},
}

func validateOutcome(o *Entry) error {
	if o.Outcome == nil {
		return nil
	}
	s, ok := outcomeStates[o.Outcome.Kind]
	if !ok || (o.Outcome.State != s[0] && o.Outcome.State != s[1]) {
		return fmt.Errorf("ledger: outcome %q/%q is not a known kind and state", o.Outcome.Kind, o.Outcome.State)
	}
	return nil
}

// Accepted reports whether the work was kept: a merged PR, a kept issue or a comment acted on. A
// merged status with no outcome counts as an accepted PR, so runs written before outcomes existed
// keep their meaning. Rejected is the other state of each kind; anything else is pending.
func Accepted(e Entry) bool {
	if e.Outcome != nil {
		return e.Outcome.State == outcomeStates[e.Outcome.Kind][0]
	}
	return e.Status == "merged"
}

// Rejected reports whether the work was closed unmerged, dismissed or ignored.
func Rejected(e Entry) bool {
	return e.Outcome != nil && e.Outcome.State == outcomeStates[e.Outcome.Kind][1]
}

// Scored is the outcome count and cost of a group of runs. Cost is the group's whole cost, failed
// and rejected runs included, taken from the same sum as Totals.CostUSD: cheaper by doing less
// useful work shows up as fewer accepted outcomes, not as a lower cost.
type Scored struct {
	Key                string  `json:"key"`
	Runs               int     `json:"runs"`
	Accepted           int     `json:"accepted"`
	Rejected           int     `json:"rejected"`
	Pending            int     `json:"pending"`
	CostUSD            float64 `json:"cost_usd"`
	CostPerAcceptedUSD float64 `json:"cost_per_accepted_usd"` // 0 with none accepted
}

// Score groups entries by key and scores each group, in key order. Runs for which key returns ""
// form the group "(none)".
func Score(entries []Entry, key func(Entry) string) []Scored {
	sums := map[string]*Totals{}
	out := map[string]*Scored{}
	for _, e := range entries {
		k := key(e)
		if k == "" {
			k = "(none)"
		}
		if out[k] == nil {
			out[k], sums[k] = &Scored{Key: k}, &Totals{}
		}
		s := out[k]
		add(sums[k], e)
		s.Runs++
		switch {
		case Accepted(e):
			s.Accepted++
		case Rejected(e):
			s.Rejected++
		default:
			s.Pending++
		}
	}
	res := make([]Scored, 0, len(out))
	for k, s := range out {
		s.CostUSD = sums[k].CostUSD
		if s.Accepted > 0 {
			s.CostPerAcceptedUSD = s.CostUSD / float64(s.Accepted)
		}
		res = append(res, *s)
	}
	slices.SortFunc(res, func(a, b Scored) int { return cmp.Compare(a.Key, b.Key) })
	return res
}

// ByWorkflow, ByTeam and ByVariant are the grouping keys the metrics command uses.
func ByWorkflow(e Entry) string { return e.Workflow }
func ByTeam(e Entry) string     { return e.Team }
func ByVariant(e Entry) string  { return e.Variant }

// Text renders rows as a table. Cost is operator detail: this is for the operator CLI only.
func Text(rows []Scored) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%-24s %5s %8s %8s %7s %9s %12s\n", "group", "runs", "accepted", "rejected", "pending", "cost_usd", "per_accepted")
	for _, r := range rows {
		per := "-"
		if r.Accepted > 0 {
			per = fmt.Sprintf("%.2f", r.CostPerAcceptedUSD)
		}
		fmt.Fprintf(&b, "%-24s %5d %8d %8d %7d %9.2f %12s\n", r.Key, r.Runs, r.Accepted, r.Rejected, r.Pending, r.CostUSD, per)
	}
	return b.String()
}

// PickVariant assigns a run to one of a workflow's prompt variants, so two variants run side by
// side. The choice depends only on the run id: the same run always gets the same variant, and a
// large set of runs splits close to evenly. It returns "" with no variants.
func PickVariant(runID string, variants []string) string {
	if len(variants) == 0 {
		return ""
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(runID))
	return variants[int(h.Sum32()%uint32(len(variants)))]
}
