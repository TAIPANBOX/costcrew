package estate

import (
	"database/sql"

	"github.com/TAIPANBOX/costcrew/internal/world"
)

// execer is *sql.DB and *sql.Tx both: Seed writes drivers inside its own
// transaction, and B3-SPEC.md's apply table writes one outside any
// transaction of its own, and this is the same INSERT either way.
type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// InsertDriver writes one drivers row: extracted from Seed's own inline
// insert (below) so a second caller -- B3-SPEC.md section 3's apply table,
// for driver.one-time and driver.recurring -- writes the same row shape
// rather than a second copy of the column order.
// InsertDriver writes one drivers row. The amount is NULL unless the driver
// carries one (kind "announced"): a one-time or recurring driver writes no
// number, and reads back with Expected nil.
func InsertDriver(x execer, d world.Driver) error {
	var expected any
	if d.Expected != nil {
		expected = *d.Expected
	}
	_, err := x.Exec(`INSERT INTO drivers
		(date_start, date_end, scope, label, kind, source, expected_daily_cents)
		VALUES (?,?,?,?,?,?,?)`,
		d.Start, d.End, d.Scope, d.Label, d.Kind, d.Source, expected)
	return err
}

// EnsureDriverExpectedColumn adds drivers.expected_daily_cents to a store made
// before driver.announced (ANNOUNCED-DRIVER-SPEC.md section 3). CREATE TABLE IF
// NOT EXISTS does nothing to a table that already exists, so an older store
// needs the ALTER as well. The ALTER is guarded by a column check, the way
// internal/anomaly's hint columns are, so the second run finds the column and
// changes nothing. A row written before the column reads NULL.
func EnsureDriverExpectedColumn(db *sql.DB) error {
	rows, err := db.Query(`SELECT name FROM pragma_table_info('drivers')`)
	if err != nil {
		return err
	}
	have := false
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		if name == "expected_daily_cents" {
			have = true
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if have {
		return nil
	}
	_, err = db.Exec(`ALTER TABLE drivers ADD COLUMN expected_daily_cents INTEGER`)
	return err
}
