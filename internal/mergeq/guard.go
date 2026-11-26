package mergeq

import (
	"context"
	"fmt"
	"strings"

	"github.com/KaaS-team/factory/ee/contract"
	"github.com/KaaS-team/factory/internal/attention"
	"github.com/KaaS-team/factory/internal/proof"
	"github.com/KaaS-team/factory/internal/risk"
)

// Facts is what the landing guard knows about one queued PR.
type Facts struct {
	Proof     proof.Evidence
	Change    risk.Change
	Protected []string // the charter's protected paths
}

// Guard decides, before a PR goes into a batch, whether it may land. Only a PR with proof and an
// allowed risk class, or a human approval, passes. The rest leave the queue with a reason and an
// attention item.
type Guard struct {
	Facts     func(ctx context.Context, e Entry) (Facts, error)
	Policy    contract.Policy
	Attention *attention.Store
}

// Landing is the result of a guard check.
type Landing struct {
	Land   bool
	Kind   attention.Kind // set when Land is false
	Reason string         // plain English, set when Land is false
}

// Check returns the outcome for e. Proof comes first: no human approval stands in for missing proof.
func (g *Guard) Check(ctx context.Context, e Entry) (Landing, error) {
	f, err := g.Facts(ctx, e)
	if err != nil {
		return Landing{}, fmt.Errorf("facts %s: %w", e.ID, err)
	}
	if r := proof.Check(f.Proof); !r.OK {
		return Landing{Kind: attention.MissingProof, Reason: "Missing proof: " + r.Reason + "."}, nil
	}
	c := f.Change
	c.Protected = f.Protected
	s := risk.Classify(c)
	if risk.AutoMergeAllowed(ctx, g.Policy, s.Class).Allow || g.Attention.Approved(attention.MergeApproval, e.ID) {
		return Landing{Land: true}, nil
	}
	return Landing{Kind: attention.MergeApproval, Reason: fmt.Sprintf("A person must approve this merge: %s risk (%s), and policy does not auto-merge that class.", s.Class, strings.Join(s.Reasons, "; "))}, nil
}

// guardStore filters the queue through the guard.
type guardStore struct {
	Store
	q *CoreQueue
}

func (s guardStore) Queued(ctx context.Context) ([]Entry, error) {
	es, err := s.Store.Queued(ctx)
	if err != nil {
		return nil, err
	}
	g := s.q.d.Guard
	var ok []Entry
	for _, e := range es {
		o, err := g.Check(ctx, e)
		if err != nil {
			return nil, err
		}
		if o.Land {
			ok = append(ok, e)
			continue
		}
		if err := s.q.hold(ctx, e, o); err != nil {
			return nil, err
		}
	}
	return ok, nil
}

// hold takes a PR out of the queue: a comment with the reason, an attention item, phase needs-human.
func (q *CoreQueue) hold(ctx context.Context, e Entry, o Landing) error {
	if _, err := q.d.Guard.Attention.Raise(o.Kind, e.ID, o.Reason); err != nil {
		return err
	}
	return coreStore{q}.hold(ctx, e.ID, o.Reason)
}
