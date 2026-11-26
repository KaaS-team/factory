package plan

import (
	"fmt"
	"regexp"
	"strings"
)

// A PRD is markdown: "# Title", intro text, then "## Slices" (or "## Requirements"). Each "### Name"
// under it is one slice of work. A slice may have a "Depends: Name, Name" line (slice names), a
// "Files: `a`, `b`" line, and one "- criterion" bullet per acceptance criterion.
//
// A slice is sized to about 30 minutes of agent work, so a slice with more than MaxCriteria bullets
// or MaxFiles files is split into parts that run in order, and a PRD with more than MaxSlices
// slices is refused: it is more than one epic.
const (
	MaxCriteria = 4
	MaxFiles    = 4
	MaxSlices   = 12
)

// PRDTicket is one ticket drafted from a slice.
type PRDTicket struct {
	Key, Title, Body string
}

// PRDPlan is an epic and its ordered tickets.
type PRDPlan struct {
	Title string
	// Spec is the epic text: the PRD intro and one AC line per criterion across all tickets.
	Spec    string
	Tickets []PRDTicket
}

var (
	listItem  = regexp.MustCompile(`^\s*[-*]\s+(.*\S)\s*$`)
	codeSpan  = regexp.MustCompile("`([^`]+)`")
	depLine   = regexp.MustCompile(`(?i)^depends:\s*(.*)$`)
	filesLine = regexp.MustCompile(`(?i)^files:\s*(.*)$`)
)

type slice struct {
	name     string
	depends  []string
	files    []string
	criteria []string
}

// FromPRD turns PRD markdown into an epic and ordered tickets. Every problem is returned together.
func FromPRD(md string) (PRDPlan, error) {
	title, intro, slices, problems := parsePRD(md)
	if title == "" {
		problems = append(problems, "the PRD has no \"# Title\" line")
	}
	if len(slices) == 0 {
		problems = append(problems, "the PRD has no slices: add a \"## Slices\" section with one \"### Name\" per slice")
	}
	if len(slices) > MaxSlices {
		problems = append(problems, fmt.Sprintf("the PRD has %d slices; the cap is %d. Split it into two PRDs", len(slices), MaxSlices))
	}
	if len(problems) > 0 {
		return PRDPlan{}, prdError(problems)
	}

	// last[name] is the key of the final part of a slice, which later slices depend on.
	last := map[string]string{}
	var out PRDPlan
	out.Title = title
	var spec strings.Builder
	spec.WriteString(strings.TrimSpace(intro) + "\n\n")
	n, ac := 0, 0
	for _, s := range slices {
		chunks := chunk(s.criteria, MaxCriteria)
		prev := ""
		for i, cs := range chunks {
			n++
			key := fmt.Sprintf("T%d", n)
			t := s.name
			if len(chunks) > 1 {
				t = fmt.Sprintf("%s (part %d of %d)", s.name, i+1, len(chunks))
			}
			var deps []string
			if prev != "" {
				deps = append(deps, prev)
			}
			if i == 0 {
				for _, d := range s.depends {
					k, ok := last[strings.ToLower(d)]
					if !ok {
						problems = append(problems, fmt.Sprintf("slice %q depends on %q, which is not an earlier slice", s.name, d))
						continue
					}
					deps = append(deps, k)
				}
			}
			var b strings.Builder
			b.WriteString(Marker + "\n")
			if len(deps) == 0 {
				b.WriteString("Deps: none\n")
			} else {
				b.WriteString("Deps: " + strings.Join(deps, ", ") + "\n")
			}
			fmt.Fprintf(&b, "## Why\n\n%s: part of the epic %q.\n\n## Acceptance\n\n", s.name, title)
			for j, c := range cs {
				fmt.Fprintf(&b, "- AC-%d: %s\n", j+1, c)
				ac++
				fmt.Fprintf(&spec, "AC-%d: %s\n", ac, c)
			}
			b.WriteString("\n## Files\n\n")
			for _, f := range s.files {
				fmt.Fprintf(&b, "- `%s`\n", f)
			}
			out.Tickets = append(out.Tickets, PRDTicket{Key: key, Title: t, Body: b.String()})
			prev = key
		}
		last[strings.ToLower(s.name)] = prev
	}
	if len(problems) > 0 {
		return PRDPlan{}, prdError(problems)
	}
	out.Spec = strings.TrimRight(spec.String(), "\n") + "\n"
	return out, nil
}

// Text renders the plan in the form `factory plan --spec` reads: the spec, then one
// "--- ticket KEY: title" block per ticket.
func (p PRDPlan) Text() string {
	var b strings.Builder
	b.WriteString(p.Spec)
	for _, t := range p.Tickets {
		fmt.Fprintf(&b, "--- ticket %s: %s\n%s", t.Key, t.Title, t.Body)
	}
	return b.String()
}

func chunk(s []string, n int) [][]string {
	var out [][]string
	for len(s) > n {
		out = append(out, s[:n])
		s = s[n:]
	}
	return append(out, s)
}

// parsePRD reads the title, the intro and the slices, and collects the problems of each slice.
func parsePRD(md string) (title, intro string, slices []slice, problems []string) {
	var cur *slice
	inSlices, sawSlices := false, false
	var introB strings.Builder
	flush := func() {
		if cur == nil {
			return
		}
		switch {
		case len(cur.criteria) == 0:
			problems = append(problems, fmt.Sprintf("slice %q has no acceptance criteria: add one \"- criterion\" bullet", cur.name))
		case len(cur.files) == 0:
			problems = append(problems, fmt.Sprintf("slice %q has no \"Files:\" line", cur.name))
		case len(cur.files) > MaxFiles:
			problems = append(problems, fmt.Sprintf("slice %q lists %d files; the cap is %d. Split the slice", cur.name, len(cur.files), MaxFiles))
		}
		slices = append(slices, *cur)
		cur = nil
	}
	for _, line := range strings.Split(md, "\n") {
		switch {
		case strings.HasPrefix(line, "# ") && title == "":
			title = strings.TrimSpace(line[2:])
		case strings.HasPrefix(line, "## "):
			flush()
			h := strings.ToLower(strings.TrimSpace(line[3:]))
			inSlices = h == "slices" || h == "requirements"
			sawSlices = sawSlices || inSlices
			if !inSlices && !sawSlices {
				introB.WriteString(line + "\n")
			}
		case inSlices && strings.HasPrefix(line, "### "):
			flush()
			cur = &slice{name: strings.TrimSpace(line[4:])}
		case cur != nil:
			t := strings.TrimSpace(line)
			if m := depLine.FindStringSubmatch(t); m != nil {
				for _, d := range strings.Split(m[1], ",") {
					if d = strings.TrimSpace(d); d != "" && !strings.EqualFold(d, "none") {
						cur.depends = append(cur.depends, d)
					}
				}
			} else if m := filesLine.FindStringSubmatch(t); m != nil {
				for _, c := range codeSpan.FindAllStringSubmatch(m[1], -1) {
					cur.files = append(cur.files, c[1])
				}
			} else if m := listItem.FindStringSubmatch(line); m != nil {
				cur.criteria = append(cur.criteria, m[1])
			}
		case !sawSlices && title != "":
			introB.WriteString(line + "\n")
		}
	}
	flush()
	intro = introB.String()
	if strings.TrimSpace(intro) == "" {
		intro = title
	}
	return title, intro, slices, problems
}

type prdError []string

func (e prdError) Error() string { return "PRD: " + strings.Join(e, "; ") }

// Problems lists each problem on its own.
func (e prdError) Problems() []string { return e }
