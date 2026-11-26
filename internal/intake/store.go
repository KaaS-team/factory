// Package intake turns a Slack mention, a Sentry alert or an HTTP call into a ticket. Each source
// is an http.Handler that checks who is calling, builds a NewTicket and hands it to a Store. The
// Store makes creation idempotent, so a retried webhook never makes a second ticket.
package intake

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/KaaS-team/factory/ee/contract"
	"github.com/KaaS-team/factory/internal/api"
)

// The ticket sources. Each is a valid contract.LedgerTicket.Source.
const (
	SourceSlack  = "slack"
	SourceSentry = "sentry"
	SourceAPI    = "api"
)

// NewTicket is a request to create work. Key identifies it within Source: a Slack message, a
// Sentry issue id or a caller's idempotency key. Fingerprint, when set, must match on a repeat.
type NewTicket struct {
	Source      string
	Key         string
	Fingerprint string
	Title       string
	Body        string
	URL         string // where the work came from, for links back
}

// Created is a ticket the store holds. New is false when the key was seen before.
type Created struct {
	ID     api.TicketID `json:"id"`
	Source string       `json:"source"`
	Title  string       `json:"title"`
	Body   string       `json:"body"`
	URL    string       `json:"url,omitempty"`
	New    bool         `json:"-"`

	Ledger      contract.LedgerTicket `json:"ledger"` // the run ledger's ticket block, see LedgerTicket
	Key         string                `json:"key"`
	Fingerprint string                `json:"fingerprint,omitempty"`
	At          time.Time             `json:"at"`
}

// ErrConflict means the key was used before for different content.
var ErrConflict = errors.New("intake: key already used for a different request")

// Store creates tickets idempotently.
type Store interface {
	Create(ctx context.Context, n NewTicket) (Created, error)
}

// LedgerTicket is the run ledger's ticket.source block for c. One resolver: every intake path
// writes the source through it.
func LedgerTicket(c Created) contract.LedgerTicket {
	return contract.LedgerTicket{Source: c.Source, ID: string(c.ID), URL: c.URL}
}

// FileStore keeps tickets in memory and, when Path is set, in a JSON lines file that it replays on
// open. IDs are "<source>-<n>".
type FileStore struct {
	mu   sync.Mutex
	path string
	all  []Created
	seen map[string]int
	now  func() time.Time
}

// OpenFileStore loads path (empty means memory only).
func OpenFileStore(path string) (*FileStore, error) {
	s := &FileStore{path: path, seen: map[string]int{}, now: time.Now}
	if path == "" {
		return s, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for _, line := range splitLines(raw) {
		var c Created
		if err := json.Unmarshal(line, &c); err != nil {
			return nil, fmt.Errorf("intake: %s: %w", path, err)
		}
		s.seen[c.Source+"\x00"+c.Key] = len(s.all)
		s.all = append(s.all, c)
	}
	return s, nil
}

func splitLines(b []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, c := range b {
		if c == '\n' {
			if i > start {
				out = append(out, b[start:i])
			}
			start = i + 1
		}
	}
	if start < len(b) {
		out = append(out, b[start:])
	}
	return out
}

// Create implements Store.
func (s *FileStore) Create(_ context.Context, n NewTicket) (Created, error) {
	if n.Source == "" || n.Key == "" || n.Title == "" {
		return Created{}, errors.New("intake: source, key and title are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if i, ok := s.seen[n.Source+"\x00"+n.Key]; ok {
		c := s.all[i]
		if c.Fingerprint != n.Fingerprint {
			return Created{}, ErrConflict
		}
		c.New = false
		return c, nil
	}
	c := Created{
		ID: api.TicketID(fmt.Sprintf("%s-%d", n.Source, countSource(s.all, n.Source)+1)), Source: n.Source,
		Title: n.Title, Body: n.Body, URL: n.URL, Key: n.Key, Fingerprint: n.Fingerprint, At: s.now().UTC(),
	}
	c.Ledger = LedgerTicket(c)
	if s.path != "" {
		line, _ := json.Marshal(c)
		f, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return Created{}, err
		}
		_, werr := f.Write(append(line, '\n'))
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			return Created{}, werr
		}
	}
	s.seen[n.Source+"\x00"+n.Key] = len(s.all)
	s.all = append(s.all, c)
	c.New = true
	return c, nil
}

func countSource(all []Created, src string) int {
	n := 0
	for _, c := range all {
		if c.Source == src {
			n++
		}
	}
	return n
}
