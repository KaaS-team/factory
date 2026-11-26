// Package onboard is `factory onboard`: the safe first step on a real repo. It detects the repo,
// writes a starter config that turns shadow mode on (the factory records what it would do and
// changes nothing), and lists what the repo is missing. The config text comes from initwiz and the
// readiness checks from firstrun, so neither is written twice.
package onboard

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/KaaS-team/factory/internal/config"
	"github.com/KaaS-team/factory/internal/firstrun"
	"github.com/KaaS-team/factory/internal/initwiz"
)

// Detected is what onboard learns about the repo without running anything.
type Detected struct {
	Repo     string // owner/name from the origin remote; empty when there is none
	Language string // the language of the first marker file found; empty when none
	Marker   string // that marker file
}

// markers maps a file that names a toolchain to its language, most specific first.
var markers = []struct{ file, lang string }{
	{"go.mod", "Go"}, {"package.json", "JavaScript or TypeScript"}, {"pyproject.toml", "Python"},
	{"requirements.txt", "Python"}, {"Cargo.toml", "Rust"}, {"pom.xml", "Java"},
	{"build.gradle", "Java"}, {"Gemfile", "Ruby"}, {"composer.json", "PHP"},
}

// Detect reads dir and its origin remote.
func Detect(dir string) Detected {
	d := Detected{Repo: initwiz.RepoFromOrigin(dir)}
	for _, m := range markers {
		if _, err := os.Stat(filepath.Join(dir, m.file)); err == nil {
			d.Language, d.Marker = m.lang, m.file
			break
		}
	}
	return d
}

// Options configures Run.
type Options struct {
	Dir string
	// DryRun previews the config and writes nothing.
	DryRun bool
	// Force overwrites an existing repo config file.
	Force bool
	// Tier and Tracker pick the starter preset; the defaults are the cautious "solo" and "github".
	Tier, Tracker string
	// Ready configures the readiness checks first-run owns.
	Ready firstrun.ReadyOptions
}

// Report is the result of Run.
type Report struct {
	Detected Detected
	Config   string // the starter config text
	Path     string // where it was written; empty on a dry run
	Ready    firstrun.Ready
}

// Run detects the repo, writes (or previews) the starter shadow config, and checks readiness.
func Run(ctx context.Context, o Options) (Report, error) {
	if o.Dir == "" {
		o.Dir = "."
	}
	if o.Tier == "" {
		o.Tier = "solo"
	}
	if o.Tracker == "" {
		o.Tracker = "github"
	}
	a := initwiz.Answers{Tier: o.Tier, Tracker: o.Tracker, Repo: initwiz.RepoFromOrigin(o.Dir), Shadow: true}
	text, err := initwiz.Render(a)
	if err != nil {
		return Report{}, err
	}
	r := Report{Detected: Detect(o.Dir), Config: string(text)}
	if !o.DryRun {
		if r.Path, err = initwiz.Write(o.Dir, a, o.Force); err != nil {
			return Report{}, err
		}
	}
	o.Ready.Dir = o.Dir
	r.Ready = firstrun.CheckReady(ctx, o.Ready)
	return r, nil
}

// Markdown renders the report.
func (r Report) Markdown() string {
	var b strings.Builder
	b.WriteString("## Onboard\n\n")
	repo := r.Detected.Repo
	if repo == "" {
		repo = "none (no origin remote)"
	}
	lang := r.Detected.Language
	if lang == "" {
		lang = "unknown"
	} else {
		lang += " (" + r.Detected.Marker + ")"
	}
	fmt.Fprintf(&b, "- Repo: %s\n- Language: %s\n", repo, lang)
	if r.Path != "" {
		fmt.Fprintf(&b, "- Wrote %s with shadow mode on\n", r.Path)
	} else {
		fmt.Fprintf(&b, "- Dry run: would write %s with shadow mode on\n", config.RepoFileYAML)
	}
	b.WriteString("\n```yaml\n" + r.Config + "```\n\n")
	b.WriteString("Shadow mode records what the factory would do and creates no branch or pull request. `factory shadow` summarizes it.\n\n")
	b.WriteString(r.Ready.Markdown(nil))
	b.WriteString("\n## Missing\n\n")
	n := 0
	for _, c := range r.Ready.Checks {
		if c.Status == firstrun.Pass {
			continue
		}
		n++
		fmt.Fprintf(&b, "- %s (%s)", c.Name, c.Status)
		if c.Detail != "" {
			b.WriteString(": " + c.Detail)
		}
		b.WriteString("\n")
	}
	if n == 0 {
		b.WriteString("Nothing. Every readiness check passed.\n")
	} else {
		b.WriteString("\n`factory first-run --ready --fix` repairs what can be repaired in place.\n")
	}
	return b.String()
}

// Option configures NewCommand.
type Option func(*cmdOpts)

type cmdOpts struct {
	prot func(context.Context) (firstrun.Protection, error)
	run  firstrun.Runner
}

// WithProtection supplies the branch protection check; f builds it only when onboard runs.
func WithProtection(f func(context.Context) (firstrun.Protection, error)) Option {
	return func(o *cmdOpts) { o.prot = f }
}

// WithRunner replaces the build and test runner of the readiness checks (tests use it).
func WithRunner(r firstrun.Runner) Option { return func(o *cmdOpts) { o.run = r } }

// NewCommand returns `factory onboard`.
func NewCommand(opts ...Option) *cobra.Command {
	var co cmdOpts
	for _, f := range opts {
		f(&co)
	}
	var o Options
	c := &cobra.Command{
		Use:   "onboard",
		Short: "Detect this repo, write a starter shadow config and list what is missing",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			o.Ready.Run = co.run
			if co.prot != nil {
				if p, err := co.prot(cmd.Context()); err == nil {
					o.Ready.Protected = p
				}
			}
			r, err := Run(cmd.Context(), o)
			if err != nil {
				return err
			}
			if _, err := fmt.Fprint(cmd.OutOrStdout(), r.Markdown()); err != nil {
				return err
			}
			if !r.Ready.OK() {
				return errors.New("the repo is not ready for agents")
			}
			return nil
		},
	}
	f := c.Flags()
	f.StringVar(&o.Dir, "dir", ".", "the repository to onboard")
	f.BoolVar(&o.DryRun, "dry-run", false, "show the config and the gaps; write nothing")
	f.BoolVar(&o.Force, "force", false, "overwrite an existing .factory/factory.yaml")
	f.StringVar(&o.Tier, "tier", "solo", "starter tier preset, as in `factory init`")
	f.StringVar(&o.Tracker, "tracker", "github", "issue tracker, as in `factory init`")
	return c
}
