// Package learn carries retro lessons out of the state directory: a daily lessons PR per repo, a
// JSONL export across every registered repo, and an optional sink that gets each new lesson.
package learn

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Lesson is one retro finding worth keeping.
type Lesson struct {
	ID      string    `json:"id"`
	Repo    string    `json:"repo"`
	Text    string    `json:"text"`
	At      time.Time `json:"at"`
	Learned bool      `json:"learned,omitempty"` // true once a lessons PR carries it
}

// Store keeps lessons as one JSONL file per repo under Dir. Reads never write.
type Store struct{ Dir string }

// DefaultDir is where lessons live, relative to the working directory.
const DefaultDir = ".factory/state/lessons"

func fileName(repo string) string {
	return strings.NewReplacer("/", "__", "\\", "__").Replace(repo) + ".jsonl"
}

func (s Store) path(repo string) string { return filepath.Join(s.Dir, fileName(repo)) }

// Lessons reads a repo's lessons, oldest first. A repo with no file has none.
func (s Store) Lessons(repo string) ([]Lesson, error) {
	f, err := os.Open(s.path(repo))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var out []Lesson
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for n := 1; sc.Scan(); n++ {
		if strings.TrimSpace(sc.Text()) == "" {
			continue
		}
		var l Lesson
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			return nil, fmt.Errorf("%s line %d: %w", s.path(repo), n, err)
		}
		l.Repo = repo
		out = append(out, l)
	}
	return out, sc.Err()
}

// Add records a lesson for repo.
func (s Store) Add(l Lesson) error {
	if l.Repo == "" || strings.TrimSpace(l.Text) == "" {
		return errors.New("learn: a lesson needs a repo and text")
	}
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(l)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(s.path(l.Repo), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, werr := f.Write(append(b, '\n'))
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}

// MarkLearned rewrites repo's file with the given ids marked learned.
func (s Store) MarkLearned(repo string, ids []string) error {
	ls, err := s.Lessons(repo)
	if err != nil {
		return err
	}
	set := map[string]bool{}
	for _, id := range ids {
		set[id] = true
	}
	var sb strings.Builder
	for _, l := range ls {
		if set[l.ID] {
			l.Learned = true
		}
		b, err := json.Marshal(l)
		if err != nil {
			return err
		}
		sb.Write(b)
		sb.WriteByte('\n')
	}
	tmp := s.path(repo) + ".tmp"
	if err := os.WriteFile(tmp, []byte(sb.String()), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path(repo))
}

// Source is what Export reads. Store satisfies it.
type Source interface {
	Lessons(repo string) ([]Lesson, error)
}

// Export writes every lesson of every repo to w as one JSONL stream, repos in sorted order. It only
// reads: it opens no database and marks nothing learned. It returns how many lessons it wrote.
func Export(w io.Writer, src Source, repos []string) (int, error) {
	rs := append([]string(nil), repos...)
	sort.Strings(rs)
	enc := json.NewEncoder(w)
	n := 0
	for _, r := range rs {
		ls, err := src.Lessons(r)
		if err != nil {
			return n, fmt.Errorf("learn export %s: %w", r, err)
		}
		for _, l := range ls {
			if err := enc.Encode(l); err != nil {
				return n, err
			}
			n++
		}
	}
	return n, nil
}

// Guard remembers the day each repo's lessons PR was last opened.
type Guard interface {
	Last(name string) (time.Time, bool)
	Set(name string, at time.Time) error
}

// Daily opens each repo's lessons PR at most once per UTC day.
type Daily struct {
	Guard Guard
	Open  func(ctx context.Context, repo string) error
}

func day(t time.Time) string { return t.UTC().Format("2006-01-02") }

// Tick opens the PR for every repo that has not had one today. A failed Open leaves the repo due
// for the next tick, and the other repos still run. The errors come back joined; callers log them
// and carry on, so a failure never stops intake.
func (d Daily) Tick(ctx context.Context, now time.Time, repos []string) error {
	var errs []error
	for _, r := range repos {
		if last, ok := d.Guard.Last(r); ok && day(last) == day(now) {
			continue
		}
		if err := d.Open(ctx, r); err != nil {
			errs = append(errs, fmt.Errorf("lessons PR for %s: %w", r, err))
			continue
		}
		if err := d.Guard.Set(r, now); err != nil {
			errs = append(errs, fmt.Errorf("lessons PR for %s: record: %w", r, err))
		}
	}
	return errors.Join(errs...)
}

// Sink receives each new lesson, for example by calling an MCP tool.
type Sink interface {
	Send(ctx context.Context, l Lesson) error
}

// Notify sends each lesson to sink. A failing send is reported through onErr and does not stop the
// others; the lessons PR never depends on the sink.
func Notify(ctx context.Context, sink Sink, ls []Lesson, onErr func(Lesson, error)) {
	if sink == nil {
		return
	}
	for _, l := range ls {
		if err := sink.Send(ctx, l); err != nil && onErr != nil {
			onErr(l, err)
		}
	}
}
