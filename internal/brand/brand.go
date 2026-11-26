// Package brand holds the one line every factory output carries: the PR footer, the commit trailer
// and the --version link. It never carries operator detail (#210).
package brand

import (
	"regexp"
	"strings"
)

// URL is where the brand line points.
const URL = "https://kaas.team"

// DefaultFooter is the PR body footer when config sets none.
const DefaultFooter = "Shipped with [KaaS Factory](" + URL + ")"

// TrailerKey is the git trailer every factory commit carries.
const TrailerKey = "Shipped-with"

// Trailer is the full trailer line.
const Trailer = TrailerKey + ": KaaS Factory (" + URL + ")"

// LeakRE matches operator detail that must never reach a customer: model names, token counts,
// cost, claude.ai links, attribution trailers and provider keys. The PR redactor and the brand
// checks share it.
var LeakRE = regexp.MustCompile(`(?i)` + strings.Join([]string{
	`\bclaude[-\s]?(opus|sonnet|haiku|fable|instant|\d)`,
	`\b(opus|sonnet|haiku|fable|gemini|gpt)[-\s]?\d`,
	`claude\.ai`,
	`claude-session:`,
	`co-authored-by:.*(claude|anthropic)`,
	`generated with.*claude`,
	`\b\d[\d,._]*\s*[km]?\s*(input |output |cache )?tokens?\b`,
	`\btokens?\b\s*[:=]\s*\d`,
	`\$\s?\d`,
	`\b\d+(\.\d+)?\s*(usd|dollars)\b`,
	`\b(cost|spend|spent)\b\s*[:=]?\s*(usd|us\$)?\s*\d`,
	`cost_usd`,
	`sk-ant-`,
}, "|"))

// Footer resolves the configured footer. An empty one gives DefaultFooter. A configured line that
// names a model or a cost, spans lines, or lacks the kaas.team link falls back to the default.
func Footer(configured string) string {
	s := strings.TrimSpace(configured)
	if s == "" || strings.ContainsAny(s, "\r\n") || LeakRE.MatchString(s) || !strings.Contains(s, "kaas.team") {
		return DefaultFooter
	}
	return s
}

// AppendFooter ends body with the footer, once.
func AppendFooter(body, footer string) string {
	f := Footer(footer)
	body = strings.TrimRight(body, "\n")
	if strings.HasSuffix(body, f) {
		return body
	}
	if body == "" {
		return f
	}
	return body + "\n\n---\n" + f
}

// WithTrailer ends a commit message with the Shipped-with trailer, once. It joins an existing
// trailer block (a Signed-off-by line) without a blank line between them.
func WithTrailer(msg string) string {
	msg = strings.TrimRight(msg, "\n")
	if strings.Contains(msg, "\n"+TrailerKey+":") || strings.HasPrefix(msg, TrailerKey+":") {
		return msg
	}
	last := msg[strings.LastIndex(msg, "\n\n")+1:]
	if strings.Contains(msg, "\n\n") && trailerBlock(last) {
		return msg + "\n" + Trailer
	}
	return msg + "\n\n" + Trailer
}

var trailerLine = regexp.MustCompile(`^[A-Za-z][A-Za-z-]*: \S`)

func trailerBlock(s string) bool {
	for _, l := range strings.Split(strings.TrimSpace(s), "\n") {
		if !trailerLine.MatchString(l) {
			return false
		}
	}
	return true
}
