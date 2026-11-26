//go:build ee

// SPDX-License-Identifier: LicenseRef-KaaS-EE

// Package audit is the hash-chained audit log of contract.Audit. Events go to one JSONL file, one
// event per line. Each line carries the hash of the line before it, so an edited, deleted,
// inserted or reordered event is found by Verify.
//
// Deleting the newest events leaves a shorter intact chain. Ship the log to a SIEM (see Exporter)
// to catch that case; a copy off the host is the anchor.
package audit

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/KaaS-team/factory/ee/contract"
)

// Genesis is the PrevHash of Seq 1: 32 zero bytes in hex.
var Genesis = strings.Repeat("0", 2*sha256.Size)

// Canonical returns the canonical form of e, as the contract's AuditEvent comment defines it.
func Canonical(e contract.AuditEvent) []byte {
	data := e.Data
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var d strings.Builder
	d.WriteString("{")
	for i, k := range keys {
		if i > 0 {
			d.WriteString(",")
		}
		d.WriteString(jcsString(k) + ":" + jcsString(data[k]))
	}
	d.WriteString("}")
	t := e.Time.UTC().Truncate(time.Millisecond).Format("2006-01-02T15:04:05.000Z")
	return []byte(`{"action":` + jcsString(string(e.Action)) + `,"actor":` + jcsString(e.Actor) + `,"data":` + d.String() +
		`,"outcome":` + jcsString(string(e.Outcome)) + `,"reason":` + jcsString(string(e.Reason)) +
		`,"resource":` + jcsString(e.Resource) + `,"seq":` + strconv.FormatUint(e.Seq, 10) + `,"time":` + jcsString(t) + `}`)
}

func jcsString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\t':
			b.WriteString(`\t`)
		case '\n':
			b.WriteString(`\n`)
		case '\f':
			b.WriteString(`\f`)
		case '\r':
			b.WriteString(`\r`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

// Hash is sha256(prev || canonical(e)) in lowercase hex, where prev is the raw bytes of prevHex.
func Hash(prevHex string, e contract.AuditEvent) (string, error) {
	prev, err := hex.DecodeString(prevHex)
	if err != nil || len(prev) != sha256.Size {
		return "", fmt.Errorf("bad prev hash %q", prevHex)
	}
	sum := sha256.Sum256(append(prev, Canonical(e)...))
	return hex.EncodeToString(sum[:]), nil
}

// Store is a contract.Audit on one JSONL file. One process writes a file at a time.
type Store struct {
	path string
	mu   sync.Mutex
}

var _ contract.Audit = (*Store)(nil)

// Open returns the store at path. The file is created on the first Record.
func Open(path string) *Store { return &Store{path: path} }

// read returns every stored event in file order. A line that is not an event is an error.
func (s *Store) read() ([]contract.AuditEvent, error) {
	f, err := os.Open(filepath.Clean(s.path))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var out []contract.AuditEvent
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for n := 1; sc.Scan(); n++ {
		if len(bytes.TrimSpace(sc.Bytes())) == 0 {
			continue
		}
		var e contract.AuditEvent
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			return out, fmt.Errorf("%w: line %d is not an event", contract.ErrChainBroken, n)
		}
		out = append(out, e)
	}
	return out, sc.Err()
}

// Record appends e to the chain.
func (s *Store) Record(_ context.Context, e contract.AuditEvent) (contract.AuditEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	last, err := s.tail()
	if err != nil {
		return contract.AuditEvent{}, err
	}
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	e.Time = e.Time.UTC().Truncate(time.Millisecond)
	e.Seq, e.PrevHash = last.Seq+1, Genesis
	if last.Seq > 0 {
		e.PrevHash = last.Hash
	}
	if e.Hash, err = Hash(e.PrevHash, e); err != nil {
		return contract.AuditEvent{}, err
	}
	line, err := json.Marshal(e)
	if err != nil {
		return contract.AuditEvent{}, err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o750); err != nil {
		return contract.AuditEvent{}, err
	}
	f, err := os.OpenFile(filepath.Clean(s.path), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return contract.AuditEvent{}, err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		_ = f.Close()
		return contract.AuditEvent{}, err
	}
	return e, f.Close()
}

// tail returns the last event, or the zero event for an empty log.
func (s *Store) tail() (contract.AuditEvent, error) {
	evs, err := s.read()
	if err != nil || len(evs) == 0 {
		return contract.AuditEvent{}, err
	}
	return evs[len(evs)-1], nil
}

// Verify recomputes the chain from the first event.
func (s *Store) Verify(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	evs, err := s.read()
	prev := Genesis
	for i, e := range evs {
		h, herr := Hash(prev, e)
		if herr != nil || e.Seq != uint64(i)+1 || e.PrevHash != prev || h != e.Hash {
			return fmt.Errorf("%w at seq %d", contract.ErrChainBroken, i+1)
		}
		prev = e.Hash
	}
	return err
}

// Search returns the matching events, oldest first.
func (s *Store) Search(_ context.Context, q contract.AuditQuery) ([]contract.AuditEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	evs, err := s.read()
	if err != nil {
		return nil, err
	}
	var out []contract.AuditEvent
	for _, e := range evs {
		if (q.Actor != "" && e.Actor != q.Actor) || (q.Action != "" && e.Action != q.Action) || (q.Outcome != "" && e.Outcome != q.Outcome) ||
			(!q.Since.IsZero() && e.Time.Before(q.Since)) || (!q.Until.IsZero() && !e.Time.Before(q.Until)) {
			continue
		}
		if out = append(out, e); q.Limit > 0 && len(out) == q.Limit {
			break
		}
	}
	return out, nil
}
