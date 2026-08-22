// Package estate holds the cost data the console reports on: charges, the
// driver registry that explains them, and the intake plane of budgets and
// requests built on top.
//
// It lives in the same SQLite file as the console's own state. The Python
// original kept a second database, DuckDB, for this: 48 704 rows of ordinary
// SQL with no window functions, which is well inside what one engine does.
//
// One porting decision worth naming, because it removes a whole class of bug.
// The original writes the month as strftime(day,'%Y-%m'). SQLite's strftime
// takes its arguments the other way round, and getting it backwards does not
// raise: it returns NULL, the sum quietly becomes zero, and the page still
// renders. Every such site here is substr(day,1,7) instead, which is exact for
// an ISO date, faster, and cannot be written backwards.
package estate

import (
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/TAIPANBOX/costcrew/internal/pyrand"
)

// Schema creates the estate's tables. Ingest drops and rebuilds them, the way
// the original's re-runnable ingest does.
const Schema = `
CREATE TABLE IF NOT EXISTS charges(
  source TEXT, day TEXT, service TEXT, team TEXT, category TEXT,
  billed REAL, effective REAL, quantity REAL, unit TEXT, meter TEXT,
  model TEXT, workload TEXT);
CREATE TABLE IF NOT EXISTS drivers(
  date_start TEXT, date_end TEXT, scope TEXT, label TEXT, kind TEXT, source TEXT);
CREATE TABLE IF NOT EXISTS budgets(
  source TEXT, team TEXT, month TEXT, budget REAL);
CREATE TABLE IF NOT EXISTS requests(
  id INTEGER, source TEXT, team TEXT, title TEXT, kind TEXT,
  est_monthly_usd REAL, status TEXT, target_month TEXT, note TEXT);
CREATE INDEX IF NOT EXISTS charges_source_day ON charges(source, day);
CREATE INDEX IF NOT EXISTS charges_lookup ON charges(source, category, team);
`

// ------------------------------------------------------------------ ingest

// Ingest rebuilds the estate from the generated datasets. Re-runnable, and
// deterministic: the same input directory always produces the same tables,
// which the original promised and did not deliver until an ORDER BY was added
// to the budget seeding.
func Ingest(db *sql.DB, synthDir, driversPath string) (int, error) {
	if _, err := db.Exec(Schema); err != nil {
		return 0, err
	}
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	for _, t := range []string{"charges", "drivers", "budgets", "requests"} {
		if _, err := tx.Exec("DELETE FROM " + t); err != nil {
			return 0, err
		}
	}

	n, err := ingestCharges(tx, synthDir)
	if err != nil {
		return 0, err
	}
	if n == 0 {
		// An empty estate renders as a console full of zeroes rather than as
		// an error, which is how a wrong path gets mistaken for a quiet day.
		return 0, fmt.Errorf("no charge rows under %s: is the estate generated?", synthDir)
	}
	if err := ingestDrivers(tx, driversPath); err != nil {
		return 0, err
	}
	if err := buildIntake(tx); err != nil {
		return 0, err
	}
	return n, tx.Commit()
}

// The platform datasets, plus the organisation's AI spend. AI rows carry
// consumption alongside cost, because FOCUS is heading toward token
// consumption as a first-class column and a token count derived from a dollar
// amount is not a measurement.
var sources = []string{"aws", "gcp", "onprem", "ai", "saas"}

func ingestCharges(tx *sql.Tx, dir string) (int, error) {
	stmt, err := tx.Prepare(`INSERT INTO charges VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	total := 0
	for _, key := range sources {
		path := filepath.Join(dir, "focus_synth_"+key+".csv")
		f, err := os.Open(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return 0, err
		}
		rows, err := readCSVDicts(f)
		f.Close()
		if err != nil {
			return 0, fmt.Errorf("%s: %w", filepath.Base(path), err)
		}
		for _, r := range rows {
			if _, err := stmt.Exec(
				key, r["charge_period_start"], r["service_name"],
				nullable(r["team"]), r["charge_category"],
				num(r["billed_cost"]), num(r["effective_cost"]),
				nullNum(r["consumed_quantity"]), nullable(r["consumed_unit"]),
				nullable(r["sku_meter"]), nullable(r["model"]), nullable(r["workload"]),
			); err != nil {
				return 0, err
			}
			total++
		}
	}
	return total, nil
}

func ingestDrivers(tx *sql.Tx, path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var doc struct {
		Drivers []struct {
			DateStart string `json:"date_start"`
			DateEnd   string `json:"date_end"`
			Scope     string `json:"scope"`
			Label     string `json:"label"`
			Kind      string `json:"kind"`
			Source    string `json:"source"`
		} `json:"drivers"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	for _, d := range doc.Drivers {
		scope, kind := d.Scope, d.Kind
		if scope == "" {
			scope = "*"
		}
		if kind == "" {
			kind = "one-time"
		}
		if _, err := tx.Exec(`INSERT INTO drivers VALUES (?,?,?,?,?,?)`,
			d.DateStart, d.DateEnd, scope, d.Label, kind, d.Source); err != nil {
			return err
		}
	}
	return nil
}

// ------------------------------------------------------------------ intake

var teamFactors = map[string]float64{
	"ml": 0.88, "data": 1.12, "sre-platform": 1.05, "product-web": 1.06,
}

// buildIntake seeds budgets per (source, team, month) and the requests
// pipeline the org teams hand to FinOps.
//
// The ORDER BY is load-bearing and not tidiness: the seeded generator below is
// consumed in this loop's order, and a GROUP BY promises none. Measured on the
// original 2026-08-22, six runs of that query over one store returned six
// different row orders, so every rebuild produced different budgets while the
// docstring said it was deterministic.
func buildIntake(tx *sql.Tx) error {
	rows, err := tx.Query(`
		SELECT source, team, substr(day,1,7) AS m, SUM(billed)
		FROM charges
		WHERE category='Usage' AND team IS NOT NULL
		  AND source IN ('aws','gcp','onprem')
		GROUP BY 1,2,3 ORDER BY 1,2,3`)
	if err != nil {
		return err
	}
	type row struct {
		source, team, month string
		actual              float64
	}
	var all []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.source, &r.team, &r.month, &r.actual); err != nil {
			rows.Close()
			return err
		}
		all = append(all, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(all) == 0 {
		return fmt.Errorf("no usage rows to build budgets from")
	}

	last := ""
	for _, r := range all {
		if r.month > last {
			last = r.month
		}
	}
	// The month before the last one, used as the base for the open month: a
	// budget set from a month-to-date figure would always look generous.
	prevOfLast := ""
	for _, r := range all {
		if r.month < last && r.month > prevOfLast {
			prevOfLast = r.month
		}
	}
	prevActual := map[string]float64{}
	for _, r := range all {
		if r.month == prevOfLast {
			prevActual[r.source+"\x00"+r.team] = r.actual
		}
	}

	rng := pyrand.New(99)
	ins, err := tx.Prepare(`INSERT INTO budgets VALUES (?,?,?,?)`)
	if err != nil {
		return err
	}
	defer ins.Close()

	for _, r := range all {
		f, ok := teamFactors[r.team]
		if !ok {
			f = 1.05
		}
		f = f * (1 + rng.Uniform(-0.03, 0.03))
		base := r.actual
		if r.month == last {
			if p, ok := prevActual[r.source+"\x00"+r.team]; ok {
				base = p
			}
		}
		if _, err := ins.Exec(r.source, r.team, r.month, round10(base*f)); err != nil {
			return err
		}
	}

	// September is seeded from July plus six percent, so the console opens
	// with a month ahead of the data rather than an empty one.
	for _, r := range all {
		if r.month == "2026-07" {
			if _, err := ins.Exec(r.source, r.team, "2026-09", round10(r.actual*1.06)); err != nil {
				return err
			}
		}
	}
	return seedRequests(tx)
}

// round10 is Python's round(x/10)*10, banker's rounding and all: round(2.5)
// is 2, not 3, and Go's math.Round goes the other way.
func round10(v float64) float64 {
	q := v / 10
	f := math.Floor(q)
	switch d := q - f; {
	case d > 0.5:
		f++
	case d == 0.5:
		if math.Mod(f, 2) != 0 {
			f++
		}
	}
	return f * 10
}

type request struct {
	id                        int
	source, team, title       string
	kind                      string
	est                       float64
	status, targetMonth, note string
}

var seededRequests = []request{
	{1, "aws", "ml", "Two extra GPU training nodes", "capacity", 820.0, "estimated", "2026-10", "Q4 model refresh; investigator estimate attached"},
	{2, "aws", "data", "Decommission RDS read replicas", "decommission", -140.0, "approved", "2026-09", "Follows the June RDS step-down"},
	{3, "gcp", "ml", "GKE burst pool for training", "capacity", 540.0, "estimated", "2026-09", "Matches the April GKE step trend"},
	{4, "gcp", "product-web", "Black Friday scale test", "change", 300.0, "new", "2026-11", "Needs optimizer sizing before estimate"},
	{5, "onprem", "sre-platform", "Storage array expansion, tranche 2", "capacity", 610.0, "new", "2026-11", "After the May tranche filled"},
	{6, "onprem", "data", "Archive tier for cold backups", "change", -90.0, "estimated", "2026-10", "Backup & DR reduction"},
	{7, "aws", "sre-platform", "Extend RI coverage", "change", -120.0, "estimated", "2026-09", "Optimizer proposal, awaiting approval"},
}

func seedRequests(tx *sql.Tx) error {
	for _, r := range seededRequests {
		if _, err := tx.Exec(`INSERT INTO requests VALUES (?,?,?,?,?,?,?,?,?)`,
			r.id, r.source, r.team, r.title, r.kind, r.est, r.status,
			r.targetMonth, r.note); err != nil {
			return err
		}
	}
	return nil
}

// ------------------------------------------------------------------ queries

// MonthPair returns the month before the newest one, and the newest, for a
// source. The newest is partial by definition, which is why so much of the
// product judges the one before it.
func MonthPair(db *sql.DB, source string) (prev, cur string, err error) {
	var lo, hi sql.NullString
	var n int
	err = db.QueryRow(`SELECT MIN(day), MAX(day), COUNT(*) FROM charges WHERE source=?`,
		source).Scan(&lo, &hi, &n)
	if err != nil {
		return "", "", err
	}
	if !hi.Valid {
		return "", "", fmt.Errorf("no charge data for source %q", source)
	}
	cur = hi.String[:7]
	y, _ := strconv.Atoi(cur[:4])
	m, _ := strconv.Atoi(cur[5:7])
	m--
	if m == 0 {
		m, y = 12, y-1
	}
	return fmt.Sprintf("%04d-%02d", y, m), cur, nil
}

type BudgetRow struct {
	Team    string
	Month   string
	Budget  float64
	Actual  float64
	Var     float64
	VarPct  float64
	HasPct  bool
	Partial bool
}

// BudgetVsActual is per team per month: the budget from the intake plane
// against actual usage.
func BudgetVsActual(db *sql.DB, source string, nMonths int, includeFuture bool) ([]BudgetRow, error) {
	rows, err := db.Query(`
		SELECT b.team, b.month, b.budget, COALESCE(a.actual, 0) AS actual
		FROM budgets b
		LEFT JOIN (SELECT team, substr(day,1,7) m, SUM(billed) actual
		           FROM charges WHERE source=? AND category='Usage'
		           GROUP BY 1,2) a
		  ON a.team = b.team AND a.m = b.month
		WHERE b.source=?
		ORDER BY b.month DESC, b.team`, source, source)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var raw []BudgetRow
	for rows.Next() {
		var r BudgetRow
		if err := rows.Scan(&r.Team, &r.Month, &r.Budget, &r.Actual); err != nil {
			return nil, err
		}
		raw = append(raw, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	_, cur, err := MonthPair(db, source)
	if err != nil {
		return nil, err
	}
	pool := map[string]bool{}
	for _, r := range raw {
		if includeFuture || r.Month <= cur {
			pool[r.Month] = true
		}
	}
	months := make([]string, 0, len(pool))
	for m := range pool {
		months = append(months, m)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(months)))
	if len(months) > nMonths {
		months = months[:nMonths]
	}
	keep := map[string]bool{}
	for _, m := range months {
		keep[m] = true
	}

	out := make([]BudgetRow, 0, len(raw))
	for _, r := range raw {
		if !keep[r.Month] {
			continue
		}
		r.Var = r.Actual - r.Budget
		if r.Budget != 0 {
			r.VarPct = r.Var / r.Budget * 100
			r.HasPct = true
		}
		r.Partial = r.Month == cur
		out = append(out, r)
	}
	return out, nil
}

type Request struct {
	ID                        int
	Source, Team, Title, Kind string
	Est                       float64
	Status, TargetMonth, Note string
}

func Requests(db *sql.DB) ([]Request, error) {
	rows, err := db.Query(`SELECT id, source, team, title, kind,
		est_monthly_usd, status, target_month, note FROM requests ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Request
	for rows.Next() {
		var r Request
		if err := rows.Scan(&r.ID, &r.Source, &r.Team, &r.Title, &r.Kind,
			&r.Est, &r.Status, &r.TargetMonth, &r.Note); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ------------------------------------------------------------------ helpers

func readCSVDicts(f *os.File) ([]map[string]string, error) {
	r := csv.NewReader(f)
	head, err := r.Read()
	if err != nil {
		return nil, err
	}
	var out []map[string]string
	for {
		rec, err := r.Read()
		if err != nil {
			if err.Error() == "EOF" {
				break
			}
			return nil, err
		}
		m := make(map[string]string, len(head))
		for i, h := range head {
			if i < len(rec) {
				m[h] = rec[i]
			}
		}
		out = append(out, m)
	}
	return out, nil
}

func nullable(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}

func num(s string) float64 {
	v, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return v
}

func nullNum(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return num(s)
}
