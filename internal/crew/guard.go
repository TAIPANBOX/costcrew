package crew

import (
	"database/sql"
	"fmt"

	"github.com/TAIPANBOX/costcrew/internal/money"
)

// Recorder is the same interface anomaly.Recorder is, restated so this package
// does not depend on the one that depends on it.
type Recorder interface {
	Emit(kind, actor, severity string, data map[string]any, onBehalfOf []string) error
}

// CheckGuards says, out loud, which analysts went past the guard they were
// given this month.
//
// The console has always been able to SHOW this: the crew page counts it and
// the card draws a bar. Nothing said it to anybody else, so an operator who
// was not looking at the page never learned, and the estate's own alerting
// could not tell them because the event did not exist.
//
// It reports rather than enforces, and the event it emits says so. The stack's
// budget_exhausted carries a denied verdict, and this console denies nothing;
// budget_threshold is a warning with no verdict, which is what is true. Making
// the guard actually bite is a proxy's job, not a console's, and pretending
// otherwise in a record somebody audits would be worse than not recording it.
//
// Idempotent by the caller: it takes the month, so running it twice for the
// same month emits twice. The caller decides when a month is worth saying
// again, because that is a question about notification and not about money.
func CheckGuards(db *sql.DB, month string, rec Recorder) (past int, by money.Cents, err error) {
	if rec == nil {
		return 0, 0, nil
	}
	roster, err := Roster(db)
	if err != nil {
		return 0, 0, err
	}
	spend, err := SpendInMonth(db, month)
	if err != nil {
		return 0, 0, err
	}
	for _, a := range roster {
		if a.Monthly <= 0 {
			continue
		}
		spent := spend[a.Name]
		if spent <= a.Monthly {
			continue
		}
		over := spent - a.Monthly
		past++
		by += over
		// Severity by how far past, not by the fact of being past. One cent
		// over a guard and twice the guard are different mornings.
		// `low`, not "warning": the shared envelope's severity is a closed enum
		// (agent-passport SPEC 6.1) and "warning" is not in it, so every
		// budget_threshold emitted in this band went out as a line any
		// validating consumer refuses whole. This is the COMMON band, an
		// analyst between its guard and one and a half times it.
		sev := "low"
		switch {
		case spent >= a.Monthly*2:
			sev = "high"
		case spent >= a.Monthly*3/2:
			sev = "medium"
		}
		if e := rec.Emit("guard_passed", a.Name, sev, map[string]any{
			"analyst":      a.Name,
			"month":        month,
			"desk":         a.Desk,
			"owner":        a.Owner,
			"guard":        a.Monthly.String(),
			"spent":        spent.String(),
			"over":         over.String(),
			"over_cents":   int64(over),
			"over_percent": fmt.Sprintf("%.0f", float64(over)/float64(a.Monthly)*100),
			"enforced":     false,
			"note": "This console records the guard; it does not enforce it. " +
				"Nothing was refused.",
		}, nil); e != nil {
			return past, by, e
		}
	}
	return past, by, nil
}
