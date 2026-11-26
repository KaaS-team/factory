package mergeq

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"
)

// lockedGate lets the in-memory world serve parallel gate runs.
type lockedGate struct {
	w  *world
	mu sync.Mutex
}

func (g *lockedGate) Run(ctx context.Context, ref string) (Verdict, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.w.Run(ctx, ref)
}

func specRunner(w *world, batch int) *Runner {
	return &Runner{Git: w, Gate: &lockedGate{w: w}, Store: w, Cfg: Config{BatchSize: batch, Speculate: true}}
}

// Eight changes, one bad, at every position: seven land and the bad one is named. The gate never
// runs more than once per change, and over the sweep it runs well under serial. Position 4 ties
// serial (8 runs): the half-batch and the batch are both red, so it splits twice.
func TestSpeculativeEightWithOneBadBeatsSerial(t *testing.T) {
	const n = 8
	total := 0
	t.Cleanup(func() {
		if total >= n*n {
			t.Errorf("sweep used %d gate runs, serial needs %d", total, n*n)
		}
	})
	for bad := 1; bad <= n; bad++ {
		t.Run(fmt.Sprintf("bad=%d", bad), func(t *testing.T) {
			w := newWorld(n, bad)
			rep, err := specRunner(w, n).Run(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(rep.Landed) != n-1 || slices.Contains(rep.Landed, fmt.Sprint(bad)) {
				t.Fatalf("landed %v", rep.Landed)
			}
			if len(rep.Ejected) != 1 || rep.Ejected[0].ID != fmt.Sprint(bad) {
				t.Fatalf("ejected %v, want only PR %d", rep.Ejected, bad)
			}
			if rep.GateRuns != w.gateRuns {
				t.Fatalf("report counts %d gate runs, gate saw %d", rep.GateRuns, w.gateRuns)
			}
			t.Logf("gate runs %d (serial %d)", rep.GateRuns, n)
			total += rep.GateRuns
			if rep.GateRuns > n || (bad == 6 && rep.GateRuns >= n) {
				t.Fatalf("%d gate runs against serial %d", rep.GateRuns, n)
			}
		})
	}
}

// With nothing bad the whole batch lands from one pair of runs.
func TestSpeculativeGreenBatchLandsOnce(t *testing.T) {
	w := newWorld(8)
	rep, err := specRunner(w, 8).Run(context.Background())
	if err != nil || len(rep.Landed) != 8 || len(w.ffs) != 1 {
		t.Fatalf("landed %v, fast-forwards %d, err %v", rep.Landed, len(w.ffs), err)
	}
}

// A culprit whose failure was seen stacked on a green prefix is ejected without a run of its own.
func TestAfterLandProvesLoneCulprit(t *testing.T) {
	results := map[string]Result{
		"1@a":         {IDs: []string{"1"}, Heads: []string{"a"}, Base: "B", Pass: true},
		"1@a,2@b":     {IDs: []string{"1", "2"}, Heads: []string{"a", "b"}, Base: "B", Log: "FAIL"},
		"1@a,2@b,3@c": {IDs: []string{"1", "2", "3"}, Heads: []string{"a", "b", "c"}, Base: "B", Log: "FAIL"},
	}
	out := AfterLand(results, []string{"1"}, []string{"a"}, true)
	if r := out["2@b"]; r.Pass || r.Inferred || r.Log != "FAIL" {
		t.Fatalf("lone culprit %+v, want a proven red", r)
	}
	if r := out["2@b,3@c"]; !r.Inferred {
		t.Fatalf("two left %+v, want inferred", r)
	}
	// A green prefix on a different base proves nothing.
	results["1@a"] = Result{IDs: []string{"1"}, Heads: []string{"a"}, Base: "B0", Pass: true}
	if r := AfterLand(results, []string{"1"}, []string{"a"}, true)["2@b"]; !r.Inferred {
		t.Fatalf("other base %+v, want inferred", r)
	}
}
