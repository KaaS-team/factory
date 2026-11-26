// Package attention keeps the list of things that need a person: a plan to approve, a failure the
// factory cannot fix, a budget stop, a policy refusal, a review disagreement, a merge to approve.
package attention

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Kind is why a person is needed.
type Kind string

// The kinds.
const (
	PlanApproval       Kind = "plan_approval"
	ExternalFailure    Kind = "external_failure"
	BudgetStop         Kind = "budget_stop"
	PolicyRefusal      Kind = "policy_refusal"
	ReviewDisagreement Kind = "review_disagreement"
	MergeApproval      Kind = "merge_approval"
	MissingProof       Kind = "missing_proof"
)

// Action is what a person may do about an item.
type Action string

// The actions.
const (
	Approve Action = "approve"
	Reject  Action = "reject"
	Retry   Action = "retry"
	Dismiss Action = "dismiss"
)

// Item is one thing that needs a person. Why is plain English and carries no operator detail.
type Item struct {
	ID       string    `json:"id"`
	Kind     Kind      `json:"kind"`
	Ticket   string    `json:"ticket"`
	Why      string    `json:"why"`
	Links    []string  `json:"links,omitempty"`
	Actions  []Action  `json:"actions"`
	Created  time.Time `json:"created"`
	Resolved *Resolved `json:"resolved,omitempty"`
}

// Resolved records how an item was closed.
type Resolved struct {
	Action Action    `json:"action"`
	By     string    `json:"by"`
	At     time.Time `json:"at"`
}

// ErrNotFound is returned for an unknown item id.
var ErrNotFound = errors.New("attention: no such item")

// ErrNotAllowed is returned when an action is not one of the item's allowed actions.
var ErrNotAllowed = errors.New("attention: action not allowed for this item")

// Lister is the read side. Clients reach it through the Control when it implements Lister.
type Lister interface {
	Open(ctx context.Context) ([]Item, error)
}

// Store is a JSON file of items. An empty path keeps them in memory only.
type Store struct {
	mu    sync.Mutex
	path  string
	now   func() time.Time
	items []Item
}

// Open loads the store at path; a missing file is an empty store.
func Open(path string) (*Store, error) {
	s := &Store{path: path, now: time.Now}
	if path == "" {
		return s, nil
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("attention: %w", err)
	}
	if err := json.Unmarshal(b, &s.items); err != nil {
		return nil, fmt.Errorf("attention: read %s: %w", path, err)
	}
	return s, nil
}

// Raise adds an item. An open item with the same kind and ticket is returned unchanged, so a
// repeating cause does not pile up. A kind with no actions gets approve and reject.
func (s *Store) Raise(kind Kind, ticket, why string, links ...string) (Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, it := range s.items {
		if it.Resolved == nil && it.Kind == kind && it.Ticket == ticket {
			return it, nil
		}
	}
	it := Item{
		ID: fmt.Sprintf("a%d", len(s.items)+1), Kind: kind, Ticket: ticket, Why: why, Links: links,
		Actions: actionsFor(kind), Created: s.now().UTC(),
	}
	s.items = append(s.items, it)
	return it, s.save()
}

// Resolve closes an item with one of its allowed actions.
func (s *Store) Resolve(id string, a Action, by string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.items {
		if s.items[i].ID != id {
			continue
		}
		ok := false
		for _, x := range s.items[i].Actions {
			ok = ok || x == a
		}
		if !ok {
			return ErrNotAllowed
		}
		if s.items[i].Resolved == nil {
			s.items[i].Resolved = &Resolved{Action: a, By: by, At: s.now().UTC()}
		}
		return s.save()
	}
	return ErrNotFound
}

// Open returns the unresolved items, oldest first.
func (s *Store) Open(_ context.Context) ([]Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Item
	for _, it := range s.items {
		if it.Resolved == nil {
			out = append(out, it)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
	return out, nil
}

// Approved reports whether a person approved the open-or-closed item of this kind for the ticket.
func (s *Store) Approved(kind Kind, ticket string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, it := range s.items {
		if it.Kind == kind && it.Ticket == ticket && it.Resolved != nil && it.Resolved.Action == Approve {
			return true
		}
	}
	return false
}

func (s *Store) save() error {
	if s.path == "" {
		return nil
	}
	b, err := json.MarshalIndent(s.items, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func actionsFor(k Kind) []Action {
	switch k {
	case ExternalFailure:
		return []Action{Retry, Dismiss}
	case BudgetStop, MissingProof:
		return []Action{Retry, Approve, Dismiss}
	default:
		return []Action{Approve, Reject}
	}
}

// Text renders items for a terminal, the board and the TUI list: one line per item.
func Text(items []Item) string {
	if len(items) == 0 {
		return "Nothing needs you.\n"
	}
	var b strings.Builder
	for _, it := range items {
		fmt.Fprintf(&b, "%s  %s  %s: %s\n", it.ID, it.Ticket, it.Kind, it.Why)
	}
	return b.String()
}

// Digest renders the Slack DM text. It is empty when nothing is open, so no message is sent.
func Digest(items []Item) string {
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d item(s) need you:\n", len(items))
	for _, it := range items {
		fmt.Fprintf(&b, "• %s (%s): %s [%s]", it.Ticket, it.Kind, it.Why, joinActions(it.Actions))
		for _, l := range it.Links {
			b.WriteString(" " + l)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func joinActions(as []Action) string {
	s := make([]string, len(as))
	for i, a := range as {
		s[i] = string(a)
	}
	return strings.Join(s, "/")
}

// Sender delivers a digest as a Slack DM.
type Sender interface {
	DM(ctx context.Context, text string) error
}

// SendDigest sends the digest of the open items; it sends nothing when none are open.
func SendDigest(ctx context.Context, l Lister, s Sender) error {
	items, err := l.Open(ctx)
	if err != nil {
		return err
	}
	if d := Digest(items); d != "" {
		return s.DM(ctx, d)
	}
	return nil
}
