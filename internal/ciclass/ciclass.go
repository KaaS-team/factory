// Package ciclass sorts a failing CI log into one of the run ledger's failure classes.
package ciclass

import (
	"regexp"
	"strings"
)

// Class is a run ledger failure_class value (docs/contracts/run-ledger.v1.schema.json).
type Class string

// The classes the manager acts on.
const (
	Retryable Class = "retryable"  // flake, network, runner: rerun
	RepoSetup Class = "repo_setup" // missing secret, broken main: a human fixes the repo
	External  Class = "external"   // upstream outage: wait and tell a human
	Agent     Class = "agent"      // the change broke it: send the log back to the task
)

type rule struct {
	class Class
	re    *regexp.Regexp
}

// Rules run in order, so the first match wins. Setup and outage signs come before flake signs,
// because a missing secret often prints a timeout line too.
var rules = []rule{
	{RepoSetup, regexp.MustCompile(`(?i)(secret|token|credential)s? .{0,40}(not (set|found|provided)|is (empty|missing))|Input required and not supplied|Resource not accessible by integration|main (is|branch is) (broken|red)|Context access might be invalid|unable to resolve action`)},
	{External, regexp.MustCompile(`(?i)(status\.github\.com|githubstatus|upstream (connect )?(error|outage)|503 service unavailable.*(registry|proxy\.golang|pypi|npmjs)|registry\.npmjs\.org.*(5\d\d)|incident)`)},
	{Retryable, regexp.MustCompile(`(?i)(i/o timeout|connection (reset|refused)|TLS handshake timeout|ETIMEDOUT|ECONNRESET|temporary failure in name resolution|the runner has received a shutdown signal|runner .{0,30}lost communication|no space left on device|rate limit exceeded|\bflaky\b|--- FLAKE|received a signal 15|context deadline exceeded.*(download|fetch))`)},
}

// Classify returns the class of a failing log. A log that matches no setup, outage or flake sign is
// the agent's: the change broke it, and the excerpt goes back to the task.
func Classify(log string) Class {
	for _, r := range rules {
		if r.re.MatchString(log) {
			return r.class
		}
	}
	return Agent
}

// Excerpt returns up to max bytes of the log's tail, on a line boundary, where the failure usually
// is. It is what the manager sends back to the agent.
func Excerpt(log string, max int) string {
	log = strings.TrimSpace(log)
	if max <= 0 || len(log) <= max {
		return log
	}
	tail := log[len(log)-max:]
	if i := strings.IndexByte(tail, '\n'); i >= 0 && i < len(tail)-1 {
		tail = tail[i+1:]
	}
	return "...\n" + tail
}
