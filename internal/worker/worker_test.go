package worker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KaaS-team/factory/internal/dispatch/broker"
)

// cuttable is a transport whose network can be cut, to model a worker that vanishes.
type cuttable struct{ cut atomic.Bool }

func (c *cuttable) RoundTrip(r *http.Request) (*http.Response, error) {
	if c.cut.Load() {
		return nil, errors.New("network cut")
	}
	return http.DefaultTransport.RoundTrip(r)
}

func plane(t *testing.T, ttl time.Duration, pools ...string) (*httptest.Server, *broker.Leaser, map[string]string) {
	t.Helper()
	ps, toks := broker.NewPools(), map[string]string{}
	for _, name := range append(pools, broker.SubmitPool) {
		tok, err := broker.NewToken()
		if err != nil {
			t.Fatal(err)
		}
		if err := ps.AddHash(name, broker.HashToken(tok)); err != nil {
			t.Fatal(err)
		}
		toks[name] = tok
	}
	l := broker.NewLeaser(broker.LeaserOptions{TTL: ttl})
	srv := httptest.NewServer((&Plane{L: l, Pools: ps}).Handler())
	t.Cleanup(srv.Close)
	return srv, l, toks
}

func waitState(t *testing.T, l *broker.Leaser, id, state string) broker.JobStatus {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if st, _ := l.Status(id); st.State == state {
			return st
		}
		time.Sleep(10 * time.Millisecond)
	}
	st, _ := l.Status(id)
	t.Fatalf("job %s is %q, want %q", id, st.State, state)
	return st
}

func TestLostWorkerLeaseIsReLeased(t *testing.T) {
	srv, l, toks := plane(t, 400*time.Millisecond, "default")
	ctx, stop := context.WithCancel(context.Background())
	defer stop()

	net := &cuttable{}
	started := make(chan struct{})
	a := &Worker{PlaneURL: srv.URL, Token: toks["default"], Labels: map[string]string{"name": "a"},
		Client: &http.Client{Transport: net}, Beat: 50 * time.Millisecond, Wait: time.Second,
		Run: func(ctx context.Context, _ broker.Job, out io.Writer) (json.RawMessage, error) {
			io.WriteString(out, "a started\n")
			close(started)
			<-ctx.Done() // a hangs until the test ends
			return nil, ctx.Err()
		}}
	go a.Serve(ctx, 0)

	id, err := l.Submit(broker.Job{ID: "j1"})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	net.cut.Store(true) // a is lost: no heartbeat, no completion reaches the plane

	b := &Worker{PlaneURL: srv.URL, Token: toks["default"], Labels: map[string]string{"name": "b"},
		Beat: 50 * time.Millisecond, Wait: time.Second,
		Run: func(_ context.Context, _ broker.Job, out io.Writer) (json.RawMessage, error) {
			io.WriteString(out, "b ran\n")
			return json.RawMessage(`{"by":"b"}`), nil
		}}
	go b.Serve(ctx, time.Second)

	st := waitState(t, l, id, broker.StateDone)
	if st.Attempts != 2 || string(st.Result) != `{"by":"b"}` {
		t.Fatalf("want 2 attempts and b's result, got %+v", st)
	}
	if !strings.Contains(strings.Join(st.Log, "|"), "b ran") {
		t.Fatalf("log lost: %v", st.Log)
	}
}

func TestStaleCompleteIsRejected(t *testing.T) {
	now := time.Now()
	l := broker.NewLeaser(broker.LeaserOptions{TTL: time.Second, Now: func() time.Time { return now }})
	l.Submit(broker.Job{ID: "j"})
	w1 := l.Register("p", nil, nil)
	first, _ := l.TryLease(w1.ID)
	now = now.Add(2 * time.Second) // w1 misses its heartbeat
	w2 := l.Register("p", nil, nil)
	second, _ := l.TryLease(w2.ID)
	if second == nil || second.ID == first.ID {
		t.Fatal("job was not re-leased")
	}
	if err := l.Complete(first.ID, true, nil); !errors.Is(err, broker.ErrStaleLease) {
		t.Fatalf("stale complete: %v", err)
	}
	if err := l.Heartbeat(w1.ID); !errors.Is(err, broker.ErrLostWorker) {
		t.Fatalf("lost worker heartbeat: %v", err)
	}
}

func TestAttemptsRunOut(t *testing.T) {
	now := time.Now()
	l := broker.NewLeaser(broker.LeaserOptions{TTL: time.Second, MaxAttempts: 2, Now: func() time.Time { return now }})
	l.Submit(broker.Job{ID: "j"})
	for range 2 {
		w := l.Register("p", nil, nil)
		if ls, _ := l.TryLease(w.ID); ls == nil {
			t.Fatal("no lease")
		}
		now = now.Add(2 * time.Second)
	}
	if st, _ := l.Status("j"); st.State != broker.StateExhaust {
		t.Fatalf("state %q", st.State)
	}
}

func TestSelectionByLabelsAndCaps(t *testing.T) {
	l := broker.NewLeaser(broker.LeaserOptions{})
	l.Submit(broker.Job{ID: "gpu", Selector: map[string]string{"pool": "gpu"}})
	l.Submit(broker.Job{ID: "docker", Caps: []string{"docker"}})
	plain := l.Register("default", map[string]string{"os": "linux"}, nil)
	if ls, _ := l.TryLease(plain.ID); ls != nil {
		t.Fatalf("plain worker took %s", ls.Job.ID)
	}
	gpu := l.Register("gpu", nil, nil)
	if ls, _ := l.TryLease(gpu.ID); ls == nil || ls.Job.ID != "gpu" {
		t.Fatalf("gpu worker got %+v", ls)
	}
	dk := l.Register("default", nil, []string{"docker"})
	if ls, _ := l.TryLease(dk.ID); ls == nil || ls.Job.ID != "docker" {
		t.Fatalf("docker worker got %+v", ls)
	}
}

func TestDrainStopsLeases(t *testing.T) {
	l := broker.NewLeaser(broker.LeaserOptions{})
	l.Submit(broker.Job{ID: "j"})
	w := l.Register("p", nil, nil)
	if err := l.Drain(w.ID); err != nil {
		t.Fatal(err)
	}
	if ls, _ := l.TryLease(w.ID); ls != nil {
		t.Fatal("draining worker got a lease")
	}
}

func TestAuth(t *testing.T) {
	srv, _, toks := plane(t, time.Second, "default", "other")
	do := func(method, path, tok string) int {
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(`{}`))
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if c := do("POST", "/v1/workers", ""); c != 401 {
		t.Fatalf("no token: %d", c)
	}
	if c := do("POST", "/v1/workers", "wrong"); c != 401 {
		t.Fatalf("bad token: %d", c)
	}
	if c := do("POST", "/v1/jobs", toks["default"]); c != 401 {
		t.Fatalf("worker token submitted a job: %d", c)
	}
	if c := do("POST", "/v1/workers", toks[broker.SubmitPool]); c != 401 {
		t.Fatalf("submit token registered a worker: %d", c)
	}
	if c := do("POST", "/v1/workers", toks["default"]); c != 200 {
		t.Fatalf("good token: %d", c)
	}
}

func TestWorkerCannotUseAnotherPoolsWorkerID(t *testing.T) {
	srv, l, toks := plane(t, time.Second, "a", "b")
	w := l.Register("a", nil, nil)
	req, _ := http.NewRequest("POST", srv.URL+"/v1/workers/"+w.ID+"/heartbeat", nil)
	req.Header.Set("Authorization", "Bearer "+toks["b"])
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("got %d", resp.StatusCode)
	}
}

func TestPoolsStoreOnlyHashes(t *testing.T) {
	tok, _ := broker.NewToken()
	h := broker.HashToken(tok)
	if h == tok || len(h) != 64 {
		t.Fatalf("hash %q", h)
	}
	ps := broker.NewPools()
	if err := ps.AddHash("p", "short"); err == nil {
		t.Fatal("a non-hash was accepted")
	}
}

func TestExecRunnerAllowlist(t *testing.T) {
	run := ExecRunner([]string{"echo"})
	var sb strings.Builder
	if _, err := run(context.Background(), broker.Job{Payload: json.RawMessage(`{"argv":["echo","hi"]}`)}, &sb); err != nil || sb.String() != "hi\n" {
		t.Fatalf("echo: %v %q", err, sb.String())
	}
	if _, err := run(context.Background(), broker.Job{Payload: json.RawMessage(`{"argv":["/bin/sh","-c","id"]}`)}, &sb); err == nil {
		t.Fatal("sh was allowed")
	}
}
