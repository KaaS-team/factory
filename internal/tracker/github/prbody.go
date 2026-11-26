package github

import (
	"fmt"
	"strings"

	"github.com/KaaS-team/factory/internal/brand"
)

// Proof is the verify stage's test-that-bites result.
type Proof struct {
	Status   string
	Tests    []string
	Reverted []string
}

// PRParts is what the PR body is built from beyond the pr stage's own text. Each piece is already rendered.
type PRParts struct {
	WhatChanged string
	Acceptance  string
	Closes      string
	// RunDetails names the issue holding the run-details comment (see IssueRef).
	RunDetails string
	// Footer is the configured footer line; empty gives brand.DefaultFooter. Every body ends with it.
	Footer string
}

// IssueRef names a GitHub issue so the PR body links to it.
func IssueRef(n int) string { return fmt.Sprintf("#%d", n) }

// RenderWhatChanged builds the body's opening section from the goal, the diff and the proof (src/engine/pr-body.ts).
func RenderWhatChanged(goal string, files []string, proof *Proof) string {
	lines := []string{"## What changed", "", strings.TrimSpace(goal), ""}
	if len(files) > 0 {
		quoted := make([]string, len(files))
		for i, f := range files {
			quoted[i] = "`" + f + "`"
		}
		lines = append(lines, "- Files: "+strings.Join(quoted, ", "))
	} else {
		lines = append(lines, "- Files: none in the diff")
	}
	if proof != nil && proof.Status == "bites" && len(proof.Tests) > 0 {
		lines = append(lines, "- Fails without it: "+quoteAll(proof.Tests)+", red with "+orDefault(quoteAll(proof.Reverted), "the change")+" reverted")
	} else {
		status := "did not run"
		if proof != nil {
			status = proof.Status
		}
		lines = append(lines, "- Fails without it: no test shown (proof "+status+")")
	}
	return strings.Join(lines, "\n")
}

func quoteAll(xs []string) string {
	q := make([]string, len(xs))
	for i, x := range xs {
		q[i] = "`" + x + "`"
	}
	return strings.Join(q, ", ")
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// RenderPRBody assembles the PR body: What changed, the pr stage's text, Acceptance, the closing
// line and the pointer to the run-details comment. Redact runs first so no model name, token count,
// cost or claude.ai link reaches the PR, then the one brand footer ends the body (#210).
func RenderPRBody(body string, p PRParts) string {
	if p.WhatChanged != "" {
		body = p.WhatChanged + "\n\n" + body
	}
	if p.Acceptance != "" {
		body += "\n\n" + p.Acceptance
	}
	if p.Closes != "" {
		body += "\n\n" + p.Closes
	}
	if p.RunDetails != "" {
		body += "\n\nThe receipt is in the run-details comment on " + p.RunDetails + "."
	}
	return brand.AppendFooter(Redact(body), p.Footer)
}

var leakRE = brand.LeakRE

// Redact drops every line that carries model, token, cost, claude.ai or key detail.
func Redact(s string) string {
	lines := strings.Split(s, "\n")
	kept := lines[:0]
	for _, l := range lines {
		if !leakRE.MatchString(l) {
			kept = append(kept, l)
		}
	}
	return strings.Join(kept, "\n")
}
