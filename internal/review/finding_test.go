package review

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/KaaS-team/factory/internal/reviewlearn"
)

const testDiff = `diff --git a/a.go b/a.go
--- a/a.go
+++ b/a.go
@@ -1,2 +1,3 @@
 package a
+var Bad = 1
 var Old = 2
`

func good() Finding {
	return Finding{Kind: "naming", File: "a.go", Line: 2, Quote: "var Bad", Message: "m", Confidence: 0.9}
}

func TestUncitedFindingIsDropped(t *testing.T) {
	cases := map[string]Finding{
		"file":  {Kind: "k", File: "z.go", Line: 2, Quote: "var Bad", Confidence: 1},
		"line":  {Kind: "k", File: "a.go", Line: 99, Quote: "var Bad", Confidence: 1},
		"quote": {Kind: "k", File: "a.go", Line: 2, Quote: "var Good", Confidence: 1},
		"empty": {Kind: "k", File: "a.go", Line: 2, Confidence: 1},
	}
	for name, f := range cases {
		s := &Station{}
		kept, dropped := s.Process(testDiff, []Finding{f})
		if len(kept) != 0 || len(dropped) != 1 || !strings.Contains(dropped[0].Reason, "citation") {
			t.Errorf("%s: kept=%v dropped=%v", name, kept, dropped)
		}
	}
	s := &Station{}
	if kept, _ := s.Process(testDiff, []Finding{good()}); len(kept) != 1 {
		t.Fatalf("cited finding was dropped")
	}
}

func TestRemovedLinesAreNotCitable(t *testing.T) {
	d := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1,2 +1,1 @@\n-gone\n keep\n"
	if err := IndexDiff(d).Check(Finding{File: "a.go", Line: 1, Quote: "gone"}); err == nil {
		t.Fatal("removed line cited")
	}
}

func TestDedupeAcrossRounds(t *testing.T) {
	s := &Station{}
	s.Process(testDiff, []Finding{good()})
	kept, dropped := s.Process(testDiff, []Finding{good()})
	if len(kept) != 0 || dropped[0].Reason != "duplicate" {
		t.Fatalf("kept=%v dropped=%v", kept, dropped)
	}
}

func TestSelfCritiqueDropsLowConfidence(t *testing.T) {
	f := good()
	f.Confidence = 0.2
	kept, dropped := (&Station{}).Process(testDiff, []Finding{f})
	if len(kept) != 0 || dropped[0].Reason != "low confidence" {
		t.Fatalf("kept=%v dropped=%v", kept, dropped)
	}
}

func TestThreeDismissalsSuppressTheFourth(t *testing.T) {
	l := reviewlearn.Learner{Root: t.TempDir()}
	for i := 0; i < 3; i++ {
		if err := l.Record(reviewlearn.Dismissal{Kind: "Naming", File: "a.go"}); err != nil {
			t.Fatal(err)
		}
		rules, _ := l.Rules()
		kept, _ := (&Station{Rules: rules}).Process(testDiff, []Finding{good()})
		if want := i < 2; (len(kept) == 1) != want {
			t.Fatalf("after %d dismissals kept=%d", i+1, len(kept))
		}
	}
	rules, err := l.Rules()
	if err != nil {
		t.Fatal(err)
	}
	kept, dropped := (&Station{Rules: rules}).Process(testDiff, []Finding{good()})
	if len(kept) != 0 || !strings.Contains(dropped[0].Reason, "learned rule") {
		t.Fatalf("fourth finding kept=%v dropped=%v", kept, dropped)
	}
}

func TestRewordRule(t *testing.T) {
	l := reviewlearn.Learner{Root: t.TempDir()}
	for i := 0; i < 3; i++ {
		_ = l.Record(reviewlearn.Dismissal{Kind: "naming", Reword: "Consider a clearer name."})
	}
	rules, _ := l.Rules()
	kept, _ := (&Station{Rules: rules}).Process(testDiff, []Finding{good()})
	if len(kept) != 1 || kept[0].Message != "Consider a clearer name." {
		t.Fatalf("kept=%v", kept)
	}
}

func TestParseFindings(t *testing.T) {
	fs, err := ParseFindings("here:\n```json\n[{\"kind\":\"k\",\"file\":\"a.go\",\"line\":2,\"quote\":\"q\",\"confidence\":0.7}]\n```")
	if err != nil || len(fs) != 1 || fs[0].Line != 2 {
		t.Fatalf("%v %v", fs, err)
	}
	if _, err := ParseFindings("nothing"); err == nil {
		t.Fatal("want error")
	}
}

type fakeRev struct {
	name string
	fs   []Finding
	err  error
}

func (f fakeRev) Name() string { return f.name }
func (f fakeRev) Review(context.Context, string) ([]Finding, error) {
	return append([]Finding(nil), f.fs...), f.err
}

func TestRoundMergesByCitation(t *testing.T) {
	a, b := good(), good()
	a.Confidence, b.Confidence = 0.6, 0.7
	s := &Station{}
	kept, _, err := s.Round(context.Background(), testDiff, fakeRev{name: "one", fs: []Finding{a}}, fakeRev{name: "two", fs: []Finding{b}}, fakeRev{name: "bad", err: errors.New("down")})
	if err != nil || len(kept) != 1 || kept[0].Source != "one+two" || kept[0].Confidence < 0.79 {
		t.Fatalf("kept=%+v err=%v", kept, err)
	}
	if _, _, err := s.Round(context.Background(), testDiff, fakeRev{err: errors.New("x")}); err == nil {
		t.Fatal("primary error lost")
	}
}
