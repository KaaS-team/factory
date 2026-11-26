// Package mergeq is a batched, speculative merge queue. It tests a batch of ready PRs together on a
// temporary ref, fast-forwards the base when the gate passes, and bisects a red batch until the
// culprit PR is alone. Plan is the pure decision function; Run applies its actions through the
// narrow Git, Gate and Store interfaces. Ideas follow bors, Mergify, GitHub merge queue and
// uber/submitqueue (Apache-2.0, speculative stacked builds); no code
// is copied from them.
package mergeq

import "strings"

// DefaultBatchSize is the batch size used when Config.BatchSize is not positive.
const DefaultBatchSize = 4

// Entry is one queued PR and the head it had when it was queued.
type Entry struct {
	ID       string
	Head     string
	Single   bool   // tested alone, never in a batch with other PRs
	Approved string // the head SHA that was approved, for display; Plan reads it from PRState
}

// PRState is what the outside world says about a PR now. A PR missing from State.PRs counts as closed.
type PRState struct {
	Head   string
	Open   bool
	Merged bool
	// Approved is the head SHA a human approved. With State.Pin, the PR may land only while Head equals it.
	Approved string
}

// Result is the outcome of running the gate on one tested set of PRs.
type Result struct {
	IDs   []string
	Heads []string
	Base  string // the base the set was merged onto
	Ref   string // the temp ref that was gated
	Tip   string // the commit the temp ref points at; recorded as merged_sha when the set lands
	Pass  bool
	Log   string // gate log tail when the gate failed
	// Inferred marks a red result deduced from a bisect, never observed on its own.
	Inferred bool
}

// State is everything Plan needs: the base, batch size, live PR states and gate results so far.
type State struct {
	Base      string
	BatchSize int
	// Pin holds a PR whose head is not its approved SHA: it is refused, never requeued.
	Pin     bool
	PRs     map[string]PRState
	Results map[string]Result // keyed by Key
}

// Kind names what an Action asks for.
type Kind string

// The kinds of Action Plan can return.
const (
	KindTest    Kind = "test"    // merge IDs onto base and run the gate
	KindLand    Kind = "land"    // fast-forward base to Ref, which passed the gate
	KindEject   Kind = "eject"   // remove ID from the queue with the failing log tail
	KindDrop    Kind = "drop"    // remove ID because it closed or merged
	KindRequeue Kind = "requeue" // record ID's new head so it is tested again
	KindRefuse  Kind = "refuse"  // remove ID because its head moved after approval; a human approves the new head
)

// Drop reasons.
const (
	ReasonClosed = "closed"
	ReasonMerged = "merged"
	// ReasonUnapproved is the refuse reason: the head is not the approved SHA.
	ReasonUnapproved = "approval-stale"
)

// Action is one step Plan asks Run to take.
type Action struct {
	Kind   Kind
	IDs    []string // test and land: the PRs, in queue order
	Heads  []string // test and land: their heads
	Ref    string   // land: the gated ref
	Tip    string   // land: the commit Ref points at
	ID     string   // eject, drop, requeue
	Head   string   // requeue and refuse: the new head
	Reason string   // drop
	Log    string   // eject
}

// Key identifies a set of PRs at specific heads, in order.
func Key(ids, heads []string) string {
	parts := make([]string, len(ids))
	for i := range ids {
		parts[i] = ids[i] + "@" + heads[i]
	}
	return strings.Join(parts, ",")
}

func split(es []Entry) (ids, heads []string) {
	for _, e := range es {
		ids = append(ids, e.ID)
		heads = append(heads, e.Head)
	}
	return ids, heads
}

// Plan decides the next step for a queue. It first returns housekeeping for closed, merged or moved
// PRs. Otherwise it returns one action: land a green batch, eject a lone culprit, or test the next
// batch or half-batch. It returns nil when the queue is empty.
func Plan(queue []Entry, st State) []Action {
	var housekeeping []Action
	var live []Entry
	for _, e := range queue {
		p := st.PRs[e.ID]
		switch {
		case p.Merged:
			housekeeping = append(housekeeping, Action{Kind: KindDrop, ID: e.ID, Reason: ReasonMerged})
		case !p.Open:
			housekeeping = append(housekeeping, Action{Kind: KindDrop, ID: e.ID, Reason: ReasonClosed})
		case st.Pin && p.Head != p.Approved:
			housekeeping = append(housekeeping, Action{Kind: KindRefuse, ID: e.ID, Head: p.Head, Reason: ReasonUnapproved})
		case p.Head != e.Head:
			housekeeping = append(housekeeping, Action{Kind: KindRequeue, ID: e.ID, Head: p.Head})
		default:
			live = append(live, e)
		}
	}
	if len(housekeeping) > 0 {
		return housekeeping
	}
	if len(live) == 0 {
		return nil
	}
	size := st.BatchSize
	if size <= 0 {
		size = DefaultBatchSize
	}
	limit := len(Take(live, size))
	// A red prefix means a bisect is in progress: continue inside the shortest one, the tightest
	// set known to hold a culprit.
	for n := 1; n <= limit; n++ {
		if r, ok := lookup(st, live[:n]); ok && !r.Pass {
			return []Action{resolve(st, live[:n])}
		}
	}
	batch := live[:limit]
	if r, ok := lookup(st, batch); ok && r.Pass && r.Base == st.Base {
		return []Action{land(batch, r)}
	}
	return []Action{test(batch)}
}

// resolve narrows a set known to be red to its culprit.
func resolve(st State, set []Entry) Action {
	for {
		if len(set) == 1 {
			r, _ := lookup(st, set)
			if r.Inferred {
				return test(set) // never run alone: prove it before ejecting
			}
			return Action{Kind: KindEject, ID: set[0].ID, Log: r.Log}
		}
		half, _, _ := Halve(set)
		r, ok := lookup(st, half)
		switch {
		case !ok:
			return test(half)
		case r.Pass && r.Base == st.Base:
			return land(half, r)
		case r.Pass:
			return test(half) // green on an older base: test again
		default:
			set = half
		}
	}
}

func lookup(st State, es []Entry) (Result, bool) {
	ids, heads := split(es)
	r, ok := st.Results[Key(ids, heads)]
	return r, ok
}

func test(es []Entry) Action {
	ids, heads := split(es)
	return Action{Kind: KindTest, IDs: ids, Heads: heads}
}

func land(es []Entry, r Result) Action {
	ids, heads := split(es)
	return Action{Kind: KindLand, IDs: ids, Heads: heads, Ref: r.Ref, Tip: r.Tip}
}

// AfterLand returns the results with a red result for the PRs left over from every red set that
// began with the landed PRs, because the culprit of such a set is among the rest.
// With prove, a lone PR left from a red set whose landed prefix passed on the
// same base is a proven red, not an inferred one, so it is ejected without a run of its own.
func AfterLand(results map[string]Result, landedIDs, landedHeads []string, prove bool) map[string]Result {
	out := make(map[string]Result, len(results))
	for k, r := range results {
		out[k] = r
	}
	landed, landedOK := results[Key(landedIDs, landedHeads)]
	for _, r := range results {
		if r.Pass || len(r.IDs) <= len(landedIDs) || !hasPrefix(r.IDs, landedIDs) || !hasPrefix(r.Heads, landedHeads) {
			continue
		}
		ids, heads := r.IDs[len(landedIDs):], r.Heads[len(landedIDs):]
		k := Key(ids, heads)
		if _, ok := out[k]; !ok {
			// One PR left, and the landed prefix passed on the same base: the stacked set failed because of
			// this PR alone, so its failure was observed and needs no run of its own.
			proven := prove && len(ids) == 1 && landedOK && landed.Pass && landed.Base == r.Base
			out[k] = Result{IDs: ids, Heads: heads, Log: r.Log, Inferred: !proven}
		}
	}
	return out
}

func hasPrefix(s, prefix []string) bool {
	if len(prefix) > len(s) {
		return false
	}
	for i := range prefix {
		if s[i] != prefix[i] {
			return false
		}
	}
	return true
}
