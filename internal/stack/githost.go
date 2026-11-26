package stack

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"

	gogithub "github.com/google/go-github/v79/github"
)

// GitHubHost is the real Host: pull requests through the GitHub API, branches through git in Dir.
// The caller builds the client with the repository's own credentials.
type GitHubHost struct {
	Client      *gogithub.Client
	Owner, Repo string
	Dir         string // a clone of the repository
	Remote      string // default origin
}

var _ Host = GitHubHost{}

func (h GitHubHost) remote() string {
	if h.Remote == "" {
		return "origin"
	}
	return h.Remote
}

func (h GitHubHost) OpenPR(ctx context.Context, head, base, title string) (int, error) {
	pr, _, err := h.Client.PullRequests.Create(ctx, h.Owner, h.Repo, &gogithub.NewPullRequest{Title: &title, Head: &head, Base: &base})
	if err != nil {
		return 0, err
	}
	return pr.GetNumber(), nil
}

func (h GitHubHost) Retarget(ctx context.Context, pr int, base string) error {
	_, _, err := h.Client.PullRequests.Edit(ctx, h.Owner, h.Repo, pr, &gogithub.PullRequest{Base: &gogithub.PullRequestBranch{Ref: &base}})
	return err
}

func (h GitHubHost) Merged(ctx context.Context, pr int) (bool, error) {
	p, _, err := h.Client.PullRequests.Get(ctx, h.Owner, h.Repo, pr)
	if err != nil {
		return false, err
	}
	return p.GetMerged(), nil
}

func (h GitHubHost) git(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = h.Dir
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(errb.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

// Head fetches the branch and returns the remote tip.
func (h GitHubHost) Head(ctx context.Context, branch string) (string, error) {
	if _, err := h.git(ctx, "fetch", "--quiet", h.remote(), branch); err != nil {
		return "", err
	}
	return h.git(ctx, "rev-parse", "FETCH_HEAD")
}

// Rebase replays the commits after oldBase onto the remote tip of onto, then pushes with a lease.
func (h GitHubHost) Rebase(ctx context.Context, branch, onto, oldBase string) (string, error) {
	r := h.remote()
	for _, b := range []string{branch, onto} {
		if _, err := h.git(ctx, "fetch", "--quiet", r, b); err != nil {
			return "", err
		}
	}
	tmp := "refs/factory/stack/" + branch
	if _, err := h.git(ctx, "fetch", "--quiet", r, "+"+branch+":"+tmp); err != nil {
		return "", err
	}
	if _, err := h.git(ctx, "checkout", "--quiet", "--detach", tmp); err != nil {
		return "", err
	}
	if _, err := h.git(ctx, "rebase", "--onto", r+"/"+onto, oldBase); err != nil {
		_, _ = h.git(ctx, "rebase", "--abort")
		return "", err
	}
	sha, err := h.git(ctx, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	old, err := h.git(ctx, "rev-parse", tmp)
	if err != nil {
		return "", err
	}
	if _, err := h.git(ctx, "push", "--quiet", "--force-with-lease="+branch+":"+old, r, "HEAD:refs/heads/"+branch); err != nil {
		return "", err
	}
	return sha, nil
}
