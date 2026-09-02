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
func InsertDriver(x execer, d world.Driver) error {
	_, err := x.Exec(`INSERT INTO drivers VALUES (?,?,?,?,?,?)`,
		d.Start, d.End, d.Scope, d.Label, d.Kind, d.Source)
	return err
}
