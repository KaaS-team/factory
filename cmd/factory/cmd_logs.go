package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/KaaS-team/factory/internal/api"
	"github.com/KaaS-team/factory/internal/logs"
	"github.com/KaaS-team/factory/internal/store"
)

func init() {
	register("logs", func() *cobra.Command { return logs.NewCommand(logBackends()) }, noTool("gap: no logs tool yet; a session log may hold operator detail"))
}

// logBackends reads the session the run loop recorded for a ticket and its cloud run log.
// Local sessions have no run log file yet, so they are refused by name.
func logBackends() logs.Backends {
	cloud := func(s api.Session) (logs.Source, error) {
		tok := os.Getenv(envRoutinesToken)
		if tok == "" {
			return nil, fmt.Errorf("logs: env var %s is not set", envRoutinesToken)
		}
		return logs.RoutinesFactory(logs.NewHTTPRunLog(routinesURL(), tok, &http.Client{Timeout: 30 * time.Second}))(s)
	}
	return logs.Backends{
		Latest: func(ctx context.Context, id api.TicketID) (api.Session, error) {
			if _, err := os.Stat(dbPath); errors.Is(err, fs.ErrNotExist) {
				return api.Session{}, logs.ErrNoSession
			}
			st, err := store.Open(dbPath)
			if err != nil {
				return api.Session{}, err
			}
			defer func() { _ = st.Close() }()
			return st.LatestSession(ctx, id)
		},
		Factories: map[string]func(api.Session) (logs.Source, error){"routines": cloud, "fire": cloud},
	}
}
