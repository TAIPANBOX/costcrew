package estate

// The bound on an announced saving (ANNOUNCED-DRIVER-SPEC.md section 11, finding
// (b)). Save time and apply time both ask this one function.

import (
	"database/sql"
	"fmt"

	"github.com/TAIPANBOX/costcrew/internal/money"
)

// SavingRefusal is the reason a saving is refused, or "" when it is within the
// bound. Only a saving (a negative daily step) is bounded: it may not exceed the
// median daily spend of the service on the desk over the PreStartDays before
// start, or the named team's own median when a team is named. A saving with no
// spend in that window to measure against is refused. An increase is never
// bounded: a model swap can be five to twenty times the spend it replaces.
func SavingRefusal(db *sql.DB, desk, service, team, start string, daily money.Cents) (string, error) {
	if daily >= 0 {
		return "", nil
	}
	median, ok, err := SpendMedian(db, desk, service, team, start)
	if err != nil {
		return "", err
	}
	from, to := PreStartWindow(start)
	what := "the service " + service
	if team != "" {
		what = "team " + team + " on the service " + service
	}
	where := fmt.Sprintf("%s on %s over the %d days before %s (%s to %s)", what, desk, PreStartDays, start, from, to)
	if !ok {
		return fmt.Sprintf("a saving of %s a day has no spend to measure against: %s has no charge in that window",
			daily.String(), where), nil
	}
	// Compared without negating daily, so the most negative value cannot overflow.
	if daily < -median {
		return fmt.Sprintf("a saving of %s a day is larger than the median daily spend of %s for %s",
			daily.String(), median.String(), where), nil
	}
	return "", nil
}
