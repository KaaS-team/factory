package learn

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type memGuard map[string]time.Time

func (m memGuard) Last(n string) (time.Time, bool) { t, ok := m[n]; return t, ok }
func (m memGuard) Set(n string, t time.Time) error { m[n] = t; return nil }

func TestDailyOncePerDayPerRepo(t *testing.T) {
	opens := map[string]int{}
	d := Daily{Guard: memGuard{}, Open: func(_ context.Context, r string) error { opens[r]++; return nil }}
	day1 := time.Date(2026, 1, 1, 1, 0, 0, 0, time.UTC)
	repos := []string{"o/a", "o/b"}
	for _, off := range []time.Duration{0, time.Hour, 20 * time.Hour} { // all on Jan 1
		if err := d.Tick(context.Background(), day1.Add(off), repos); err != nil {
			t.Fatal(err)
		}
	}
	if opens["o/a"] != 1 || opens["o/b"] != 1 {
		t.Fatalf("day 1 opens = %v, want one per repo", opens)
	}
	_ = d.Tick(context.Background(), day1.Add(24*time.Hour), repos)
	if opens["o/a"] != 2 || opens["o/b"] != 2 {
		t.Fatalf("day 2 opens = %v, want a second one per repo", opens)
	}
}

func TestDailyFailureRetriesNextPollAndSkipsNoRepo(t *testing.T) {
	fail := true
	opens := map[string]int{}
	d := Daily{Guard: memGuard{}, Open: func(_ context.Context, r string) error {
		opens[r]++
		if r == "o/a" && fail {
			return errors.New("boom")
		}
		return nil
	}}
	now := time.Date(2026, 1, 1, 1, 0, 0, 0, time.UTC)
	err := d.Tick(context.Background(), now, []string{"o/a", "o/b"})
	if err == nil || !strings.Contains(err.Error(), "o/a") {
		t.Fatalf("err = %v, want the o/a failure", err)
	}
	if opens["o/b"] != 1 {
		t.Fatal("a failing repo stopped the next repo")
	}
	fail = false
	if err := d.Tick(context.Background(), now.Add(15*time.Second), []string{"o/a", "o/b"}); err != nil {
		t.Fatal(err)
	}
	if opens["o/a"] != 2 || opens["o/b"] != 1 {
		t.Fatalf("opens = %v, want o/a retried once and o/b not again", opens)
	}
}

func TestExportAllReposOneJSONLWritesNothing(t *testing.T) {
	dir := t.TempDir()
	s := Store{Dir: filepath.Join(dir, "lessons")}
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, l := range []Lesson{{ID: "1", Repo: "o/b", Text: "b one", At: at}, {ID: "2", Repo: "o/a", Text: "a one", At: at}, {ID: "3", Repo: "o/a", Text: "a two", At: at}} {
		if err := s.Add(l); err != nil {
			t.Fatal(err)
		}
	}
	before := snapshot(t, dir)
	var buf bytes.Buffer
	n, err := Export(&buf, s, []string{"o/b", "o/a"})
	if err != nil || n != 3 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	var got []string
	for _, ln := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var l Lesson
		if err := json.Unmarshal([]byte(ln), &l); err != nil {
			t.Fatalf("line %q: %v", ln, err)
		}
		got = append(got, l.Repo+":"+l.ID)
	}
	if strings.Join(got, ",") != "o/a:2,o/a:3,o/b:1" {
		t.Fatalf("got %v", got)
	}
	if after := snapshot(t, dir); after != before {
		t.Fatalf("export changed the state directory:\n%s\n--\n%s", before, after)
	}
}

func snapshot(t *testing.T, dir string) string {
	t.Helper()
	var sb strings.Builder
	_ = filepath.Walk(dir, func(p string, i os.FileInfo, err error) error {
		if err == nil && !i.IsDir() {
			b, _ := os.ReadFile(p)
			sb.WriteString(p + "\n" + string(b) + i.ModTime().String() + "\n")
		}
		return nil
	})
	return sb.String()
}

type fakeHost struct {
	prs     int
	content string
	err     error
}

func (h *fakeHost) ReadFile(context.Context, string, string) (string, error) { return "# L\n", nil }
func (h *fakeHost) OpenPR(_ context.Context, _, _, _, content, _, _ string) (int, error) {
	if h.err != nil {
		return 0, h.err
	}
	h.prs++
	h.content = content
	return h.prs, nil
}

type failSink struct{ calls int }

func (f *failSink) Send(context.Context, Lesson) error { f.calls++; return errors.New("sink down") }

func TestFailingSinkDoesNotFailThePR(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	_ = s.Add(Lesson{ID: "1", Repo: "o/a", Text: "x"})
	_ = s.Add(Lesson{ID: "2", Repo: "o/a", Text: "y"})
	h, sink := &fakeHost{}, &failSink{}
	var reported int
	o := Opener{Store: s, Host: h, Sink: sink, OnErr: func(Lesson, error) { reported++ }}
	if err := o.Open(context.Background(), "o/a"); err != nil {
		t.Fatalf("a failing sink failed the PR: %v", err)
	}
	if h.prs != 1 || !strings.Contains(h.content, "- x\n- y") {
		t.Fatalf("prs=%d content=%q", h.prs, h.content)
	}
	if sink.calls != 2 || reported != 2 {
		t.Fatalf("sink calls=%d reported=%d, want both lessons tried and both failures reported", sink.calls, reported)
	}
	ls, _ := s.Lessons("o/a")
	if !ls[0].Learned || !ls[1].Learned {
		t.Fatal("lessons not marked learned after the PR")
	}
	if err := o.Open(context.Background(), "o/a"); err != nil || h.prs != 1 {
		t.Fatalf("second open made a PR for nothing: prs=%d err=%v", h.prs, err)
	}
}

func TestFailedPRLeavesLessonsUnlearned(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	_ = s.Add(Lesson{ID: "1", Repo: "o/a", Text: "x"})
	sink := &failSink{}
	o := Opener{Store: s, Host: &fakeHost{err: errors.New("403")}, Sink: sink}
	if err := o.Open(context.Background(), "o/a"); err == nil {
		t.Fatal("want the host error")
	}
	if ls, _ := s.Lessons("o/a"); ls[0].Learned || sink.calls != 0 {
		t.Fatal("a failed PR marked lessons learned or fed the sink")
	}
}
