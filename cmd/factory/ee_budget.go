//go:build ee

// SPDX-License-Identifier: LicenseRef-KaaS-EE

package main

import (
	"os"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	eebudget "github.com/KaaS-team/factory/ee/budget"
	"github.com/KaaS-team/factory/ee/contract"
	"github.com/KaaS-team/factory/internal/budget"
)

// defaultRunEstimateUSD is what a run is expected to cost when FACTORY_RUN_ESTIMATE_USD is unset.
const defaultRunEstimateUSD = 0.25

func init() { rootHooks = append(rootHooks, enforceBudgets) }

// enforceBudgets makes the dispatch guard refuse a run whose team has less left than a run is
// expected to cost. Limits and spend are read from the repo on every check.
func enforceBudgets(*cobra.Command) {
	est := defaultRunEstimateUSD
	if v, err := strconv.ParseFloat(os.Getenv("FACTORY_RUN_ESTIMATE_USD"), 64); err == nil && v >= 0 {
		est = v
	}
	budget.SetEnforcer(&budget.Enforcer{
		Estimate: eebudget.Micros(est),
		Open: func() (contract.Budget, error) {
			bs, err := budget.Load(budget.DefaultPath)
			if err != nil {
				return nil, err
			}
			rows, err := loadSpend(spendPath)
			if err != nil {
				return nil, err
			}
			return eebudget.FromLegacy(bs, rows, time.Now())
		},
	})
}
