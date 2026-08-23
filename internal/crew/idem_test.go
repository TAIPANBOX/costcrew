package crew_test

import (
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/store"
)

// The mandate backfill runs on every start, so a second run must report zero.
//
// It reported two, on every start, forever, on every installation: two
// analysts were hired on the fixture's last day, the skip condition read that
// date as the seed script's rather than as theirs, and they were rewritten
// with the values they already had. SQLite counts the rows an UPDATE matched
// rather than the rows it changed, so the number was true about the statement
// and false about the estate.
func TestBackfillMandateConverges(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := crew.SeedRoster(st.DB(), "installer"); err != nil {
		t.Fatal(err)
	}
	first, err := crew.BackfillMandate(st.DB(), "installer")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		n, err := crew.BackfillMandate(st.DB(), "installer")
		if err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatalf("run %d filled in the mandate for %d analysts; the first "+
				"run did %d and nothing has changed since, so every start "+
				"reports work that was never missing", i+2, n, first)
		}
	}
	// And it still DOES the work when there is work: a mandate cleared by hand
	// must come back, or the convergence above was bought by doing nothing.
	if _, err := st.DB().Exec(
		`UPDATE analysts SET mission='', rights='' WHERE name=(SELECT MIN(name) FROM analysts)`); err != nil {
		t.Fatal(err)
	}
	if n, err := crew.BackfillMandate(st.DB(), "installer"); err != nil || n != 1 {
		t.Errorf("a cleared mandate was refilled for %d analysts, want 1 (%v)", n, err)
	}
}
