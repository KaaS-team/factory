package sandbox

import (
	"bytes"
	"io"
	"sort"
	"sync"
)

const redacted = "[redacted]"

// flushAfter is how long a line may grow before the redactor writes its safe prefix.
const flushAfter = 64 << 10

// Redactor is an io.WriteCloser that replaces each secret value with "[redacted]" before it
// reaches w, even when a value is split across writes. Close writes what is left.
type Redactor struct {
	mu   sync.Mutex
	w    io.Writer
	vals [][]byte // longest first
	max  int
	buf  []byte
}

// NewRedactor returns a Redactor over w. Empty values are ignored.
func NewRedactor(w io.Writer, values []string) *Redactor {
	r := &Redactor{w: w}
	for _, v := range values {
		if v != "" {
			r.vals = append(r.vals, []byte(v))
			r.max = max(r.max, len(v))
		}
	}
	sort.Slice(r.vals, func(i, j int) bool { return len(r.vals[i]) > len(r.vals[j]) })
	return r
}

func (r *Redactor) mask(b []byte) []byte {
	for _, v := range r.vals {
		b = bytes.ReplaceAll(b, v, []byte(redacted))
	}
	return b
}

// Write buffers p, and writes every complete line masked. It reports len(p) on success.
func (r *Redactor) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = append(r.buf, p...)
	if i := bytes.LastIndexByte(r.buf, '\n'); i >= 0 {
		if _, err := r.w.Write(r.mask(r.buf[:i+1])); err != nil {
			return 0, err
		}
		r.buf = append([]byte(nil), r.buf[i+1:]...)
	}
	if len(r.buf) > flushAfter {
		// Keep a tail as long as the longest value, so a value cut by the flush is still caught.
		cut := len(r.buf) - r.max
		if _, err := r.w.Write(r.mask(r.buf[:cut])); err != nil {
			return 0, err
		}
		r.buf = append([]byte(nil), r.buf[cut:]...)
	}
	return len(p), nil
}

// Close writes the partial last line, masked.
func (r *Redactor) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, err := r.w.Write(r.mask(r.buf))
	r.buf = nil
	return err
}
