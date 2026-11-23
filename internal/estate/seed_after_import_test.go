package estate

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// TestSeedDoesNotReseedOverImportedCalls: an installation that holds real
// gateway calls is not a fresh one, even when no call was spend and charges
// is empty. Seeding it again put the generated estate back on every start,
// and the next import with the launcher's yes wiped it again.
func TestSeedDoesNotReseedOverImportedCalls(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`CREATE TABLE ai_calls(file_sha256 TEXT, row_no INTEGER, blocked INTEGER);
		INSERT INTO ai_calls VALUES ('x', 1, 1)`); err != nil {
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
