package budget

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/KaaS-team/factory/ee/contract"
)

// Enforcer refuses a run that would pass a contract.Budget limit. Open builds the budget on every
// call, so an edit to limits or spend takes effect without a restart. Estimate is what a run is
// expected to cost; a run starts only when its team has at least that much left.
type Enforcer struct {
	Open     func() (contract.Budget, error)
	Estimate contract.Micros
}

var (
	enfMu    sync.RWMutex
	enforcer *Enforcer
)

// SetEnforcer installs e for Allow. Nil removes it.
func SetEnforcer(e *Enforcer) {
	enfMu.Lock()
	defer enfMu.Unlock()
	enforcer = e
}

// KeyFor maps a legacy scope to a contract key. Only team scopes have one.
func KeyFor(scope string) (contract.BudgetKey, bool) {
	if id, ok := strings.CutPrefix(scope, "team:"); ok && id != "" {
		return contract.BudgetKey{Scope: contract.BudgetTeam, ID: id}, true
	}
	return contract.BudgetKey{}, false
}

// enforce returns an error wrapping both ErrOverBudget and contract.ErrBudgetExceeded when scope
// has less left than a run is expected to cost. The text names no amount.
func enforce(scope string) error {
	enfMu.RLock()
	e := enforcer
	enfMu.RUnlock()
	k, ok := KeyFor(scope)
	if e == nil || !ok {
		return nil
	}
	b, err := e.Open()
	if err != nil {
		return err
	}
	left, limited, err := b.Remaining(context.Background(), k)
	if err != nil {
		return err
	}
	need := max(e.Estimate, 1)
	if limited && left < need {
		return fmt.Errorf("%s: %w: %w", scope, ErrOverBudget, contract.ErrBudgetExceeded)
	}
	return nil
}
