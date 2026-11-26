package learn

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// LessonsFile is the file a lessons PR appends to, in the target repo.
const LessonsFile = ".factory/memory/lessons.md"

// Host is the repo host a lessons PR is opened on.
type Host interface {
	// ReadFile returns the file on the base branch; missing means empty.
	ReadFile(ctx context.Context, repo, path string) (string, error)
	// OpenPR commits content to path on a new branch and opens a draft PR. It returns the PR number.
	OpenPR(ctx context.Context, repo, branch, path, content, title, body string) (int, error)
}

// Opener builds the lessons PR for a repo from its unlearned lessons.
type Opener struct {
	Store Store
	Host  Host
	Sink  Sink // optional
	Now   func() time.Time
	OnErr func(Lesson, error) // a sink failure; never fails the PR
}

// Open opens one lessons PR for repo. With no new lessons it does nothing and returns nil.
func (o Opener) Open(ctx context.Context, repo string) error {
	all, err := o.Store.Lessons(repo)
	if err != nil {
		return err
	}
	var fresh []Lesson
	for _, l := range all {
		if !l.Learned {
			fresh = append(fresh, l)
		}
	}
	if len(fresh) == 0 {
		return nil
	}
	cur, err := o.Host.ReadFile(ctx, repo, LessonsFile)
	if err != nil {
		return err
	}
	var sb strings.Builder
	sb.WriteString(strings.TrimRight(cur, "\n"))
	for _, l := range fresh {
		sb.WriteString("\n- " + strings.Join(strings.Fields(l.Text), " "))
	}
	sb.WriteString("\n")
	now := o.Now
	if now == nil {
		now = time.Now
	}
	branch := "factory/learning-" + now().UTC().Format("20060102")
	title := fmt.Sprintf("Lessons from retros (%d)", len(fresh))
	body := "## Summary\n\nAppends " + fmt.Sprint(len(fresh)) + " lesson(s) from finished runs to `" + LessonsFile + "`.\n"
	if _, err := o.Host.OpenPR(ctx, repo, branch, LessonsFile, sb.String(), title, body); err != nil {
		return err
	}
	ids := make([]string, len(fresh))
	for i, l := range fresh {
		ids[i] = l.ID
	}
	if err := o.Store.MarkLearned(repo, ids); err != nil {
		return err
	}
	Notify(ctx, o.Sink, fresh, o.OnErr)
	return nil
}
