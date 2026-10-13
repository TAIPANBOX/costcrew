package estate_test

// ANNOUNCED-DRIVER-SPEC.md section 3: drivers gains one nullable column, the
// announced daily amount. A row written before the column reads NULL and
// behaves exactly as it did; the column is added by a guarded ALTER that a
// second run does nothing to.
//
// Red against the stub EnsureDriverExpectedColumn (no-op) and an InsertDriver
// that does not yet write the amount: the old database cannot be read because
// Drivers selects the column, and the announced row reads back with no amount.

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/estate"
	"github.com/TAIPANBOX/costcrew/internal/world"

	_ "modernc.org/sqlite"
)

func openTemp(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatal(err)
	}
}

func seededTemp(t *testing.T) *sql.DB {
	t.Helper()
	db := openTemp(t)
	if _, err := estate.Seed(db); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestAnOldDatabaseGainsTheColumnAndItsDriversReadAsBefore(t *testing.T) {
	db := openTemp(t)
	mustExec(t, db, `CREATE TABLE drivers(date_start TEXT, date_end TEXT, scope TEXT, label TEXT, kind TEXT, source TEXT)`)
	mustExec(t, db, `INSERT INTO drivers VALUES ('2026-09-01','2026-09-30','svc','batch','recurring','onprem')`)
	if err := estate.EnsureDriverExpectedColumn(db); err != nil {
		t.Fatal(err)
	}
	if err := estate.EnsureDriverExpectedColumn(db); err != nil { // idempotent
		t.Fatalf("second run: %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('drivers') WHERE name='expected_daily_cents'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("after EnsureDriverExpectedColumn the column is missing (%d matches)", n)
	}
	ds, err := estate.Drivers(db)
	if err != nil || len(ds) != 1 {
		t.Fatalf("drivers = %v, %v", ds, err)
	}
	if ds[0].Expected != nil {
		t.Fatalf("an old row read Expected=%d, want nil (NULL, not 0)", *ds[0].Expected)
	}
}

func TestAnAnnouncedDriverRoundTripsItsAmount(t *testing.T) {
	db := seededTemp(t)
	v := int64(-30000)
	if err := estate.InsertDriver(db, world.Driver{Start: "2026-11-01", End: "2026-11-30", Scope: "svc", Label: "swap", Kind: "announced", Source: "ai", Expected: &v}); err != nil {
		t.Fatal(err)
	}
	ds, err := estate.Drivers(db)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range ds {
		if d.Label != "swap" {
			continue
		}
		if d.Expected == nil || *d.Expected != v {
			t.Fatalf("Expected = %v, want -30000", d.Expected)
		}
		return
	}
	t.Fatal("the announced row was not read back")
}

// A one-time or recurring row written through the same writer carries no
// amount: the column stays NULL, which is what "nil for the other kinds" means.
func TestARecurringDriverWritesNoAmount(t *testing.T) {
	db := seededTemp(t)
	if err := estate.InsertDriver(db, world.Driver{Start: "2026-11-01", End: "2026-11-30", Scope: "svc", Label: "rhythm", Kind: "recurring", Source: "aws"}); err != nil {
		t.Fatal(err)
	}
	ds, err := estate.Drivers(db)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range ds {
		if d.Label == "rhythm" && d.Expected != nil {
			t.Fatalf("a recurring driver read Expected=%d, want nil", *d.Expected)
		}
	}
}
