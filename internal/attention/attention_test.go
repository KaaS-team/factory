package attention

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestRaiseDedupesAndPersists(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "a.json")
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := s.Raise(PlanApproval, "T-1", "plan ready", "https://example.test/1")
	b, _ := s.Raise(PlanApproval, "T-1", "plan ready again")
	if a.ID != b.ID {
		t.Fatalf("open duplicate raised twice: %s %s", a.ID, b.ID)
	}
	_, _ = s.Raise(BudgetStop, "T-2", "budget reached")
	s2, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := s2.Open(context.Background())
	if len(got) != 2 || got[0].Ticket != "T-1" {
		t.Fatalf("reloaded items = %+v", got)
	}
}

func TestResolve(t *testing.T) {
	s, _ := Open("")
	it, _ := s.Raise(ExternalFailure, "T-1", "CI outage")
	if err := s.Resolve(it.ID, Approve, "me"); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("approve on external failure: %v", err)
	}
	if err := s.Resolve("zz", Retry, "me"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown id: %v", err)
	}
	if err := s.Resolve(it.ID, Retry, "me"); err != nil {
		t.Fatal(err)
	}
	if open, _ := s.Open(context.Background()); len(open) != 0 {
		t.Fatalf("still open: %+v", open)
	}
	m, _ := s.Raise(MergeApproval, "T-3", "medium risk")
	if s.Approved(MergeApproval, "T-3") {
		t.Fatal("approved before anyone approved")
	}
	_ = s.Resolve(m.ID, Approve, "me")
	if !s.Approved(MergeApproval, "T-3") {
		t.Fatal("approval not seen")
	}
}

type sink struct{ got []string }

func (k *sink) DM(_ context.Context, t string) error { k.got = append(k.got, t); return nil }

func TestDigest(t *testing.T) {
	s, _ := Open("")
	k := &sink{}
	if err := SendDigest(context.Background(), s, k); err != nil || len(k.got) != 0 {
		t.Fatalf("empty store sent %v, err %v", k.got, err)
	}
	_, _ = s.Raise(PolicyRefusal, "T-9", "policy refused the model", "https://example.test/9")
	_ = SendDigest(context.Background(), s, k)
	if len(k.got) != 1 || !strings.Contains(k.got[0], "T-9") || !strings.Contains(k.got[0], "https://example.test/9") {
		t.Fatalf("digest = %q", k.got)
	}
	if Text(nil) == "" {
		t.Fatal("empty text")
	}
}
