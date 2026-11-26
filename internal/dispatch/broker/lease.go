package broker

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

// SubmitPool is the reserved pool name whose token may submit and read jobs.
const SubmitPool = "_submit"

// Errors returned by the worker leasing side.
var (
	ErrUnknownWorker = errors.New("broker: unknown worker")
	ErrLostWorker    = errors.New("broker: worker lease expired")
	ErrStaleLease    = errors.New("broker: lease is not current")
	ErrBadToken      = errors.New("broker: token not accepted")
)

// Pools maps a pool name to the SHA-256 of its token. The token itself is never stored.
type Pools struct {
	mu     sync.RWMutex
	hashes map[string][32]byte
}

// NewPools returns an empty set of pools.
func NewPools() *Pools { return &Pools{hashes: map[string][32]byte{}} }

// HashToken returns the hex SHA-256 stored for a token. Tokens are random, so a plain hash is enough.
func HashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// NewToken returns a fresh random pool token.
func NewToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("broker: token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// AddHash registers a pool by the hex hash of its token.
func (p *Pools) AddHash(pool, hexHash string) error {
	raw, err := hex.DecodeString(hexHash)
	if err != nil || len(raw) != 32 || pool == "" {
		return fmt.Errorf("broker: pool %q needs a 64 character hex token hash", pool)
	}
	var h [32]byte
	copy(h[:], raw)
	p.mu.Lock()
	p.hashes[pool] = h
	p.mu.Unlock()
	return nil
}

// Authenticate returns the pool a token belongs to.
func (p *Pools) Authenticate(token string) (string, error) {
	h := sha256.Sum256([]byte(token))
	p.mu.RLock()
	defer p.mu.RUnlock()
	found := ""
	for name, want := range p.hashes {
		if subtle.ConstantTimeCompare(h[:], want[:]) == 1 {
			found = name
		}
	}
	if found == "" {
		return "", ErrBadToken
	}
	return found, nil
}

// Job is one unit of work. Selector labels and Caps must all be met by the worker that takes it.
type Job struct {
	ID       string            `json:"id"`
	Selector map[string]string `json:"selector,omitempty"` // for example {"pool":"gpu","os":"linux"}
	Caps     []string          `json:"caps,omitempty"`
	Payload  json.RawMessage   `json:"payload,omitempty"`
}

// Job states.
const (
	StateQueued  = "queued"
	StateLeased  = "leased"
	StateDone    = "done"
	StateFailed  = "failed"
	StateExhaust = "failed_lost" // every attempt lost its worker
)

// JobStatus is what the control plane reports for a job.
type JobStatus struct {
	ID       string          `json:"id"`
	State    string          `json:"state"`
	Attempts int             `json:"attempts"`
	Worker   string          `json:"worker,omitempty"`
	Result   json.RawMessage `json:"result,omitempty"`
	Log      []string        `json:"log,omitempty"`
}

// Worker is a registered worker. Labels always carry os, arch and pool.
type Worker struct {
	ID       string
	Pool     string
	Labels   map[string]string
	Caps     []string
	Draining bool
	seen     time.Time
}

// Lease is a worker's hold on a job until Expires. A heartbeat extends it.
type Lease struct {
	ID      string    `json:"lease_id"`
	Job     Job       `json:"job"`
	Expires time.Time `json:"expires"`
}

type jobRec struct {
	job      Job
	state    string
	attempts int
	leaseID  string
	worker   string
	result   json.RawMessage
	log      []string
}

// LeaserOptions tune a Leaser. Zero values mean: 30 second TTL, 3 attempts, the wall clock.
type LeaserOptions struct {
	TTL         time.Duration
	MaxAttempts int
	Now         func() time.Time
	MaxLogLines int // kept per job; zero means 1000
}

// Leaser is the control plane's worker registry and job queue. A lease that is not heartbeat
// before its expiry goes back to the queue, up to MaxAttempts.
type Leaser struct {
	mu      sync.Mutex
	o       LeaserOptions
	workers map[string]*Worker
	jobs    map[string]*jobRec
	order   []string
	leases  map[string]string // lease id to job id
	wake    chan struct{}
	seq     int
}

// NewLeaser returns an empty Leaser.
func NewLeaser(o LeaserOptions) *Leaser {
	if o.TTL <= 0 {
		o.TTL = 30 * time.Second
	}
	if o.MaxAttempts <= 0 {
		o.MaxAttempts = 3
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.MaxLogLines <= 0 {
		o.MaxLogLines = 1000
	}
	return &Leaser{o: o, workers: map[string]*Worker{}, jobs: map[string]*jobRec{}, leases: map[string]string{}, wake: make(chan struct{})}
}

// TTL is how long a lease or worker lives without a heartbeat.
func (l *Leaser) TTL() time.Duration { return l.o.TTL }

func (l *Leaser) nextID(prefix string) string {
	l.seq++
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%s-%d-%s", prefix, l.seq, hex.EncodeToString(b))
}

func (l *Leaser) notify() {
	close(l.wake)
	l.wake = make(chan struct{})
}

// Submit queues a job. An empty ID gets one. A duplicate ID is an error.
func (l *Leaser) Submit(j Job) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if j.ID == "" {
		j.ID = l.nextID("job")
	}
	if _, dup := l.jobs[j.ID]; dup {
		return "", fmt.Errorf("broker: job %q already exists", j.ID)
	}
	l.jobs[j.ID] = &jobRec{job: j, state: StateQueued}
	l.order = append(l.order, j.ID)
	l.notify()
	return j.ID, nil
}

// Register adds a worker to pool. The pool label is forced to the token's pool.
func (l *Leaser) Register(pool string, labels map[string]string, caps []string) *Worker {
	l.mu.Lock()
	defer l.mu.Unlock()
	lb := make(map[string]string, len(labels)+1)
	for k, v := range labels {
		lb[k] = v
	}
	lb["pool"] = pool
	w := &Worker{ID: l.nextID("w"), Pool: pool, Labels: lb, Caps: append([]string(nil), caps...), seen: l.o.Now()}
	l.workers[w.ID] = w
	return w
}

func (l *Leaser) worker(id string) (*Worker, error) {
	w, ok := l.workers[id]
	if !ok {
		return nil, ErrUnknownWorker
	}
	if l.o.Now().Sub(w.seen) > l.o.TTL {
		return nil, ErrLostWorker
	}
	return w, nil
}

// WorkerPool returns the pool a worker registered in.
func (l *Leaser) WorkerPool(id string) (string, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	w, ok := l.workers[id]
	if !ok {
		return "", false
	}
	return w.Pool, true
}

// Heartbeat marks the worker alive and extends its leases. A worker already declared lost gets
// ErrLostWorker and must register again.
func (l *Leaser) Heartbeat(id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	w, err := l.worker(id)
	if err != nil {
		return err
	}
	w.seen = l.o.Now()
	return nil
}

// Drain stops new leases for the worker. Its current lease may finish.
func (l *Leaser) Drain(id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	w, err := l.worker(id)
	if err != nil {
		return err
	}
	w.Draining = true
	return nil
}

func matches(w *Worker, j Job) bool {
	for k, v := range j.Selector {
		if w.Labels[k] != v {
			return false
		}
	}
	have := map[string]bool{}
	for _, c := range w.Caps {
		have[c] = true
	}
	for _, c := range j.Caps {
		if !have[c] {
			return false
		}
	}
	return true
}

// TryLease hands the worker the oldest queued job it matches, or nil.
func (l *Leaser) TryLease(id string) (*Lease, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweepLocked()
	w, err := l.worker(id)
	if err != nil {
		return nil, err
	}
	w.seen = l.o.Now()
	if w.Draining {
		return nil, nil
	}
	for _, jid := range l.order {
		r := l.jobs[jid]
		if r.state != StateQueued || !matches(w, r.job) {
			continue
		}
		r.state, r.worker, r.leaseID = StateLeased, w.ID, l.nextID("l")
		r.attempts++
		l.leases[r.leaseID] = jid
		return &Lease{ID: r.leaseID, Job: r.job, Expires: l.o.Now().Add(l.o.TTL)}, nil
	}
	return nil, nil
}

// Lease waits up to wait for a job the worker matches. It returns nil on timeout.
func (l *Leaser) Lease(ctx context.Context, id string, wait time.Duration) (*Lease, error) {
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	tick := time.NewTicker(l.o.TTL / 4)
	defer tick.Stop()
	for {
		l.mu.Lock()
		ch := l.wake
		l.mu.Unlock()
		ls, err := l.TryLease(id)
		if err != nil || ls != nil {
			return ls, err
		}
		select {
		case <-ch:
		case <-tick.C: // re-check, so an expired lease is re-leased without a Submit
		case <-deadline.C:
			return nil, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// sweepLocked requeues the jobs of workers that stopped heartbeating. Every entry point calls it.
func (l *Leaser) sweepLocked() {
	now := l.o.Now()
	requeued := false
	for _, r := range l.jobs {
		if r.state != StateLeased {
			continue
		}
		w, ok := l.workers[r.worker]
		if ok && now.Sub(w.seen) <= l.o.TTL {
			continue
		}
		delete(l.leases, r.leaseID)
		r.leaseID, r.worker = "", ""
		if r.attempts >= l.o.MaxAttempts {
			r.state = StateExhaust
			continue
		}
		r.state, requeued = StateQueued, true
	}
	if requeued {
		l.notify()
	}
}

func (l *Leaser) held(leaseID string) (*jobRec, error) {
	jid, ok := l.leases[leaseID]
	if !ok {
		return nil, ErrStaleLease
	}
	return l.jobs[jid], nil
}

// AppendLog stores log lines for the lease's job and counts as a heartbeat.
func (l *Leaser) AppendLog(leaseID string, lines ...string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	r, err := l.held(leaseID)
	if err != nil {
		return err
	}
	if w, ok := l.workers[r.worker]; ok {
		w.seen = l.o.Now()
	}
	r.log = append(r.log, lines...)
	if over := len(r.log) - l.o.MaxLogLines; over > 0 {
		r.log = r.log[over:]
	}
	return nil
}

// Complete finishes the job. A lease that was re-leased elsewhere returns ErrStaleLease, so a
// slow worker cannot overwrite the new owner's result.
func (l *Leaser) Complete(leaseID string, ok bool, result json.RawMessage) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweepLocked()
	r, err := l.held(leaseID)
	if err != nil {
		return err
	}
	delete(l.leases, leaseID)
	r.state, r.result, r.leaseID = StateDone, result, ""
	if !ok {
		r.state = StateFailed
	}
	return nil
}

// Status reports a job.
func (l *Leaser) Status(id string) (JobStatus, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweepLocked()
	r, ok := l.jobs[id]
	if !ok {
		return JobStatus{}, false
	}
	return JobStatus{ID: id, State: r.state, Attempts: r.attempts, Worker: r.worker, Result: r.result, Log: append([]string(nil), r.log...)}, true
}
