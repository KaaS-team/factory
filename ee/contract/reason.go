// SPDX-License-Identifier: Apache-2.0

package contract

import "errors"

// ReasonCode is a stable, dotted, machine-readable reason. Codes never change meaning once
// shipped; a new case gets a new code. Every Decision carries exactly one.
type ReasonCode string

// Authorizer reason codes, in the order Authorizer.Can checks them.
const (
	ReasonUnknownAction   ReasonCode = "authz.unknown_action"    // the action is not in Actions
	ReasonDeprovisioned   ReasonCode = "authz.deprovisioned"     // the principal is disabled
	ReasonCrossOrg        ReasonCode = "authz.cross_org"         // the resource is in another org
	ReasonHumanRequired   ReasonCode = "authz.human_required"    // only a user may take the action
	ReasonNoBinding       ReasonCode = "authz.no_binding"        // no role binding covers the resource
	ReasonScopeTooNarrow  ReasonCode = "authz.scope_too_narrow"  // granted only below org scope, the action needs org
	ReasonRoleLacksAction ReasonCode = "authz.role_lacks_action" // bindings cover the resource, none grants it
	ReasonGranted         ReasonCode = "authz.granted"           // allowed
)

// Policy reason codes.
const (
	ReasonPolicyAllowed          ReasonCode = "policy.allowed"           // allowed
	ReasonPolicyNotAllowlisted   ReasonCode = "policy.not_allowlisted"   // the value is on no effective allowlist
	ReasonPolicyApprovalRequired ReasonCode = "policy.approval_required" // the action needs a human approval first
	ReasonPolicyUnknownCheck     ReasonCode = "policy.unknown_check"     // the request kind is not a Check constant
	ReasonPolicyWidening         ReasonCode = "policy.widening"          // a lower layer tried to widen (ErrPolicyWidening)
	ReasonPolicyLayerOrder       ReasonCode = "policy.layer_order"       // layers out of order or repeated (ErrPolicyLayerOrder)
	ReasonPolicyInvalid          ReasonCode = "policy.invalid"           // the file does not match the schema (ErrPolicyInvalid)
)

// Audit and budget reason codes, used as the text of their sentinel errors.
const (
	ReasonAuditChainBroken              ReasonCode = "audit.chain_broken"
	ReasonBudgetExceeded                ReasonCode = "budget.exceeded"
	ReasonBudgetUnknownReservation      ReasonCode = "budget.unknown_reservation"
	ReasonIdentityUnauthenticated       ReasonCode = "identity.unauthenticated"
	ReasonIdentityDeprovisioned         ReasonCode = "identity.deprovisioned"
	ReasonIdentityNotFound              ReasonCode = "identity.not_found"
	ReasonIdentityUnsupportedCredential ReasonCode = "identity.unsupported_credential"
)

// Sentinel errors. Each one's text is its reason code, so a port in another language compares the
// same string. Implementations wrap them with detail (fmt.Errorf("%w: ...", ErrX)); callers test
// with errors.Is.
var (
	ErrUnauthenticated       = errors.New(string(ReasonIdentityUnauthenticated))
	ErrDeprovisioned         = errors.New(string(ReasonIdentityDeprovisioned))
	ErrNotFound              = errors.New(string(ReasonIdentityNotFound))
	ErrUnsupportedCredential = errors.New(string(ReasonIdentityUnsupportedCredential))
	ErrPolicyWidening        = errors.New(string(ReasonPolicyWidening))
	ErrPolicyLayerOrder      = errors.New(string(ReasonPolicyLayerOrder))
	ErrPolicyInvalid         = errors.New(string(ReasonPolicyInvalid))
	ErrChainBroken           = errors.New(string(ReasonAuditChainBroken))
	ErrBudgetExceeded        = errors.New(string(ReasonBudgetExceeded))
	ErrUnknownReservation    = errors.New(string(ReasonBudgetUnknownReservation))
)

// Decision is the answer of Authorizer.Can and Policy.Evaluate. Allow is true only with
// ReasonGranted or ReasonPolicyAllowed. Detail is free text for the operator log; it never
// reaches a customer view and nothing may parse it.
type Decision struct {
	Allow  bool       `json:"allow"`
	Reason ReasonCode `json:"reason"`
	Detail string     `json:"detail,omitempty"`
}
