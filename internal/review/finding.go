package review

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/KaaS-team/factory/internal/reviewlearn"
)

// Finding is one review comment. It must cite a line that is in the diff and quote it.
type Finding struct {
	Check      string  `json:"check,omitempty"`
	Kind       string  `json:"kind"`
	File       string  `json:"file"`
	Line       int     `json:"line"`
	Quote      string  `json:"quote"`
	Message    string  `json:"message"`
	Confidence float64 `json:"confidence"`
	Source     string  `json:"source,omitempty"` // the reviewers that reported it
}

// Dropped is a finding the station removed, and why. Reasons are plain text for customers.
type Dropped struct {
	Finding Finding
	Reason  string
}

// ParseFindings reads the JSON array of findings out of an agent's output. Text around the array,
// such as a code fence, is ignored.
func ParseFindings(output string) ([]Finding, error) {
	i, j := strings.Index(output, "["), strings.LastIndex(output, "]")
	if i < 0 || j < i {
		return nil, errors.New("review: no findings array in output")
	}
	var fs []Finding
	if err := json.Unmarshal([]byte(output[i:j+1]), &fs); err != nil {
		return nil, fmt.Errorf("review: findings: %w", err)
	}
	return fs, nil
}

// DiffIndex holds every line of the new side of a diff: file -> line number -> text.
type DiffIndex map[string]map[int]string

var hunkRe = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@`)

// IndexDiff parses a unified diff. Added and context lines are citable; removed lines are not.
func IndexDiff(diff string) DiffIndex {
	idx := DiffIndex{}
	var file string
	line := 0
	inHunk := false
	for _, l := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(l, "diff --git "):
			file, inHunk = "", false
		case !inHunk && strings.HasPrefix(l, "+++ "):
			p := strings.TrimPrefix(l, "+++ ")
			if p == "/dev/null" {
				file = ""
			} else {
				file = strings.TrimPrefix(p, "b/")
			}
		case strings.HasPrefix(l, "@@"):
			if m := hunkRe.FindStringSubmatch(l); m != nil && file != "" {
				line, _ = strconv.Atoi(m[1])
				inHunk = true
				if idx[file] == nil {
					idx[file] = map[int]string{}
				}
			}
		case !inHunk || file == "":
		case strings.HasPrefix(l, "+"), strings.HasPrefix(l, " "):
			idx[file][line] = l[1:]
			line++
		}
	}
	return idx
}

// Check returns an error unless the finding cites a line in the diff and quotes it.
func (d DiffIndex) Check(f Finding) error {
	lines, ok := d[f.File]
	if !ok {
		return fmt.Errorf("file %q is not in the diff", f.File)
	}
	text, ok := lines[f.Line]
	if !ok {
		return fmt.Errorf("line %d of %s is not in the diff", f.Line, f.File)
	}
	q := strings.TrimSpace(f.Quote)
	if q == "" {
		return errors.New("finding quotes no line")
	}
	if !strings.Contains(text, q) {
		return fmt.Errorf("quote does not match line %d of %s", f.Line, f.File)
	}
	return nil
}

// Rules is the learned-rule lookup the station applies.
type Rules interface {
	Apply(kind string) (reviewlearn.Rule, bool)
}

// DefaultMinConfidence is the self-critique cut when Station.MinConfidence is zero.
const DefaultMinConfidence = 0.5

// Station turns raw findings into the ones worth showing. Every step is code: citation check,
// learned rules, dedupe across rounds, then the self-critique cut on confidence.
type Station struct {
	Rules         Rules   // may be nil
	MinConfidence float64 // DefaultMinConfidence when zero
	seen          map[string]bool
}

// Process filters findings from one round against that round's diff. It remembers what it kept,
// so a later round does not repeat it.
func (s *Station) Process(diff string, in []Finding) (kept []Finding, dropped []Dropped) {
	min := s.MinConfidence
	if min == 0 {
		min = DefaultMinConfidence
	}
	if s.seen == nil {
		s.seen = map[string]bool{}
	}
	idx := IndexDiff(diff)
	for _, f := range in {
		if err := idx.Check(f); err != nil {
			dropped = append(dropped, Dropped{f, "no valid citation: " + err.Error()})
			continue
		}
		if s.Rules != nil {
			if r, ok := s.Rules.Apply(f.Kind); ok {
				if r.Action == reviewlearn.Suppress {
					dropped = append(dropped, Dropped{f, "suppressed by a learned rule"})
					continue
				}
				if r.Action == reviewlearn.Reword && r.Message != "" {
					f.Message = r.Message
				}
			}
		}
		if f.Confidence < min {
			dropped = append(dropped, Dropped{f, "low confidence"})
			continue
		}
		k := f.File + ":" + strconv.Itoa(f.Line) + ":" + strings.ToLower(strings.TrimSpace(f.Kind))
		if s.seen[k] {
			dropped = append(dropped, Dropped{f, "duplicate"})
			continue
		}
		s.seen[k] = true
		kept = append(kept, f)
	}
	return kept, dropped
}

// Merge joins findings from two reviewers by citation. Two findings on the same file and line
// become one that names both sources and takes the higher confidence, raised by agreement.
func Merge(a, b []Finding) []Finding {
	out := append([]Finding(nil), a...)
	pos := map[string]int{}
	for i, f := range out {
		pos[cite(f)] = i
	}
	for _, f := range b {
		i, ok := pos[cite(f)]
		if !ok {
			pos[cite(f)] = len(out)
			out = append(out, f)
			continue
		}
		m := out[i]
		if f.Confidence > m.Confidence {
			m.Confidence, m.Message = f.Confidence, f.Message
		}
		m.Confidence = min(1, m.Confidence+0.1)
		if f.Source != "" && f.Source != m.Source {
			m.Source = strings.Trim(m.Source+"+"+f.Source, "+")
		}
		out[i] = m
	}
	return out
}

func cite(f Finding) string { return f.File + ":" + strconv.Itoa(f.Line) }

// Reviewer produces findings for a diff. A second reviewer from another model family plugs in here
// by wrapping an Agent that talks ACP to the configured harness.
type Reviewer interface {
	Name() string
	Review(ctx context.Context, diff string) ([]Finding, error)
}

// Round runs the primary reviewer and any seconds, merges their findings by citation and passes
// them through the station. A failing second reviewer is skipped; the primary's error is returned.
func (s *Station) Round(ctx context.Context, diff string, primary Reviewer, seconds ...Reviewer) ([]Finding, []Dropped, error) {
	all, err := primary.Review(ctx, diff)
	if err != nil {
		return nil, nil, err
	}
	for i := range all {
		all[i].Source = primary.Name()
	}
	for _, r := range seconds {
		fs, err := r.Review(ctx, diff)
		if err != nil {
			continue
		}
		for i := range fs {
			fs[i].Source = r.Name()
		}
		all = Merge(all, fs)
	}
	kept, dropped := s.Process(diff, all)
	return kept, dropped, nil
}
