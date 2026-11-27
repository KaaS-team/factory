package enterprise

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/KaaS-team/factory/ee/contract"
	"github.com/KaaS-team/factory/internal/api"
	"github.com/KaaS-team/factory/internal/audit"
	"github.com/KaaS-team/factory/internal/budget"
	"github.com/KaaS-team/factory/internal/dispatch/spend"
	"github.com/KaaS-team/factory/internal/meter/cap"
)

// collector is an in-process OTLP/HTTP logs endpoint. It keeps every request body.
type collector struct {
	srv    *httptest.Server
	mu     sync.Mutex
	bodies []string
	paths  []string
}

func newCollector(t *testing.T) *collector {
	t.Helper()
	c := &collector{}
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		c.mu.Lock()
		c.bodies, c.paths = append(c.bodies, string(b)), append(c.paths, r.URL.Path)
		c.mu.Unlock()
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(c.srv.Close)
	return c
}

// has reports whether a received export carries every needle.
func (c *collector) has(needles ...string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, b := range c.bodies {
		ok := true
		for _, n := range needles {
			ok = ok && strings.Contains(b, n)
		}
		if ok {
			return true
		}
	}
	return false
}

// exportAudit points the OTLP exporter at c, installs it as the process audit sink and returns a
// flush that sends what was recorded and restores the old sink.
func exportAudit(t *testing.T, c *collector, extra ...audit.Sink) (flush func()) {
	t.Helper()
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", c.srv.URL)
	o, err := audit.NewOTLPFromEnv(t.Context())
	if err != nil || o == nil {
		t.Fatalf("otlp exporter: %v %v", o, err)
	}
	restore := audit.SetSink(audit.Multi(append([]audit.Sink{o}, extra...)))
	return func() {
		restore()
		if err := o.Close(context.Background()); err != nil {
			t.Fatalf("flush otlp: %v", err)
		}
	}
}

// TestAuditReachesCollector: an audited action is exported to the OTLP/HTTP collector as a log record.
func TestAuditReachesCollector(t *testing.T) {
	c := newCollector(t)
	flush := exportAudit(t, c)
	audit.Record(t.Context(), "run.cancel", "acme/app", "T-7", contract.ErrBudgetExceeded)
	flush()
	if !c.has("run.cancel", "acme/app", "budget.exceeded") {
		t.Fatalf("collector saw no export carrying the action, target and reason; paths %v", c.paths)
	}
	for _, p := range c.paths {
		if p != "/v1/logs" {
			t.Errorf("export went to %q, want /v1/logs", p)
		}
	}
}

// fakeDispatcher counts the sessions it starts.
type fakeDispatcher struct {
	api.Dispatcher
	started int
}

func (f *fakeDispatcher) Dispatch(context.Context, api.Brief) (api.Session, error) {
	f.started++
	return api.Session{ID: "s"}, nil
}

// runBudgetStop is the budget scenario over any contract.Budget with a 1,000,000 micro cap on team
// acme: a session reserves 800,000, spends past it and is stopped, and the next run for that team
// is refused with the budget reason before an agent starts. stopped receives the reservation of
// the stopped session.
func runBudgetStop(t *testing.T, b contract.Budget, stopped func(contract.Reservation)) {
	t.Helper()
	key := contract.BudgetKey{Scope: contract.BudgetTeam, ID: "acme"}
	budget.SetEnforcer(&budget.Enforcer{Open: func() (contract.Budget, error) { return b, nil }, Estimate: 1})
	t.Cleanup(func() { budget.SetEnforcer(nil) })
	d := &fakeDispatcher{}
	run := spend.Wrap(d)
	brief := api.Brief{Repo: "acme/app"}

	if _, err := run.Dispatch(t.Context(), brief); err != nil {
		t.Fatalf("run under the cap was refused: %v", err)
	}
	res, err := b.Reserve(t.Context(), []contract.BudgetKey{key}, 800_000)
	if err != nil {
		t.Fatal(err)
	}
	cancelled := false
	w := &cap.Watch{Budget: b, Res: res, Stop: func() { cancelled = true }, Stopped: stopped}
	if err := w.Add(500_000); err != nil || cancelled {
		t.Fatalf("below the hold: err %v, cancelled %v", err, cancelled)
	}
	if err := w.Add(500_000); !errors.Is(err, cap.ErrOverCap) || !cancelled {
		t.Fatalf("past the hold: err %v, cancelled %v; want ErrOverCap and a stop", err, cancelled)
	}
	if err := w.Done(t.Context()); err != nil {
		t.Fatal(err)
	}

	before := d.started
	_, err = run.Dispatch(t.Context(), brief)
	if !errors.Is(err, contract.ErrBudgetExceeded) || !strings.Contains(err.Error(), string(contract.ReasonBudgetExceeded)) {
		t.Fatalf("run over the cap: %v, want the %s reason", err, contract.ReasonBudgetExceeded)
	}
	if d.started != before {
		t.Fatal("an agent session started for a team over its cap")
	}
	if _, err := run.Dispatch(t.Context(), api.Brief{Repo: "globex/app"}); err != nil {
		t.Fatalf("another team was stopped by acme's cap: %v", err)
	}
}

// memBudget is the smallest contract.Budget, so the untagged build proves the core's half of the stop.
type memBudget struct {
	mu    sync.Mutex
	limit contract.Micros
	spent contract.Micros
	held  map[string]contract.Reservation
}

func (m *memBudget) Reserve(_ context.Context, keys []contract.BudgetKey, amt contract.Micros) (contract.Reservation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.spent+amt > m.limit {
		return contract.Reservation{}, contract.ErrBudgetExceeded
	}
	r := contract.Reservation{ID: "r1", Keys: keys, Amount: amt}
	m.held = map[string]contract.Reservation{r.ID: r}
	return r, nil
}

func (m *memBudget) Commit(_ context.Context, id string, actual contract.Micros) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.held[id]; !ok {
		return contract.ErrUnknownReservation
	}
	delete(m.held, id)
	m.spent += actual
	return nil
}

func (m *memBudget) Release(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.held, id)
	return nil
}

func (m *memBudget) Remaining(_ context.Context, k contract.BudgetKey) (contract.Micros, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if k.ID != "acme" {
		return 0, false, nil
	}
	return max(m.limit-m.spent, 0), true, nil
}

// TestBudgetStopCore: the core refuses a run for a team over its cap with the budget reason.
func TestBudgetStopCore(t *testing.T) {
	runBudgetStop(t, &memBudget{limit: 1_000_000}, nil)
}

// recordRunCancel audits one budget stop through the process sink.
func recordRunCancel(t *testing.T) {
	t.Helper()
	audit.Record(t.Context(), "run.cancel", "acme/app", "T-7", contract.ErrBudgetExceeded)
}
