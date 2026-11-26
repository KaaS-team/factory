package main

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/KaaS-team/factory/internal/dispatch/broker"
	"github.com/KaaS-team/factory/internal/worker"
	"github.com/spf13/cobra"
)

func init() {
	register("worker", newWorker, noTool("a long-running process on the customer's compute"))
}

func newWorker() *cobra.Command {
	c := &cobra.Command{Use: "worker", Short: "Run a self-hosted worker, or the plane that leases to it"}
	c.AddCommand(newWorkerRun(), newWorkerPlane(), newWorkerToken())
	return c
}

func newWorkerRun() *cobra.Command {
	var plane, tokenFile string
	var labels, caps, allow []string
	var drain time.Duration
	c := &cobra.Command{
		Use:   "run",
		Short: "Connect outbound to the control plane, lease jobs and run them here",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			tok, err := readSecretFile(tokenFile)
			if err != nil {
				return err
			}
			lb := map[string]string{}
			for _, kv := range labels {
				k, v, ok := strings.Cut(kv, "=")
				if !ok || k == "" || k == "pool" {
					return fmt.Errorf("worker: label %q must be key=value; the pool comes from the token", kv)
				}
				lb[k] = v
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGTERM, os.Interrupt)
			defer stop()
			w := &worker.Worker{PlaneURL: strings.TrimRight(plane, "/"), Token: tok, Labels: lb, Caps: caps, Run: worker.ExecRunner(allow)}
			_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "worker: started, outbound only")
			err = w.Serve(ctx, drain)
			_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "worker: drained")
			return err
		},
	}
	f := c.Flags()
	f.StringVar(&plane, "plane", "", "control plane URL (https)")
	f.StringVar(&tokenFile, "token-file", os.Getenv("KAAS_WORKER_TOKEN_FILE"), "file holding the pool token")
	f.StringSliceVar(&labels, "label", nil, "label key=value (os and arch are added)")
	f.StringSliceVar(&caps, "cap", nil, "capability this worker offers, for example docker")
	f.StringSliceVar(&allow, "allow", []string{"factory"}, "program names a job may run")
	f.DurationVar(&drain, "drain", 5*time.Minute, "how long SIGTERM lets the current job finish")
	_ = c.MarkFlagRequired("plane")
	return c
}

func newWorkerPlane() *cobra.Command {
	var listen, poolsFile string
	var ttl time.Duration
	c := &cobra.Command{
		Use:   "plane",
		Short: "Serve the worker API: registry, leases and re-leasing",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			pools, err := loadPools(poolsFile)
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGTERM, os.Interrupt)
			defer stop()
			h := (&worker.Plane{L: broker.NewLeaser(broker.LeaserOptions{TTL: ttl}), Pools: pools}).Handler()
			srv := &http.Server{Addr: listen, Handler: h, ReadHeaderTimeout: 10 * time.Second}
			go func() {
				<-ctx.Done()
				sd, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				_ = srv.Shutdown(sd)
			}()
			if err := srv.ListenAndServe(); err != http.ErrServerClosed {
				return err
			}
			return nil
		},
	}
	f := c.Flags()
	f.StringVar(&listen, "listen", ":8080", "address to listen on")
	f.StringVar(&poolsFile, "pools", "", "file of `pool sha256hex` lines; hashes only, never tokens")
	f.DurationVar(&ttl, "ttl", 30*time.Second, "lease and worker lifetime without a heartbeat")
	_ = c.MarkFlagRequired("pools")
	return c
}

func newWorkerToken() *cobra.Command {
	return &cobra.Command{
		Use:   "token <pool>",
		Short: "Mint a pool token: the token goes to stdout, the line for --pools to stderr",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			tok, err := broker.NewToken()
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "%s %s\n", args[0], broker.HashToken(tok))
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), tok)
			return nil
		},
	}
}

// readSecretFile returns the trimmed contents of a secret file and refuses one others can read.
func readSecretFile(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("worker: set --token-file or KAAS_WORKER_TOKEN_FILE")
	}
	fi, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("worker: token file: %w", err)
	}
	if fi.Mode().Perm()&0o007 != 0 {
		return "", fmt.Errorf("worker: token file %s must not be readable by others", path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("worker: token file: %w", err)
	}
	if tok := strings.TrimSpace(string(b)); tok != "" {
		return tok, nil
	}
	return "", fmt.Errorf("worker: token file %s is empty", path)
}

func loadPools(path string) (*broker.Pools, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("worker: pools file: %w", err)
	}
	defer func() { _ = f.Close() }()
	p, n := broker.NewPools(), 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, hash, ok := strings.Cut(line, " ")
		if !ok {
			return nil, fmt.Errorf("worker: pools line %q must be `pool hash`", name)
		}
		if err := p.AddHash(name, strings.TrimSpace(hash)); err != nil {
			return nil, err
		}
		n++
	}
	if n == 0 {
		return nil, fmt.Errorf("worker: pools file %s has no pools", path)
	}
	return p, sc.Err()
}
