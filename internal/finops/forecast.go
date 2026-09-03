package finops

import (
	"database/sql"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/estate"
	"github.com/TAIPANBOX/costcrew/internal/money"
	"github.com/TAIPANBOX/costcrew/internal/world"
)

// A forecast is only a forecast once it is FROZEN.
//
// An unfrozen forecast is a number that improves every time somebody edits it,
// and accuracy measured against one is not a measurement, it is a compliment.
// Freezing is what makes the next month's comparison mean something, which is
// why the accuracy KPI refuses to report until at least one month has been.
//
// The ladder is the canon's: within 20 percent is where a practice starts,
// 15 is where it is working, 12 is where it is trusted. A forecast inside 5
// percent usually means somebody is forecasting the past.
const (
	LadderStart   = 20.0
	LadderWorking = 15.0
	LadderTrusted = 12.0
)

const ForecastSchema = `
CREATE TABLE IF NOT EXISTS forecasts(
  period TEXT NOT NULL, source TEXT NOT NULL,
  forecast_cents INTEGER NOT NULL, basis TEXT,
  frozen_at TEXT, frozen_by TEXT,
  PRIMARY KEY (period, source));
`

type Forecast struct {
	Period   string
	Source   string
	Forecast money.Cents
	Basis    string
	FrozenAt string
	FrozenBy string

	// Filled in once the month has closed.
	Actual   money.Cents
	HasAct   bool
	ErrorPct float64
	Grade    string
}

// Project estimates a month from what the estate has done so far.
//
// Run rate over the days that have data, extended to the whole month, with the
// LAST CLOSED month as a sanity anchor. Deliberately simple and stated as
// simple: a forecast whose method nobody can explain is one nobody can argue
// with, and the argument is the useful part.
func Project(db *sql.DB, period string) (map[string]money.Cents, string, error) {
	return ProjectAsAt(db, period, 31)
}

// ProjectAsAt is the projection somebody would have made on the Nth of the
// month, from the days that had landed by then.
//
// A forecast for a month that has already finished, made from all of its days,
// is not a forecast: it equals the actual, scores nothing, and would fill the
// accuracy table with A+ for work nobody did. A history of forecasts is only
// worth keeping if each was made while the answer was still unknown.
func ProjectAsAt(db *sql.DB, period string, through int) (map[string]money.Cents, string, error) {
	if through < 1 {
		through = 1
	}
	cut := fmt.Sprintf("%02d", through)
	rows, err := db.Query(`SELECT source, SUM(billed_cents), COUNT(DISTINCT day)
		FROM charges WHERE substr(day,1,7)=? AND substr(day,9,2)<=?
		GROUP BY source ORDER BY source`, period, cut)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()

	out := map[string]money.Cents{}
	days := 0
	for rows.Next() {
		var src string
		var total int64
		var n int
		if err := rows.Scan(&src, &total, &n); err != nil {
			return nil, "", err
		}
		if n > days {
			days = n
		}
		out[src] = money.Cents(total)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	if days == 0 {
		return nil, "", fmt.Errorf("%s has no charge data to project from", period)
	}

	inMonth := daysInMonth(period)
	for src, sofar := range out {
		// Integer arithmetic throughout: a projection is a decision about
		// money and must land on the same cent everywhere.
		out[src] = money.Cents(int64(sofar) * int64(inMonth) / int64(days))
	}
	basis := fmt.Sprintf("run rate over the %d days of %s that have data, extended to %d",
		days, period, inMonth)
	return out, basis, nil
}

// DriverLine is one registered driver's own contribution to a driver-aware
// projection: what it is, the window it was measured over (clipped to the
// month being projected), and its own measured effect.
type DriverLine struct {
	Label, Kind, Start, End string
	Effect                  money.Cents
}

// ProjectWithDrivers is Project, made driver-aware for one desk.
// C3-SPEC.md section 2.
//
// The naive run rate (Project/ProjectAsAt) treats every dollar that has
// landed as equally likely to repeat every day of the month. A registered
// driver's own days are exactly the days that do NOT behave like an
// ordinary day, and blending them into the same daily average is what
// "averaged away" means in C3-SPEC.md section 1: a one-time bump inside the
// days that have landed gets smeared across the whole month at the naive
// method's own extension factor, counted for more than it is, while one
// still ahead of the days that have landed is not counted at all.
//
// So every calendar day any registered driver's own window covers, clipped
// to the month being projected, is pulled OUT of the run-rate side of the
// arithmetic entirely -- out of both the sum and the day count -- and the
// clean days left are extended across the rest of the month exactly as
// today. Each driver then gets its OWN small run rate, over its own scope
// (the driver's Scope when it names one service, the whole desk when it is
// "*") and its own window, extended across however many calendar days that
// window covers inside the month: a one-time driver's window is one day, so
// this is simply that day's own measured total, added once; a recurring
// driver's window can span many days, so this is its own per-day rate
// repeated across every one of them -- "recurring ones repeat by their
// window", C3-SPEC.md section 2's own words, not a periodicity this
// registry does not carry. The total is the clean baseline plus every
// driver's own line: no day is ever priced twice, because the two sides
// partition the month's calendar days rather than overlapping.
//
// Cents-exact, rounded once at the total: every division here is ONE
// integer division per line (the clean baseline, and each driver's own
// line), same as ProjectAsAt already does for a whole desk, and the grand
// total is the exact sum of those already-settled integers -- summing
// integers loses nothing, so nothing downstream of the per-line divisions
// ever rounds again.
//
// Known limitation, stated rather than silently assumed: two drivers whose
// windows and scopes both overlap (a desk-wide "*" driver and a
// service-scoped one active on the same days) would each measure some of
// the same underlying charges, and the total would count that overlap
// twice. Neither this fixture's own registry nor an analyst applying
// driver.one-time/driver.recurring today produces that shape (each driver
// on a desk comes from a distinct anomaly or a distinct hand-written
// event), so it is left as a limitation rather than built out on no case to
// test it against.
func ProjectWithDrivers(db *sql.DB, desk, period string) (money.Cents, string, []DriverLine, error) {
	return projectWithDriversAsAt(db, desk, period, 31)
}

// projectWithDriversAsAt is ProjectWithDrivers as it would have looked
// through the Nth of the month, the same relationship ProjectAsAt has to
// Project -- and the reason ProjectWithDrivers is not simply "through 31"
// inlined: Freeze needs to honour its own caller's `through` (history.go
// seeds a track record frozen on the 12th of every month, deliberately
// before the month has finished, so its accuracy table grades something
// other than a perfect score) without exposing that cutoff on the public,
// spec-named signature.
func projectWithDriversAsAt(db *sql.DB, desk, period string, through int) (money.Cents, string, []DriverLine, error) {
	if through < 1 {
		through = 1
	}
	cut := fmt.Sprintf("%02d", through)

	byDay, err := dayCents(db, desk, "", period, cut)
	if err != nil {
		return 0, "", nil, err
	}
	if len(byDay) == 0 {
		return 0, "", nil, fmt.Errorf("%s has no charge data on %s to project from", period, desk)
	}

	inMonth := daysInMonth(period)
	monthStart := period + "-01"
	monthEnd := fmt.Sprintf("%s-%02d", period, inMonth)

	all, err := estate.Drivers(db)
	if err != nil {
		return 0, "", nil, err
	}

	// A driver's own scope decides what it excludes from the baseline: "*"
	// claims the WHOLE desk-day (wholeDeskExcluded), a named service claims
	// only that service's own share of the day (scoped, below), leaving
	// every other service on the same desk in the baseline where it
	// belongs. The first version of this excluded the whole desk-day
	// regardless of scope, which made a service-scoped RECURRING driver
	// whose window spans many months -- N04, "Month-end batch on the
	// storage array", the real shape onprem's own registry carries -- wipe
	// every OTHER service on that desk out of the projection for as long as
	// its window covers the month, because the baseline saw nothing left to
	// average and the driver's own line only ever measures its own scope.
	// Found by the parity gate against the seeded estate, not by any test:
	// TestProjectWithDriversExcludesOnlyItsOwnScopeFromTheBaseline holds it
	// now.
	type applied struct {
		d          world.Driver
		start, end string
		scoped     map[string]money.Cents // nil for "*"; that scope IS byDay
	}
	wholeDeskExcluded := map[string]bool{}
	var overlapping []applied
	for _, d := range all {
		if d.Source != desk {
			continue
		}
		start, end, ok := driverWindowInMonth(d, monthStart, monthEnd)
		if !ok {
			// Hostile (End before Start) or the boundary case (the window
			// ends before the projection starts, or starts after it ends):
			// either way, not applied. C3-SPEC.md section 4.
			continue
		}
		ov := applied{d: d, start: start, end: end}
		if d.Scope == "*" {
			for day := start; day <= end; day = nextDay(day) {
				wholeDeskExcluded[day] = true
			}
		} else {
			ov.scoped, err = dayCents(db, desk, d.Scope, period, cut)
			if err != nil {
				return 0, "", nil, err
			}
		}
		overlapping = append(overlapping, ov)
	}

	var cleanSofar money.Cents
	var cleanLandedDays int
	for day, v := range byDay {
		if wholeDeskExcluded[day] {
			continue
		}
		remainder := v
		for _, ov := range overlapping {
			if ov.scoped == nil { // a "*" driver: already excluded above
				continue
			}
			if day < ov.start || day > ov.end {
				continue
			}
			remainder -= ov.scoped[day]
		}
		cleanSofar += remainder
		cleanLandedDays++
	}
	cleanDaysInMonth := inMonth - len(wholeDeskExcluded)

	var total money.Cents
	var basis string
	switch {
	case cleanLandedDays > 0 && cleanDaysInMonth > 0:
		total = money.Cents(int64(cleanSofar) * int64(cleanDaysInMonth) / int64(cleanLandedDays))
		basis = fmt.Sprintf("run rate over the %d non-driver days of %s that have data, extended to %d",
			cleanLandedDays, period, cleanDaysInMonth)
	default:
		// Every day of the month a "*" driver's own window did not claim
		// has no data of its own, or a "*" driver's window claims the whole
		// month: the total comes entirely from the driver lines below.
		basis = fmt.Sprintf("every day of %s that has landed falls inside a driver's own window", period)
	}

	var lines []DriverLine
	for _, ov := range overlapping {
		d := ov.d
		scoped := ov.scoped
		if scoped == nil {
			scoped = byDay
		}
		var sofar money.Cents
		var landed int
		for day, v := range scoped {
			if day < ov.start || day > ov.end {
				continue
			}
			sofar += v
			landed++
		}
		windowDays := inclusiveDaysBetween(ov.start, ov.end)
		var effect money.Cents
		if landed > 0 {
			// ONE division, multiply before divide: (sofar * windowDays) /
			// landed, not (sofar/landed) * windowDays, which truncates the
			// per-day rate to a whole cent before it is ever multiplied and
			// is exactly the "round per driver before the total" mutant
			// C3-SPEC.md section 4 names.
			effect = money.Cents(int64(sofar) * int64(windowDays) / int64(landed))
		}
		total += effect
		lines = append(lines, DriverLine{
			Label: d.Label, Kind: d.Kind, Start: ov.start, End: ov.end, Effect: effect,
		})
	}
	sort.Slice(lines, func(i, j int) bool {
		if lines[i].Start != lines[j].Start {
			return lines[i].Start < lines[j].Start
		}
		return lines[i].Label < lines[j].Label
	})
	if len(lines) > 0 {
		parts := make([]string, 0, len(lines))
		for _, l := range lines {
			parts = append(parts, fmt.Sprintf("%s (%s, %s to %s, %s)", l.Label, l.Kind, l.Start, l.End, l.Effect))
		}
		basis += "; drivers applied: " + strings.Join(parts, ", ")
	}
	return total, basis, lines, nil
}

// dayCents is one desk's (or, when service is non-empty, one desk-service's)
// billed cents per day within a period, up to the cutoff day-of-month --
// the same rows ProjectAsAt's own query sums, kept per day instead of
// summed outright, because a driver-aware projection needs to pull specific
// days out of the total rather than only ever seeing the total itself.
func dayCents(db *sql.DB, source, service, period, cut string) (map[string]money.Cents, error) {
	q := `SELECT day, SUM(billed_cents) FROM charges
		WHERE source=? AND substr(day,1,7)=? AND substr(day,9,2)<=?`
	args := []any{source, period, cut}
	if service != "" {
		q += ` AND service=?`
		args = append(args, service)
	}
	q += ` GROUP BY day`
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]money.Cents{}
	for rows.Next() {
		var d string
		var v int64
		if err := rows.Scan(&d, &v); err != nil {
			return nil, err
		}
		out[d] = money.Cents(v)
	}
	return out, rows.Err()
}

// driverWindowInMonth clips a driver's own [Start,End] to the calendar month
// a period covers. Every date here is "2006-01-02", which sorts the same as
// a string and as a time, so clipping and comparing both need only string
// comparison, never a parse.
//
// ok is false when the driver's own window is malformed (End before Start,
// C3-SPEC.md section 4's hostile input: a broken registry row is never
// applied, and never crashes on) or does not reach into the month at all
// (the boundary case, section 4: "a driver whose window ends before the
// projection starts").
func driverWindowInMonth(d world.Driver, monthStart, monthEnd string) (start, end string, ok bool) {
	if d.End < d.Start {
		return "", "", false
	}
	start, end = d.Start, d.End
	if start < monthStart {
		start = monthStart
	}
	if end > monthEnd {
		end = monthEnd
	}
	if start > end {
		return "", "", false
	}
	return start, end, true
}

func nextDay(day string) string {
	t, err := time.Parse("2006-01-02", day)
	if err != nil {
		return day
	}
	return t.AddDate(0, 0, 1).Format("2006-01-02")
}

// inclusiveDaysBetween is the inclusive count of calendar days from start to
// end, both "2006-01-02". Never less than one: an unparseable pair (never
// produced by driverWindowInMonth's own callers, guarded here anyway rather
// than trusted) reads as a single day sooner than as a division by zero
// three calls up the stack.
//
// Named apart from internal/finops/dataquality.go's own daysBetween
// (C9-SPEC.md): that one is EXCLUSIVE and clamped at zero, this one is
// INCLUSIVE and clamped at one, and the two disagreeing under one shared
// name is exactly the collision Phase C integration found between this
// branch (C3) and C9 -- same package, two files, so `go build`, not the
// merge itself, is what caught it.
func inclusiveDaysBetween(start, end string) int {
	s, err1 := time.Parse("2006-01-02", start)
	e, err2 := time.Parse("2006-01-02", end)
	if err1 != nil || err2 != nil {
		return 1
	}
	n := int(e.Sub(s).Hours()/24) + 1
	if n < 1 {
		return 1
	}
	return n
}

func daysInMonth(period string) int {
	t, err := time.Parse("2006-01", period)
	if err != nil {
		return 30
	}
	return t.AddDate(0, 1, -1).Day()
}

// Freeze writes the projection down and stops it moving.
func Freeze(db *sql.DB, period, by string) error {
	return FreezeAsAt(db, period, by, 31)
}

// FreezeAsAt records the forecast somebody made on the Nth of the month.
//
// Driver-aware since C3-SPEC.md: each desk's own row is
// projectWithDriversAsAt's figure and basis, not ProjectAsAt's naive
// run-rate blend, because the whole point of C3 is that the number scored
// next month is the one that already knew about a dated migration or a
// recurring window instead of averaging it away. "Unchanged mechanics"
// (C3-SPEC.md section 2) is the schema, the re-freeze guard, the
// transaction and one row per desk -- all exactly as before -- not the
// figure a desk's own row carries.
func FreezeAsAt(db *sql.DB, period, by string, through int) error {
	if _, err := db.Exec(ForecastSchema); err != nil {
		return err
	}
	if frozen, err := IsFrozen(db, period); err != nil {
		return err
	} else if frozen {
		return fmt.Errorf("%s is already frozen: re-freezing it would move a number "+
			"somebody has already been shown", period)
	}
	if through < 1 {
		through = 1
	}
	desks, err := desksInPeriod(db, period, fmt.Sprintf("%02d", through))
	if err != nil {
		return err
	}
	if len(desks) == 0 {
		return fmt.Errorf("%s has no charge data to project from", period)
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339)
	for _, desk := range desks {
		figure, basis, _, err := projectWithDriversAsAt(db, desk, period, through)
		if err != nil {
			// desksInPeriod already filtered to desks with landed data at
			// this cutoff, so projectWithDriversAsAt's own "no charge data"
			// refusal cannot fire here -- this stays a hard error, not a
			// skip, so a genuine store failure is never swallowed as though
			// the desk simply had nothing to say.
			return err
		}
		if _, err := tx.Exec(`INSERT INTO forecasts
			(period, source, forecast_cents, basis, frozen_at, frozen_by)
			VALUES (?,?,?,?,?,?)`, period, desk, int64(figure), basis, now, by); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// desksInPeriod is the same WHERE clause ProjectAsAt's own query uses,
// split out so Freeze can enumerate desks before pricing each one
// separately: a desk appears here if and only if it would have appeared in
// ProjectAsAt's map, so switching Freeze to a driver-aware figure never
// changes WHICH desks get a forecast row, only what each one says.
func desksInPeriod(db *sql.DB, period, cut string) ([]string, error) {
	rows, err := db.Query(`SELECT DISTINCT source FROM charges
		WHERE substr(day,1,7)=? AND substr(day,9,2)<=? ORDER BY source`, period, cut)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// SetForecastBasis overwrites every desk's own recorded basis for an already
// frozen period. C3-SPEC.md section 2: "the option's summary becomes the
// freeze's recorded basis" -- internal/finops/apply.go's forecast.freeze
// case calls this once Freeze itself has succeeded, replacing the
// projectWithDriversAsAt sentence with the analyst's own written words,
// because the analyst was reading the SAME driver-aware packet and named
// what a generated sentence cannot: which of these drivers actually
// explains the number to the person about to see it frozen. Every desk's
// row is overwritten together, the same way every desk already shared one
// basis before this change: the freeze is one decision, not one per desk.
func SetForecastBasis(db *sql.DB, period, basis string) error {
	if basis == "" {
		return nil
	}
	_, err := db.Exec(`UPDATE forecasts SET basis=? WHERE period=?`, basis, period)
	return err
}

func IsFrozen(db *sql.DB, period string) (bool, error) {
	if _, err := db.Exec(ForecastSchema); err != nil {
		return false, err
	}
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM forecasts WHERE period=?`, period).Scan(&n)
	return n > 0, err
}

// Forecasts reads every frozen month and scores the ones that have closed.
//
// A forecast for a month still running is left unscored rather than compared
// against a partial actual, which would flatter every forecast ever made.
func Forecasts(db *sql.DB, openPeriod string) ([]Forecast, error) {
	if _, err := db.Exec(ForecastSchema); err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT period, source, forecast_cents, COALESCE(basis,''),
		COALESCE(frozen_at,''), COALESCE(frozen_by,'')
		FROM forecasts ORDER BY period DESC, source`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Forecast
	for rows.Next() {
		var f Forecast
		var v int64
		if err := rows.Scan(&f.Period, &f.Source, &v, &f.Basis,
			&f.FrozenAt, &f.FrozenBy); err != nil {
			return nil, err
		}
		f.Forecast = money.Cents(v)
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for i := range out {
		if out[i].Period >= openPeriod {
			// Still running. Comparing a whole-month forecast with a
			// month-to-date actual makes every forecast look generous.
			continue
		}
		var actual int64
		if err := db.QueryRow(`SELECT COALESCE(SUM(billed_cents),0) FROM charges
			WHERE substr(day,1,7)=? AND source=?`,
			out[i].Period, out[i].Source).Scan(&actual); err != nil {
			return nil, err
		}
		out[i].Actual = money.Cents(actual)
		out[i].HasAct = true
		if out[i].Forecast != 0 {
			out[i].ErrorPct = math.Abs(float64(out[i].Actual-out[i].Forecast)) /
				math.Abs(float64(out[i].Forecast)) * 100
			out[i].Grade = grade(out[i].ErrorPct)
		}
	}
	return out, nil
}

func grade(errPct float64) string {
	switch {
	case errPct <= LadderTrusted:
		return "trusted"
	case errPct <= LadderWorking:
		return "working"
	case errPct <= LadderStart:
		return "starting"
	}
	return "off"
}

// Accuracy is the practice-level number, over SCORED months only.
//
// It returns ok=false when nothing has been scored, which is what the KPI
// library reports as a refusal rather than as a zero. A zero would read as a
// perfect forecast.
// OpenPeriod is the month the estate is still in.
//
// It is a fact about the CHARGES, not about whatever month a page happens to
// be filtered to, and confusing the two is how two pages came to report the
// same accuracy as 11.7% over 84 month-desks and 11.9% over 78. Both were
// arithmetically right and they disagreed on screen, which is worse than one
// of them being wrong: a reader cannot tell which to believe.
func OpenPeriod(db *sql.DB) (string, error) {
	var m string
	err := db.QueryRow(`SELECT COALESCE(MAX(substr(day,1,7)),'') FROM charges`).Scan(&m)
	return m, err
}

// Accuracy scores every forecast whose month has finished.
//
// openPeriod is the month still running, from OpenPeriod. Pass a page's filter
// here and the score silently changes with the filter.
func Accuracy(db *sql.DB, openPeriod string) (float64, int, bool, error) {
	fs, err := Forecasts(db, openPeriod)
	if err != nil {
		return 0, 0, false, err
	}
	var sum float64
	var n int
	for _, f := range fs {
		if !f.HasAct || f.Forecast == 0 {
			continue
		}
		sum += f.ErrorPct
		n++
	}
	if n == 0 {
		return 0, 0, false, nil
	}
	return sum / float64(n), n, true, nil
}

// Missed is the drivers registered against a desk that overlap a period's
// own calendar month but that the period's own recorded basis never named.
// C3-SPEC.md section 1: "the next month's packet carries the miss with the
// driver that was missed" -- when a driver is registered AFTER a freeze (an
// analyst naming, in hindsight, what a miss actually turned out to be), the
// next packet or KPI built for this desk names it, because the frozen
// basis -- ProjectWithDrivers's own sentence, or the option's summary that
// SetForecastBasis replaced it with -- is the only record of what the
// freeze itself already knew about.
//
// basis is passed in rather than re-read, so a caller already holding a
// Forecast (from Forecasts) never pays for a second lookup, and a caller
// exploring a basis that was never actually frozen (the packet's own
// projectWithDriversAsAt sentence for the desk's CURRENT month, C3-SPEC.md's
// "which drivers moved it") can ask the same question of it.
func Missed(db *sql.DB, desk, period, basis string) ([]world.Driver, error) {
	all, err := estate.Drivers(db)
	if err != nil {
		return nil, err
	}
	inMonth := daysInMonth(period)
	monthStart := period + "-01"
	monthEnd := fmt.Sprintf("%s-%02d", period, inMonth)

	var out []world.Driver
	for _, d := range all {
		if d.Source != desk {
			continue
		}
		if _, _, ok := driverWindowInMonth(d, monthStart, monthEnd); !ok {
			continue
		}
		if strings.Contains(basis, d.Label) {
			continue
		}
		out = append(out, d)
	}
	return out, nil
}

// Miss is one scored month-desk's gap between what was frozen and what
// happened, with whichever registered drivers its own basis never named.
type Miss struct {
	Forecast
	MissedDrivers []world.Driver
}

// LargestMiss finds the scored month-desk with the largest error, and names
// the drivers Missed finds for it. C3-SPEC.md section 2: "the KPI ... names
// the largest miss's driver when one exists" -- the "when one exists"
// qualifies the DRIVER, not the miss itself: a largest miss is reported
// whenever anything has been scored, and MissedDrivers is simply empty when
// its own registered drivers were all already named in what was frozen.
//
// Ranked by ErrorPct, the same measure the ladder and the accuracy KPI
// already grade on -- a bigger desk's larger dollar gap is not automatically
// the worse MISS, and a percentage is what this KPI already promises to
// report. Every field graded here (top.Forecast, top.Actual, top.ErrorPct)
// comes straight from Forecasts, which reads the FROZEN forecast_cents
// column against the actual at period end: grading a freshly recomputed
// projection instead is exactly the "grade accuracy against the live
// figure instead of the frozen one" mutant C3-SPEC.md section 4 names, and
// it is why nothing here calls Project or ProjectWithDrivers at all.
func LargestMiss(db *sql.DB, openPeriod string) (Miss, bool, error) {
	fs, err := Forecasts(db, openPeriod)
	if err != nil {
		return Miss{}, false, err
	}
	var top Forecast
	haveTop := false
	for _, f := range fs {
		if !f.HasAct || f.Forecast == 0 {
			continue
		}
		if !haveTop || worseMiss(f, top) {
			top, haveTop = f, true
		}
	}
	if !haveTop {
		return Miss{}, false, nil
	}
	missed, err := Missed(db, top.Source, top.Period, top.Basis)
	if err != nil {
		return Miss{}, false, err
	}
	return Miss{Forecast: top, MissedDrivers: missed}, true, nil
}

// worseMiss orders two scored month-desks by ErrorPct, breaking a tie first
// on the absolute cents gap and then on period and source, so the same data
// picks the same "largest miss" on every call regardless of map iteration
// order upstream in Forecasts.
func worseMiss(a, b Forecast) bool {
	if a.ErrorPct != b.ErrorPct {
		return a.ErrorPct > b.ErrorPct
	}
	da, db_ := a.Actual.Sub(a.Forecast).Abs(), b.Actual.Sub(b.Forecast).Abs()
	if da != db_ {
		return da > db_
	}
	if a.Period != b.Period {
		return a.Period > b.Period
	}
	return a.Source < b.Source
}

// FrozenPeriods lists what has been frozen, newest first.
func FrozenPeriods(db *sql.DB) ([]string, error) {
	if _, err := db.Exec(ForecastSchema); err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT DISTINCT period FROM forecasts ORDER BY period DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(out)))
	return out, rows.Err()
}

func LadderText() string {
	return strings.Join([]string{
		fmt.Sprintf("within %.0f%% is where a practice starts", LadderStart),
		fmt.Sprintf("%.0f%% is where it is working", LadderWorking),
		fmt.Sprintf("%.0f%% is where it is trusted", LadderTrusted),
	}, ", ")
}
