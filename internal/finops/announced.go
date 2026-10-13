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
	"github.com/TAIPANBOX/costcrew/internal/world"
)

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
	// Split is how a decision that names no team divides its step across the
	// teams on its desk; nil when it names one (ANNOUNCED-DRIVER-SPEC.md section 11).
	Split []TeamShare
}

// TeamShare is one team's part of a decision that names no team: its median
// daily spend over the window before the start, its share of the service's
// total of those medians in percent, and the change a day its share takes.
type TeamShare struct {
	Team     string
	Median   money.Cents
	Percent  float64
	Expected money.Cents
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

		spend, err := decisionDayCents(db, d, period, "31")
		if err != nil {
			return nil, err
		}
		base, haveBase, err := estate.SpendMedian(db, d.Source, d.Scope, d.Team, d.Start)
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

		if d.Team == "" {
			split, err := announcedSplit(db, d)
			if err != nil {
				return nil, err
			}
			e.Split = split
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

// decisionDayCents is the spend a decision is measured against: its named
// team's when it names one, the service's on its desk when it does not
// (ANNOUNCED-DRIVER-SPEC.md section 11).
func decisionDayCents(db *sql.DB, d world.Driver, period, cut string) (map[string]money.Cents, error) {
	if d.Kind == "announced" && d.Team != "" {
		return teamDayCents(db, d.Source, d.Scope, d.Team, period, cut)
	}
	return dayCents(db, d.Source, d.Scope, period, cut)
}

// teamDayCents is one team's billed cents per day on one desk's service within
// a period, up to the cutoff day: dayCents narrowed to a single team.
func teamDayCents(db *sql.DB, source, service, team, period, cut string) (map[string]money.Cents, error) {
	rows, err := db.Query(`SELECT day, SUM(billed_cents) FROM charges
		WHERE source=? AND service=? AND team=? AND substr(day,1,7)=? AND substr(day,9,2)<=?
		GROUP BY day`, source, service, team, period, cut)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]money.Cents{}
	for rows.Next() {
		var day string
		var v int64
		if err := rows.Scan(&day, &v); err != nil {
			return nil, err
		}
		out[day] = money.Cents(v)
	}
	return out, rows.Err()
}

// announcedSplit is how a decision that names no team divides its step: each
// team on the desk with spend before the start takes its share in proportion to
// its median (estate.SplitByMedian). Sorted by team name.
func announcedSplit(db *sql.DB, d world.Driver) ([]TeamShare, error) {
	medians, err := estate.TeamSpendMedians(db, d.Source, d.Scope, d.Start)
	if err != nil {
		return nil, err
	}
	expected := estate.SplitByMedian(money.Cents(*d.Expected), medians)
	var total money.Cents
	for _, m := range medians {
		total += m
	}
	teams := make([]string, 0, len(medians))
	for team := range medians {
		teams = append(teams, team)
	}
	sort.Strings(teams)
	out := make([]TeamShare, 0, len(teams))
	for _, team := range teams {
		s := TeamShare{Team: team, Median: medians[team], Expected: expected[team]}
		if total > 0 {
			s.Percent = float64(medians[team]) * 100 / float64(total)
		}
		out = append(out, s)
	}
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
		// A window with a valid start is always counted in its own start month,
		// so this is unreachable for a row the console wrote; it is an error,
		// not a silent empty record, if the store ever holds one that is not.
		return AnnouncedEffect{}, false, fmt.Errorf("announced decision %q on %s is registered for %s but is not counted in that month", label, source, d.Start)
	}
	return AnnouncedEffect{}, false, nil
}
