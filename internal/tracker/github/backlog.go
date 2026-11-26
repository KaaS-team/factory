package github

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/KaaS-team/factory/internal/api"
	"github.com/KaaS-team/factory/internal/audit"
	"github.com/KaaS-team/factory/internal/backlog"
	gogithub "github.com/google/go-github/v79/github"
)

var _ backlog.Port = (*Tracker)(nil)

// LabelRankPrefix starts the label that stores an issue's pickup rank, as factory:rank:<n>. Rank 1
// runs first; an issue without one keeps priority 0, as before.
const LabelRankPrefix = "factory:rank:"

// rankOf reads the rank label of an issue, or 0 when it has none or it is malformed.
func rankOf(labels []string) int {
	for _, l := range labels {
		if n, ok := strings.CutPrefix(l, LabelRankPrefix); ok {
			if r, err := strconv.Atoi(n); err == nil && r > 0 {
				return r
			}
		}
	}
	return 0
}

// LinkSub makes child a sub-issue of parent. GitHub names the child by its database id, not its number.
func (t *Tracker) LinkSub(ctx context.Context, parent, child api.TicketID) (err error) {
	defer audit.Done(ctx, "tracker.link_sub_issue", string(parent), string(child), &err)
	pn, err := number(parent)
	if err != nil {
		return err
	}
	cn, err := number(child)
	if err != nil {
		return err
	}
	is, _, err := t.client.Issues.Get(ctx, t.owner, t.repo, cn)
	if err != nil {
		return fmt.Errorf("github tracker: read issue %d: %w", cn, err)
	}
	if _, _, err := t.client.SubIssue.Add(ctx, t.owner, t.repo, int64(pn), gogithub.SubIssueRequest{SubIssueID: is.GetID()}); err != nil {
		return fmt.Errorf("github tracker: link %d under %d: %w", cn, pn, err)
	}
	return nil
}

// SetRank swaps the issue's factory:rank:<n> label for the one that shows rank. Other labels stay.
func (t *Tracker) SetRank(ctx context.Context, id api.TicketID, rank int) (err error) {
	defer audit.Done(ctx, "tracker.set_rank", string(id), string(id), &err)
	if rank < 1 {
		return fmt.Errorf("github tracker: rank %d is not positive", rank)
	}
	n, err := number(id)
	if err != nil {
		return err
	}
	is, _, err := t.client.Issues.Get(ctx, t.owner, t.repo, n)
	if err != nil {
		return fmt.Errorf("github tracker: read labels of %d: %w", n, err)
	}
	target, has := LabelRankPrefix+strconv.Itoa(rank), false
	for _, l := range is.Labels {
		name := l.GetName()
		switch {
		case name == target:
			has = true
		case strings.HasPrefix(name, LabelRankPrefix):
			if _, err := t.client.Issues.RemoveLabelForIssue(ctx, t.owner, t.repo, n, name); err != nil {
				return fmt.Errorf("github tracker: remove %s from %d: %w", name, n, err)
			}
		}
	}
	if !has {
		if _, _, err := t.client.Issues.AddLabelsToIssue(ctx, t.owner, t.repo, n, []string{target}); err != nil {
			return fmt.Errorf("github tracker: add %s to %d: %w", target, n, err)
		}
	}
	return nil
}
