package mergeq

import (
	"context"
	"sync"
)

// The speculative step follows the idea in uber/submitqueue: build the next changes as stacked
// merges, run their gates in parallel, and keep the longest prefix that passes. No code is copied.
// A prefix is the first k queued PRs stacked on the base in order, so a passing prefix also proves
// every shorter one. Beside each batch the Runner tests the stacked half-prefix. When the batch is
// red and the half is green, the half lands in the same round and the rest is the only suspect.
// When the rest is one PR and the green prefix landed on the same base, that PR is the culprit
// without a run of its own (see AfterLand).

// speculate adds a stacked half-prefix beside every test action, runs the group in parallel and
// records the results. It returns the actions Run still has to apply, without the tests it ran.
func (r *Runner) speculate(ctx context.Context, actions []Action, results map[string]Result, rep *Report) ([]Action, error) {
	if !r.Cfg.Speculate || len(actions) != 1 || actions[0].Kind != KindTest || len(actions[0].IDs) < 2 {
		return actions, nil
	}
	a := actions[0]
	group := []Action{a}
	for _, k := range []int{len(a.IDs) / 2} {
		if k < 1 {
			continue
		}
		p := Action{Kind: KindTest, IDs: a.IDs[:k], Heads: a.Heads[:k]}
		if _, known := results[Key(p.IDs, p.Heads)]; !known {
			group = append(group, p)
		}
	}
	// Merging touches the repository, so it runs one at a time; only the gates run together.
	res := make([]Result, len(group))
	ran := make([]bool, len(group))
	for i, g := range group {
		m, conflicted, err := r.merge(ctx, g)
		if err != nil {
			return nil, err
		}
		res[i], ran[i] = m, !conflicted
	}
	errs := make([]error, len(group))
	var wg sync.WaitGroup
	for i := range group {
		if !ran[i] {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			res[i], errs[i] = r.gate(ctx, res[i])
		}()
	}
	wg.Wait()
	for i, g := range group {
		if errs[i] != nil {
			return nil, errs[i]
		}
		if ran[i] {
			rep.GateRuns++
		}
		results[Key(g.IDs, g.Heads)] = res[i]
	}
	return nil, nil
}
