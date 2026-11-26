// SPDX-License-Identifier: Apache-2.0

package contract

import "context"

// BudgetScope is what a budget limit is counted against.
type BudgetScope string

// The scopes. One run's spend counts against every key that names it: its org, its team, its
// factory instance, the bot that ran it and the action it was spent on.
const (
	BudgetOrg     BudgetScope = "org"     // ID is the org id
	BudgetTeam    BudgetScope = "team"    // ID is the team id
	BudgetFactory BudgetScope = "factory" // ID is the factory instance name
	BudgetBot     BudgetScope = "bot"     // ID is the bot's Principal.ID
	BudgetAction  BudgetScope = "action"  // ID is an Action, such as run.start
)

// BudgetKey names one budget.
type BudgetKey struct {
	Scope BudgetScope `json:"scope"`
	ID    string      `json:"id"`
}

// Micros is an amount of US dollars in millionths. Budgets count in integers so every port adds
// the same way; a BudgetLimit's USD becomes Micros by rounding USD*1e6 to the nearest integer.
type Micros int64

// Reservation is money held for work not yet done.
type Reservation struct {
	ID     string      `json:"id"`
	Keys   []BudgetKey `json:"keys"`
	Amount Micros      `json:"amount"`
}

// Budget holds and spends money against limits. A key with no limit is unlimited.
//
// For a key, spent is the sum of committed amounts in the current period and held is the sum of
// open reservations naming it; remaining is limit - spent - held, and may go below zero only
// through Commit.
type Budget interface {
	// Reserve holds amount against every key at once. If amount is more than any key's
	// remaining, it returns an error wrapping ErrBudgetExceeded that names the key, and holds
	// nothing on any key. amount must be > 0.
	Reserve(ctx context.Context, keys []BudgetKey, amount Micros) (Reservation, error)

	// Commit records actual as spent on the reservation's keys and closes it. actual may be more
	// or less than the amount held; the hold is replaced by actual, even past the limit, since the
	// money is already spent. A closed or unknown id returns ErrUnknownReservation.
	Commit(ctx context.Context, reservationID string, actual Micros) error

	// Release closes the reservation with nothing spent. A closed or unknown id returns
	// ErrUnknownReservation.
	Release(ctx context.Context, reservationID string) error

	// Remaining returns the key's remaining amount; ok is false for a key with no limit.
	Remaining(ctx context.Context, key BudgetKey) (remaining Micros, ok bool, err error)
}
