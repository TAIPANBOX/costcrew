package crew

// The supervisor's own plan-ask ledger. B4-STEP-TWO-SPEC.md section 4: "one
// call per sprint, priced before it is made, metered through the gateway,
// journaled like every other call" and "settles the actual cost into the
// supervisor's own spend (the same ledger a task's call settles into, so
// SpendInMonth sees it)".
//
// A dedicated table (plan_asks, added to Schema in crew.go), not a tasks
// row: the call this step adds happens BEFORE the sprint it plans is ever
// approved -- crew.Approve refuses a second time on a label already on the
// board, and Approve itself is unchanged, so nothing here may create a
// sprints row under the plan's own label without breaking that check. There
// is no sprints row yet for SpendInMonth's existing tasks-JOIN-sprints query
// to find, which is why this is a second, small ledger keyed by calendar
// month rather than a reservation on some pre-existing task.
//
// This is the SAME shape provenance.go's own SettleLiveSpend already holds
// (the true amount kept in micro-dollars first, rounded to cents once, "up,
// never down": a call that cost money must not record less than it cost)
// applied to one settlement at a time rather than SettleLiveSpend's
// largest-remainder distribution across many rows, because a plan ask is
// exactly one call and there is nothing to distribute.

import (
	"database/sql"

	"github.com/TAIPANBOX/costcrew/internal/money"
)

// PlanAskOutcome is what became of one planning call.
type PlanAskOutcome string

const (
	PlanAskAccepted PlanAskOutcome = "accepted"
	PlanAskRefused  PlanAskOutcome = "refused"
)

// SettlePlanAsk records one planning call's cost and outcome, and returns
// the cents it booked. cents is rounded UP from micros -- "up, never down",
// the same rule SettleLiveSpend already holds -- so a call that cost a
// fraction of a cent is never recorded as free. micros is 0 for a call that
// was refused before it was ever made (no gateway configured, or the worst
// case exceeded the supervisor's own PerTask): SettlePlanAsk still writes a
// row for the journal to point at, and it books zero cents, which is the
// truth.
//
// The ledger carries no item detail -- section 4's own words are "the cost
// and the outcome" -- so it takes none: the items a person actually
// approves travel through crew.Approve's own Plan value on the
// /approve-model round trip, re-validated fresh against the roster rather
// than trusted from a stored copy of what an earlier moment accepted.
func SettlePlanAsk(db *sql.DB, sprintLabel, month, analyst string, micros int64, outcome PlanAskOutcome, reason string) (money.Cents, error) {
	cents := (micros + 9_999) / 10_000
	if _, err := db.Exec(`INSERT INTO plan_asks
		(sprint_label, month, analyst, micros, cents, outcome, reason, created)
		VALUES (?,?,?,?,?,?,?, datetime('now'))`,
		sprintLabel, month, analyst, micros, cents, string(outcome), nullIf(reason)); err != nil {
		return 0, err
	}
	return money.Cents(cents), nil
}
