// Package policygate is where the core asks the enterprise policy a question. It holds one
// contract.Policy, set at start-up by the ee build. With none set every check allows, so the
// default build behaves as before.
package policygate

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/KaaS-team/factory/ee/contract"
	"github.com/KaaS-team/factory/internal/api"
)

// ErrDenied is wrapped by every refusal. The text of a refusal carries the reason code and the
// value, never a model name or host detail beyond what the caller passed in.
var ErrDenied = errors.New("refused by policy")

var (
	mu      sync.RWMutex
	current contract.Policy
)

// Configure sets the policy. nil turns enforcement off.
func Configure(p contract.Policy) {
	mu.Lock()
	defer mu.Unlock()
	current = p
}

// Check asks the policy one question. It returns nil when allowed or when no policy is set, and
// an error wrapping ErrDenied and carrying the reason code otherwise.
func Check(ctx context.Context, c contract.Check, value string) error {
	mu.RLock()
	p := current
	mu.RUnlock()
	if p == nil {
		return nil
	}
	if d := p.Evaluate(ctx, contract.PolicyRequest{Check: c, Value: value}); !d.Allow {
		return Refusal(d.Reason, c)
	}
	return nil
}

// Refusal builds the error for a denial.
func Refusal(r contract.ReasonCode, c contract.Check) error {
	return fmt.Errorf("%w: %s (%s)", ErrDenied, r, c)
}

// Wrap returns d with the harness and model of every brief checked before it starts. A brief with
// no model uses the stage default, which this layer cannot see, so only a named model is checked.
func Wrap(d api.Dispatcher) api.Dispatcher { return gated{d} }

type gated struct{ api.Dispatcher }

// Harness names the coding-agent harness a preset runs on. The claude presets run Claude Code;
// any other preset is its own harness.
func Harness(agent string) string {
	if agent == "" || agent == "claude" || strings.HasPrefix(agent, "claude-") {
		return "claude-code"
	}
	return agent
}

func (g gated) Dispatch(ctx context.Context, b api.Brief) (api.Session, error) {
	if err := Check(ctx, contract.CheckHarness, Harness(b.Agent)); err != nil {
		return api.Session{}, fmt.Errorf("run not started: %w", err)
	}
	if b.Model != "" {
		if err := Check(ctx, contract.CheckModel, b.Model); err != nil {
			return api.Session{}, fmt.Errorf("run not started: %w", err)
		}
	}
	return g.Dispatcher.Dispatch(ctx, b)
}
