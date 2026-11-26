//go:build ee

// SPDX-License-Identifier: LicenseRef-KaaS-EE

package main

import (
	"fmt"
	"net/http"
	"os"

	"github.com/spf13/cobra"

	"github.com/KaaS-team/factory/ee/scim"
)

func init() {
	register("scim", scimCmd, noTool("a long-running server an identity provider calls, not an agent"))
}

func scimCmd() *cobra.Command {
	var addr string
	cmd := &cobra.Command{
		Use:   "scim",
		Short: "Serve SCIM 2.0 Users and Groups for an identity provider (enterprise)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			token := os.Getenv("FACTORY_SCIM_TOKEN")
			if token == "" {
				return fmt.Errorf("scim: set FACTORY_SCIM_TOKEN to the bearer token the identity provider sends")
			}
			srv := &http.Server{Addr: addr, Handler: scim.NewHandler(scim.NewMemoryStore(), token)}
			go func() { <-cmd.Context().Done(); _ = srv.Close() }()
			if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				return err
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&addr, "addr", "127.0.0.1:8089", "listen address")
	return cmd
}
