package estate

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// TestSeedDoesNotReseedOverImportedCalls: an installation whose imported
// gateway calls include spend is not a fresh one, whatever charges holds:
// seeding it would put generated money under real decisions.
func TestSeedDoesNotReseedOverImportedCalls(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`CREATE TABLE ai_calls(file_sha256 TEXT, row_no INTEGER, blocked INTEGER, price_basis TEXT);
		INSERT INTO ai_calls VALUES ('x', 1, 0, 'book')`); err != nil {
		t.Fatal(err)
	}
	n, err := Seed(db)
	if err != nil {
		t.Fatal(err)
	}
	var charges int
	db.QueryRow(`SELECT COUNT(*) FROM charges`).Scan(&charges)
	if n != 0 || charges != 0 {
		t.Errorf("Seed wrote %d (charges %d) over a store that holds imported calls", n, charges)
	}
}

// TestSeedStillSeedsAFreshStore: the control. No charges and no imported
// calls is a fresh installation, and it is seeded.
func TestSeedStillSeedsAFreshStore(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`CREATE TABLE ai_calls(file_sha256 TEXT, row_no INTEGER, blocked INTEGER)`); err != nil {
		t.Fatal(err)
	}
	n, err := Seed(db)
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Error("a fresh store with an empty ai_calls table was not seeded")
	}
}

// TestSeedReseedsAStoreHoldingOnlyCallsThatWereNotSpend: a store an older
// build wiped (blocked or fallback-priced calls only, charges empty) is
// seeded again, which repairs it; the importer now keeps the estate for such
// calls, so this happens once.
func TestSeedReseedsAStoreHoldingOnlyCallsThatWereNotSpend(t *testing.T) {
	for _, row := range []string{"('x', 1, 1, NULL)", "('x', 1, 0, 'fallback')"} {
		db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "s.db"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`CREATE TABLE ai_calls(file_sha256 TEXT, row_no INTEGER, blocked INTEGER, price_basis TEXT);
			INSERT INTO ai_calls VALUES ` + row); err != nil {
			t.Fatal(err)
		}
		n, err := Seed(db)
		db.Close()
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			t.Errorf("a store holding only %s was not seeded", row)
		}
	}
}
