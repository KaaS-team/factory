// Package risk scores a change as low, medium or high and asks policy whether that class may merge
// without a human.
package risk

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/KaaS-team/factory/ee/contract"
)

// File is one changed file.
type File struct {
	Path    string
	Added   int
	Removed int
}

// Change is the diff the score reads.
type Change struct {
	Files []File
	// Protected holds the charter's protected paths: a path prefix, or a glob matched with path.Match.
	Protected []string
}

// Score is the class, a number for sorting, and the reasons, in plain English.
type Score struct {
	Class   contract.RiskClass
	Points  int
	Reasons []string
}

// Size thresholds, in changed lines.
const (
	smallDiff  = 50
	mediumDiff = 300
)

// Classify scores c. Protected paths and a very large diff are high. Code changes with no test change
// are at least medium. A change of only docs, or only tests, with a small diff is low.
func Classify(c Change) Score {
	var s Score
	lines, code, tests := 0, 0, 0
	for _, f := range c.Files {
		lines += f.Added + f.Removed
		switch kind(f.Path) {
		case kindDoc:
		case kindTest:
			tests++
		default:
			code++
		}
		if IsProtected(f.Path, c.Protected) {
			s.Points += 100
			s.Reasons = append(s.Reasons, "touches protected path "+f.Path)
		}
	}
	switch {
	case len(c.Files) == 0:
		s.Points += 100
		s.Reasons = append(s.Reasons, "no changed files known")
	case lines > mediumDiff:
		s.Points += 100
		s.Reasons = append(s.Reasons, fmt.Sprintf("large diff, %d lines", lines))
	case lines > smallDiff:
		s.Points += 30
		s.Reasons = append(s.Reasons, fmt.Sprintf("medium diff, %d lines", lines))
	}
	if code > 0 {
		s.Points += 20
		if tests == 0 {
			s.Points += 30
			s.Reasons = append(s.Reasons, "code changed with no test change")
		}
	}
	switch {
	case s.Points >= 100:
		s.Class = contract.RiskHigh
	case s.Points >= 30:
		s.Class = contract.RiskMedium
	default:
		s.Class = contract.RiskLow
	}
	if len(s.Reasons) == 0 {
		s.Reasons = []string{"small change"}
	}
	return s
}

// AutoMergeAllowed asks policy whether the class may merge without a human review.
func AutoMergeAllowed(ctx context.Context, p contract.Policy, class contract.RiskClass) contract.Decision {
	return p.Evaluate(ctx, contract.PolicyRequest{Check: contract.CheckAutoMerge, Value: string(class)})
}

// IsProtected reports whether p is under any protected entry.
func IsProtected(p string, protected []string) bool {
	for _, pr := range protected {
		pr = strings.TrimSuffix(pr, "/")
		if p == pr || strings.HasPrefix(p, pr+"/") {
			return true
		}
		if ok, _ := path.Match(pr, p); ok {
			return true
		}
	}
	return false
}

type fileKind int

const (
	kindCode fileKind = iota
	kindDoc
	kindTest
)

func kind(p string) fileKind {
	base := path.Base(p)
	switch {
	case strings.HasSuffix(base, "_test.go"), strings.Contains(base, ".test."), strings.Contains(base, ".spec."),
		strings.HasPrefix(p, "test/"), strings.HasPrefix(p, "tests/"), strings.Contains(p, "/testdata/"):
		return kindTest
	case strings.HasPrefix(p, "docs/"), strings.HasSuffix(base, ".md"), strings.HasSuffix(base, ".txt"),
		base == "LICENSE", base == "CHANGELOG":
		return kindDoc
	}
	return kindCode
}
