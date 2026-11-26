//go:build ee

// SPDX-License-Identifier: LicenseRef-KaaS-EE

package audit

import (
	"context"

	"github.com/KaaS-team/factory/ee/contract"
	"github.com/KaaS-team/factory/internal/audit"
)

// ActionFor maps the action names the core records to contract actions. A name with no entry is
// kept as it is, so the chain still holds it.
var ActionFor = map[string]contract.Action{
	"dispatch.start": contract.ActionRunStart,
}

// Sink is an internal/audit Sink that appends every record to a chain. The OTLP and daily-file
// sinks keep working beside it.
type Sink struct {
	Log contract.Audit
}

// Write appends r to the chain.
func (s Sink) Write(ctx context.Context, r audit.Entry) error {
	a := r.Attributes
	act := contract.Action(a[audit.KeyAction])
	if m, ok := ActionFor[a[audit.KeyAction]]; ok {
		act = m
	}
	out := contract.OutcomeAllow
	if a[audit.KeyResult] == "error" {
		out = contract.OutcomeError
	}
	data := map[string]string{}
	if t := a[audit.KeyTicket]; t != "" {
		data["ticket"] = t
	}
	_, err := s.Log.Record(ctx, contract.AuditEvent{
		Time: r.Timestamp, Actor: a[audit.KeyActor], Action: act, Resource: a[audit.KeyTarget], Outcome: out, Data: data,
	})
	return err
}

// Emit records one event and ignores a write failure: a failed audit write never fails the action
// it describes. Use it for security-relevant actions such as a budget stop.
func Emit(ctx context.Context, log contract.Audit, e contract.AuditEvent) {
	if log == nil {
		return
	}
	_, _ = log.Record(ctx, e)
}
