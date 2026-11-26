// Package dashboard is `factory dashboard`: it serves the fleet app (internal/web) on a loopback
// address behind the host guard. Without --allow-actions the app is read-only.
package dashboard

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/KaaS-team/factory/internal/api"
	"github.com/KaaS-team/factory/internal/serve/hostguard"
	"github.com/KaaS-team/factory/internal/web"
)

// DefaultAddr is the listen address of `factory dashboard`: loopback only.
const DefaultAddr = "127.0.0.1:8787"

type ctlKey struct{}

// WithControl returns a context that carries the Control NewCommand serves.
func WithControl(ctx context.Context, ctl api.Control) context.Context {
	return context.WithValue(ctx, ctlKey{}, ctl)
}

// NewCommand returns `factory dashboard`, which serves Handler until its context ends.
// The Control comes from the command context (see WithControl).
func NewCommand() *cobra.Command {
	var addr string
	var operator, allowActions bool
	cmd := &cobra.Command{
		Use:   "dashboard",
		Short: "Serve the fleet web app: board, attention inbox and run timelines",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			ctl, ok := ctx.Value(ctlKey{}).(api.Control)
			if !ok {
				return errors.New("dashboard: no Control in the command context")
			}
			ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", addr)
			if err != nil {
				return err
			}
			srv := &http.Server{Handler: hostguard.New(addr, os.Getenv)(web.Handler(ctl, web.Options{Operator: operator, AllowActions: allowActions})), ReadHeaderTimeout: 10 * time.Second}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "dashboard on http://%s\n", ln.Addr())
			done := make(chan struct{})
			go func() {
				defer close(done)
				<-ctx.Done()
				sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				_ = srv.Shutdown(sctx)
				_ = srv.Close()
			}()
			err = srv.Serve(ln)
			if errors.Is(err, http.ErrServerClosed) {
				<-done
				return nil
			}
			return err
		},
	}
	cmd.Flags().StringVar(&addr, "addr", DefaultAddr, "listen address (loopback by default)")
	cmd.Flags().BoolVar(&operator, "operator", false, "show usage and budget numbers")
	cmd.Flags().BoolVar(&allowActions, "allow-actions", false, "let the local user retry, cancel, steer and resolve attention items")
	return cmd
}
