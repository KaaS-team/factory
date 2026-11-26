package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func fixedReview() PlanReview {
	return PlanReview{
		Epic: "Import a PRD as an epic",
		Tickets: []PlanTicket{
			{ID: "T1", Title: "Parse the PRD slices", Wave: 0,
				Why: "A PRD arrives as markdown. The factory needs its title, intro and slices before it can draft tickets.",
				Acceptance: []string{
					"AC-1: a PRD with a title and two slices parses into two slices",
					"AC-2: a PRD with no slices is refused with a message that names the missing section",
				},
				Files: []string{"internal/plan/prd.go", "internal/plan/prd_test.go"}},
			{ID: "T2", Title: "Split oversized slices", Wave: 1, Depends: []string{"T1"},
				Why:        "A slice with more than four criteria is more than 30 minutes of agent work.",
				Acceptance: []string{"AC-1: a slice with six criteria becomes two tickets that run in order"},
				Files:      []string{"internal/plan/prd.go"}},
			{ID: "T3", Title: "Wire --from-prd into factory plan", Wave: 1, Depends: []string{"T1"},
				Why:        "The import is reachable only from Go until the CLI has a flag for it.",
				Acceptance: []string{"AC-1: factory plan --from-prd prd.md prints the waves", "AC-2: --from-prd with --spec is refused"},
				Files:      []string{"cmd/factory/cmd_plan.go"}},
			{ID: "T4", Title: "Document the PRD format", Wave: 2, Depends: []string{"T2", "T3"},
				Why:        "Product managers write the PRD, so the format needs a page they can read.",
				Acceptance: []string{"AC-1: docs/prd.md shows a PRD that parses"},
				Files:      []string{"docs/prd.md"}},
		},
	}
}

func reviewAt(r PlanReview, w, h int) *ReviewModel {
	m := NewReview(r)
	m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return m
}

func reviewPress(m *ReviewModel, keys ...string) tea.Cmd {
	var cmd tea.Cmd
	for _, k := range keys {
		_, cmd = m.Update(keyMsg(k))
	}
	return cmd
}

func TestGoldenReview(t *testing.T) {
	problems := fixedReview()
	problems.Tickets[3].Acceptance = nil
	problems.Problems = []string{"ticket T4 covers no acceptance criterion"}
	cases := []struct {
		name string
		r    PlanReview
		w, h int
		keys []string
	}{
		{"review_100x30", fixedReview(), 100, 30, nil},
		{"review_second_100x30", fixedReview(), 100, 30, []string{"j"}},
		{"review_60x30", fixedReview(), 60, 30, nil},
		{"review_problems_100x30", problems, 100, 30, []string{"G"}},
		{"review_revise_100x30", fixedReview(), 100, 30, []string{"r", "s", "p", "l", "i", "t"}},
		{"review_empty_100x30", PlanReview{Epic: "Empty", Problems: []string{"there are no tickets"}}, 100, 30, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := reviewAt(tc.r, tc.w, tc.h)
			reviewPress(m, tc.keys...)
			got := strip(m.View().Content)
			checkSize(t, got, tc.w, tc.h)
			goldenCheck(t, "testdata/"+tc.name+".golden", got)
		})
	}
}

// checkSize fails when a view is not exactly w x h cells: a line too wide wraps and breaks the screen.
func checkSize(t *testing.T, view string, w, h int) {
	t.Helper()
	lines := strings.Split(view, "\n")
	if len(lines) != h {
		t.Errorf("view has %d lines, want %d", len(lines), h)
	}
	for i, l := range lines {
		if n := len([]rune(l)); n > w {
			t.Errorf("line %d is %d cells, wider than %d: %q", i+1, n, w, l)
		}
	}
}

func TestReviewApprove(t *testing.T) {
	m := reviewAt(fixedReview(), 100, 30)
	if cmd := reviewPress(m, "a"); cmd != nil {
		t.Fatal("a quit before the confirmation")
	}
	if !strings.Contains(strip(m.View().Content), "Approve and file 4 tickets? y/n") {
		t.Fatalf("no confirmation:\n%s", strip(m.View().Content))
	}
	cmd := reviewPress(m, "y")
	if cmd == nil || cmd() != tea.Quit() {
		t.Fatal("y did not quit")
	}
	if d := m.Decision(); d.Verdict != VerdictApprove || d.Note != "" {
		t.Fatalf("decision = %+v", d)
	}
}

func TestReviewApproveCancel(t *testing.T) {
	m := reviewAt(fixedReview(), 100, 30)
	reviewPress(m, "a", "n")
	if d := m.Decision(); d.Verdict != VerdictNone {
		t.Fatalf("n approved: %+v", d)
	}
	if !strings.Contains(strip(m.View().Content), "approval cancelled") {
		t.Fatal("no cancel note")
	}
}

// A plan with problems cannot be approved, the same rule as `factory plan --approve`.
func TestReviewApproveBlockedByProblems(t *testing.T) {
	r := fixedReview()
	r.Problems = []string{"ticket T4 lists no Files."}
	m := reviewAt(r, 100, 30)
	reviewPress(m, "a")
	if !strings.Contains(strip(m.View().Content), "cannot approve: fix the 1 problem first") {
		t.Fatalf("no reason shown:\n%s", strip(m.View().Content))
	}
	reviewPress(m, "y")
	if d := m.Decision(); d.Verdict != VerdictNone {
		t.Fatalf("approved a plan with problems: %+v", d)
	}
}

func TestReviewRevise(t *testing.T) {
	m := reviewAt(fixedReview(), 100, 30)
	reviewPress(m, "r", "enter")
	if m.Decision().Verdict != VerdictNone {
		t.Fatal("an empty note was sent")
	}
	cmd := reviewPress(m, "m", "e", "r", "g", "e", "enter")
	if cmd == nil || cmd() != tea.Quit() {
		t.Fatal("enter did not quit")
	}
	if d := m.Decision(); d.Verdict != VerdictRevise || d.Note != "merge" {
		t.Fatalf("decision = %+v", d)
	}
}

func TestReviewReviseEscKeepsBrowsing(t *testing.T) {
	m := reviewAt(fixedReview(), 100, 30)
	reviewPress(m, "r", "x", "esc", "j")
	if m.Decision().Verdict != VerdictNone || m.cursor != 1 {
		t.Fatalf("esc did not return to browsing: verdict %v cursor %d", m.Decision().Verdict, m.cursor)
	}
}

func TestReviewQuitWithoutDeciding(t *testing.T) {
	m := reviewAt(fixedReview(), 100, 30)
	if cmd := reviewPress(m, "q"); cmd == nil || cmd() != tea.Quit() {
		t.Fatal("q did not quit")
	}
	if m.Decision().Verdict != VerdictNone {
		t.Fatal("q decided")
	}
}

func TestReviewCursorStaysInRange(t *testing.T) {
	m := reviewAt(fixedReview(), 100, 30)
	reviewPress(m, "k", "k")
	if m.cursor != 0 {
		t.Fatalf("cursor = %d", m.cursor)
	}
	reviewPress(m, "j", "j", "j", "j", "j")
	if m.cursor != 3 {
		t.Fatalf("cursor = %d", m.cursor)
	}
}

// A long ticket scrolls in the detail pane instead of pushing the footer off the screen.
func TestReviewDetailScrolls(t *testing.T) {
	r := fixedReview()
	for range 30 {
		r.Tickets[0].Acceptance = append(r.Tickets[0].Acceptance, "AC-9: one more criterion")
	}
	m := reviewAt(r, 100, 30)
	first := strip(m.View().Content)
	checkSize(t, first, 100, 30)
	if !strings.Contains(first, "more below: pgdn") {
		t.Fatalf("no scroll hint:\n%s", first)
	}
	reviewPress(m, "pgdown")
	if strip(m.View().Content) == first {
		t.Fatal("pgdn did not scroll")
	}
}
