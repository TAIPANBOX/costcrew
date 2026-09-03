package finops

// The data-quality analyst's own measurement: C9-SPEC.md section 2.
// `@yurii 2026-09-02`, the ask this role exists to answer: "більш повною
// мірою замінити людей на цих посадах." Its mission in roles.yaml: "Check
// that what the console reports can be traced to a charge, and stop the
// crew when it cannot."
//
// Three figures per source and desk, every day, each against a threshold
// roles.yaml names and nothing this file invents:
//
//   - freshness: days since the last charge, against T.stale.
//   - tag coverage / untagged share: the share of this source's month with
//     no team from FOCUS Tags or the store's allocation -- charges.team IS
//     NULL, which is exactly the pot internal/finops/allocation.go already
//     calls "Shared" -- against T.untagged.
//   - unallocated share: the share of that same pot an allocation RULE
//     still could not place (Allocate's own Unallocated, the estate-wide
//     KPI already named "Cost with no owner"), per source, against the
//     SAME T.untagged: roles.yaml's own meaning field for that threshold is
//     "unallocated share above which data quality reports it", so untagged
//     and unallocated are two different questions in this console's
//     existing vocabulary, both measured against the one percentage
//     roles.yaml gives.
//
// Every comparison runs in integer cents (crossesShare), never a float
// division: two orderings of the same rows must never cross a boundary
// differently, the same property money's own package header explains for
// summation.

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/money"
	"github.com/TAIPANBOX/costcrew/internal/world"
)

// ErrThresholdMissing is refused rather than guessed: C9-SPEC.md section 4's
// own hostile case, "a threshold row missing from the roles data (refuse to
// measure, say so)". roles.yaml is the authority; a name it does not carry,
// or a value this file cannot parse as a whole number, measures nothing.
var ErrThresholdMissing = errors.New("data quality cannot measure: roles.yaml names no usable value for this threshold")

// Finding is one source's three figures for one day, cents-exact.
type Finding struct {
	Source string

	// Freshness, against T.stale.
	HasCharge      bool // false when this source has never been charged at all
	FreshnessDays  int  // days since the last charge; meaningless when !HasCharge
	StaleThreshold int  // roles.yaml's T.stale
	Stale          bool // !HasCharge, or FreshnessDays >= StaleThreshold

	// Tag coverage and unallocated share, both against T.untagged, both
	// scoped to this source's own current month (Source's charges whose day
	// falls in the same month as `day`).
	MonthCents           money.Cents // this source's direct + shared cost this month
	UntaggedCents        money.Cents // charges.team IS NULL this month (allocation.go's "Shared")
	UntaggedPct          float64     // for display; the CROSSING test uses cents, never this
	UnallocatedCents     money.Cents // the share of UntaggedCents no allocation rule could place
	UnallocatedPct       float64
	UntaggedThresholdPct int64 // roles.yaml's T.untagged, whole percent
	UntaggedCrossed      bool
	UnallocatedCrossed   bool

	Crossed bool   // Stale || UntaggedCrossed || UnallocatedCrossed
	Reason  string // every crossed threshold, in one line; empty when none crossed
}

// DataQuality measures every desk (world.Desks) for day, the date the
// data-quality analyst's report is dated. Cents-exact throughout: every
// crossing decision is an integer comparison over the cents Allocate and the
// charges table already hold, never a float division.
func DataQuality(db *sql.DB, day string) ([]Finding, error) {
	staleDays, err := wholeNumberThreshold("T.stale", false)
	if err != nil {
		return nil, err
	}
	untaggedPct, err := wholeNumberThreshold("T.untagged", true)
	if err != nil {
		return nil, err
	}

	month := day
	if len(day) >= 7 {
		month = day[:7]
	}
	alloc, err := Allocate(db, month)
	if err != nil {
		return nil, err
	}

	out := make([]Finding, 0, len(world.Desks))
	for _, d := range world.Desks {
		f := Finding{Source: d.Name, StaleThreshold: int(staleDays), UntaggedThresholdPct: untaggedPct}

		var lastDay sql.NullString
		if err := db.QueryRow(`SELECT MAX(day) FROM charges WHERE source=?`, d.Name).Scan(&lastDay); err != nil {
			return nil, err
		}
		if lastDay.Valid && lastDay.String != "" {
			f.HasCharge = true
			f.FreshnessDays = daysBetween(lastDay.String, day)
		}
		f.Stale = !f.HasCharge || f.FreshnessDays >= int(staleDays)

		src := alloc.BySource[d.Name]
		f.MonthCents = src.Direct + src.Shared
		f.UntaggedCents = src.Shared
		f.UnallocatedCents = src.Unallocated
		f.UntaggedPct, _ = money.Pct(f.UntaggedCents, f.MonthCents)
		f.UnallocatedPct, _ = money.Pct(f.UnallocatedCents, f.MonthCents)
		f.UntaggedCrossed = crossesShare(f.UntaggedCents, f.MonthCents, untaggedPct)
		f.UnallocatedCrossed = crossesShare(f.UnallocatedCents, f.MonthCents, untaggedPct)

		f.Crossed = f.Stale || f.UntaggedCrossed || f.UnallocatedCrossed
		f.Reason = reasonFor(f)
		out = append(out, f)
	}
	return out, nil
}

// crossesShare reports whether part is at or above pct percent of whole,
// computed in integer cents throughout (part*100 against whole*pct) so the
// boundary is exact and never depends on a float64 rounding the same tie
// two different ways -- money's own package header explains why that is not
// hypothetical (two orderings of the same rows once rounded the same true
// sum two different ways).
func crossesShare(part, whole money.Cents, pct int64) bool {
	if whole <= 0 {
		return false // no spend at all this month is not an untagged share; nothing to cross
	}
	return int64(part)*100 >= int64(whole)*pct
}

// daysBetween is to minus from, in whole days, clamped at zero: a `day`
// param before the last charge on file is not this function's problem to
// report as negative.
func daysBetween(from, to string) int {
	f, err1 := time.Parse("2006-01-02", from)
	t, err2 := time.Parse("2006-01-02", to)
	if err1 != nil || err2 != nil {
		return 0
	}
	d := int(t.Sub(f).Hours() / 24)
	if d < 0 {
		return 0
	}
	return d
}

// wholeNumberThreshold reads one named threshold from roles.yaml and parses
// its leading whole number: "3" for T.stale, or the "10" in "10% of the
// desk's month" for T.untagged when percent is true. A name roles.yaml does
// not carry, or a Value this cannot parse this way, refuses outright --
// never a guessed default -- which is what makes it the one function this
// file's "threshold row missing from the roles data" hostile case exercises
// directly (dataquality_internal_test.go).
func wholeNumberThreshold(name string, percent bool) (int64, error) {
	th, ok := crew.ThresholdFor(name)
	if !ok {
		return 0, fmt.Errorf("%w: no threshold named %q", ErrThresholdMissing, name)
	}
	v := strings.TrimSpace(th.Value)
	if percent {
		i := strings.IndexByte(v, '%')
		if i <= 0 {
			return 0, fmt.Errorf("%w: %s's value %q names no whole-number percentage before a %%",
				ErrThresholdMissing, name, th.Value)
		}
		v = strings.TrimSpace(v[:i])
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%w: %s's value %q is not a whole, non-negative number",
			ErrThresholdMissing, name, th.Value)
	}
	return n, nil
}

// reasonFor is the halt request's own words: "naming the desk and the
// reason" (roles.yaml's owes line for this role). The desk itself is named
// by the caller (it is Finding.Source); this is just the reason.
func reasonFor(f Finding) string {
	var parts []string
	if f.Stale {
		if !f.HasCharge {
			parts = append(parts, fmt.Sprintf("no charge on record at all (T.stale is %d days)", f.StaleThreshold))
		} else {
			parts = append(parts, fmt.Sprintf("no charge for %d days (T.stale is %d)", f.FreshnessDays, f.StaleThreshold))
		}
	}
	if f.UntaggedCrossed {
		parts = append(parts, fmt.Sprintf("%.1f%% of this month is untagged (T.untagged is %d%%)",
			f.UntaggedPct, f.UntaggedThresholdPct))
	}
	if f.UnallocatedCrossed {
		parts = append(parts, fmt.Sprintf("%.1f%% of this month is unallocated (T.untagged is %d%%)",
			f.UnallocatedPct, f.UntaggedThresholdPct))
	}
	return strings.Join(parts, "; ")
}
