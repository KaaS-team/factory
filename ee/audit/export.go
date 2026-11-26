//go:build ee

// SPDX-License-Identifier: LicenseRef-KaaS-EE

package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/KaaS-team/factory/ee/contract"
)

// Exporter ships chained events to a SIEM. Every exporter sends the whole event, hashes included,
// so the receiver can run the same check.
type Exporter interface {
	Export(ctx context.Context, events []contract.AuditEvent) error
}

// JSONLFile appends one event per line to Path.
type JSONLFile struct{ Path string }

// Export appends the events.
func (j JSONLFile) Export(_ context.Context, events []contract.AuditEvent) error {
	if err := os.MkdirAll(filepath.Dir(j.Path), 0o750); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Clean(j.Path), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	for _, e := range events {
		line, merr := json.Marshal(e)
		if merr == nil {
			_, merr = f.Write(append(line, '\n'))
		}
		if merr != nil {
			_ = f.Close()
			return merr
		}
	}
	return f.Close()
}

// Webhook posts the events as one JSON array to URL. Token, when set, goes in a Bearer header.
type Webhook struct {
	URL    string
	Token  string
	Client *http.Client
}

// Export posts the events.
func (w Webhook) Export(ctx context.Context, events []contract.AuditEvent) error {
	body, err := json.Marshal(events)
	if err != nil {
		return err
	}
	return post(ctx, w.Client, w.URL, body, "application/json", authorization("Bearer", w.Token))
}

// SplunkHEC posts the events to a Splunk HTTP Event Collector, one envelope per event.
type SplunkHEC struct {
	URL        string // the collector's /services/collector/event endpoint
	Token      string
	SourceType string // default "kaas:audit"
	Client     *http.Client
}

// Export posts the events as concatenated HEC envelopes.
func (s SplunkHEC) Export(ctx context.Context, events []contract.AuditEvent) error {
	st := s.SourceType
	if st == "" {
		st = "kaas:audit"
	}
	var buf bytes.Buffer
	for _, e := range events {
		env := map[string]any{"time": float64(e.Time.UnixMilli()) / 1000, "sourcetype": st, "event": e}
		line, err := json.Marshal(env)
		if err != nil {
			return err
		}
		buf.Write(line)
	}
	return post(ctx, s.Client, s.URL, buf.Bytes(), "application/json", authorization("Splunk", s.Token))
}

func authorization(scheme, token string) string {
	if token == "" {
		return ""
	}
	return scheme + " " + token
}

func post(ctx context.Context, c *http.Client, endpoint string, body []byte, ctype, auth string) error {
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("audit export: %q is not an http(s) URL", endpoint)
	}
	if c == nil {
		c = &http.Client{Timeout: 15 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", ctype)
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := c.Do(req)
	if err != nil {
		return fmt.Errorf("audit export: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("audit export: collector answered %d", resp.StatusCode)
	}
	return nil
}
