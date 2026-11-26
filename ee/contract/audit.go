// SPDX-License-Identifier: Apache-2.0

package contract

import (
	"context"
	"time"
)

// Outcome is how an audited action ended.
type Outcome string

// The outcomes.
const (
	OutcomeAllow Outcome = "allow" // permitted and done
	OutcomeDeny  Outcome = "deny"  // refused by Authorizer, Policy or Budget; Reason says which
	OutcomeError Outcome = "error" // permitted but failed
)

// AuditEvent is one entry of the audit log. Seq, PrevHash and Hash are set by Audit.Record.
//
// # Canonical form
//
// The canonical form of an event is the UTF-8 JSON text of an object with exactly these eight
// members, and no others: "action", "actor", "data", "outcome", "reason", "resource", "seq",
// "time". It follows RFC 8785 (JSON Canonicalization Scheme) for this subset:
//
//   - Members appear in ascending order of their names, compared as byte strings; the same holds
//     inside "data".
//   - No whitespace anywhere outside strings.
//   - "seq" is a base-10 integer with no sign, exponent, fraction or leading zero.
//   - "time" is the event time in UTC as "YYYY-MM-DDTHH:MM:SS.mmmZ": always three fraction
//     digits, truncated (not rounded) to the millisecond. This is what JavaScript's
//     Date.prototype.toISOString prints.
//   - "data" is an object of string values, {} when empty. Its keys are ASCII letters, digits,
//     '_', '.' and '-', so byte order and RFC 8785's UTF-16 order agree. "reason" is "" when unset.
//   - Strings escape only: '"' as \", '\' as \\, U+0008 \b, U+0009 \t, U+000A \n, U+000C \f,
//     U+000D \r, and any other code point below U+0020 as \u00xx with lowercase hex. Every other
//     character, including '<', '>', '&', U+2028 and non-ASCII, is written as itself in UTF-8.
//
// # Hash chain
//
// hash = sha256(prev_hash || canonical_event), where || is byte concatenation, prev_hash is the
// 32 raw bytes of the previous event's hash (32 zero bytes for Seq 1), and canonical_event is the
// canonical form above. Hash and PrevHash are stored as 64 lowercase hex characters. Seq starts at
// 1 and rises by one per event, so a deleted, inserted or reordered event breaks the chain.
// testdata/conformance.json pins the canonical text and hash of three events.
type AuditEvent struct {
	Seq      uint64            `json:"seq"`
	Time     time.Time         `json:"time"`
	Actor    string            `json:"actor"`    // the Principal.ID
	Action   Action            `json:"action"`   // what was asked
	Resource string            `json:"resource"` // "org", "org/team" or "owner/name", as the caller names it
	Outcome  Outcome           `json:"outcome"`
	Reason   ReasonCode        `json:"reason"`         // the Decision's reason, or "" for allow with no check
	Data     map[string]string `json:"data,omitempty"` // small, non-secret detail; never tokens, keys or prompts
	PrevHash string            `json:"prev_hash"`
	Hash     string            `json:"hash"`
}

// AuditQuery filters Audit.Search. Empty fields match everything; Since is inclusive, Until is
// exclusive. Limit 0 means no limit.
type AuditQuery struct {
	Actor   string
	Action  Action
	Outcome Outcome
	Since   time.Time
	Until   time.Time
	Limit   int
}

// Audit is the append-only, hash-chained audit log.
type Audit interface {
	// Record appends e. It sets e.Seq to the next number, e.PrevHash to the last event's Hash,
	// and e.Hash per the chain rule, and returns the stored event. A zero e.Time is set to now.
	// Seq, PrevHash and Hash given by the caller are ignored. Concurrent Records are serialised:
	// no two events share a Seq.
	Record(ctx context.Context, e AuditEvent) (AuditEvent, error)

	// Verify recomputes every hash from the first event. It returns nil for an intact chain, and
	// an error wrapping ErrChainBroken that names the first bad Seq otherwise.
	Verify(ctx context.Context) error

	// Search returns the matching events in Seq order, oldest first, at most q.Limit of them.
	Search(ctx context.Context, q AuditQuery) ([]AuditEvent, error)
}
