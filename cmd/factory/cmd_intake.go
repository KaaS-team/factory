package main

import (
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/KaaS-team/factory/internal/intake"
)

func init() {
	register("intake", func() *cobra.Command {
		root := &cobra.Command{Use: "intake", Short: "Take work from Slack and Sentry"}
		var addr, store, slackSecretEnv, slackTokenEnv, sentrySecretEnv string
		serve := &cobra.Command{
			Use:   "serve",
			Short: "Serve the Slack events and Sentry webhook endpoints",
			Long: "Secrets are read from the named environment variables, never from flags. A source whose secret " +
				"variable is unset is not served. The HTTP API needs an identity provider and is mounted by the enterprise build.",
			RunE: func(cmd *cobra.Command, _ []string) error {
				st, err := intake.OpenFileStore(store)
				if err != nil {
					return err
				}
				var sl *intake.Slack
				if s := os.Getenv(slackSecretEnv); s != "" {
					sl = &intake.Slack{Secret: s, Store: st}
					if tok := os.Getenv(slackTokenEnv); tok != "" {
						sl.Replier = intake.Poster{Token: tok}
					}
				}
				var se *intake.Sentry
				if s := os.Getenv(sentrySecretEnv); s != "" {
					se = &intake.Sentry{Secret: s, Store: st}
				}
				if sl == nil && se == nil {
					return fmt.Errorf("intake: set %s or %s", slackSecretEnv, sentrySecretEnv)
				}
				srv := &http.Server{Addr: addr, Handler: intake.Mux(nil, sl, se), ReadHeaderTimeout: 10 * time.Second}
				go func() { <-cmd.Context().Done(); _ = srv.Close() }()
				if err := srv.ListenAndServe(); err != http.ErrServerClosed {
					return err
				}
				return nil
			},
		}
		serve.Flags().StringVar(&addr, "addr", "127.0.0.1:8787", "listen address")
		serve.Flags().StringVar(&store, "store", ".factory/intake/tickets.jsonl", "ticket file")
		serve.Flags().StringVar(&slackSecretEnv, "slack-secret-env", "FACTORY_SLACK_SIGNING_SECRET", "env var holding the Slack signing secret")
		serve.Flags().StringVar(&slackTokenEnv, "slack-token-env", "FACTORY_SLACK_BOT_TOKEN", "env var holding the Slack bot token")
		serve.Flags().StringVar(&sentrySecretEnv, "sentry-secret-env", "FACTORY_SENTRY_CLIENT_SECRET", "env var holding the Sentry client secret")
		root.AddCommand(serve)
		return root
	}, noTool("serves webhooks for other systems to call; it is a daemon, not an agent action"))
}
