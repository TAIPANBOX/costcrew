package estate

import (
	"database/sql"
	"fmt"

	"github.com/TAIPANBOX/costcrew/internal/money"
	"github.com/TAIPANBOX/costcrew/internal/world"
)

// SeedSchema is the estate as this implementation holds it. Amounts are
// integer cents in a column called billed_cents, and the name says the unit so
// nobody divides by a hundred twice.
const SeedSchema = `
CREATE TABLE IF NOT EXISTS charges(
  source TEXT NOT NULL, day TEXT NOT NULL, service TEXT NOT NULL,
  team TEXT, category TEXT NOT NULL, billed_cents INTEGER NOT NULL,
  quantity REAL, unit TEXT, meter TEXT, model TEXT,
  provenance TEXT);         -- NULL means generated; set by a connector's reader
CREATE TABLE IF NOT EXISTS drivers(
  date_start TEXT, date_end TEXT, scope TEXT, label TEXT, kind TEXT, source TEXT);
CREATE TABLE IF NOT EXISTS attribution(
  source TEXT, team TEXT, service TEXT, day_start TEXT, day_end TEXT,
  agent TEXT, confidence TEXT);
CREATE INDEX IF NOT EXISTS charges_series ON charges(source, team, service, day);
CREATE INDEX IF NOT EXISTS charges_day ON charges(source, day);
`

// Seed builds the estate from the generated world.
//
// It never runs against a store that already has charges: an existing estate
// is somebody's work, and a start-up that quietly regenerates it destroys
// whatever was recorded against those numbers.
func Seed(db *sql.DB) (int, error) {
	if _, err := db.Exec(SeedSchema); err != nil {
		return 0, err
	}
	var have int
	if err := db.QueryRow(`SELECT COUNT(*) FROM charges`).Scan(&have); err != nil {
		return 0, err
	}
	if have > 0 {
		return 0, nil
	}

	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	ins, err := tx.Prepare(`INSERT INTO charges
		(source, day, service, team, category, billed_cents, quantity, unit, meter, model)
		VALUES (?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return 0, err
	}
	defer ins.Close()

	rows := world.Generate()
	for _, r := range rows {
		var qty any
		if r.Unit != "" {
			qty = r.Quantity
		}
		if _, err := ins.Exec(r.Source, r.Day, r.Service, nullIf(r.Team),
			r.Category, int64(r.Billed), qty, nullIf(r.Unit), nullIf(r.Meter),
			nullIf(r.Model)); err != nil {
			return 0, err
		}
	}

	for _, d := range world.Drivers() {
		if _, err := tx.Exec(`INSERT INTO drivers VALUES (?,?,?,?,?,?)`,
			d.Start, d.End, d.Scope, d.Label, d.Kind, d.Source); err != nil {
			return 0, err
		}
	}

	// Attribution is what a gateway would supply once model calls carry an
	// agent header. Seeded here for the two events the world says an agent
	// caused, so the caused-by column has something true to show; everything
	// else stays at team grain and the page says which grain it is.
	for _, e := range world.Planted {
		if e.CausedBy == "" {
			continue
		}
		if _, err := tx.Exec(`INSERT INTO attribution VALUES (?,?,?,?,?,?,?)`,
			e.Source, e.Team, e.Service, e.Day, e.Day, e.CausedBy, "gateway-header"); err != nil {
			return 0, err
		}
	}

	if len(rows) == 0 {
		return 0, fmt.Errorf("the generated world produced no rows")
	}
	return len(rows), tx.Commit()
}

func nullIf(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// SeriesKey identifies one daily cost line.
type SeriesKey struct {
	Source, Team, Service string
}

// SeriesDays returns one series as a dense run of days, filling absent days
// with zero.
//
// Dense matters. A detector handed only the days that exist cannot tell a day
// with no spend from a day with no data, and the second one is the incident.
func SeriesDays(db *sql.DB, k SeriesKey) ([]string, []money.Cents, error) {
	rows, err := db.Query(`SELECT day, SUM(billed_cents) FROM charges
		WHERE source=? AND team=? AND service=? GROUP BY day ORDER BY day`,
		k.Source, k.Team, k.Service)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	byDay := map[string]money.Cents{}
	var first, last string
	for rows.Next() {
		var d string
		var v int64
		if err := rows.Scan(&d, &v); err != nil {
			return nil, nil, err
		}
		byDay[d] = money.Cents(v)
		if first == "" {
			first = d
		}
		last = d
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	if first == "" {
		return nil, nil, nil
	}
	return densify(first, last, byDay)
}

// AllSeries lists every distinct cost line in the estate.
func AllSeries(db *sql.DB) ([]SeriesKey, error) {
	rows, err := db.Query(`SELECT DISTINCT source, COALESCE(team,''), service
		FROM charges ORDER BY 1,2,3`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SeriesKey
	for rows.Next() {
		var k SeriesKey
		if err := rows.Scan(&k.Source, &k.Team, &k.Service); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// AgentFor returns the agent whose own spend produced a day on a series, and
// how that was established. An empty agent is the honest answer whenever no
// attribution reaches that far, and the caller must say so rather than guess.
func AgentFor(db *sql.DB, k SeriesKey, day string) (agent, confidence string, err error) {
	err = db.QueryRow(`SELECT agent, confidence FROM attribution
		WHERE source=? AND team=? AND service=? AND ? BETWEEN day_start AND day_end`,
		k.Source, k.Team, k.Service, day).Scan(&agent, &confidence)
	if err == sql.ErrNoRows {
		return "", "", nil
	}
	return agent, confidence, err
}
