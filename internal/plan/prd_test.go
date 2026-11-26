package plan

import (
	"flag"
	"os"
	"strings"
	"testing"
)

var updatePRD = flag.Bool("update-prd", false, "rewrite testdata/prd/search.golden")

// TestPRDGolden: the fixture PRD becomes one epic and ordered tickets, byte for byte.
func TestPRDGolden(t *testing.T) {
	md, err := os.ReadFile("testdata/prd/search.md")
	if err != nil {
		t.Fatal(err)
	}
	p, err := FromPRD(string(md))
	if err != nil {
		t.Fatal(err)
	}
	got := p.Text()
	if *updatePRD {
		if err := os.WriteFile("testdata/prd/search.golden", []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile("testdata/prd/search.golden")
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("PRD output changed; rerun with -update-prd if intended.\ngot:\n%s", got)
	}
}

// TestPRDTicketsPassTheDAGChecks: what FromPRD drafts is a valid plan with one wave per dependency step.
func TestPRDTicketsPassTheDAGChecks(t *testing.T) {
	md, _ := os.ReadFile("testdata/prd/search.md")
	p, err := FromPRD(string(md))
	if err != nil {
		t.Fatal(err)
	}
	var pl Plan
	for _, d := range p.Tickets {
		tk, err := NewTicket(d.Key, d.Title, d.Body)
		if err != nil {
			t.Fatal(err)
		}
		pl.Tickets = append(pl.Tickets, tk)
	}
	if pr := pl.Problems(); len(pr) > 0 {
		t.Fatalf("problems: %v", pr)
	}
	w, err := pl.Waves()
	if err != nil || len(w) != 4 { // the 5-criterion CLI slice is split in two, in order
		t.Errorf("waves=%v err=%v", w, err)
	}
}

func TestPRDSplitsABigSlice(t *testing.T) {
	md, _ := os.ReadFile("testdata/prd/search.md")
	p, _ := FromPRD(string(md))
	if len(p.Tickets) != 4 || p.Tickets[2].Title != "Saved search CLI (part 1 of 2)" || !strings.Contains(p.Tickets[3].Body, "Deps: T3") {
		t.Errorf("tickets: %+v", p.Tickets)
	}
}

func TestPRDProblems(t *testing.T) {
	for name, tc := range map[string]struct{ md, want string }{
		"no title":       {"## Slices\n### A\nFiles: `a`\n- x\n", "no \"# Title\""},
		"no slices":      {"# T\nintro\n", "no slices"},
		"no files":       {"# T\n## Slices\n### A\n- x\n", "no \"Files:\" line"},
		"no criteria":    {"# T\n## Slices\n### A\nFiles: `a`\n", "no acceptance criteria"},
		"too many files": {"# T\n## Slices\n### A\nFiles: `a`, `b`, `c`, `d`, `e`\n- x\n", "cap is 4"},
		"unknown dep":    {"# T\n## Slices\n### A\nDepends: B\nFiles: `a`\n- x\n", "not an earlier slice"},
		"forward dep":    {"# T\n## Slices\n### A\nDepends: B\nFiles: `a`\n- x\n### B\nFiles: `b`\n- y\n", "not an earlier slice"},
	} {
		_, err := FromPRD(tc.md)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", name, err, tc.want)
		}
	}
	var sb strings.Builder
	sb.WriteString("# T\n## Slices\n")
	for i := 0; i <= MaxSlices; i++ {
		sb.WriteString("### S" + string(rune('A'+i)) + "\nFiles: `a`\n- x\n")
	}
	if _, err := FromPRD(sb.String()); err == nil || !strings.Contains(err.Error(), "cap is 12") {
		t.Errorf("slice cap: %v", err)
	}
}
