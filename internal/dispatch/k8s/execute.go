package k8s

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/KaaS-team/factory/internal/api"
	"github.com/KaaS-team/factory/internal/dispatch/brief"
	"github.com/KaaS-team/factory/internal/dispatch/local"
	"github.com/KaaS-team/factory/internal/sandbox"
	"github.com/KaaS-team/factory/pkg/sandbox/remote"
)

const readyTimeout = 5 * time.Minute

// execute runs one session end to end and always deletes what it created.
func (d *Dispatcher) execute(ctx context.Context, b api.Brief, key string, preset local.Preset) (err error) {
	dir, err := d.cfg.Source(ctx, b)
	if err != nil {
		return err
	}
	env, haveEnv, err := sandbox.Load(dir)
	if err != nil {
		return err
	}
	image := d.cfg.DefaultImage
	snapshot := ""
	if haveEnv {
		image = env.Image
		if s, ok := d.cfg.Cache.Get(env.Hash); ok {
			image, snapshot = s, s
		}
	}
	if image == "" {
		return errors.New("k8s: the repo has no .factory/environment.toml and no default image is set")
	}
	secrets, err := d.secretValues(env)
	if err != nil {
		return err
	}

	name := objectName(key)
	labels := map[string]string{"app.kubernetes.io/managed-by": "factory", "factory/run": name}
	crd, err := d.isCRD(ctx)
	if err != nil {
		return err
	}
	obj, pol := d.k.collection(crd), d.k.netpols()
	// A cancelled ctx must not stop the cleanup.
	defer func() {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		errs := []error{err}
		if _, e := d.k.ok(cctx, http.MethodDelete, obj+"/"+name, nil); e != nil {
			errs = append(errs, e)
		}
		if d.cfg.ProxyURL != "" {
			if _, e := d.k.ok(cctx, http.MethodDelete, pol+"/"+name, nil); e != nil {
				errs = append(errs, e)
			}
		}
		err = errors.Join(errs...)
	}()

	if d.cfg.ProxyURL != "" {
		if _, err := d.k.ok(ctx, http.MethodPost, pol, netpolBody(name, labels, d.cfg.ProxyCIDR, d.cfg.ProxyPort)); err != nil {
			return err
		}
	}
	if _, err := d.k.ok(ctx, http.MethodPost, obj, objectBody(crd, name, image, labels)); err != nil {
		return err
	}
	if err := d.waitReady(ctx, crd, obj+"/"+name); err != nil {
		return err
	}

	rt, err := remote.New(d.cfg.RuntimeURL(name), d.cfg.RuntimeToken, d.cfg.HTTP)
	if err != nil {
		return err
	}
	sink := io.Discard
	if d.cfg.Logs != nil {
		sink = d.cfg.Logs(key)
	}
	logs := sandbox.NewRedactor(sink, secrets)
	defer func() { err = errors.Join(err, logs.Close()) }()
	sh := func(script string, argv ...string) error {
		return rt.Exec(ctx, name, append([]string{"sh", "-c", script, "sh"}, argv...), logs, logs)
	}

	if err := rt.CopyIn(ctx, name, tarFile, tarDir(dir)); err != nil {
		return err
	}
	if err := sh(`mkdir -p /run/factory "$1" && tar -xf "$2" -C "$1" && rm -f "$2"`, workdir, tarFile); err != nil {
		return fmt.Errorf("k8s: copy in: %w", err)
	}
	if err := d.writeEnv(ctx, rt, name, env, preset); err != nil {
		return err
	}
	run := `. ` + envFile + `; cd ` + workdir + ` && `
	if haveEnv && snapshot == "" {
		for _, c := range env.Setup {
			if err := sh(run + c); err != nil {
				return fmt.Errorf("k8s: setup %q: %w", c, err)
			}
		}
		if d.cfg.Commit != nil {
			img, err := d.cfg.Commit(ctx, name, env)
			if err != nil {
				return fmt.Errorf("k8s: save snapshot: %w", err)
			}
			if err := d.cfg.Cache.Put(env.Hash, img); err != nil {
				return err
			}
		}
	}
	budget := b.Budget.USD
	if budget <= 0 {
		budget = 5
	}
	argv, err := preset.Argv(local.Run{Brief: b, Prompt: brief.Prompt(b.Name), Stage: "build", Budget: budget})
	if err != nil {
		return err
	}
	return sh(run+`exec "$@"`, argv...)
}

func (d *Dispatcher) waitReady(ctx context.Context, crd bool, path string) error {
	ctx, cancel := context.WithTimeout(ctx, readyTimeout)
	defer cancel()
	for {
		o, err := d.k.ok(ctx, http.MethodGet, path, nil)
		if err != nil {
			return err
		}
		if ready(crd, o) {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("k8s: sandbox was not ready: %w", ctx.Err())
		case <-time.After(time.Second):
		}
	}
}

// secretValues resolves the secrets an environment names. A missing one fails the run, naming it.
func (d *Dispatcher) secretValues(env sandbox.Environment) ([]string, error) {
	var vals []string
	for _, n := range env.Secrets {
		v, ok := d.lookupSecret(n)
		if !ok {
			return nil, fmt.Errorf("k8s: secret %s is not available", n)
		}
		vals = append(vals, v)
	}
	return vals, nil
}

func (d *Dispatcher) lookupSecret(n string) (string, bool) {
	if d.cfg.Secrets == nil {
		return "", false
	}
	return d.cfg.Secrets(n)
}

// writeEnv puts the secrets, the proxy and the preset's env in a file the run sources. The values
// travel in the request body to a file in the sandbox, never on a command line.
func (d *Dispatcher) writeEnv(ctx context.Context, rt *remote.Client, name string, env sandbox.Environment, preset local.Preset) error {
	vars := map[string]string{}
	for k, v := range preset.Env {
		vars[k] = v
	}
	for _, n := range env.Secrets {
		vars[n], _ = d.lookupSecret(n)
	}
	if d.cfg.ProxyURL != "" {
		for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy"} {
			vars[k] = d.cfg.ProxyURL
		}
	}
	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	for _, k := range keys {
		sb.WriteString("export " + k + "='" + strings.ReplaceAll(vars[k], "'", `'\''`) + "'\n")
	}
	return rt.CopyIn(ctx, name, envFile, strings.NewReader(sb.String()))
}
