package finops

import (
	"database/sql"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/money"
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
	proj, basis, err := ProjectAsAt(db, period, through)
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339)
	for src, v := range proj {
		if _, err := tx.Exec(`INSERT INTO forecasts
			(period, source, forecast_cents, basis, frozen_at, frozen_by)
			VALUES (?,?,?,?,?,?)`, period, src, int64(v), basis, now, by); err != nil {
			return err
		}
	}
	return tx.Commit()
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
