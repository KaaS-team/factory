package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/KaaS-team/factory/internal/backlog"
	"github.com/KaaS-team/factory/internal/mcp"
)

// backlogPort opens the tracker the backlog commands act on; tests replace it with a fake.
var backlogPort = func() (backlog.Port, error) { return planTracker() }

func init() {
	mcp.BacklogRun = func(ctx context.Context, stdin string, args ...string) (string, error) {
		var out strings.Builder
		root := newRoot()
		root.SetOut(&out)
		root.SetErr(&out)
		root.SetIn(strings.NewReader(stdin))
		root.SetArgs(args)
		err := root.ExecuteContext(ctx)
		return out.String(), err
	}
}

// readFrom reads the file a --from flag names; "-" reads the command's stdin.
func readFrom(cmd *cobra.Command, path string) (string, error) {
	var b []byte
	var err error
	if path == "-" {
		b, err = io.ReadAll(cmd.InOrStdin())
	} else {
		b, err = os.ReadFile(path)
	}
	return string(b), err
}

func printDrafts(w io.Writer, ds []backlog.Draft, filed []backlog.Filed, asJSON bool) error {
	if asJSON {
		return json.NewEncoder(w).Encode(map[string]any{"drafts": ds, "filed": filed})
	}
	if filed == nil {
		for _, d := range ds {
			_, _ = fmt.Fprintf(w, "draft: %s\n", d.Title)
		}
		_, err := fmt.Fprintln(w, "nothing filed; pass --approve to file")
		return err
	}
	for _, f := range filed {
		verb := "filed"
		if f.Existed {
			verb = "exists"
		}
		_, _ = fmt.Fprintf(w, "%s #%s %s\n", verb, f.ID, f.Title)
	}
	return nil
}
