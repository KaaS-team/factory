//go:build ee

// SPDX-License-Identifier: LicenseRef-KaaS-EE

package audit

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KaaS-team/factory/ee/contract"
	"github.com/KaaS-team/factory/internal/audit"
)

type caseFile struct {
	Audit struct {
		Genesis string                `json:"genesis_prev_hash"`
		Events  []contract.AuditEvent `json:"events"`
		Want    []struct {
			Seq       uint64 `json:"seq"`
			Canonical string `json:"canonical"`
			PrevHash  string `json:"prev_hash"`
			Hash      string `json:"hash"`
		} `json:"want"`
		Tamper struct {
			Seq    int    `json:"seq"`
			Value  string `json:"value"`
			Broken int    `json:"want_broken_seq"`
		} `json:"tamper"`
	} `json:"audit"`
}

func cases(t *testing.T) caseFile {
	t.Helper()
	data, err := os.ReadFile("../contract/testdata/conformance.json")
	if err != nil {
		t.Fatal(err)
	}
	var c caseFile
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

// TestConformance runs the contract's audit-chain cases against the file store, then edits the
// stored row the way the tamper case says and expects Verify to name the same seq.
func TestConformance(t *testing.T) {
	c := cases(t).Audit
	path := filepath.Join(t.TempDir(), "chain.jsonl")
	s := Open(path)
	for i, e := range c.Events {
		got, err := s.Record(t.Context(), e)
		if err != nil {
			t.Fatal(err)
		}
		w := c.Want[i]
		if string(Canonical(got)) != w.Canonical || got.Seq != w.Seq || got.PrevHash != w.PrevHash || got.Hash != w.Hash {
			t.Errorf("seq %d: got %s %s %s", w.Seq, Canonical(got), got.PrevHash, got.Hash)
		}
	}
	if Genesis != c.Genesis {
		t.Errorf("genesis %s, want %s", Genesis, c.Genesis)
	}
	if err := s.Verify(t.Context()); err != nil {
		t.Fatalf("intact chain: %v", err)
	}
	if got, _ := s.Search(t.Context(), contract.AuditQuery{Since: c.Events[1].Time, Until: c.Events[2].Time}); len(got) != 1 || got[0].Seq != 2 {
		t.Errorf("search [event 2, event 3): %+v", got)
	}
	edit(t, path, c.Tamper.Seq, func(e *contract.AuditEvent) { e.Actor = c.Tamper.Value })
	err := Open(path).Verify(t.Context())
	if !errors.Is(err, contract.ErrChainBroken) || !strings.HasSuffix(err.Error(), " seq "+itoa(c.Tamper.Broken)) {
		t.Errorf("tampered chain: %v, want seq %d", err, c.Tamper.Broken)
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

// edit rewrites the stored row at seq with f and leaves the hashes alone.
func edit(t *testing.T, path string, seq int, f func(*contract.AuditEvent)) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	var e contract.AuditEvent
	if err := json.Unmarshal([]byte(lines[seq-1]), &e); err != nil {
		t.Fatal(err)
	}
	f(&e)
	b, _ := json.Marshal(e)
	lines[seq-1] = string(b)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func fill(t *testing.T, n int) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sub", "chain.jsonl")
	s := Open(path)
	for range n {
		if _, err := s.Record(t.Context(), contract.AuditEvent{Actor: "u1", Action: contract.ActionMerge, Resource: "o/r", Outcome: contract.OutcomeAllow}); err != nil {
			t.Fatal(err)
		}
	}
	return s, path
}

func TestDeletedAndReorderedEventsBreakTheChain(t *testing.T) {
	for name, mut := range map[string]func([]string) []string{
		"delete middle": func(l []string) []string { return append(l[:1:1], l[2:]...) },
		"swap":          func(l []string) []string { l[1], l[2] = l[2], l[1]; return l },
		"delete first":  func(l []string) []string { return l[1:] },
	} {
		t.Run(name, func(t *testing.T) {
			s, path := fill(t, 4)
			data, _ := os.ReadFile(path)
			lines := mut(strings.Split(strings.TrimSpace(string(data)), "\n"))
			if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := s.Verify(t.Context()); !errors.Is(err, contract.ErrChainBroken) {
				t.Errorf("got %v", err)
			}
		})
	}
}

func TestGarbageLineBreaksTheChain(t *testing.T) {
	s, path := fill(t, 2)
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	_, _ = f.WriteString("not json\n")
	_ = f.Close()
	if err := s.Verify(t.Context()); !errors.Is(err, contract.ErrChainBroken) {
		t.Errorf("got %v", err)
	}
}

func TestRestartContinuesChainAndConcurrentRecordsAreSerialised(t *testing.T) {
	s, path := fill(t, 3)
	s2 := Open(path)
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if _, err := s2.Record(t.Context(), contract.AuditEvent{Actor: "u2", Action: contract.ActionRunStart, Outcome: contract.OutcomeDeny}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if err := s.Verify(t.Context()); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Search(t.Context(), contract.AuditQuery{Actor: "u2", Action: contract.ActionRunStart, Outcome: contract.OutcomeDeny})
	if len(got) != 20 || got[0].Seq != 4 || got[19].Seq != 23 {
		t.Errorf("got %d events, first %d", len(got), got[0].Seq)
	}
}

func TestSinkMapsCoreRecordsIntoTheChain(t *testing.T) {
	s := Open(filepath.Join(t.TempDir(), "c.jsonl"))
	sink := Sink{Log: s}
	restore := audit.SetSink(sink)
	defer restore()
	audit.Record(context.Background(), "dispatch.start", "o/r#1", "T-1", nil)
	audit.Record(context.Background(), "tracker.comment", "o/r#1", "T-1", errors.New("boom"))
	got, _ := s.Search(t.Context(), contract.AuditQuery{})
	if len(got) != 2 || got[0].Action != contract.ActionRunStart || got[0].Outcome != contract.OutcomeAllow ||
		got[1].Action != "tracker.comment" || got[1].Outcome != contract.OutcomeError {
		t.Errorf("got %+v", got)
	}
	if err := s.Verify(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestExporters(t *testing.T) {
	evs := []contract.AuditEvent{{Seq: 1, Time: time.Unix(1700000000, 0), Actor: "u", Action: contract.ActionMerge, Hash: "h"}}
	var gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
	}))
	defer srv.Close()
	if err := (SplunkHEC{URL: srv.URL, Token: "tok"}).Export(t.Context(), evs); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Splunk tok" || !strings.Contains(gotBody, `"sourcetype":"kaas:audit"`) || !strings.Contains(gotBody, `"time":1700000000`) || !strings.Contains(gotBody, `"hash":"h"`) {
		t.Errorf("hec: %q %q", gotAuth, gotBody)
	}
	if err := (Webhook{URL: srv.URL, Token: "t2"}).Export(t.Context(), evs); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer t2" || !strings.HasPrefix(gotBody, "[{") {
		t.Errorf("webhook: %q %q", gotAuth, gotBody)
	}
	path := filepath.Join(t.TempDir(), "siem.jsonl")
	if err := (JSONLFile{Path: path}).Export(t.Context(), evs); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(path); strings.Count(string(data), "\n") != 1 {
		t.Errorf("file: %q", data)
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) }))
	defer bad.Close()
	if err := (Webhook{URL: bad.URL}).Export(t.Context(), evs); err == nil {
		t.Error("a 500 must be an error")
	}
	if err := (Webhook{URL: "file:///x"}).Export(t.Context(), evs); err == nil {
		t.Error("a non-http URL must be refused")
	}
}
