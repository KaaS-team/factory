// Command cidrift prints the drift between the staged ci/ workflows and .github/workflows.
// It exits 1 when the comparison cannot run, and with -strict also when any file has drifted.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/KaaS-team/factory/internal/cidrift"
)

func main() { os.Exit(run(os.Args[1:], ".", os.Stdout, os.Stderr)) }

func run(args []string, root string, out, errw *os.File) int {
	fs := flag.NewFlagSet("cidrift", flag.ContinueOnError)
	fs.SetOutput(errw)
	strict := fs.Bool("strict", false, "exit 1 when a staged workflow has drifted")
	if fs.Parse(args) != nil {
		return 2
	}
	ds, err := cidrift.Check(root)
	if err != nil {
		_, _ = fmt.Fprintln(errw, "cidrift:", err)
		return 1
	}
	for _, d := range ds {
		_, _ = fmt.Fprintln(out, "drift:", d)
	}
	if len(ds) == 0 {
		_, _ = fmt.Fprintln(out, "cidrift: staged ci/ workflows match .github/workflows")
	} else if *strict {
		return 1
	}
	return 0
}
