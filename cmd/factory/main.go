// Command factory is the KaaS Factory CLI. It builds the cobra command tree: the board and run
// commands over the api.Control contract, plus the plan, pack, eval, audit, MCP and design
// commands mounted from internal packages. People and CI call it as `factory <command>`, and the
// pack and workflow files run it as a subprocess. Each command lives in its own file here and stays
// a thin parse, call and render layer over internal code.
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/KaaS-team/factory/internal/api"
	"github.com/KaaS-team/factory/internal/audit"
	"github.com/KaaS-team/factory/internal/brand"
	"github.com/KaaS-team/factory/internal/tracker/github"
	"github.com/KaaS-team/factory/pkg/sandbox"
)

// version is set at release time with -ldflags "-X main.version=...".
var version = "0.0.0-dev"

// commit and date are set the same way (-X main.commit, -X main.date).
var commit, date = "none", "unknown"

func newRoot() *cobra.Command {
	root := &cobra.Command{
		Use:           "factory",
		Short:         "Plan work into tickets and run them on parallel coding-agent sessions",
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       fmt.Sprintf("%s (commit %s, built %s)", version, commit, date),
	}
	root.SetVersionTemplate("factory {{.Version}}\n" + brand.URL + "\n")
	addRegistered(root)
	return root
}

// planTracker builds the GitHub tracker `factory plan` files tickets into and reads epics from.
func planTracker() (*github.Tracker, error) {
	l, err := loadConfig(nil)
	if err != nil {
		return nil, err
	}
	if l.Config.Tracker != "github" {
		return nil, fmt.Errorf("plan: filing tickets and reading epics needs tracker github, not %q", l.Config.Tracker)
	}
	g, err := newGitHubConn(l.Config)
	if err != nil {
		return nil, err
	}
	return g.tracker()
}

// runSub runs one factory subcommand and returns what it printed.
func runSub(ctx context.Context, args ...string) (string, error) {
	var out bytes.Buffer
	root := newRoot()
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetIn(strings.NewReader(""))
	root.SetArgs(args)
	err := root.ExecuteContext(ctx)
	return out.String(), err
}

// attach builds the Control when cmd runs and hands it to the package through with.
func attach(cmd *cobra.Command, with func(context.Context, api.Control) context.Context) {
	cmd.PersistentPreRunE = func(c *cobra.Command, _ []string) error {
		// A replay plays a recorded file, so it needs no tracker or live fleet.
		if f := c.Flags().Lookup("replay"); f != nil && f.Value.String() != "" {
			return nil
		}
		ctl, err := control(nil)
		if err != nil {
			return err
		}
		c.SetContext(with(c.Context(), ctl))
		return nil
	}
}

func main() {
	if err := sandbox.MaybeEnter(); err != nil {
		fmt.Fprintln(os.Stderr, "factory:", err)
		os.Exit(1)
	}
	flush := audit.Install(context.Background())
	err := newRoot().Execute()
	flush()
	if err != nil {
		fmt.Fprintln(os.Stderr, "factory:", err)
		var ec interface{ ExitCode() int }
		if errors.As(err, &ec) {
			os.Exit(ec.ExitCode())
		}
		os.Exit(1)
	}
}
