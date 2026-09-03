package main

// B7-SPEC.md section 3: "It writes nothing to the estate: no task, no
// artifact, no charge, no journal row. It reads a store and prints. A bench
// that changes the thing it measures is not a bench."

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
	"github.com/TAIPANBOX/costcrew/internal/estate"
	"github.com/TAIPANBOX/costcrew/internal/store"
)

func rowCount(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
		t.Fatalf("counting %s: %v", table, err)
	}
	return n
}

func snapshotRowCounts(t *testing.T, dir string) map[string]int {
	t.Helper()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	db := st.DB()
	return map[string]int{
		"charges":   rowCount(t, db, "charges"),
		"drivers":   rowCount(t, db, "drivers"),
		"anomalies": rowCount(t, db, "anomalies"),
		"analysts":  rowCount(t, db, "analysts"),
		"tasks":     rowCount(t, db, "tasks"),
		"artifacts": rowCount(t, db, "artifacts"),
	}
}

func TestBenchWritesNothingToTheEstate(t *testing.T) {
	dir := t.TempDir()

	// Seed once, exactly as run() does on a fresh -dir, so the "before"
	// snapshot is what the estate looks like the moment a bench could
	// start reading it.
	if code, _, errOut := runArgs(t, "-dir", dir, "-skill", "investigate", "-engine", "mock", "-seed", "3"); code != 0 {
		t.Fatalf("seeding run failed: exit %d, stderr %s", code, errOut)
	}
	before := snapshotRowCounts(t, dir)
	if before["charges"] == 0 || before["anomalies"] == 0 || before["analysts"] == 0 {
		t.Fatal("the seeding this test depends on did not actually seed anything")
	}

	// A second full bench run against the SAME already-seeded dir: this is
	// the run under test, since the first one above already covers whatever
	// ensureSeeded itself writes (which is seeding, not the bench's own
	// scoring pass -- see main.go's ensureSeeded comment for why that is
	// sanctioned separately). -skill investigate so both known cases score
	// rather than one.
	if code, out, errOut := runArgs(t, "-dir", dir, "-skill", "investigate", "-engine", "mock", "-seed", "3"); code != 0 {
		t.Fatalf("the run itself failed: exit %d, stderr %s, stdout %s", code, errOut, out)
	}

	after := snapshotRowCounts(t, dir)
	for table, want := range before {
		if got := after[table]; got != want {
			t.Errorf("%s: %d rows before the bench ran, %d after: the bench wrote to it",
				table, want, got)
		}
	}

	// No journal row: the hash-chained journal file must not even exist,
	// since nothing in this command ever opens a Recorder against it.
	if _, err := os.Stat(filepath.Join(dir, "events.ndjson")); !os.IsNotExist(err) {
		t.Errorf("events.ndjson exists after a bench run (stat err: %v); the bench "+
			"journalled something", err)
	}
}

// Coordinator review of PR #25, 2026-09-03: ensureSeeded ran anomaly.Run
// unconditionally, so pointing the bench at an EXISTING store -- charges
// already there, no detection pass of the bench's own to answer for --
// would ADD anomaly rows to data the bench did not seed. This is that
// store: charges seeded directly (not through the bench, standing in for a
// live console's own data, or charges newer than whatever detection last
// ran), an anomalies table that exists and holds nothing, and the bench
// run against it after the fact.
func TestBenchDoesNotDetectAgainstAnExistingStore(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := estate.Seed(st.DB()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(anomaly.Schema); err != nil {
		t.Fatal(err)
	}
	before := rowCount(t, st.DB(), "anomalies")
	if before != 0 {
		t.Fatalf("this test's own fixture already carries %d anomalies; it needs zero "+
			"to prove the bench did not just add to an existing count", before)
	}
	st.Close()

	if code, _, errOut := runArgs(t, "-dir", dir, "-skill", "investigate", "-engine", "mock"); code != 0 {
		t.Fatalf("the run itself failed: exit %d, stderr %s", code, errOut)
	}

	st2, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	if after := rowCount(t, st2.DB(), "anomalies"); after != before {
		t.Errorf("anomalies: %d rows before the bench ran against an existing store, "+
			"%d after: the bench ran detection against a store it did not seed",
			before, after)
	}
}
