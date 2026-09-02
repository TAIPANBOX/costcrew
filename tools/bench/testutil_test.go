package main

import (
	"database/sql"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/store"
)

// seededTestDB is a fresh store brought up exactly the way ensureSeeded
// brings up a fresh -dir: the generated charges, the roster, and one
// detection pass. Every test that needs the real fixture's two known
// driver cases (E02 on gcp/GKE, E04 on onprem/Batch cluster) uses this,
// rather than each test hand-building its own subset of the estate, so a
// change to the fixture is felt here the same way it would be felt by a
// real run.
func seededTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	db := st.DB()
	if err := ensureSeeded(db); err != nil {
		t.Fatal(err)
	}
	return db
}
