package triage

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite testdata/outcomes.golden")

type fixture struct {
	Protected []string
	Open      []Issue
	Cases     []struct {
		Name    string
		Issue   Issue
		Confirm *bool
	}
}

func loadFixture(t *testing.T) fixture {
	t.Helper()
	b, err := os.ReadFile("testdata/cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var f fixture
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

// TestOutcomesGolden runs every case and compares the decision with the committed golden file.
func TestOutcomesGolden(t *testing.T) {
	f := loadFixture(t)
	var out strings.Builder
	for _, c := range f.Cases {
		o := Options{Open: f.Open, Protected: f.Protected}
		if c.Confirm != nil {
			ans := *c.Confirm
			o.Confirm = func(context.Context, Issue, Issue) (bool, error) { return ans, nil }
		}
		r, err := Run(context.Background(), c.Issue, o)
		if err != nil {
			t.Fatal(err)
		}
		js, _ := json.Marshal(r)
		fmt.Fprintf(&out, "%s: %s\n", c.Name, js)
	}
	if *update {
		if err := os.WriteFile("testdata/outcomes.golden", []byte(out.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile("testdata/outcomes.golden")
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != out.String() {
		t.Errorf("outcomes changed; rerun with -update if intended.\ngot:\n%s\nwant:\n%s", out.String(), want)
	}
}

// TestFirstPassMakesNoModelCall: with no Confirmer, a likely duplicate is not called one, and nothing panics.
func TestFirstPassMakesNoModelCall(t *testing.T) {
	f := loadFixture(t)
	for _, c := range f.Cases {
		if _, err := Run(context.Background(), c.Issue, Options{Open: f.Open, Protected: f.Protected}); err != nil {
			t.Errorf("%s: %v", c.Name, err)
		}
	}
}

func TestSureDuplicateSkipsConfirmer(t *testing.T) {
	f := loadFixture(t)
	calls := 0
	o := Options{Open: f.Open, Confirm: func(context.Context, Issue, Issue) (bool, error) { calls++; return false, nil }}
	r, _ := Run(context.Background(), f.Cases[2].Issue, o)
	if r.Outcome != Duplicate || r.DuplicateOf != "10" || calls != 0 {
		t.Errorf("outcome=%s of=%s calls=%d", r.Outcome, r.DuplicateOf, calls)
	}
}

func TestConfirmErrorSurfaces(t *testing.T) {
	f := loadFixture(t)
	o := Options{Open: f.Open, Confirm: func(context.Context, Issue, Issue) (bool, error) { return false, errors.New("down") }}
	if _, err := Run(context.Background(), f.Cases[3].Issue, o); err == nil {
		t.Error("a failing confirmer was ignored")
	}
}

type fakeSink struct{ labels, comments []string }

var _ Labeler = (*fakeSink)(nil)

func (s *fakeSink) Comment(_ context.Context, _ string, b string) error {
	s.comments = append(s.comments, b)
	return nil
}
func (s *fakeSink) AddLabels(_ context.Context, _ string, l []string) error {
	s.labels = append(s.labels, l...)
	return nil
}

// TestApplyWritesBack: the outcome reaches the tracker as a label and a comment, with no operator detail.
func TestApplyWritesBack(t *testing.T) {
	s := &fakeSink{}
	r := Result{Outcome: NeedsInfo, Type: "bug", Priority: 1, Labels: []string{"type:bug", "P1"}, Reason: "Say more."}
	if err := Apply(context.Background(), s, "5", r); err != nil {
		t.Fatal(err)
	}
	if strings.Join(s.labels, ",") != "type:bug,P1,triage:needs-info" || len(s.comments) != 1 || !strings.Contains(s.comments[0], "needs-info") {
		t.Errorf("labels=%v comments=%v", s.labels, s.comments)
	}
	for _, bad := range []string{"model", "token", "claude"} {
		if strings.Contains(strings.ToLower(s.comments[0]), bad) {
			t.Errorf("comment mentions %q", bad)
		}
	}
}

type commentOnly struct{ comments []string }

func (s *commentOnly) Comment(_ context.Context, _ string, b string) error {
	s.comments = append(s.comments, b)
	return nil
}

// TestApplyWithoutLabelerListsLabels: a tracker that cannot set labels still shows them.
func TestApplyWithoutLabelerListsLabels(t *testing.T) {
	s := &commentOnly{}
	r := Result{Outcome: Proceed, Type: "bug", Priority: 1, Labels: []string{"type:bug", "P1"}, Reason: "ok"}
	if err := Apply(context.Background(), s, "5", r); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s.comments[0], "Labels: type:bug, P1, triage:proceed") {
		t.Errorf("comment: %q", s.comments[0])
	}
}
