//go:build ee

// SPDX-License-Identifier: LicenseRef-KaaS-EE

package admin

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strconv"

	"github.com/BurntSushi/toml"
	"github.com/KaaS-team/factory/ee/budget"
	"github.com/KaaS-team/factory/ee/contract"
	"github.com/KaaS-team/factory/ee/policy"
)

// budgetRow is one effective limit and what has been used of it.
type budgetRow struct {
	Scope, ID, Period string
	Limit, Used       string // in dollars, for people
	LimitUSD          string // the form value
	Max, Value        int64  // micros, for the usage bar
	Percent           int
	Status            string // pass under 80%, wait from 80%, fail at or over the limit
	Known             bool   // usage is known (a Budget is configured)
	Layer             string // the narrowest layer that sets this key, which an edit changes
}

type budgetsData struct {
	Rows     []budgetRow
	Problem  string
	Problems []policy.Violation
	CanEdit  bool
	NoUsage  bool
}

func (c *Console) budgets(w http.ResponseWriter, r *http.Request, p contract.Principal) {
	d := c.budgetRows(r)
	d.CanEdit = c.canEdit(r, p, contract.ActionBudgetEdit)
	c.page.render(w, http.StatusOK, "budgets", "Budgets", notice(r), d)
}

func (c *Console) budgetRows(r *http.Request) budgetsData {
	var d budgetsData
	d.NoUsage = c.cfg.Budget == nil
	eff, err := policy.Resolve(c.cfg.Policy)
	if err != nil {
		d.Problem = "The policy layers do not compile, so no limits are in force. Fix them on the Policy page."
		return d
	}
	if eff == nil {
		return d
	}
	for _, l := range eff.Budgets() {
		key := contract.BudgetKey{Scope: l.Scope, ID: l.ID}
		row := budgetRow{Scope: string(l.Scope), ID: l.ID, Period: string(l.Period), Max: int64(budget.Micros(l.USD)),
			Limit: usd(budget.Micros(l.USD)), LimitUSD: strconv.FormatFloat(l.USD, 'f', -1, 64), Layer: string(c.layerOf(l))}
		if c.cfg.Budget != nil {
			if left, ok, err := c.cfg.Budget.Remaining(r.Context(), key); err == nil && ok {
				row.Known = true
				row.Value = row.Max - int64(left)
				row.Used = usd(contract.Micros(row.Value))
				if row.Max > 0 {
					row.Percent = int(row.Value * 100 / row.Max)
				}
				row.Status = level(row.Percent)
				row.Value = min(max(row.Value, 0), row.Max)
			}
		}
		d.Rows = append(d.Rows, row)
	}
	return d
}

// level is the status of a usage percentage.
func level(pct int) string {
	switch {
	case pct >= 100:
		return "fail"
	case pct >= 80:
		return "wait"
	}
	return "pass"
}

// usd prints micros as dollars with cents.
func usd(m contract.Micros) string {
	neg := m < 0
	if neg {
		m = -m
	}
	cents := (int64(m) + 5000) / 10000
	s := strconv.FormatInt(cents/100, 10)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	s = fmt.Sprintf("$%s.%02d", s, cents%100)
	if neg {
		s = "-" + s
	}
	return s
}

// layerOf is the narrowest configured layer that names l's key and period; that file holds the
// limit in force, since lower layers may only narrow.
func (c *Console) layerOf(l contract.BudgetLimit) contract.Layer {
	for _, ly := range slices.Backward(layers) {
		if f, _, ok := c.layerFile(ly); ok && budgetIndex(f, l.Scope, l.ID, l.Period) >= 0 {
			return ly
		}
	}
	return contract.LayerOrg
}

func (c *Console) layerFile(l contract.Layer) (contract.PolicyFile, string, bool) {
	path, _ := c.layerPath(l)
	if path == "" {
		return contract.PolicyFile{}, "", false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return contract.PolicyFile{}, path, false
	}
	f, err := policy.Decode(data)
	return f, path, err == nil
}

func budgetIndex(f contract.PolicyFile, s contract.BudgetScope, id string, per contract.Period) int {
	return slices.IndexFunc(f.Budgets, func(b contract.BudgetLimit) bool { return b.Scope == s && b.ID == id && b.Period == per })
}

// budgetSave changes one limit in the layer that sets it, or adds a new one to the org layer.
// The new file goes through the same check as the policy editor before it is written.
func (c *Console) budgetSave(w http.ResponseWriter, r *http.Request, p contract.Principal) {
	scope, id := contract.BudgetScope(r.PostFormValue("scope")), r.PostFormValue("id")
	per := contract.Period(r.PostFormValue("period"))
	amount, err := strconv.ParseFloat(r.PostFormValue("usd"), 64)
	if err != nil || amount < 0 || id == "" {
		c.budgetProblem(w, r, "Give a limit in dollars, zero or more, and the key it applies to.")
		return
	}
	l := c.layerOf(contract.BudgetLimit{Scope: scope, ID: id, Period: per})
	f, path, ok := c.layerFile(l)
	_, signed := c.layerPath(l)
	switch {
	case !ok:
		c.budgetProblem(w, r, "The "+string(l)+" policy file could not be read.")
		return
	case signed:
		c.budgetProblem(w, r, "The "+string(l)+" layer is signed. Change its budget in its config repo.")
		return
	}
	from := "none"
	if i := budgetIndex(f, scope, id, per); i >= 0 {
		from = strconv.FormatFloat(f.Budgets[i].USD, 'f', -1, 64)
		f.Budgets[i].USD = amount
	} else {
		f.Budgets = append(f.Budgets, contract.BudgetLimit{Scope: scope, ID: id, USD: amount, Period: per})
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(f); err != nil {
		c.budgetProblem(w, r, "The policy file could not be written.")
		return
	}
	ev := map[string]string{"layer": string(l), "budget.scope": string(scope), "budget.id": id, "budget.period": string(per),
		"usd.from": from, "usd.to": strconv.FormatFloat(amount, 'f', -1, 64)}
	if problems := c.save(r, p, contract.ActionBudgetEdit, l, path, buf.Bytes(), ev); problems != nil {
		d := c.budgetRows(r)
		d.CanEdit, d.Problems = true, problems
		c.page.render(w, http.StatusUnprocessableEntity, "budgets", "Budgets", "", d)
		return
	}
	done(w, r, "/admin/budgets", "budget-saved")
}

func (c *Console) budgetProblem(w http.ResponseWriter, r *http.Request, msg string) {
	d := c.budgetRows(r)
	d.CanEdit, d.Problem = true, msg
	c.page.render(w, http.StatusUnprocessableEntity, "budgets", "Budgets", "", d)
}
