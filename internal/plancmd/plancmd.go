// Package plancmd is `factory plan`: it turns a goal, an epic or a spec into a ticket DAG, prints the
// waves, and with --approve files the tickets. Approval is the one human gate before work starts.
package plancmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/KaaS-team/factory/internal/api"
	"github.com/KaaS-team/factory/internal/plan"
	"github.com/KaaS-team/factory/internal/plan/intent"
)

// Filer is the narrow write port the command needs; api.Tracker has no way to create a ticket.
type Filer interface {
	// Find returns the id of an existing ticket with this title.
	Find(ctx context.Context, title string) (api.TicketID, bool, error)
	// Create files one ticket and returns its id.
	Create(ctx context.Context, t api.Ticket) (api.TicketID, error)
}

// SubLinker is the optional Filer write that makes a ticket a sub-issue of another: decompose.
type SubLinker interface {
	LinkSub(ctx context.Context, parent, child api.TicketID) error
}

// SpecFunc turns a goal into spec text; it is the model step.
type SpecFunc func(ctx context.Context, goal string) (string, error)

// EpicFunc returns the body of epic issue n.
type EpicFunc func(ctx context.Context, n int) (string, error)

// ErrNoSpecSource is returned by the default SpecFunc until the agent runner is wired.
var ErrNoSpecSource = errors.New("drafting a spec from a goal needs the agent runner, which is not wired yet; pass --spec <file>")

// Option configures NewCommand.
type Option func(*deps)

type deps struct {
	specFor func(dir string) SpecFunc
	spec    SpecFunc
	epic    EpicFunc
	filer   Filer
	ranker  Ranker
}

// WithSpecFunc sets the model step that drafts spec text from a goal.
func WithSpecFunc(f SpecFunc) Option { return func(d *deps) { d.spec = f } }

// WithSpecFactory sets the model step per repository, so --repo-dir decides where the agent reads.
func WithSpecFactory(f func(dir string) SpecFunc) Option { return func(d *deps) { d.specFor = f } }

// WithEpic sets how `--epic <n>` reads the epic body.
func WithEpic(f EpicFunc) Option { return func(d *deps) { d.epic = f } }

// WithFiler sets where --approve files tickets.
func WithFiler(f Filer) Option { return func(d *deps) { d.filer = f } }

// WithRanker sets the tracker `--rank` reads ready tickets from and writes priorities to.
func WithRanker(r Ranker) Option { return func(d *deps) { d.ranker = r } }

// TicketJSON is one ticket in the printed DAG.
type TicketJSON struct {
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	Files   []string `json:"files"`
	Depends []string `json:"depends"`
}

// DAG is what --json prints: the tickets, their waves and any problems.
type DAG struct {
	Spec     string       `json:"spec"`
	Tickets  []TicketJSON `json:"tickets"`
	Waves    [][]string   `json:"waves"`
	Problems []string     `json:"problems"`
	// LongestChain is the longest dependency chain; Warnings holds the planner's chain warning.
	LongestChain int      `json:"longest_chain"`
	Warnings     []string `json:"warnings"`
	// Units lists the tasks of each unit when --slice cut the plan.
	Units []UnitJSON `json:"units,omitempty"`
}

// UnitJSON is one sliced unit: one PR and its tasks in order.
type UnitJSON struct {
	ID    string   `json:"id"`
	Tasks []string `json:"tasks"`
}

// NewCommand returns `factory plan "<goal>" | --epic <n> | --spec <file>` with --approve and --json.
func NewCommand(opts ...Option) *cobra.Command {
	d := &deps{spec: func(context.Context, string) (string, error) { return "", ErrNoSpecSource }}
	for _, o := range opts {
		o(d)
	}
	var specFile, repoDir string
	var epic int
	var approve, asJSON, slice bool
	var rank string
	c := &cobra.Command{
		Use:   "plan [goal]",
		Short: "Turn a goal, epic or spec into a ticket DAG; --approve files it",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if rank != "" {
				if len(args) > 0 || specFile != "" || epic != 0 || slice {
					return errors.New("--rank takes no goal, --spec, --epic or --slice")
				}
				return runRank(ctx, cmd.OutOrStdout(), d.ranker, rank, approve)
			}
			if repoDir != "" {
				if st, err := os.Stat(repoDir); err != nil || !st.IsDir() {
					return fmt.Errorf("--repo-dir %s is not a directory", repoDir)
				}
				if d.specFor != nil {
					d.spec = d.specFor(repoDir)
				}
			}
			text, slug, err := source(ctx, d, args, specFile, epic)
			if err != nil {
				return err
			}
			res, problems, err := build(ctx, text, slug)
			if err != nil {
				return err
			}
			var sliced *plan.Sliced
			if slice && len(problems) == 0 {
				sl, err := plan.Slice(res.Plan, plan.MaxUnitTasks)
				if err != nil {
					return err
				}
				if res.Waves, err = sl.Plan.Waves(); err != nil {
					return err
				}
				res.Plan, sliced = sl.Plan, &sl
			}
			if err := render(cmd.OutOrStdout(), res, problems, asJSON, sliced); err != nil {
				return err
			}
			if !approve {
				return nil
			}
			if len(problems) > 0 {
				return errors.New("refusing to file: the plan has problems")
			}
			if d.filer == nil {
				return errors.New("--approve needs a tracker to file into")
			}
			parent := api.TicketID("")
			if epic != 0 {
				parent = api.TicketID(strconv.Itoa(epic))
			}
			return file(ctx, cmd.OutOrStdout(), d.filer, res.Plan, res.Waves, parent)
		},
	}
	c.Flags().StringVar(&specFile, "spec", "", "read the spec and tickets from `file`")
	c.Flags().IntVar(&epic, "epic", 0, "read the spec and tickets from epic issue `n`")
	c.Flags().StringVar(&repoDir, "repo-dir", "", "the repository the agent reads when drafting from a goal (default: here)")
	c.Flags().BoolVar(&slice, "slice", false, "cut straight dependency chains into units of about 30 minutes, one PR each")
	c.Flags().StringVar(&rank, "rank", "", "rank ready tickets by `method` (rice or wsjf) from their Score line; with --approve write the priorities")
	c.Flags().BoolVar(&approve, "approve", false, "file the tickets (or write the ranks); without it nothing is written")
	c.Flags().BoolVar(&asJSON, "json", false, "print the DAG as JSON")
	return c
}

func source(ctx context.Context, d *deps, args []string, specFile string, epic int) (text, slug string, err error) {
	n := len(args)
	if specFile != "" {
		n++
	}
	if epic != 0 {
		n++
	}
	if n != 1 {
		return "", "", errors.New("give exactly one of a goal, --epic or --spec")
	}
	switch {
	case specFile != "":
		b, err := os.ReadFile(specFile)
		if err != nil {
			return "", "", err
		}
		return string(b), slugOf(specFile), nil
	case epic != 0:
		if d.epic == nil {
			return "", "", errors.New("--epic needs a tracker; pass --spec until one is wired")
		}
		text, err = d.epic(ctx, epic)
		return text, "01-epic-" + strconv.Itoa(epic), err
	}
	text, err = d.spec(ctx, args[0])
	return text, "01-goal", err
}

var slugRe = regexp.MustCompile(`^\d{2}-[a-z0-9]+(?:-[a-z0-9]+)*$`)

// slugOf turns a spec file name into an NN-words slug, prefixing 01- when the stem lacks a number.
func slugOf(path string) string {
	stem := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if slugRe.MatchString(stem) {
		return stem
	}
	words := strings.Trim(regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(strings.ToLower(stem), "-"), "-")
	if words == "" {
		words = "spec"
	}
	return "01-" + words
}

// fixed is the Drafter over text that already holds a spec and its tickets. Tickets follow the spec,
// each opened by a line "--- ticket <key>: <title>" and holding a planned body v1.
type fixed struct {
	slug, text string
}

const ticketMark = "--- ticket "

func (f fixed) split() (spec string, tickets []intent.Draft) {
	text := f.text
	if strings.HasPrefix(text, ticketMark) {
		text = "\n" + text
	}
	spec, rest, found := strings.Cut(text, "\n"+ticketMark)
	if !found {
		return f.text, nil
	}
	for _, blk := range strings.Split(rest, "\n"+ticketMark) {
		head, body, _ := strings.Cut(blk, "\n")
		key, title, _ := strings.Cut(head, ":")
		tickets = append(tickets, intent.Draft{Key: strings.TrimSpace(key), Title: strings.TrimSpace(title), Body: body})
	}
	return spec, tickets
}

func (f fixed) Spec(context.Context, string) (intent.Spec, error) {
	s, _ := f.split()
	return intent.Spec{Slug: f.slug, Markdown: s}, nil
}

func (f fixed) Tickets(context.Context, intent.Spec) ([]intent.Draft, error) {
	_, t := f.split()
	return t, nil
}

// build runs the DAG through intent; a rejected draft comes back as problems, not an error.
func build(ctx context.Context, text, slug string) (intent.Result, []string, error) {
	res, err := intent.Run(ctx, fixed{slug: slug, text: text}, "plan")
	var pe *intent.ProblemsError
	if errors.As(err, &pe) {
		return res, pe.Problems, nil
	}
	return res, nil, err
}

func render(w io.Writer, res intent.Result, problems []string, asJSON bool, sliced *plan.Sliced) error {
	longest := res.Plan.LongestChain()
	var warnings []string
	if warn := plan.ChainWarning(longest); warn != "" && len(problems) == 0 {
		warnings = append(warnings, warn)
	}
	by := map[string]plan.Ticket{}
	for _, t := range res.Plan.Tickets {
		by[t.ID] = t
	}
	if asJSON {
		dag := DAG{Spec: res.Spec.Slug, Waves: res.Waves, Problems: problems, LongestChain: longest, Warnings: warnings}
		if dag.Problems == nil {
			dag.Problems = []string{}
		}
		if dag.Warnings == nil {
			dag.Warnings = []string{}
		}
		if sliced != nil {
			for _, u := range sliced.Units {
				uj := UnitJSON{ID: u.ID}
				for _, k := range u.Tasks {
					uj.Tasks = append(uj.Tasks, k.ID)
				}
				dag.Units = append(dag.Units, uj)
			}
		}
		for _, t := range res.Plan.Tickets {
			dag.Tickets = append(dag.Tickets, TicketJSON{ID: t.ID, Title: t.Title, Files: t.Files, Depends: t.Depends})
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(dag)
	}
	if len(problems) == 0 {
		say(w, "%s", plan.Markdown(res.Plan, res.Waves))
		if sliced != nil {
			say(w, "Sliced %d tickets into %d units; the longest chain went from %d to %d.\n", len(sliced.Units)+extra(sliced), len(sliced.Units), sliced.ChainBefore, sliced.ChainAfter)
		}
		for _, wn := range warnings {
			say(w, "Warning: %s\n", wn)
		}
	}
	if len(problems) > 0 {
		say(w, "Problems:\n")
		for _, p := range problems {
			say(w, "  - %s\n", p)
		}
	}
	return nil
}

// file creates each ticket once, dependencies first, skipping titles the tracker already has.
func file(ctx context.Context, w io.Writer, f Filer, p plan.Plan, waves [][]string, parent api.TicketID) error {
	link, canLink := f.(SubLinker)
	by := map[string]plan.Ticket{}
	for _, t := range p.Tickets {
		by[t.ID] = t
	}
	ids := map[string]api.TicketID{}
	for _, wave := range waves {
		for _, key := range wave {
			t := by[key]
			if id, ok, err := f.Find(ctx, t.Title); err != nil {
				return err
			} else if ok {
				ids[key] = id
				say(w, "exists #%s %s\n", id, t.Title)
				if err := linkSub(ctx, w, link, canLink, parent, id); err != nil {
					return err
				}
				continue
			}
			at := api.Ticket{Title: t.Title, Files: t.Files}
			var refs []string
			for _, dep := range t.Depends {
				at.BlockedBy = append(at.BlockedBy, ids[dep])
				refs = append(refs, "#"+string(ids[dep]))
			}
			at.Body = strings.TrimRight(withOutOfScope(t, p), "\n") + "\n\nFiles: " + strings.Join(t.Files, ", ") + "\n"
			if len(refs) > 0 {
				at.Body += "Blocked by: " + strings.Join(refs, ", ") + "\n"
			}
			id, err := f.Create(ctx, at)
			if err != nil {
				return fmt.Errorf("filing %s: %w", key, err)
			}
			ids[key] = id
			say(w, "filed #%s %s\n", id, t.Title)
			if err := linkSub(ctx, w, link, canLink, parent, id); err != nil {
				return err
			}
		}
	}
	return nil
}

// say writes to a command's output; a failed write to the terminal has nowhere useful to go.
func say(w io.Writer, format string, a ...any) { _, _ = fmt.Fprintf(w, format, a...) }

// linkSub makes child a sub-issue of parent when there is a parent and the filer can link; decompose.
func linkSub(ctx context.Context, w io.Writer, l SubLinker, can bool, parent, child api.TicketID) error {
	if parent == "" || !can {
		return nil
	}
	if err := l.LinkSub(ctx, parent, child); err != nil {
		return fmt.Errorf("linking #%s under #%s: %w", child, parent, err)
	}
	say(w, "linked #%s under #%s\n", child, parent)
	return nil
}

// withOutOfScope renders t's body with an "## Out of scope" section naming the other tickets of the
// plan, unless the body has one: the issue form is Why (the problem), Acceptance and Out of scope.
func withOutOfScope(t plan.Ticket, p plan.Plan) string {
	body := t.Body.Render()
	if _, ok := t.Body.Section("Out of scope"); ok || len(p.Tickets) < 2 {
		return body
	}
	var b strings.Builder
	b.WriteString(strings.TrimRight(body, "\n") + "\n\n## Out of scope\n\nThese belong to other tickets of the same plan:\n\n")
	for _, o := range p.Tickets {
		if o.ID != t.ID {
			fmt.Fprintf(&b, "- %s: %s\n", o.ID, o.Title)
		}
	}
	return b.String()
}

// extra is the number of tickets slicing folded into earlier units.
func extra(s *plan.Sliced) int {
	n := 0
	for _, u := range s.Units {
		n += len(u.Tasks) - 1
	}
	return n
}
