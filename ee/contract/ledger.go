// SPDX-License-Identifier: Apache-2.0

package contract

import "time"

// RunLedgerSchema is the value of RunLedgerEntry.Schema for this version.
const RunLedgerSchema = "kaas.run/v1"

// RunLedgerEntry is one line of .factory/ledger/runs.jsonl. docs/contracts/run-ledger.v1.schema.json
// is the source; this type round-trips it, and a test checks the two have the same fields.
// Optional numbers and booleans are pointers so that 0 and false survive a round trip.
type RunLedgerEntry struct {
	Schema       string         `json:"schema"` // RunLedgerSchema
	RunID        string         `json:"run_id"`
	Repo         string         `json:"repo"` // owner/name
	Team         string         `json:"team,omitempty"`
	Workflow     string         `json:"workflow,omitempty"` // the named workflow, for cost per accepted outcome
	Variant      string         `json:"variant,omitempty"`  // the workflow's prompt variant, empty for the default
	Ticket       LedgerTicket   `json:"ticket"`
	Agent        LedgerAgent    `json:"agent"`
	StartedAt    time.Time      `json:"started_at"`
	EndedAt      *time.Time     `json:"ended_at,omitempty"`
	Status       string         `json:"status"`                  // open, merged, failed, cancelled, abandoned
	FailureClass string         `json:"failure_class,omitempty"` // retryable, repo_setup, external, agent, policy, budget
	PR           *LedgerPR      `json:"pr,omitempty"`
	Review       *LedgerReview  `json:"review,omitempty"`
	Tokens       *LedgerTokens  `json:"tokens,omitempty"`
	CostUSD      *float64       `json:"cost_usd,omitempty"`
	QA           *LedgerQA      `json:"qa,omitempty"`
	Outcome      *LedgerOutcome `json:"outcome,omitempty"`
}

// LedgerOutcome is what repo state says happened to the run's work. Kind pr has states merged and
// closed (unmerged), issue has kept and dismissed, comment has acted (on) and ignored. A run with no
// outcome is still pending.
type LedgerOutcome struct {
	Kind  string `json:"kind"`
	State string `json:"state"`
}

// LedgerTicket is where the run's work came from.
type LedgerTicket struct {
	Source string `json:"source"` // github, linear, jira, slack, sentry, api, prd
	ID     string `json:"id"`
	URL    string `json:"url,omitempty"`
}

// LedgerAgent is what ran the work.
type LedgerAgent struct {
	Harness string `json:"harness"`
	Model   string `json:"model,omitempty"`
	Worker  string `json:"worker,omitempty"`
}

// LedgerPR is the run's pull request.
type LedgerPR struct {
	Number        *int       `json:"number,omitempty"`
	URL           string     `json:"url,omitempty"`
	HeadSHA       string     `json:"head_sha,omitempty"`
	ApprovedSHA   string     `json:"approved_sha,omitempty"`
	MergedSHA     string     `json:"merged_sha,omitempty"`
	OpenedAt      *time.Time `json:"opened_at,omitempty"`
	MergedAt      *time.Time `json:"merged_at,omitempty"`
	StackPosition *int       `json:"stack_position,omitempty"`
	LinesAdded    *int       `json:"lines_added,omitempty"`
	LinesDeleted  *int       `json:"lines_deleted,omitempty"`
}

// LedgerReview is how the PR was reviewed.
type LedgerReview struct {
	Rounds        *int   `json:"rounds,omitempty"`
	HumanApprover string `json:"human_approver,omitempty"`
	AutoMerged    *bool  `json:"auto_merged,omitempty"`
}

// LedgerTokens counts the run's tokens. Operator data: it never reaches a customer view.
type LedgerTokens struct {
	Input      *int64 `json:"input,omitempty"`
	Output     *int64 `json:"output,omitempty"`
	CacheRead  *int64 `json:"cache_read,omitempty"`
	CacheWrite *int64 `json:"cache_write,omitempty"`
}

// LedgerQA is the QA verdict on the run's change.
type LedgerQA struct {
	Verdict     string `json:"verdict,omitempty"` // pass, fail, unverified, skipped
	EvidenceURL string `json:"evidence_url,omitempty"`
}
