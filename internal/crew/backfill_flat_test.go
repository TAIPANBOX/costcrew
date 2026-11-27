package crew_test

// BackfillMandate's two repairs that only an older seeding needs: the hire
// date every analyst shared, and an analyst nobody owned.

import (
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/world"
)

func TestBackfillStaggersAFlatHireDateAndClaimsTheUnowned(t *testing.T) {
	db, _ := withRoster(t)
	if _, err := db.Exec(`UPDATE analysts SET hired=?, owner='unclaimed' WHERE name IN ('triage-aws','triage-gcp')`,
		world.LastDay); err != nil {
		t.Fatal(err)
	}
	n, err := crew.BackfillMandate(db, "new-owner")
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("filled in %d analysts, want the two with a flat date and no owner", n)
	}
	a, _ := crew.GetAnalyst(db, "triage-aws")
	b, _ := crew.GetAnalyst(db, "triage-gcp")
	for _, x := range []crew.Analyst{a, b} {
		if x.Owner != "new-owner" {
			t.Errorf("%s owner %q, want new-owner", x.Name, x.Owner)
		}
		if x.Hired == "" || x.Hired == world.LastDay {
			t.Errorf("%s hired %q: the seed script's flat date was kept", x.Name, x.Hired)
		}
	}
	if a.Hired == b.Hired {
		t.Errorf("both hired on %s: the dates are not staggered", a.Hired)
	}
	// An owner somebody set is never overwritten, and a second run converges.
	if n, err := crew.BackfillMandate(db, "someone-else"); err != nil || n != 0 {
		t.Errorf("second run: %d, %v", n, err)
	}
	if a2, _ := crew.GetAnalyst(db, "triage-aws"); a2.Owner != "new-owner" {
		t.Errorf("a second backfill moved the owner to %q", a2.Owner)
	}
}
