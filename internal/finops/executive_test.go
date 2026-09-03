package finops_test

// C8-SPEC.md section 2's first bullet: the four numbers roles.yaml's own
// executive-reporter owes ("four numbers, each with its reason ..., and
// never from a template") are named ONCE here, in internal/finops, so the
// packet internal/deliver builds and any page that ever reads them cannot
// disagree. This file holds Executive() itself; internal/deliver's own
// tests hold the packet section it feeds, and internal/finops/apply_test.go
// holds explainer.publish being wired to crew.Publish.

import (
	"database/sql"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/estate"
	"github.com/TAIPANBOX/costcrew/internal/finops"
)

// executiveFigureByID finds one of Executive()'s four by id, or fails the
// test: every case below wants to assert on a NAMED figure, not on
// positional order, so a reordering of executiveKPIIDs cannot silently
// break every assertion in this file at once.
func executiveFigureByID(t *testing.T, figs []finops.ExecutiveFigure, id string) finops.ExecutiveFigure {
	t.Helper()
	for _, f := range figs {
		if f.ID == id {
			return f
		}
	}
	t.Fatalf("Executive() named no figure %q among %d returned", id, len(figs))
	return finops.ExecutiveFigure{}
}

// Red first, against main: Executive does not exist at all yet, so this
// (and every other test in this file) fails to compile -- "undefined:
// finops.Executive" -- which is as red as a test gets. C8-SPEC.md section 4:
// "the exec-reporter packet carries the four numbers with previous values
// and the last posted explanation ... (today no such section)".
func TestExecutiveReturnsFourFiguresWithPreviousValuesAndDeltas(t *testing.T) {
	db := kpiReadyDB(t)
	figs, period, previous, err := finops.Executive(db)
	if err != nil {
		t.Fatal(err)
	}
	if period == "" {
		t.Fatal("Executive returned no period at all on a seeded, multi-month estate")
	}
	if previous == "" {
		t.Fatalf("Executive found no previous period on a seeded, multi-month estate " +
			"(the estate has more than one month, so this must not be the boundary case)")
	}
	if len(figs) != 4 {
		t.Fatalf("Executive returned %d figures, want exactly the four roles.yaml's own "+
			"executive-reporter owes", len(figs))
	}

	// period and previous must be the exact two entries Months() itself
	// puts beside each other, not merely SOME earlier month: a mutant that
	// computed the delta against the wrong period (skipping one, or reusing
	// the same period twice) would still produce a Numeric-minus-PrevNumeric
	// that is internally consistent, so that arithmetic check alone proves
	// nothing about which period was used. This does.
	months, err := finops.Months(db)
	if err != nil || len(months) < 3 {
		t.Fatalf("months = %v, %v: this test needs at least three to tell \"the period "+
			"before\" apart from \"some period before\"", months, err)
	}
	wantPeriod, wantPrevious := months[1], months[2]
	if period != wantPeriod {
		t.Fatalf("period = %q, want %q (Months()[1], the last COMPLETE month)", period, wantPeriod)
	}
	if previous != wantPrevious {
		t.Fatalf("previous = %q, want %q (Months()[2], the one right before period, "+
			"not some other month)", previous, wantPrevious)
	}
	wantCov, err := finops.Allocate(db, wantPeriod)
	if err != nil {
		t.Fatal(err)
	}
	wantPrevCov, err := finops.Allocate(db, wantPrevious)
	if err != nil {
		t.Fatal(err)
	}

	// allocation-coverage and unallocated-share both come from Allocate(),
	// which DOES read the period argument: on a seeded, multi-month estate
	// they have a real value, a real previous value, and a real delta.
	cov := executiveFigureByID(t, figs, "allocation-coverage")
	if cov.Blocked != "" {
		t.Fatalf("allocation-coverage refused on a seeded estate with cost in it: %q", cov.Blocked)
	}
	if !cov.HasPeriod {
		t.Error("allocation-coverage says no previous period on a multi-month estate")
	}
	if !cov.PrevHasVal {
		t.Error("allocation-coverage has no previous VALUE on a multi-month estate")
	}
	if !cov.HasDelta {
		t.Error("allocation-coverage has no delta when both periods have a value")
	}
	if got := cov.Numeric - cov.PrevNumeric; got != cov.Delta {
		t.Errorf("delta = %.4f, want Numeric(%.4f) - PrevNumeric(%.4f) = %.4f",
			cov.Delta, cov.Numeric, cov.PrevNumeric, got)
	}
	// Against the SOURCE of truth directly, not against Executive's own
	// arithmetic: this is what actually catches "computed the delta from
	// the wrong period", where Numeric and PrevNumeric could otherwise both
	// come from the wrong (but self-consistent) pair of months.
	if diff := cov.Numeric - wantCov.Coverage; diff > 0.05 || diff < -0.05 {
		t.Errorf("allocation-coverage.Numeric = %.4f, want Allocate(%s).Coverage = %.4f",
			cov.Numeric, wantPeriod, wantCov.Coverage)
	}
	if diff := cov.PrevNumeric - wantPrevCov.Coverage; diff > 0.05 || diff < -0.05 {
		t.Errorf("allocation-coverage.PrevNumeric = %.4f, want Allocate(%s).Coverage = %.4f "+
			"(the period right BEFORE, not some other one)", cov.PrevNumeric, wantPrevious, wantPrevCov.Coverage)
	}

	// cost-per-outcome never computes in this console yet (C7 connects the
	// outcome metric): it is refused, unconditionally, on ANY estate.
	cpo := executiveFigureByID(t, figs, "cost-per-outcome")
	if cpo.Blocked == "" {
		t.Error("cost-per-outcome is not refused on a seeded estate; it should be, until C7")
	}
}

// C8-SPEC.md section 4: "a refused KPI appears as refused in the packet,
// not as zero". Held here at the SOURCE of the figure -- before any
// rendering -- because the rendering guard (internal/deliver) can only stay
// honest if the thing it renders never claims a value it does not have.
func TestExecutiveNeverGivesARefusedKPIAValue(t *testing.T) {
	db := kpiReadyDB(t)
	figs, _, _, err := finops.Executive(db)
	if err != nil {
		t.Fatal(err)
	}
	cpo := executiveFigureByID(t, figs, "cost-per-outcome")
	if cpo.HasVal {
		t.Error("cost-per-outcome is refused (Blocked != \"\") and also claims HasVal")
	}
	if cpo.Numeric != 0 {
		t.Errorf("a refused KPI's Numeric is %v, want the Go zero value 0: a renderer that "+
			"forgets to check Blocked must have something to show that is honestly wrong, "+
			"not something that happens to look like a real reading", cpo.Numeric)
	}
}

// C8-SPEC.md section 4's boundary: "a first period with no previous value
// (\"no previous period\")". Built by hand rather than with seeded(): the
// seeded estate always carries several months (aMonth's own assertion
// requires at least two), so the boundary needs its own single-month store.
func TestExecutiveSaysNoPreviousPeriodForTheFirstPeriod(t *testing.T) {
	db := singleMonthDB(t)
	figs, period, previous, err := finops.Executive(db)
	if err != nil {
		t.Fatal(err)
	}
	if period != "2026-01" {
		t.Fatalf("period = %q, want 2026-01 (the only month this store has)", period)
	}
	if previous != "" {
		t.Fatalf("previous = %q, want \"\": this store has one month, so there is no period before it", previous)
	}
	if len(figs) != 4 {
		t.Fatalf("Executive returned %d figures on a single-month store, want 4", len(figs))
	}
	for _, f := range figs {
		if f.HasPeriod {
			t.Errorf("%s: HasPeriod is true on the estate's very first period", f.ID)
		}
	}
}

// singleMonthDB is a store with exactly one month of charges: schema for
// everything KPIs() and Allocate() touch, no SeedRules (an empty rules
// table is a legal store -- everything reports unallocated, which is still
// a real coverage number, not a refusal), and one direct charge so
// allocation-coverage and unallocated-share both have a value rather than
// refusing for an empty period.
func singleMonthDB(t *testing.T) *sql.DB {
	t.Helper()
	db := estateSchemaOnlyDB(t)
	if _, err := db.Exec(`INSERT INTO charges
		(source, day, service, team, category, billed_cents)
		VALUES ('aws', '2026-01-15', 'Amazon EC2', 'ml-platform', 'Compute', 10000)`); err != nil {
		t.Fatal(err)
	}
	return db
}

// estateSchemaOnlyDB is every schema KPIs()/Allocate() reads from, with no
// rows: internal/deliver's own deliverTestDB holds the identical shape for
// the identical reason (Packet() and Executive() are read by the SAME
// analyst's packet, C8-SPEC.md section 2, so the two test files' fixtures
// are deliberately built the same way rather than two different ways that
// could each pass while disagreeing about what a bare store looks like).
func estateSchemaOnlyDB(t *testing.T) *sql.DB {
	t.Helper()
	db := rawDB(t)
	for _, schema := range []string{estate.SeedSchema, finops.Schema, crew.Schema, anomaly.Schema} {
		if _, err := db.Exec(schema); err != nil {
			t.Fatal(err)
		}
	}
	if err := crew.EnsureLiveSpendLedger(db); err != nil {
		t.Fatal(err)
	}
	return db
}

// kpiReadyDB is the full seeded estate PLUS the schemas KPIs() itself reads
// from (tasks, anomalies, the live-spend ledger) that seeded() alone does
// not create -- apply_test.go's own applyTestDB holds the identical three
// calls, for the identical reason: estate.Seed and crew.Schema are two
// different migrations and nothing runs the second one for a caller that
// only asked for the first.
func kpiReadyDB(t *testing.T) *sql.DB {
	t.Helper()
	db := seeded(t)
	if _, err := db.Exec(crew.Schema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(anomaly.Schema); err != nil {
		t.Fatal(err)
	}
	if err := crew.EnsureLiveSpendLedger(db); err != nil {
		t.Fatal(err)
	}
	return db
}
