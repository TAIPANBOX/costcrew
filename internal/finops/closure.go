package finops

// The closure KPI: C1-SPEC.md section 2. How long the queue on one desk
// actually takes, from detection to the stamp that closed it, per desk over
// a month -- refusing on a desk with no closure, the same shape every KPI
// in KPIs() (kpi.go) already refuses rather than inventing a number it has
// no evidence for. Not wired into KPIs() itself: this step's own parity
// scope is exactly the anomalies list and the anomaly page (section 4), and
// a new row on /kpis would widen that diff. Surfacing it there is a natural
// next step and is named in this PR's body.

import (
	"database/sql"
	"fmt"
	"sort"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
)

// ClosureResult is the closure KPI for one desk.
type ClosureResult struct {
	Desk    string
	Days    float64 // median days from detected_at to closed_at
	N       int     // how many closed anomalies the median is actually over
	HasVal  bool
	Blocked string // why it cannot be computed, when it cannot
	Note    string // e.g. rows excluded for a detected_at that would not parse
}

// AnomalyClosureDays reports the median days from detection to close for
// every anomaly on desk closed within month (a "2006-01" value, or "" for
// every month the estate holds), using anomaly.DaysBetween as its basis --
// the same day count the anomaly page's own "closed after N days" line
// reads, so the two can never silently disagree about what a day even
// means. Refuses when the desk has closed nothing in that window: a median
// of zero rows is not a number, it is a placeholder wearing one.
//
// A row whose detected_at will not parse is excluded rather than crashing
// the whole desk's figure or being silently averaged in as some invented
// day count; Note says how many were excluded and why, so the figure this
// returns is never mistaken for one computed over every closed row.
func AnomalyClosureDays(db *sql.DB, desk, month string) (ClosureResult, error) {
	res := ClosureResult{Desk: desk}

	q := `SELECT detected_at, closed_at FROM anomalies
		WHERE source = ? AND closed_at IS NOT NULL AND closed_at <> ''`
	args := []any{desk}
	if month != "" {
		q += ` AND substr(closed_at,1,7) = ?`
		args = append(args, month)
	}
	rows, err := db.Query(q, args...)
	if err != nil {
		return res, err
	}
	defer rows.Close()

	var days []int
	var unparseable int
	for rows.Next() {
		var detectedAt, closedAt string
		if err := rows.Scan(&detectedAt, &closedAt); err != nil {
			return res, err
		}
		d, ok := anomaly.DaysBetween(detectedAt, closedAt)
		if !ok {
			unparseable++
			continue
		}
		days = append(days, d)
	}
	if err := rows.Err(); err != nil {
		return res, err
	}

	if unparseable > 0 {
		noun := "anomaly"
		if unparseable != 1 {
			noun = "anomalies"
		}
		res.Note = fmt.Sprintf(
			"%d closed %s had a detected_at that would not parse and were excluded from the median",
			unparseable, noun)
	}

	if len(days) == 0 {
		scope := "any month on record"
		if month != "" {
			scope = month
		}
		res.Blocked = fmt.Sprintf("nothing on the %s desk has closed in %s that could be measured", desk, scope)
		if unparseable > 0 {
			res.Blocked += fmt.Sprintf(" (%d excluded for a detected_at that would not parse)", unparseable)
		}
		return res, nil
	}

	res.Days = median(days)
	res.N = len(days)
	res.HasVal = true
	return res, nil
}

// median is the middle value of a sorted copy of vals: the exact value for
// an odd count, the average of the two middle values for an even one. A
// copy, so the caller's own slice order is never disturbed by a caller that
// reuses it afterwards.
func median(vals []int) float64 {
	s := append([]int(nil), vals...)
	sort.Ints(s)
	n := len(s)
	if n%2 == 1 {
		return float64(s[n/2])
	}
	return float64(s[n/2-1]+s[n/2]) / 2
}
