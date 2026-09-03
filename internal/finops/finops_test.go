package finops_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/TAIPANBOX/costcrew/internal/estate"
	"github.com/TAIPANBOX/costcrew/internal/finops"
	"github.com/TAIPANBOX/costcrew/internal/money"
)

func seeded(t *testing.T) *sql.DB {
	t.Helper()
	db := rawDB(t)
	if _, err := estate.Seed(db); err != nil {
		t.Fatal(err)
	}
	if err := finops.SeedRules(db); err != nil {
		t.Fatal(err)
	}
	return db
}

// rawDB is an empty sqlite store, no schema at all: seeded() builds on it
// with estate.Seed's full generated world, and executive_test.go's own
// singleMonthDB builds on it with a single hand-planted charge, for the
// boundary estate.Seed can never produce (it always spans several months).
func rawDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func aMonth(t *testing.T, db *sql.DB) string {
	t.Helper()
	ms, err := finops.Months(db)
	if err != nil || len(ms) < 2 {
		t.Fatalf("months: %v %v", ms, err)
	}
	// The second newest: the newest is partial and a partial month is a
	// different question.
	return ms[1]
}

// The property finance checks first, and the one that decides whether a
// chargeback is accepted or sent back.
func TestAllocationAddsUpToTheBill(t *testing.T) {
	db := seeded(t)
	a, err := finops.Allocate(db, aMonth(t, db))
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Teams) == 0 {
		t.Fatal("no teams in the allocation")
	}
	var loaded money.Cents
	for _, tc := range a.Teams {
		loaded += tc.Loaded()
	}
	bill := a.Direct + a.Shared
	if loaded+a.Unallocated != bill {
		t.Fatalf("the split does not reconcile: teams %s + unallocated %s = %s, bill %s\n"+
			"a chargeback that does not add up to the invoice is one finance sends back",
			loaded, a.Unallocated, loaded+a.Unallocated, bill)
	}
}

// Splitting three ways loses a cent to rounding unless somebody carries it.
func TestTheRemainderGoesSomewhereRatherThanNowhere(t *testing.T) {
	db := seeded(t)
	for _, m := range mustMonths(t, db) {
		a, err := finops.Allocate(db, m)
		if err != nil {
			t.Fatal(err)
		}
		if len(a.Teams) == 0 {
			continue
		}
		var placed money.Cents
		for _, tc := range a.Teams {
			placed += tc.Allocated
		}
		if placed+a.Unallocated != a.Shared {
			t.Errorf("%s: %s of shared cost, %s placed, %s unallocated: a cent went missing",
				m, a.Shared, placed, a.Unallocated)
		}
	}
}

func mustMonths(t *testing.T, db *sql.DB) []string {
	t.Helper()
	ms, err := finops.Months(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) > 4 {
		ms = ms[:4]
	}
	return ms
}

// Shared cost has to actually be shared, or the page has nothing to do.
func TestSharedCostIsRedistributed(t *testing.T) {
	db := seeded(t)
	a, err := finops.Allocate(db, aMonth(t, db))
	if err != nil {
		t.Fatal(err)
	}
	if a.Shared == 0 {
		t.Fatal("no shared cost in the month")
	}
	var moved int
	for _, tc := range a.Teams {
		if tc.Allocated != 0 {
			moved++
		}
	}
	if moved == 0 {
		t.Fatal("nothing was redistributed; the rules did not fire")
	}
	if a.Coverage < 50 {
		t.Errorf("coverage %.1f%%: most of the bill still has no owner", a.Coverage)
	}
}

// Leaving cost unallocated must be visible, not silent. A rule that quietly
// spreads money onto teams that never touched it is worse than one that
// refuses.
func TestUnallocatedIsCountedRatherThanHidden(t *testing.T) {
	db := seeded(t)
	rules, err := finops.Rules(db)
	if err != nil || len(rules) == 0 {
		t.Fatal(err)
	}
	m := aMonth(t, db)
	before, _ := finops.Allocate(db, m)

	for _, r := range rules {
		if err := finops.SetRule(db, r.ID, finops.Unallocated); err != nil {
			t.Fatal(err)
		}
	}
	after, err := finops.Allocate(db, m)
	if err != nil {
		t.Fatal(err)
	}
	if after.Unallocated <= before.Unallocated {
		t.Fatalf("turning every rule off did not increase unallocated cost: %s then %s",
			before.Unallocated, after.Unallocated)
	}
	if after.Placed != 0 {
		t.Errorf("%s was still placed with every rule set to unallocated", after.Placed)
	}
	// And it still reconciles.
	var loaded money.Cents
	for _, tc := range after.Teams {
		loaded += tc.Loaded()
	}
	if loaded+after.Unallocated != after.Direct+after.Shared {
		t.Error("the unallocated path does not reconcile")
	}
}

func TestAnUnknownMethodIsRefused(t *testing.T) {
	db := seeded(t)
	rules, _ := finops.Rules(db)
	if err := finops.SetRule(db, rules[0].ID, finops.Method("whatever-feels-right")); err == nil {
		t.Fatal("an invented method was accepted")
	}
}

// ------------------------------------------------------------- chargeback

// The whole reason a period is closed: the number a team was told in March
// must be the number the page shows in April.
func TestAClosedPeriodStopsMoving(t *testing.T) {
	db := seeded(t)
	m := aMonth(t, db)
	if err := finops.Close(db, m, "yurii"); err != nil {
		t.Fatal(err)
	}
	frozen, err := finops.FrozenPeriod(db, m)
	if err != nil {
		t.Fatal(err)
	}
	if !frozen.Closed || len(frozen.Teams) == 0 {
		t.Fatal("the period did not freeze")
	}
	if frozen.ClosedBy != "yurii" {
		t.Errorf("closed by %q; the person who closed it is the point", frozen.ClosedBy)
	}

	// Change the rules underneath it. The frozen numbers must not move.
	rules, _ := finops.Rules(db)
	for _, r := range rules {
		_ = finops.SetRule(db, r.ID, finops.Even)
	}
	again, err := finops.FrozenPeriod(db, m)
	if err != nil {
		t.Fatal(err)
	}
	if again.Total != frozen.Total {
		t.Fatalf("a closed period moved from %s to %s when the rules changed",
			frozen.Total, again.Total)
	}
}

// And the difference is said out loud rather than hidden.
func TestTrueUpReportsWhatMovedAfterTheClose(t *testing.T) {
	db := seeded(t)
	m := aMonth(t, db)
	if err := finops.Close(db, m, "yurii"); err != nil {
		t.Fatal(err)
	}
	if rows, total, err := finops.TrueUpFor(db, m); err != nil {
		t.Fatal(err)
	} else if len(rows) != 0 || total != 0 {
		t.Fatalf("a period that has not changed reported a true-up of %s", total)
	}

	rules, _ := finops.Rules(db)
	for _, r := range rules {
		_ = finops.SetRule(db, r.ID, finops.Even)
	}
	rows, total, err := finops.TrueUpFor(db, m)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		t.Fatal("the allocation changed and the true-up reported nothing; " +
			"a chargeback that quietly stops matching the invoice is the failure here")
	}
	_ = total
}

func TestClosingTwiceIsRefused(t *testing.T) {
	db := seeded(t)
	m := aMonth(t, db)
	if err := finops.Close(db, m, "yurii"); err != nil {
		t.Fatal(err)
	}
	if err := finops.Close(db, m, "yurii"); err == nil {
		t.Fatal("a closed period was closed again")
	}
}

// Taking a number back from a team without saying why is how a chargeback
// stops being believed.
func TestReopeningNeedsAReason(t *testing.T) {
	db := seeded(t)
	m := aMonth(t, db)
	if err := finops.Close(db, m, "yurii"); err != nil {
		t.Fatal(err)
	}
	for _, blank := range []string{"", "   "} {
		if err := finops.Reopen(db, m, blank); err == nil {
			t.Errorf("reopened with reason %q", blank)
		}
	}
	if closed, _ := finops.IsClosed(db, m); !closed {
		t.Fatal("a reasonless reopen went through anyway")
	}
	if err := finops.Reopen(db, m, "Provider credit landed after the close"); err != nil {
		t.Fatal(err)
	}
	if closed, _ := finops.IsClosed(db, m); closed {
		t.Fatal("the period is still closed after a reopen with a reason")
	}
}
