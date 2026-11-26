package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"time"

	gogithub "github.com/google/go-github/v79/github"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/KaaS-team/factory/internal/api"
	"github.com/KaaS-team/factory/internal/config"
	"github.com/KaaS-team/factory/internal/learn"
	"github.com/KaaS-team/factory/internal/serve"
)

func init() {
	register("learn", learnCmd, noTool("gap: lessons are exported and opened from the terminal; an MCP tool is owed"))
}

// learnGuardPath remembers the day each repo's lessons PR was opened.
const learnGuardPath = ".factory/state/learn-daily.json"

// learnRepos is the repos a command covers: every registered repo with --all, else the configured one.
func learnRepos(ctx context.Context, c api.Control, cfg config.Config, all bool) ([]string, error) {
	if all {
		rs, err := c.Repos(ctx)
		if err != nil {
			return nil, err
		}
		if len(rs) > 0 {
			return rs, nil
		}
	}
	if cfg.Repo == "" {
		return nil, errors.New("learn: key repo is not set")
	}
	return []string{cfg.Repo}, nil
}

// learnCmd is `factory learn`: open the lessons PR now, or with --export print the lessons as JSONL.
func learnCmd() *cobra.Command {
	var export, all bool
	cmd := &cobra.Command{
		Use:   "learn",
		Short: "Open the lessons PR, or with --export print lessons as JSONL (--all: every registered repo)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			l, err := loadConfig(nil)
			if err != nil {
				return err
			}
			c, err := newControl(l.Config)
			if err != nil {
				return err
			}
			repos, err := learnRepos(cmd.Context(), c, l.Config, all)
			if err != nil {
				return err
			}
			store := learn.Store{Dir: learn.DefaultDir}
			if export {
				_, err := learn.Export(cmd.OutOrStdout(), store, repos)
				return err // reads files only: no database, nothing marked learned
			}
			o, err := learnOpener(l.Config, store, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			var errs []error
			for _, r := range repos {
				if err := o.Open(cmd.Context(), r); err != nil {
					errs = append(errs, fmt.Errorf("%s: %w", r, err))
				}
			}
			return errors.Join(errs...)
		},
	}
	cmd.Flags().BoolVar(&export, "export", false, "print lessons as JSONL instead of opening a PR; writes no database")
	cmd.Flags().BoolVar(&all, "all", false, "cover every registered repo")
	return cmd
}

// learnOpener builds the PR opener, with the optional MCP sink from config.
func learnOpener(cfg config.Config, store learn.Store, errw io.Writer) (learn.Opener, error) {
	g, err := newGitHubConn(cfg)
	if err != nil {
		return learn.Opener{}, err
	}
	client, err := g.client()
	if err != nil {
		return learn.Opener{}, err
	}
	o := learn.Opener{Store: store, Host: ghLessonsHost{c: client, base: cfg.Base}}
	o.OnErr = func(l learn.Lesson, err error) {
		_, _ = fmt.Fprintf(errw, "factory learn: sink failed for lesson %s: %v\n", l.ID, err)
	}
	if cfg.Learn.SinkTool != "" {
		o.Sink = mcpSink{command: cfg.Learn.SinkCommand, tool: cfg.Learn.SinkTool}
	}
	return o, nil
}

// learnTick is the watcher's daily lessons PR: once per UTC day per registered repo. It is wired
// into `factory serve` and never returns an error to the loop; a failure is logged and retried on
// the next poll, so intake is never held up.
func learnTick(cfg config.Config, c api.Control, errw io.Writer) (func(context.Context, time.Time), error) {
	if cfg.Tracker != "github" {
		return nil, nil // lessons PRs go through the GitHub API only
	}
	o, err := learnOpener(cfg, learn.Store{Dir: learn.DefaultDir}, errw)
	if err != nil {
		return nil, err
	}
	guard, err := serve.OpenFileStore(learnGuardPath)
	if err != nil {
		return nil, err
	}
	d := learn.Daily{Guard: guard, Open: o.Open}
	return func(ctx context.Context, now time.Time) {
		repos, err := learnRepos(ctx, c, cfg, true)
		if err == nil {
			err = d.Tick(ctx, now, repos)
		}
		if err != nil {
			_, _ = fmt.Fprintf(errw, "factory serve: learn: %v\n", err)
		}
	}, nil
}

// ghLessonsHost opens lessons PRs through the GitHub API, so no checkout is needed.
type ghLessonsHost struct {
	c    *gogithub.Client
	base string
}

func splitRepo(repo string) (string, string, error) {
	o, n, ok := strings.Cut(repo, "/")
	if !ok || o == "" || n == "" {
		return "", "", fmt.Errorf("repo must be owner/name, got %q", repo)
	}
	return o, n, nil
}

func (h ghLessonsHost) base_() string {
	if h.base == "" {
		return "main"
	}
	return h.base
}

func (h ghLessonsHost) ReadFile(ctx context.Context, repo, path string) (string, error) {
	o, n, err := splitRepo(repo)
	if err != nil {
		return "", err
	}
	f, _, resp, err := h.c.Repositories.GetContents(ctx, o, n, path, &gogithub.RepositoryContentGetOptions{Ref: h.base_()})
	if resp != nil && resp.StatusCode == http.StatusNotFound {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return f.GetContent()
}

func (h ghLessonsHost) OpenPR(ctx context.Context, repo, branch, path, content, title, body string) (int, error) {
	o, n, err := splitRepo(repo)
	if err != nil {
		return 0, err
	}
	ref, _, err := h.c.Git.GetRef(ctx, o, n, "refs/heads/"+h.base_())
	if err != nil {
		return 0, err
	}
	if _, resp, err := h.c.Git.CreateRef(ctx, o, n, gogithub.CreateRef{Ref: "refs/heads/" + branch, SHA: ref.GetObject().GetSHA()}); err != nil &&
		(resp == nil || resp.StatusCode != http.StatusUnprocessableEntity) { // 422: today's branch exists from an earlier try
		return 0, err
	}
	opts := &gogithub.RepositoryContentFileOptions{Message: gogithub.Ptr(title), Content: []byte(content), Branch: &branch}
	if cur, _, _, err := h.c.Repositories.GetContents(ctx, o, n, path, &gogithub.RepositoryContentGetOptions{Ref: branch}); err == nil && cur != nil {
		opts.SHA = cur.SHA
	}
	if _, _, err := h.c.Repositories.UpdateFile(ctx, o, n, path, opts); err != nil {
		return 0, err
	}
	b := h.base_()
	pr, _, err := h.c.PullRequests.Create(ctx, o, n, &gogithub.NewPullRequest{Title: &title, Head: &branch, Base: &b, Body: &body, Draft: gogithub.Ptr(true)})
	if err != nil {
		return 0, err
	}
	return pr.GetNumber(), nil
}

// mcpSink sends a lesson to one tool of a stdio MCP server, started per lesson.
type mcpSink struct{ command, tool string }

func (s mcpSink) Send(ctx context.Context, l learn.Lesson) error {
	argv := strings.Fields(s.command)
	if len(argv) == 0 {
		return errors.New("learn.sink_tool is set but learn.sink_command is empty")
	}
	cl := mcp.NewClient(&mcp.Implementation{Name: "factory-learn", Version: "1"}, nil)
	cs, err := cl.Connect(ctx, &mcp.CommandTransport{Command: exec.CommandContext(ctx, argv[0], argv[1:]...)}, nil)
	if err != nil {
		return err
	}
	defer func() { _ = cs.Close() }()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: s.tool, Arguments: map[string]any{"id": l.ID, "repo": l.Repo, "text": l.Text}})
	if err != nil {
		return err
	}
	if res.IsError {
		return fmt.Errorf("tool %s reported an error", s.tool)
	}
	return nil
}
