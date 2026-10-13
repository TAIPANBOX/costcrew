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
	var team any // NULL: the step is split across the desk's teams (section 11)
	if d.Team != "" {
		team = d.Team
	}
	_, err := x.Exec(`INSERT INTO drivers
		(date_start, date_end, scope, label, kind, source, expected_daily_cents, team)
		VALUES (?,?,?,?,?,?,?,?)`,
		d.Start, d.End, d.Scope, d.Label, d.Kind, d.Source, expected, team)
	return err
}

// EnsureDriverColumns runs every idempotent column addition the drivers table
// has gained since it was first created. Each is guarded by a column check, so
// a second run changes nothing, and a row written before a column existed
// reads NULL in it.
func EnsureDriverColumns(db *sql.DB) error {
	if err := EnsureDriverExpectedColumn(db); err != nil {
		return err
	}
	return EnsureDriverTeamColumn(db)
}

// EnsureDriverTeamColumn adds drivers.team, the one team an announced decision
// moves (ANNOUNCED-DRIVER-SPEC.md section 11). NULL means the step is split.
func EnsureDriverTeamColumn(db *sql.DB) error {
	return ensureDriverColumn(db, "team", "TEXT")
}

// ensureDriverColumn adds one column to drivers when it is not there yet.
func ensureDriverColumn(db *sql.DB, name, decl string) error {
	rows, err := db.Query(`SELECT name FROM pragma_table_info('drivers')`)
	if err != nil {
		return err
	}
	have := false
	for rows.Next() {
		var col string
		if err := rows.Scan(&col); err != nil {
			rows.Close()
			return err
		}
		if col == name {
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
	_, err = db.Exec(`ALTER TABLE drivers ADD COLUMN ` + name + ` ` + decl)
	return err
}

// EnsureDriverExpectedColumn adds drivers.expected_daily_cents to a store made
// before driver.announced (ANNOUNCED-DRIVER-SPEC.md section 3). CREATE TABLE IF
// NOT EXISTS does nothing to a table that already exists, so an older store
// needs the ALTER as well. The ALTER is guarded by a column check, the way
// internal/anomaly's hint columns are, so the second run finds the column and
// changes nothing. A row written before the column reads NULL.
func EnsureDriverExpectedColumn(db *sql.DB) error {
	return ensureDriverColumn(db, "expected_daily_cents", "INTEGER")
}
