package estate

import (
	"database/sql"
	"fmt"
	"sort"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/money"
	"github.com/TAIPANBOX/costcrew/internal/world"
)

// densify fills the gaps between two dates so a series is a contiguous run.
func densify(first, last string, byDay map[string]money.Cents) ([]string, []money.Cents, error) {
	f, err := time.Parse("2006-01-02", first)
	if err != nil {
		return nil, nil, err
	}
	l, err := time.Parse("2006-01-02", last)
	if err != nil {
		return nil, nil, err
	}
	var days []string
	var vals []money.Cents
	for d := f; !d.After(l); d = d.AddDate(0, 0, 1) {
		k := d.Format("2006-01-02")
		days = append(days, k)
		vals = append(vals, byDay[k])
	}
	return days, vals, nil
}

// --------------------------------------------------------------- budgets

// BudgetSchema keeps budgets in cents, like everything else that is money.
const BudgetSchema = `
CREATE TABLE IF NOT EXISTS budgets(
  source TEXT, team TEXT, month TEXT, budget_cents INTEGER,
  PRIMARY KEY (source, team, month));
`

// budgetRates are the per-team growth assumptions, in basis points, applied to
// the previous month's actual.
//
// Basis points and not a float: a budget is a decision somebody made, and a
// decision must land on the same cent on every machine. The original drew
// these from a seeded random generator, which meant a rebuild of the estate
// silently produced different budgets.
var budgetRates = map[string]int64{
	"ml-platform":     10_900, // growing fast, funded to grow
	"data-eng":        10_400,
	"product-web":     10_600,
	"product-mobile":  10_200,
	"sre-platform":    10_100, // flat: a platform team held to its envelope
	"security":        10_500,
	"growth":          11_200, // the campaign team, given room
	"finance-systems": 9_800,  // shrinking on purpose
	"research":        11_500,
	"support-tools":   10_000,
}

const defaultBudgetRate = 10_500

// SeedBudgets sets each team's monthly budget from the month before it,
// grown by that team's rate. The open month is budgeted from the last CLOSED
// month, never from a month-to-date figure, which would always look generous.
func SeedBudgets(db *sql.DB) error {
	if _, err := db.Exec(BudgetSchema); err != nil {
		return err
	}
	var have int
	if err := db.QueryRow(`SELECT COUNT(*) FROM budgets`).Scan(&have); err != nil {
		return err
	}
	if have > 0 {
		return nil
	}

	rows, err := db.Query(`SELECT source, team, substr(day,1,7) AS m, SUM(billed_cents)
		FROM charges WHERE category='Usage' AND team IS NOT NULL
		GROUP BY 1,2,3 ORDER BY 1,2,3`)
	if err != nil {
		return err
	}
	type key struct{ source, team, month string }
	actual := map[key]money.Cents{}
	months := map[string]bool{}
	var keys []key
	for rows.Next() {
		var k key
		var v int64
		if err := rows.Scan(&k.source, &k.team, &k.month, &v); err != nil {
			rows.Close()
			return err
		}
		actual[k] = money.Cents(v)
		months[k.month] = true
		keys = append(keys, k)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(keys) == 0 {
		return fmt.Errorf("no usage rows to build budgets from")
	}

	ordered := make([]string, 0, len(months))
	for m := range months {
		ordered = append(ordered, m)
	}
	sort.Strings(ordered)
	lastClosed := ordered[len(ordered)-1]
	if len(ordered) > 1 {
		lastClosed = ordered[len(ordered)-2]
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ins, err := tx.Prepare(`INSERT OR REPLACE INTO budgets VALUES (?,?,?,?)`)
	if err != nil {
		return err
	}
	defer ins.Close()

	for _, k := range keys {
		rate, ok := budgetRates[k.team]
		if !ok {
			rate = defaultBudgetRate
		}
		base := actual[k]
		prev := prevMonth(k.month)
		if p, ok := actual[key{k.source, k.team, prev}]; ok {
			base = p
		}
		// The open month is judged against the last closed one, because a
		// month-to-date total is not a month.
		if k.month > lastClosed {
			if p, ok := actual[key{k.source, k.team, lastClosed}]; ok {
				base = p
			}
		}
		if _, err := ins.Exec(k.source, k.team, k.month, int64(base.Bps(rate))); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func prevMonth(m string) string {
	t, err := time.Parse("2006-01", m)
	if err != nil {
		return m
	}
	return t.AddDate(0, -1, 0).Format("2006-01")
}

// --------------------------------------------------------------- reporting

type BudgetRow struct {
	Source, Team, Month string
	Budget, Actual      money.Cents
	Variance            money.Cents
	VariancePct         float64
	HasPct              bool
	Open                bool
}

// BudgetVsActual is per team per month: what was allowed against what was
// spent.
func BudgetVsActual(db *sql.DB, source string) ([]BudgetRow, error) {
	rows, err := db.Query(`
		SELECT b.team, b.month, b.budget_cents, COALESCE(a.spent, 0)
		FROM budgets b
		LEFT JOIN (SELECT team, substr(day,1,7) m, SUM(billed_cents) spent
		           FROM charges WHERE source=? AND category='Usage'
		           GROUP BY 1,2) a
		  ON a.team = b.team AND a.m = b.month
		WHERE b.source=?
		ORDER BY b.month DESC, b.team`, source, source)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []BudgetRow
	for rows.Next() {
		var r BudgetRow
		var budget, actual int64
		if err := rows.Scan(&r.Team, &r.Month, &budget, &actual); err != nil {
			return nil, err
		}
		r.Source, r.Budget, r.Actual = source, money.Cents(budget), money.Cents(actual)
		r.Variance = r.Actual - r.Budget
		r.VariancePct, r.HasPct = money.Pct(r.Variance, r.Budget)
		r.Open = r.Month == world.LastDay[:7]
		out = append(out, r)
	}
	return out, rows.Err()
}

// Drivers reads the registry.
func Drivers(db *sql.DB) ([]world.Driver, error) {
	rows, err := db.Query(`SELECT date_start, date_end, scope, label, kind, source
		FROM drivers ORDER BY date_start, label`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []world.Driver
	for rows.Next() {
		var d world.Driver
		if err := rows.Scan(&d.Start, &d.End, &d.Scope, &d.Label, &d.Kind, &d.Source); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Totals is the headline: what the estate cost in a month, by desk.
func Totals(db *sql.DB, month string) (map[string]money.Cents, error) {
	rows, err := db.Query(`SELECT source, SUM(billed_cents) FROM charges
		WHERE substr(day,1,7)=? GROUP BY source ORDER BY source`, month)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]money.Cents{}
	for rows.Next() {
		var s string
		var v int64
		if err := rows.Scan(&s, &v); err != nil {
			return nil, err
		}
		out[s] = money.Cents(v)
	}
	return out, rows.Err()
}
