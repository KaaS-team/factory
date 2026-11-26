//go:build ee

// SPDX-License-Identifier: LicenseRef-KaaS-EE

package budget

import (
	"time"

	"github.com/KaaS-team/factory/ee/contract"
	"github.com/KaaS-team/factory/internal/budget"
)

// FromLegacy builds a Budget from the repo's budgets.yaml entries and its spend rows. Only team
// scopes map to a contract key. Spend inside the current window is committed up front, so the
// result answers Remaining the way budget.Check counts.
func FromLegacy(bs []budget.Budget, rows []budget.Row, now time.Time) (*Budget, error) {
	var limits []Limit
	for _, l := range bs {
		if k, ok := budget.KeyFor(l.Scope); ok {
			limits = append(limits, Limit{Key: k, Micros: Micros(l.LimitUSD), Period: contract.Period(l.Period)})
		}
	}
	b, err := New(Config{Limits: limits, Now: func() time.Time { return now }})
	if err != nil {
		return nil, err
	}
	for _, s := range budget.Check(rows, bs, now) {
		k, ok := budget.KeyFor(s.Scope)
		if !ok || s.Spent <= 0 {
			continue
		}
		b.st.Spent[keyName(k)] = counter{Spent: Micros(s.Spent), Since: b.window(k)}
	}
	return b, nil
}
