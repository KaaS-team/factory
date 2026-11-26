package plan

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

const issue636 = "## Problem\n\nDrift.\n\n## Acceptance\n\n- The prompt opens with a goal checklist.\n- AC-7: Verify fails an unchecked row.\n- [ ] Each rule has a test.\n\n## Out of scope\n\n- Not this line.\n"

func TestParseGoalReadsOnlyTheAcceptanceSection(t *testing.T) {
	g := ParseGoal(issue636)
	want := []Row{{"AC-1", "The prompt opens with a goal checklist."}, {"AC-7", "Verify fails an unchecked row."}, {"AC-3", "Each rule has a test."}}
	if fmt.Sprint(g.Rows) != fmt.Sprint(want) {
		t.Fatalf("rows = %v, want %v", g.Rows, want)
	}
	if n := len(ParseGoal("## Problem\n\n- a line\n").Rows); n != 0 {
		t.Fatalf("%d rows from a body with no Acceptance section", n)
	}
}

func TestBuildPromptOpensWithGoalAndCheckpoints(t *testing.T) {
	p, err := BuildPrompt("Title here", issue636)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p, "## Goal\n") {
		t.Fatalf("prompt does not open with the goal: %.40q", p)
	}
	if got := strings.Count(p, "| test: <name the test>"); got != 3 {
		t.Fatalf("%d goal rows with a test slot, want 3", got)
	}
	for _, c := range Checkpoints {
		if !strings.Contains(p, "- [ ] "+c.ID+": ") {
			t.Errorf("checkpoint %s missing from the prompt", c.ID)
		}
	}
	if g, c, b := strings.Index(p, "## Goal"), strings.Index(p, "## Checkpoints"), strings.Index(p, "Title here"); !(g < c && c < b) {
		t.Fatalf("order wrong: goal %d, checkpoints %d, body %d", g, c, b)
	}
}

func TestBuildPromptRefusesNoAcceptance(t *testing.T) {
	_, err := BuildPrompt("t", "## Problem\n\nNo criteria.\n")
	if !errors.Is(err, ErrNoAcceptance) {
		t.Fatalf("err = %v, want ErrNoAcceptance", err)
	}
	if strings.Contains(err.Error(), "\n") {
		t.Fatalf("the refusal is more than one line: %q", err)
	}
}

// The six deviations the issue names. Adding or dropping one is a decision, so this pins the list.
func TestCheckpointsArePinned(t *testing.T) {
	want := []string{"files-outside-plan", "scope-beyond-acceptance", "acceptance-reworded", "test-seen-failing", "cap-or-pin-raised", "importers-of-changed-files"}
	var got []string
	for _, c := range Checkpoints {
		got = append(got, c.ID)
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("checkpoints = %v, want %v", got, want)
	}
}

// completeReport answers every row and checkpoint of g.
func completeReport(g Goal) string {
	var s strings.Builder
	for _, r := range g.Rows {
		fmt.Fprintf(&s, "- [x] %s: %s | test: TestFor%s\n", r.ID, r.Text, strings.ReplaceAll(r.ID, "-", ""))
	}
	for _, c := range Checkpoints {
		fmt.Fprintf(&s, "- [x] %s: none\n", c.ID)
	}
	return s.String()
}

func TestVerifyPassesACompleteReport(t *testing.T) {
	g := ParseGoal(issue636)
	if p := g.Verify(completeReport(g)); p != nil {
		t.Fatalf("problems on a complete report: %v", p)
	}
}

func TestVerifyFailsEveryGap(t *testing.T) {
	g := ParseGoal(issue636)
	full := completeReport(g)
	drop := func(prefix string) string {
		var out []string
		for _, l := range strings.Split(full, "\n") {
			if !strings.HasPrefix(l, prefix) {
				out = append(out, l)
			}
		}
		return strings.Join(out, "\n")
	}
	cases := []struct{ name, report, want string }{
		{"row unchecked", strings.Replace(full, "- [x] AC-7", "- [ ] AC-7", 1), "goal row AC-7 is unchecked"},
		{"row missing", drop("- [x] AC-7"), "goal row AC-7 is unchecked"},
		{"row names no test", strings.Replace(full, "| test: TestForAC7", "", 1), "goal row AC-7 names no test"},
		{"row keeps the placeholder", strings.Replace(full, "| test: TestForAC7", "| test: <name the test>", 1), "goal row AC-7 names no test"},
		{"empty report", "", "goal row AC-1 is unchecked"},
	}
	for _, c := range Checkpoints {
		cases = append(cases,
			struct{ name, report, want string }{"checkpoint unchecked: " + c.ID, strings.Replace(full, "- [x] "+c.ID, "- [ ] "+c.ID, 1), "checkpoint " + c.ID + " is unanswered"},
			struct{ name, report, want string }{"checkpoint missing: " + c.ID, drop("- [x] " + c.ID), "checkpoint " + c.ID + " is unanswered"},
			struct{ name, report, want string }{"checkpoint blank: " + c.ID, strings.Replace(full, "- [x] "+c.ID+": none", "- [x] "+c.ID+":", 1), "checkpoint " + c.ID + " is unanswered"})
	}
	for _, c := range cases {
		if p := g.Verify(c.report); !strings.Contains(strings.Join(p, "\n"), c.want) {
			t.Errorf("%s: problems %v, want %q", c.name, p, c.want)
		}
	}
}

// The prompt the agent receives, filled in as asked, is a report Verify accepts: the two halves agree.
func TestPromptFilledInVerifies(t *testing.T) {
	g := ParseGoal(issue636)
	filled := strings.ReplaceAll(g.Prompt(), "- [ ] ", "- [x] ")
	filled = strings.ReplaceAll(filled, "<name the test>", "TestX")
	for _, c := range Checkpoints {
		filled = strings.Replace(filled, c.Question, "none", 1)
	}
	if p := g.Verify(filled); p != nil {
		t.Fatalf("a filled-in prompt fails verify: %v\n%s", p, filled)
	}
}
