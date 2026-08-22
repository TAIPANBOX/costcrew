package finops

import (
	"database/sql"

	"github.com/TAIPANBOX/costcrew/internal/money"
)

// Results is what the crew is for, answered in the order a stakeholder asks
// it: how much money is on the table, what did it cost to find, and what needs
// a person.
//
// One word is load-bearing throughout: FOUND, never saved. Nothing is saved
// until somebody acts, and a console that reports found money as saved is one
// whose numbers stop being believed the first time finance checks them against
// the invoice.
type Results struct {
	Period string

	// Money the crew put on the table this period.
	FoundMonthly money.Cents
	FoundAnnual  money.Cents

	// What is still unexplained, which is the honest headline: an anomaly
	// nobody has looked at for three weeks says more than one that closed.
	OpenAnomalies int
	OpenMoney     money.Cents
	OldestOpen    string

	// What the crew itself cost.
	CrewSpend money.Cents
	Tasks     int
	Posted    int
	Returned  int

	// What needs a person, which is what the section should end in.
	AwaitingStamp    int
	AwaitingDecision int

	Estate money.Cents
}

// Compute reads the period's answer out of the store.
//
// Every number here is a count or a sum over rows the console already holds:
// nothing is estimated, and nothing is carried forward from a previous run.
func Compute(db *sql.DB, period string) (Results, error) {
	r := Results{Period: period}

	// Found money is the excess on anomalies that have been explained or
	// accepted: an open one is not found yet, it is only noticed, and a
	// dismissed one was decided against.
	if err := db.QueryRow(`SELECT COALESCE(SUM(ABS(excess_cents)),0), COUNT(*)
		FROM anomalies WHERE state IN ('explained','accepted')`).
		Scan(&r.FoundMonthly, &r.AwaitingDecision); err != nil {
		return r, err
	}
	// A daily excess repeats: a spike is one day, a step is every day after
	// it. Twenty-one working days is the honest monthly figure for a step and
	// an overstatement for a spike, so the page says which it is rather than
	// this function pretending the two are the same.
	r.FoundAnnual = r.FoundMonthly * 12

	if err := db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(ABS(excess_cents)),0),
		COALESCE(MIN(day),'') FROM anomalies WHERE state='open'`).
		Scan(&r.OpenAnomalies, &r.OpenMoney, &r.OldestOpen); err != nil {
		return r, err
	}

	if err := db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(spent_cents),0),
		COALESCE(SUM(CASE WHEN state='posted' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN state='returned' THEN 1 ELSE 0 END),0)
		FROM tasks`).
		Scan(&r.Tasks, &r.CrewSpend, &r.Posted, &r.Returned); err != nil {
		return r, err
	}

	// Counted from the DELIVERABLES, not from a task state. The two drift: a
	// task sits in "active" while its deliverable is already written and
	// waiting, which is exactly the case a reviewer needs to see. Counting
	// task states reported zero while six drafts sat unread.
	if err := db.QueryRow(`SELECT COUNT(DISTINCT task) FROM artifacts
		WHERE state='draft'`).Scan(&r.AwaitingStamp); err != nil {
		return r, err
	}

	if err := db.QueryRow(`SELECT COALESCE(SUM(billed_cents),0) FROM charges
		WHERE substr(day,1,7)=?`, period).Scan(&r.Estate); err != nil {
		return r, err
	}
	return r, nil
}

// Return is the crew's own economics: money found against what the crew cost
// to run. It is a ratio somebody will quote, so it refuses to exist rather
// than divide by zero.
func (r Results) Return() (float64, bool) {
	if r.CrewSpend == 0 {
		return 0, false
	}
	return float64(r.FoundMonthly) / float64(r.CrewSpend), true
}

// FirstPass across the whole crew.
func (r Results) FirstPass() (float64, bool) {
	judged := r.Posted + r.Returned
	if judged == 0 {
		return 0, false
	}
	return float64(r.Posted) / float64(judged) * 100, true
}
