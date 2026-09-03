package anomaly

// Closure: C1-SPEC.md section 2. Detection already stamps detected_at
// (Run) and a closing transition already stamps closed_at (transition,
// above); this file adds no new column and no new transition, only the day
// count both the closure KPI (internal/finops) and the anomaly page's own
// "open for N days" / "closed after N days" line read out of the two.

import "time"

// DaysBetween is the whole number of days from `from` to `to`, both
// RFC3339 timestamps the way detected_at and closed_at are stamped
// (Run, transition). It is the ONE basis every closure figure in this
// console shares, so a per-desk median and a per-anomaly "open for N days"
// line can never silently disagree about what a day even means.
//
// ok is false when either does not parse, or when to is before from:
// closing before detection is a clock problem, not a day count, and
// guessing at one would be believed the moment it was printed. Floors
// rather than rounds -- an anomaly closed a few hours after detection is
// open for zero days, "closed the same day" in the words a person reading
// the page would use.
func DaysBetween(from, to string) (days int, ok bool) {
	f, err := time.Parse(time.RFC3339, from)
	if err != nil {
		return 0, false
	}
	t, err := time.Parse(time.RFC3339, to)
	if err != nil {
		return 0, false
	}
	d := t.Sub(f)
	if d < 0 {
		return 0, false
	}
	return int(d.Hours() / 24), true
}
