package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/KaaS-team/factory/internal/api"
	"github.com/KaaS-team/factory/internal/audit"
	"github.com/KaaS-team/factory/internal/plancmd"
	gogithub "github.com/google/go-github/v79/github"
)

var (
	_ plancmd.SubLinker = (*Tracker)(nil)
	_ plancmd.Ranker    = (*Tracker)(nil)
)

// UnrankedPriority is the priority of an issue with no P<n> label: after every ranked one.
const UnrankedPriority = 100

var priorityLabel = regexp.MustCompile(`^P(\d+)$`)

// priorityOf reads the P<n> label, the form triage and `factory plan --rank` write; no label is unranked.
func priorityOf(labels []string) int {
	for _, l := range labels {
		if m := priorityLabel.FindStringSubmatch(l); m != nil {
			if n, err := strconv.Atoi(m[1]); err == nil {
				return n
			}
		}
	}
	return UnrankedPriority
}

// LinkSub makes child a sub-issue of parent. A child already under that parent is left as it is.
func (t *Tracker) LinkSub(ctx context.Context, parent, child api.TicketID) (err error) {
	defer audit.Done(ctx, "tracker.link_sub_issue", string(child), string(child), &err)
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
	_, _, err = t.client.SubIssue.Add(ctx, t.owner, t.repo, int64(pn), gogithub.SubIssueRequest{SubIssueID: is.GetID()})
	var er *gogithub.ErrorResponse
	if errors.As(err, &er) && er.Response != nil && er.Response.StatusCode == http.StatusUnprocessableEntity &&
		strings.Contains(strings.ToLower(er.Message), "already") {
		return nil
	}
	if err != nil {
		return fmt.Errorf("github tracker: add %d under %d: %w", cn, pn, err)
	}
	return nil
}

// SetPriority makes P<priority> the issue's one priority label, so pickup (lower first) follows it.
func (t *Tracker) SetPriority(ctx context.Context, id api.TicketID, priority int) (err error) {
	defer audit.Done(ctx, "tracker.set_priority", string(id), string(id), &err)
	n, err := number(id)
	if err != nil {
		return err
	}
	if priority < 0 {
		return fmt.Errorf("github tracker: priority %d is below 0", priority)
	}
	is, _, err := t.client.Issues.Get(ctx, t.owner, t.repo, n)
	if err != nil {
		return fmt.Errorf("github tracker: read labels of %d: %w", n, err)
	}
	target := "P" + strconv.Itoa(priority)
	has := false
	for _, l := range is.Labels {
		switch name := l.GetName(); {
		case name == target:
			has = true
		case priorityLabel.MatchString(name):
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
