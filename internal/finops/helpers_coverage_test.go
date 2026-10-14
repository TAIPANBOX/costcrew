package finops_test

import (
	"database/sql"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/estate"
)

// covSchemaDB is an empty store with the estate's own tables and the crew's
// (the decision tables an announced driver is answered through), and no rows,
// so a test plants exactly the rows it reasons about.
func covSchemaDB(t *testing.T) *sql.DB {
	t.Helper()
	db := rawDB(t)
	if _, err := db.Exec(estate.SeedSchema); err != nil {
		t.Fatal(err)
	}
	if err := estate.EnsureDriverExpectedColumn(db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(crew.Schema); err != nil {
		t.Fatal(err)
	}
	return db
}

// covDayCharges plants one usage charge of cents on each of days, on the
// given desk and service, under one team.
func covDayCharges(t *testing.T, db *sql.DB, source, service string, cents int64, days ...string) {
	t.Helper()
	for _, day := range days {
		if _, err := db.Exec(`INSERT INTO charges
			(source, day, service, team, category, billed_cents) VALUES (?,?,?,?,'Usage',?)`,
			source, day, service, "team-a", cents); err != nil {
			t.Fatal(err)
		}
	}
}

// covExec runs one statement that is part of the fault being set up, and
// fails the test if the store refuses it.
func covExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("setting up the fault: %v", err)
	}
}
