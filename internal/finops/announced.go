package finops

// ANNOUNCED-DRIVER-SPEC.md section 5: once a month closes, every announced
// driver has its measured effect beside the announced one, and the decisions
// page shows both and how many days before its start the owner answered.

import (
	"database/sql"
	"fmt"
	"sort"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/estate"
	"github.com/TAIPANBOX/costcrew/internal/money"
)

// announcedBaselineDays is how far back of an announced driver's start its
// clean baseline is drawn: the median of the scope's daily spend over the up to
// 28 days before Start (section 5).
const announcedBaselineDays = 28

// AnnouncedEffect is one announced driver's record for one month. Announced is
// the amount the owner's decision said the service's daily cost would move by.
// Measured is the mean, over the covered days of the month that have spend, of
// that day's spend less the clean baseline; Days is how many such days there
// were, and zero means there is no measured figure.
type AnnouncedEffect struct {
	Label, Source, Scope, Start, End string
	Announced, Measured              money.Cents
	Days                             int
	// Answered is whether an applied option carries this decision's label.
	Answered bool
	// AnsweredDaysBeforeStart is positive when the answer came that many days
	// before Start, and negative when it came after.
	AnsweredDaysBeforeStart int
}

// AnnouncedEffects returns every announced driver with a covered day in the
// period, sorted by how far its measured effect is from its announcement,
// furthest first. An effect with no measured figure sorts after every effect
// that has one. Ties fall back to desk and label, so the same data always gives
// the same order.
func AnnouncedEffects(db *sql.DB, period string) ([]AnnouncedEffect, error) {
	all, err := estate.Drivers(db)
	if err != nil {
		return nil, err
	}
	inMonth := daysInMonth(period)
	monthStart := period + "-01"
	monthEnd := fmt.Sprintf("%s-%02d", period, inMonth)

	var out []AnnouncedEffect
	for _, d := range all {
		if d.Kind != "announced" || d.Expected == nil {
			continue
		}
		start, end, ok := driverWindowInMonth(d, monthStart, monthEnd)
		if !ok {
			continue
		}
		e := AnnouncedEffect{
			Label: d.Label, Source: d.Source, Scope: d.Scope, Start: d.Start, End: d.End,
			Announced: money.Cents(*d.Expected),
		}

		spend, err := dayCents(db, d.Source, d.Scope, period, "31")
		if err != nil {
			return nil, err
		}
		base, haveBase, err := announcedBaseline(db, d.Source, d.Scope, d.Start)
		if err != nil {
			return nil, err
		}
		if haveBase {
			// Integer sums are order-free, so ranging the map is deterministic here.
			var sum money.Cents
			n := 0
			for day, v := range spend {
				if day < start || day > end {
					continue
				}
				sum += v - base
				n++
			}
			if n > 0 {
				// One division, at the end: the mean of the covered days.
				e.Days = n
				e.Measured = money.Cents(int64(sum) / int64(n))
			}
		}

		decided, answered, err := announcedAnswer(db, d.Label)
		if err != nil {
			return nil, err
		}
		if answered {
			e.Answered = true
			e.AnsweredDaysBeforeStart = signedDays(decided, d.Start)
		}
		out = append(out, e)
	}

	sort.SliceStable(out, func(i, j int) bool {
		gi, mi := effectGap(out[i])
		gj, mj := effectGap(out[j])
		if mi != mj {
			return mi
		}
		if gi != gj {
			return gi > gj
		}
		if out[i].Source != out[j].Source {
			return out[i].Source < out[j].Source
		}
		return out[i].Label < out[j].Label
	})
	return out, nil
}

// effectGap is how far an effect's measured figure is from its announced one,
// and whether there is a measured figure at all.
func effectGap(e AnnouncedEffect) (money.Cents, bool) {
	if e.Days == 0 {
		return 0, false
	}
	return (e.Measured - e.Announced).Abs(), true
}

// announcedBaseline is the median of the scope's daily spend over the up to
// announcedBaselineDays before start, on the days that have a charge. It is
// false when no day before the start has one.
func announcedBaseline(db *sql.DB, source, service, start string) (money.Cents, bool, error) {
	from := shiftDay(start, -announcedBaselineDays)
	to := shiftDay(start, -1)
	rows, err := db.Query(`SELECT day, SUM(billed_cents) FROM charges
		WHERE source=? AND service=? AND day>=? AND day<=? GROUP BY day`,
		source, service, from, to)
	if err != nil {
		return 0, false, err
	}
	defer rows.Close()
	var vals []money.Cents
	for rows.Next() {
		var day string
		var v int64
		if err := rows.Scan(&day, &v); err != nil {
			return 0, false, err
		}
		vals = append(vals, money.Cents(v))
	}
	if err := rows.Err(); err != nil {
		return 0, false, err
	}
	if len(vals) == 0 {
		return 0, false, nil
	}
	return medianCents(vals), true, nil
}

// announcedAnswer finds the day an applied option carrying this decision's
// label was answered. applyAnnounced writes the option's summary as the
// drivers row's label, and the row keeps no option id, so the link is by
// label: two applied options with one summary would share the newer answer.
func announcedAnswer(db *sql.DB, label string) (string, bool, error) {
	var decided string
	err := db.QueryRow(`SELECT COALESCE(decided_at,'') FROM artifact_options
		WHERE class='driver.announced' AND state='applied' AND summary=?
		ORDER BY decided_at DESC LIMIT 1`, label).Scan(&decided)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if len(decided) < len("2006-01-02") {
		return "", false, nil
	}
	return decided[:len("2006-01-02")], true, nil
}

// shiftDay is day moved by n days, as the same YYYY-MM-DD string. A day that
// does not parse is returned unchanged.
func shiftDay(day string, n int) string {
	t, err := time.Parse("2006-01-02", day)
	if err != nil {
		return day
	}
	return t.AddDate(0, 0, n).Format("2006-01-02")
}

// signedDays is to minus from in whole days, negative when to is earlier.
// Unlike daysBetween, which clamps a negative span to zero, it keeps the sign,
// because an answer given after a start is a fact the page must show.
func signedDays(from, to string) int {
	f, err1 := time.Parse("2006-01-02", from)
	t, err2 := time.Parse("2006-01-02", to)
	if err1 != nil || err2 != nil {
		return 0
	}
	return int(t.Sub(f).Hours() / 24)
}

// medianCents is the median of vals. For an even count it is the integer mean
// of the two middle values, rounded down, as internal/detect's median is.
func medianCents(vals []money.Cents) money.Cents {
	s := append([]money.Cents(nil), vals...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

// AnnouncedEffectOf is the record of the announced driver with this label on
// this desk. Its measured figure is the one for the month its window starts in,
// the first month it can be measured. found is false when no announced driver
// carries that label on that desk.
func AnnouncedEffectOf(db *sql.DB, label, source string) (AnnouncedEffect, bool, error) {
	all, err := estate.Drivers(db)
	if err != nil {
		return AnnouncedEffect{}, false, err
	}
	for _, d := range all {
		if d.Kind != "announced" || d.Expected == nil || d.Label != label || d.Source != source {
			continue
		}
		if len(d.Start) < len("2006-01") {
			return AnnouncedEffect{}, false, nil
		}
		effects, err := AnnouncedEffects(db, d.Start[:len("2006-01")])
		if err != nil {
			return AnnouncedEffect{}, false, err
		}
		for _, e := range effects {
			if e.Label == label && e.Source == source && e.Start == d.Start {
				return e, true, nil
			}
		}
		// Its window overlaps its own start month, so AnnouncedEffects counts it;
		// this is reached only for a window that does not. The record still
		// carries the answer, and no measured figure.
		e := AnnouncedEffect{Label: label, Source: source, Scope: d.Scope, Start: d.Start, End: d.End,
			Announced: money.Cents(*d.Expected)}
		decided, answered, err := announcedAnswer(db, label)
		if err != nil {
			return AnnouncedEffect{}, false, err
		}
		if answered {
			e.Answered = true
			e.AnsweredDaysBeforeStart = signedDays(decided, d.Start)
		}
		return e, true, nil
	}
	return AnnouncedEffect{}, false, nil
}
