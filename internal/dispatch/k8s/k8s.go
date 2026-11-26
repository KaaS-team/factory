// Package k8s is an api.Dispatcher that runs each session in its own sandbox on Kubernetes: a
// Sandbox of kubernetes-sigs/agent-sandbox (Apache-2.0), or a plain Pod when the CRD is absent.
//
// The Kubernetes API is reached with a thin REST client. Commands and files go to the runtime
// the sandbox image serves (the pkg/sandbox/remote shape: exec with streamed output, file put,
// by sandbox name), not through the API server, so no SPDY or websocket client is needed.
//
// A run: create the sandbox (and a NetworkPolicy that leaves only the egress proxy reachable),
// wait for ready, stream the repo in as a tar, write the secrets to an env file, run the agent
// with its output masked, then delete everything, even when the run was cancelled.
package k8s

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/KaaS-team/factory/internal/api"
	"github.com/KaaS-team/factory/internal/dispatch/brief"
	"github.com/KaaS-team/factory/internal/dispatch/local"
	"github.com/KaaS-team/factory/internal/sandbox"
)

// Paths inside the sandbox.
const (
	workdir = "/workspace"
	envFile = "/run/factory/env"
	tarFile = "/tmp/workspace.tar"
)

// ErrUnsupported is returned for what a sandboxed run cannot do.
var ErrUnsupported = errors.New("k8s: not supported by a sandboxed run")

// Config configures the dispatcher. APIURL, Source and RuntimeURL are required.
type Config struct {
	APIURL    string       // Kubernetes API server
	Token     string       // its bearer token
	Namespace string       // default "factory-runs"
	HTTP      *http.Client // for the API and the runtime; default http.DefaultClient

	// Source returns the directory holding the repo checkout for a brief.
	Source func(ctx context.Context, b api.Brief) (string, error)
	// RuntimeURL is where the sandbox called name serves its runtime.
	RuntimeURL   func(name string) string
	RuntimeToken string

	DefaultImage string // used when the repo has no environment.toml
	Cache        *sandbox.Cache
	// Commit saves a set-up sandbox as an image and returns its reference. Without it setup runs
	// every time and nothing is cached.
	Commit func(ctx context.Context, name string, env sandbox.Environment) (string, error)

	// Secrets returns the value of a secret by the name environment.toml lists.
	Secrets func(name string) (string, bool)
	// ProxyURL is the egress proxy the sandbox must use; ProxyCIDR and ProxyPort are what its
	// NetworkPolicy leaves open. With ProxyURL empty no proxy or policy is set.
	ProxyURL  string
	ProxyCIDR string
	ProxyPort int

	Presets       map[string]local.Preset // default: claude
	Logs          func(key string) io.Writer
	MaxConcurrent int // default 4
	Now           func() time.Time
}

// Dispatcher implements api.Dispatcher over Kubernetes sandboxes.
type Dispatcher struct {
	ctx context.Context
	cfg Config
	k   *kube

	mu   sync.Mutex
	runs map[string]*run
	crd  *bool // discovered once
}

type run struct {
	cancel context.CancelFunc
	done   chan struct{}
	err    error // set before done closes
	start  time.Time
	end    time.Time
}

// New returns a Dispatcher. Cancelling ctx stops every run and tears its sandbox down.
func New(ctx context.Context, cfg Config) (*Dispatcher, error) {
	if cfg.APIURL == "" || cfg.Source == nil || cfg.RuntimeURL == nil {
		return nil, errors.New("k8s: APIURL, Source and RuntimeURL are required")
	}
	if cfg.ProxyURL != "" && (cfg.ProxyCIDR == "" || cfg.ProxyPort == 0) {
		return nil, errors.New("k8s: ProxyURL needs ProxyCIDR and ProxyPort")
	}
	if cfg.Namespace == "" {
		cfg.Namespace = "factory-runs"
	}
	if cfg.HTTP == nil {
		cfg.HTTP = http.DefaultClient
	}
	if cfg.Presets == nil {
		cfg.Presets = map[string]local.Preset{"claude": local.ClaudePreset}
	}
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = 4
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Cache == nil {
		cfg.Cache, _ = sandbox.OpenCache("")
	}
	k := &kube{base: cfg.APIURL, token: cfg.Token, ns: cfg.Namespace, hc: cfg.HTTP}
	return &Dispatcher{ctx: ctx, cfg: cfg, k: k, runs: map[string]*run{}}, nil
}

// Name returns "k8s".
func (d *Dispatcher) Name() string { return "k8s" }

// Caps says the runs are remote and cannot push workflow files.
func (d *Dispatcher) Caps() api.Caps {
	return api.Caps{MaxConcurrent: d.cfg.MaxConcurrent, Remote: true}
}

var notDNS = regexp.MustCompile(`[^a-z0-9-]+`)

// objectName makes a DNS-1123 label from the session key.
func objectName(key string) string {
	n := strings.Trim(notDNS.ReplaceAllString(strings.ToLower(key), "-"), "-")
	if len(n) > 63 {
		n = strings.Trim(n[:63], "-")
	}
	return n
}

// Dispatch starts the run and returns at once. A second Dispatch of the same key returns the
// same session.
func (d *Dispatcher) Dispatch(ctx context.Context, b api.Brief) (api.Session, error) {
	name, err := brief.Name(b.Ticket, b.Attempt)
	if err != nil {
		return api.Session{}, err
	}
	if b.Name != name {
		return api.Session{}, fmt.Errorf("k8s: brief is named %q, want %q", b.Name, name)
	}
	agent := b.Agent
	if agent == "" {
		agent = "claude"
	}
	preset, ok := d.cfg.Presets[agent]
	if !ok {
		return api.Session{}, fmt.Errorf("k8s: no preset named %q", agent)
	}
	key := fmt.Sprintf("factory-%s-a%d", b.Ticket, b.Attempt)
	sess := func(t time.Time) api.Session {
		return api.Session{Key: key, ID: key, Ticket: b.Ticket, Attempt: b.Attempt, Dispatcher: d.Name(), StartedAt: t}
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	if r, ok := d.runs[key]; ok {
		return sess(r.start), nil
	}
	live := 0
	for _, r := range d.runs {
		select {
		case <-r.done:
		default:
			live++
		}
	}
	if live >= d.cfg.MaxConcurrent {
		return api.Session{}, fmt.Errorf("k8s: %d runs are already live", live)
	}
	rctx, cancel := context.WithCancel(d.ctx)
	r := &run{cancel: cancel, done: make(chan struct{}), start: d.cfg.Now()}
	d.runs[key] = r
	go func() {
		defer close(r.done)
		defer cancel()
		r.err = d.execute(rctx, b, key, preset)
		r.end = d.cfg.Now()
	}()
	return sess(r.start), nil
}

func (d *Dispatcher) lookup(s api.Session) (*run, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	r, ok := d.runs[s.Key]
	if !ok {
		return nil, fmt.Errorf("k8s: no run %q", s.Key)
	}
	return r, nil
}

// Wait blocks until the run ends and returns its error, or ctx's error if ctx ends first.
func (d *Dispatcher) Wait(ctx context.Context, s api.Session) error {
	r, err := d.lookup(s)
	if err != nil {
		return err
	}
	select {
	case <-r.done:
		return r.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Steer is not supported: the agent has no stdin in the sandbox.
func (d *Dispatcher) Steer(context.Context, api.Session, string) error { return ErrUnsupported }

// Cancel stops the run and waits for its sandbox to be deleted. An unknown session is not an error.
func (d *Dispatcher) Cancel(ctx context.Context, s api.Session) error {
	r, err := d.lookup(s)
	if err != nil {
		return nil
	}
	r.cancel()
	select {
	case <-r.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Usage reports wall time only; the sandbox does not parse agent output for tokens.
func (d *Dispatcher) Usage(_ context.Context, s api.Session) (api.Usage, error) {
	r, err := d.lookup(s)
	if err != nil {
		return api.Usage{}, err
	}
	select {
	case <-r.done:
		return api.Usage{WallTime: r.end.Sub(r.start)}, nil
	default:
		return api.Usage{WallTime: d.cfg.Now().Sub(r.start)}, nil
	}
}

// isCRD reports whether to use Sandbox objects. Only a definite answer is kept: a failed or
// cancelled lookup is an error, never "no CRD".
func (d *Dispatcher) isCRD(ctx context.Context) (bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.crd == nil {
		v, err := d.k.hasCRD(ctx)
		if err != nil {
			return false, err
		}
		d.crd = &v
	}
	return *d.crd, nil
}
