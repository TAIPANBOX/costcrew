package finops_test

// C4-SPEC.md section 4's computation tests: coverage, utilisation, the
// expiry calendar and break-even, all read from the store's own commitments
// table (internal/connectors/tokenfusefocus.go) rather than the generated
// world.Commitments waterline.
//
// Red first, against main before this step: internal/finops had no
// commitments.go at all, so every function below failed to compile
// ("undefined: finops.Coverage" and five siblings) -- the same
// does-not-compile red state internal/deliver/packet_test.go's own header
// comment already treats as genuine red for a builder that does not exist
// yet on the unfixed code.

import (
	"database/sql"
	"math"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/TAIPANBOX/costcrew/internal/connectors"
	"github.com/TAIPANBOX/costcrew/internal/finops"
	"github.com/TAIPANBOX/costcrew/internal/money"
	"github.com/TAIPANBOX/costcrew/internal/world"
)

func commitmentsTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := connectors.EnsureFocusSchema(db); err != nil {
		t.Fatal(err)
	}
	return db
}

func plantRealCharge(t *testing.T, db *sql.DB, source, day, service string, cents int64) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO charges
		(source, day, service, category, billed_cents, provenance)
		VALUES (?,?,?,'Usage',?,'tokenfuse-focus')`, source, day, service, cents); err != nil {
		t.Fatal(err)
	}
}

func plantCommitment(t *testing.T, db *sql.DB, id, kind, status, source, start, end string, qty float64, monthlyCents int64) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO commitments
		(id, kind, status, quantity, unit, source, date_start, date_end, monthly_cents)
		VALUES (?,?,?,?,?,?,?,?,?)`,
		id, kind, status, qty, "hours", source, start, end, monthlyCents); err != nil {
		t.Fatal(err)
	}
}

func TestAsOfDayIsTheLatestChargeDay(t *testing.T) {
	db := commitmentsTestDB(t)
	plantRealCharge(t, db, "ai", "2026-08-01", "Anthropic API", 1000)
	plantRealCharge(t, db, "ai", "2026-09-02", "Anthropic API", 2000)
	plantRealCharge(t, db, "ai", "2026-08-15", "Anthropic API", 3000)
	day, err := finops.AsOfDay(db)
	if err != nil {
		t.Fatal(err)
	}
	if day != "2026-09-02" {
		t.Errorf("AsOfDay = %q, want 2026-09-02", day)
	}
}

func TestHasRealCommitmentsIsFalseOnAFreshStoreTrueOnceOneExists(t *testing.T) {
	db := commitmentsTestDB(t)
	has, err := finops.HasRealCommitments(db)
	if err != nil {
		t.Fatal(err)
	}
	if has {
		t.Fatal("a fresh store already has real commitments")
	}
	plantCommitment(t, db, "cud-1", "cud", "Used", "ai", "2026-01-01", "2027-01-01", 700, 150000)
	has, err = finops.HasRealCommitments(db)
	if err != nil {
		t.Fatal(err)
	}
	if !has {
		t.Fatal("a store with one commitments row still reports none")
	}
}

// TestCoverageIsCommittedOverEligiblePerDeskAndMonth is C4-SPEC.md section
// 4's cents-exact test on a fixture with one commitment: 150000 cents
// committed on the ai desk in 2026-09, 200000 cents of eligible spend the
// same month, coverage is exactly 75%.
func TestCoverageIsCommittedOverEligiblePerDeskAndMonth(t *testing.T) {
	db := commitmentsTestDB(t)
	plantRealCharge(t, db, "ai", "2026-09-01", "Anthropic API", 120000)
	plantRealCharge(t, db, "ai", "2026-09-15", "Anthropic API", 80000)
	plantCommitment(t, db, "cud-1", "cud", "Used", "ai", "2026-01-01", "2027-01-01", 700, 150000)

	rows, err := finops.Coverage(db, "2026-09")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d coverage row(s), want 1: %+v", len(rows), rows)
	}
	r := rows[0]
	if r.Source != "ai" {
		t.Errorf("source = %q, want ai", r.Source)
	}
	if r.CommittedCents != money.Cents(150000) {
		t.Errorf("committed = %s, want 1500.00", r.CommittedCents)
	}
	if r.EligibleCents != money.Cents(200000) {
		t.Errorf("eligible = %s, want 2000.00", r.EligibleCents)
	}
	if !r.OK {
		t.Fatal("coverage refused on a desk with real eligible spend")
	}
	if math.Abs(r.Pct-75.0) > 1e-9 {
		t.Errorf("coverage = %.6f%%, want exactly 75", r.Pct)
	}
}

// TestCoverageOnACloudDeskCountsOnlyComputeDatabaseAndAccelerator is the
// other half of eligibleCents: a cloud desk (unlike ai) is filtered by
// world.ResourceKind, the same classification world.buildCommitments
// already applies to the generated waterline. Amazon EC2 counts; Amazon S3
// (storage, not compute) must not.
func TestCoverageOnACloudDeskCountsOnlyComputeDatabaseAndAccelerator(t *testing.T) {
	db := commitmentsTestDB(t)
	plantRealCharge(t, db, "aws", "2026-09-01", "Amazon EC2", 90000)
	plantRealCharge(t, db, "aws", "2026-09-01", "Amazon S3", 40000)
	plantCommitment(t, db, "aws-sp-1", "savings-plan", "Used", "aws", "2026-01-01", "2027-01-01", 700, 30000)

	rows, err := finops.Coverage(db, "2026-09")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d row(s), want 1", len(rows))
	}
	if rows[0].EligibleCents != money.Cents(90000) {
		t.Errorf("eligible = %s, want 900.00 (EC2 only; S3 is not compute, database or accelerator)",
			rows[0].EligibleCents)
	}
}

// TestCoverageRefusesADeskWithNoEligibleSpend is the named boundary: "a desk
// with no eligible spend (coverage refused, said)". A commitment with no
// charges at all behind it must not report a coverage percentage -- there is
// nothing for the committed spend to be a share OF.
func TestCoverageRefusesADeskWithNoEligibleSpend(t *testing.T) {
	db := commitmentsTestDB(t)
	plantCommitment(t, db, "cud-gcp-1", "cud", "Used", "gcp", "2026-01-01", "2027-01-01", 700, 90000)
	// No charges at all for gcp in 2026-09.

	rows, err := finops.Coverage(db, "2026-09")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d coverage row(s), want 1: %+v", len(rows), rows)
	}
	if rows[0].OK {
		t.Errorf("coverage reported a percentage (%.1f%%) for a desk with no eligible spend", rows[0].Pct)
	}
	if rows[0].EligibleCents != 0 {
		t.Errorf("eligible = %s, want 0", rows[0].EligibleCents)
	}
}

// TestCoverageDoesNotRoundThroughDollarsFirst is mutant (a) in C4-SPEC.md
// section 4, "compute coverage with floats": 333 committed over 1000
// eligible is exactly one third of a percent under 33.4, 33.333...%.
// Rounding either side to the nearest whole DOLLAR before dividing -- 333
// cents to 3.00, 1000 cents (already whole) unchanged -- gives 30% instead,
// a full three points off and nowhere near float64's own rounding noise, so
// this is sensitive to that specific mutation without depending on
// IEEE-754 edge cases: two exactly-representable cent integers whose true
// quotient is not itself exactly representable is precisely where a
// "simplify by rounding to dollars first" rewrite would diverge from
// money.Pct's own direct cents ratio.
func TestCoverageDoesNotRoundThroughDollarsFirst(t *testing.T) {
	db := commitmentsTestDB(t)
	plantRealCharge(t, db, "ai", "2026-09-01", "Anthropic API", 1000)
	plantCommitment(t, db, "cud-1", "cud", "Used", "ai", "2026-01-01", "2027-01-01", 700, 333)

	rows, err := finops.Coverage(db, "2026-09")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || !rows[0].OK {
		t.Fatalf("coverage: %+v", rows)
	}
	want := 33.3
	if math.Abs(rows[0].Pct-want) > 0.05 {
		t.Errorf("coverage = %.4f%%, want close to %.1f (a whole-dollar-first computation gives 30)",
			rows[0].Pct, want)
	}
}

// TestUtilisationIsUsedOverCommittedPerCommitment reuses the same
// cents-exact fixture Coverage's own test does: 200000 cents of eligible
// spend against a 150000-cent commitment is exactly 133.333...%, over 100
// because this commitment is small next to what it is covering -- a
// legitimate reading, not a defect, since a commitment costing less than
// what it displaces is the healthy case, not the wasteful one
// world.Commitment.BelowWaterline() already names on the generated side.
func TestUtilisationIsUsedOverCommittedPerCommitment(t *testing.T) {
	db := commitmentsTestDB(t)
	plantRealCharge(t, db, "ai", "2026-09-01", "Anthropic API", 120000)
	plantRealCharge(t, db, "ai", "2026-09-15", "Anthropic API", 80000)
	plantCommitment(t, db, "cud-1", "cud", "Used", "ai", "2026-01-01", "2027-01-01", 700, 150000)

	rows, err := finops.CommitmentUtilisation(db, "2026-09")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d utilisation row(s), want 1", len(rows))
	}
	r := rows[0]
	if r.ID != "cud-1" {
		t.Errorf("id = %q, want cud-1", r.ID)
	}
	if !r.OK {
		t.Fatal("utilisation refused on a commitment with a real monthly price")
	}
	want := float64(200000) / float64(150000) * 100
	if math.Abs(r.Pct-want) > 1e-9 {
		t.Errorf("utilisation = %.6f%%, want %.6f", r.Pct, want)
	}
}

// TestUtilisationOfAZeroQuantityCommitmentDoesNotCrash is the named
// boundary: "a commitment with zero quantity". Quantity is not the
// utilisation formula's own divisor (monthly_cents is), so this proves the
// boundary is handled by construction rather than by a special case that
// could rot -- the row still computes a real percentage.
func TestUtilisationOfAZeroQuantityCommitmentDoesNotCrash(t *testing.T) {
	db := commitmentsTestDB(t)
	plantRealCharge(t, db, "ai", "2026-09-01", "Anthropic API", 50000)
	plantCommitment(t, db, "new-1", "cud", "Unused", "ai", "2026-01-01", "2027-01-01", 0, 100000)

	rows, err := finops.CommitmentUtilisation(db, "2026-09")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d row(s), want 1", len(rows))
	}
	if rows[0].Quantity != 0 {
		t.Errorf("quantity = %v, want 0 (carried through, not defaulted away)", rows[0].Quantity)
	}
	if !rows[0].OK {
		t.Error("utilisation refused on a zero-QUANTITY commitment, which has a real monthly price")
	}
}

// TestExpiryCalendarListsWithinNinetyDaysNotBeyond is C4-SPEC.md section
// 4's named case: "the calendar lists an expiry inside 90 days and not one
// outside", plus the boundary "an expiry today" (day 0, inclusive).
func TestExpiryCalendarListsWithinNinetyDaysNotBeyond(t *testing.T) {
	db := commitmentsTestDB(t)
	const from = "2026-09-02"
	plantCommitment(t, db, "expires-today", "cud", "Used", "ai",
		"2026-01-01", from, 700, 10000)
	plantCommitment(t, db, "expires-soon", "cud", "Used", "ai",
		"2026-01-01", "2026-11-01", 700, 10000) // 60 days out
	plantCommitment(t, db, "expires-later", "reserved", "Used", "aws",
		"2026-01-01", "2027-06-01", 700, 10000) // far beyond 90 days

	rows, err := finops.ExpiringCommitments(db, 90, from)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, r := range rows {
		got[r.ID] = true
	}
	if !got["expires-today"] {
		t.Error("an expiry ON the reference day is not listed; 0 days out must count as within 90")
	}
	if !got["expires-soon"] {
		t.Error("an expiry 60 days out is not listed")
	}
	if got["expires-later"] {
		t.Error("an expiry far beyond 90 days is listed")
	}
	if len(rows) != 2 {
		t.Errorf("got %d row(s), want exactly 2", len(rows))
	}
}

// TestBreakEvenMonthsForAKnownFixture is C4-SPEC.md section 4's cents-exact
// break-even case: a commitment priced at 150000 cents a month against
// 200000 cents of eligible on-demand spend saves exactly 50000 cents a
// month, and 150000 / 50000 is exactly 3, with nothing to round.
func TestBreakEvenMonthsForAKnownFixture(t *testing.T) {
	db := commitmentsTestDB(t)
	plantRealCharge(t, db, "ai", "2026-09-01", "Anthropic API", 120000)
	plantRealCharge(t, db, "ai", "2026-09-15", "Anthropic API", 80000)
	plantCommitment(t, db, "cud-1", "cud", "Used", "ai", "2026-01-01", "2027-01-01", 700, 150000)

	rows, err := finops.BreakEvens(db, "2026-09")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d break-even row(s), want 1", len(rows))
	}
	r := rows[0]
	if !r.OK {
		t.Fatalf("break-even refused on a candidate that plainly saves money: %+v", r)
	}
	if r.MonthlySavingCents != money.Cents(50000) {
		t.Errorf("monthly saving = %s, want 500.00", r.MonthlySavingCents)
	}
	if r.Months != 3 {
		t.Errorf("break-even = %d month(s), want exactly 3", r.Months)
	}
}

// TestCommitmentsFallsBackToTheGeneratedWaterline is the SaaS page's own
// switch: a fresh store, nothing imported, gets world.Commitments back
// unchanged, real=false.
func TestCommitmentsFallsBackToTheGeneratedWaterline(t *testing.T) {
	db := commitmentsTestDB(t)
	rows, real, err := finops.Commitments(db)
	if err != nil {
		t.Fatal(err)
	}
	if real {
		t.Error("real=true on a store with no connector-written commitments")
	}
	if len(rows) != len(world.Commitments) {
		t.Errorf("got %d row(s), want the generated waterline's own %d", len(rows), len(world.Commitments))
	}
}

// TestCommitmentsReadsRealRowsOnceAnyExist proves the switch flips, and that
// the mapped row carries the commitment's own id and a real utilisation
// figure rather than an empty or randomised one.
func TestCommitmentsReadsRealRowsOnceAnyExist(t *testing.T) {
	db := commitmentsTestDB(t)
	plantRealCharge(t, db, "ai", "2026-09-01", "Anthropic API", 120000)
	plantRealCharge(t, db, "ai", "2026-09-15", "Anthropic API", 80000)
	plantCommitment(t, db, "cud-1", "cud", "Used", "ai", "2026-01-01", "2027-01-01", 700, 150000)

	rows, real, err := finops.Commitments(db)
	if err != nil {
		t.Fatal(err)
	}
	if !real {
		t.Fatal("real=false on a store with a connector-written commitment")
	}
	if len(rows) != 1 {
		t.Fatalf("got %d row(s), want 1", len(rows))
	}
	r := rows[0]
	if r.Source != "ai" {
		t.Errorf("source = %q, want ai", r.Source)
	}
	if !strings.Contains(r.Name, "cud-1") {
		t.Errorf("name = %q, does not carry the commitment's own id", r.Name)
	}
	if r.Expires != "2027-01-01" {
		t.Errorf("expires = %q, want 2027-01-01", r.Expires)
	}
	want := float64(200000) / float64(150000) * 100
	if math.Abs(r.Used-want) > 1e-9 {
		t.Errorf("used = %.6f%%, want %.6f", r.Used, want)
	}
}

// TestBreakEvenNeverForACommitmentThatCostsMoreThanOnDemand: when the
// on-demand run rate does not exceed the commitment's own price, buying it
// never breaks even, and this must be SAID rather than printed as a
// negative or a divide-by-zero month count.
func TestBreakEvenNeverForACommitmentThatCostsMoreThanOnDemand(t *testing.T) {
	db := commitmentsTestDB(t)
	plantRealCharge(t, db, "ai", "2026-09-01", "Anthropic API", 40000)
	plantCommitment(t, db, "over-1", "cud", "Unused", "ai", "2026-01-01", "2027-01-01", 700, 150000)

	rows, err := finops.BreakEvens(db, "2026-09")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d row(s), want 1", len(rows))
	}
	if rows[0].OK {
		t.Errorf("break-even reported %d months for a commitment costing more than on-demand", rows[0].Months)
	}
}
