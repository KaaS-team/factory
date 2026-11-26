package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"sync"
	"time"

	"github.com/KaaS-team/factory/internal/dispatch/broker"
)

// Runner runs one job and writes its log to out. A nil error means success.
type Runner func(ctx context.Context, job broker.Job, out io.Writer) (json.RawMessage, error)

// Worker dials the control plane, registers, leases one job at a time, streams its log and
// heartbeats. It opens no listener.
type Worker struct {
	PlaneURL string
	Token    string
	Labels   map[string]string // os and arch are added when absent
	Caps     []string
	Run      Runner
	Client   *http.Client  // nil means a client with no overall timeout (long-poll)
	Beat     time.Duration // heartbeat period; zero means 10 seconds
	Wait     time.Duration // long-poll wait; zero means 20 seconds
}

func (w *Worker) client() *http.Client {
	if w.Client != nil {
		return w.Client
	}
	return &http.Client{}
}

var errGone = errors.New("worker: control plane no longer knows this worker")

func (w *Worker) call(ctx context.Context, path string, in, out any) (int, error) {
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.PlaneURL+path, body)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+w.Token)
	resp, err := w.client().Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return resp.StatusCode, errGone
	case resp.StatusCode >= 300:
		return resp.StatusCode, fmt.Errorf("worker: control plane answered %d", resp.StatusCode)
	case resp.StatusCode == http.StatusOK && out != nil:
		return resp.StatusCode, json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode, nil
}

func (w *Worker) register(ctx context.Context) (string, time.Duration, error) {
	lb := map[string]string{"os": runtime.GOOS, "arch": runtime.GOARCH}
	for k, v := range w.Labels {
		lb[k] = v
	}
	var out struct {
		ID  string `json:"worker_id"`
		TTL int    `json:"ttl_seconds"`
	}
	if _, err := w.call(ctx, "/v1/workers", Register{Labels: lb, Caps: w.Caps}, &out); err != nil {
		return "", 0, err
	}
	return out.ID, time.Duration(out.TTL) * time.Second, nil
}

// Serve runs until ctx ends. Cancelling ctx is the drain: the worker stops taking jobs, lets the
// current job finish (up to drain), then tells the control plane and returns.
func (w *Worker) Serve(ctx context.Context, drain time.Duration) error {
	beat, wait := w.Beat, w.Wait
	if beat <= 0 {
		beat = 10 * time.Second
	}
	if wait <= 0 {
		wait = 20 * time.Second
	}
	id := ""
	for ctx.Err() == nil {
		if id == "" {
			var err error
			if id, _, err = w.register(ctx); err != nil {
				if !sleep(ctx, 2*time.Second) {
					break
				}
				continue
			}
		}
		ls, err := w.poll(ctx, id, wait)
		if errors.Is(err, errGone) {
			id = ""
			continue
		}
		if err != nil || ls == nil {
			if err != nil && !sleep(ctx, time.Second) {
				break
			}
			continue
		}
		w.runLease(ctx, id, *ls, beat, drain)
	}
	if id != "" {
		bg, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = w.call(bg, "/v1/workers/"+id+"/drain", nil, nil)
	}
	return nil
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func (w *Worker) poll(ctx context.Context, id string, wait time.Duration) (*broker.Lease, error) {
	var ls broker.Lease
	code, err := w.call(ctx, fmt.Sprintf("/v1/workers/%s/lease?wait=%s", id, wait), nil, &ls)
	if err != nil || code != http.StatusOK {
		return nil, err
	}
	return &ls, nil
}

// runLease runs the job while heartbeating. The job gets its own context, so SIGTERM lets it
// finish; it is cancelled only when the drain time runs out.
func (w *Worker) runLease(ctx context.Context, id string, ls broker.Lease, beat, drain time.Duration) {
	jobCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	defer cancel()
	go func() {
		select {
		case <-ctx.Done():
			select {
			case <-time.After(drain):
				cancel()
			case <-jobCtx.Done():
			}
		case <-jobCtx.Done():
		}
	}()
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTicker(beat)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				_, _ = w.call(jobCtx, "/v1/workers/"+id+"/heartbeat", nil, nil)
			}
		}
	}()
	lw := &logWriter{w: w, ctx: jobCtx, path: "/v1/leases/" + ls.ID + "/logs"}
	res, err := w.Run(jobCtx, ls.Job, lw)
	lw.flush()
	close(stop)
	wg.Wait()
	done := Complete{OK: err == nil, Result: res}
	if err != nil {
		done.Result, _ = json.Marshal(map[string]string{"error": err.Error()})
	}
	bg, c2 := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer c2()
	_, _ = w.call(bg, "/v1/leases/"+ls.ID+"/complete", done, nil)
}

// logWriter sends each complete line to the control plane.
type logWriter struct {
	w    *Worker
	ctx  context.Context
	path string
	buf  []byte
}

func (l *logWriter) Write(p []byte) (int, error) {
	l.buf = append(l.buf, p...)
	var lines []string
	for {
		i := bytes.IndexByte(l.buf, '\n')
		if i < 0 {
			break
		}
		lines = append(lines, string(l.buf[:i]))
		l.buf = l.buf[i+1:]
	}
	l.send(lines)
	return len(p), nil
}

func (l *logWriter) flush() {
	if len(l.buf) > 0 {
		l.send([]string{string(l.buf)})
		l.buf = nil
	}
}

func (l *logWriter) send(lines []string) {
	if len(lines) > 0 {
		_, _ = l.w.call(l.ctx, l.path, lines, nil)
	}
}
