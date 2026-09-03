package finops_test

// C3-SPEC.md: the projection becomes driver-aware, the forecaster's packet
// says which drivers moved it and by how much, and the accuracy KPI grades
// the frozen figure and names the largest miss's driver when one exists.
//
// These tests build their own minimal store (estate.SeedSchema only, never
// estate.Seed) rather than reusing the big generated fixture: a cents-exact
// claim needs numbers a reader can check by hand, and the generated estate's
// own noise and growth curves make that arithmetic unreadable.

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/estate"
	"github.com/TAIPANBOX/costcrew/internal/finops"
	"github.com/TAIPANBOX/costcrew/internal/world"
)

func schemaOnlyDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(estate.SeedSchema); err != nil {
		t.Fatal(err)
	}
	return db
}

func plantCharge(t *testing.T, db *sql.DB, source, day, service string, cents int64) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO charges
		(source, day, service, team, category, billed_cents) VALUES (?,?,?,?,?,?)`,
		source, day, service, "test-team", "Usage", cents); err != nil {
		t.Fatal(err)
	}
}

func plantDriver(t *testing.T, db *sql.DB, source, scope, label, kind, start, end string) {
	t.Helper()
	if err := estate.InsertDriver(db, world.Driver{
		Start: start, End: end, Scope: scope, Label: label, Kind: kind, Source: source,
	}); err != nil {
		t.Fatal(err)
	}
}

// A one-time driver inside the days that have landed: the naive run rate
// (Project) smears its whole $50 bump across the days-extended-to-the-month
// ratio, counting it for more than it is. ProjectWithDrivers pulls that one
// day out of the run-rate side entirely and adds the bump back exactly
// once.
func TestProjectWithDriversAddsAOneTimeDriverOnceInsteadOfAveragingItAway(t *testing.T) {
	db := schemaOnlyDB(t)
	for d := 1; d <= 10; d++ {
		day := "2026-03-0" + strconv.Itoa(d)
		if d == 5 {
			plantCharge(t, db, "aws", day, "Amazon EC2", 6000) // 1000 baseline + 5000 bump
		} else {
			plantCharge(t, db, "aws", day, "Amazon EC2", 1000)
		}
	}
	plantDriver(t, db, "aws", "*", "Test migration", "one-time", "2026-03-05", "2026-03-05")

	naive, _, err := finops.Project(db, "2026-03")
	if err != nil {
		t.Fatal(err)
	}
	if naive["aws"] != 46500 {
		t.Fatalf("test fixture check: naive run rate = %s, want 465.00 (the flaw this test exists to show)", naive["aws"])
	}

	got, basis, lines, err := finops.ProjectWithDrivers(db, "aws", "2026-03")
	if err != nil {
		t.Fatal(err)
	}
	if got != 36000 {
		t.Errorf("driver-aware projection = %s, want 360.00 (30000 clean baseline + 6000 bump added once)", got)
	}
	if got == naive["aws"] {
		t.Error("driver-aware projection equals the naive one: the bump was averaged away, not moved")
	}
	if len(lines) != 1 {
		t.Fatalf("driver lines = %d, want 1", len(lines))
	}
	l := lines[0]
	if l.Label != "Test migration" || l.Kind != "one-time" || l.Start != "2026-03-05" || l.End != "2026-03-05" {
		t.Errorf("driver line = %+v, want label/kind/window for the migration", l)
	}
	if l.Effect != 6000 {
		t.Errorf("driver effect = %s, want 60.00 (the day's own charge, added once)", l.Effect)
	}
	if !strings.Contains(basis, "Test migration") {
		t.Errorf("basis %q does not name the driver it applied", basis)
	}
}

// A recurring driver's window can span many days; its own per-day rate,
// measured over whatever of that window has landed, repeats across every
// day the window covers -- not just the days that happened to land. The
// SAME desk also runs Compute Engine every day, inside and outside the
// GKE-scoped driver's own window, and that other service's own spend must
// stay in the baseline throughout: a service-scoped driver excludes only
// its own service's share of a day, never the whole desk-day (see
// TestProjectWithDriversExcludesOnlyItsOwnScopeFromTheBaseline for the
// defect this once was).
func TestProjectWithDriversRepeatsARecurringDriverAcrossItsWindow(t *testing.T) {
	db := schemaOnlyDB(t)
	// Inside the driver's own 10-day window: GKE 100/day for 5 landed days,
	// Compute Engine 900/day alongside it every one of those same days.
	for d := 1; d <= 5; d++ {
		day := "2026-04-0" + strconv.Itoa(d)
		plantCharge(t, db, "gcp", day, "GKE", 100)
		plantCharge(t, db, "gcp", day, "Compute Engine", 900)
	}
	// Outside the window: five more days of Compute Engine alone.
	for d := 11; d <= 15; d++ {
		plantCharge(t, db, "gcp", "2026-04-"+strconv.Itoa(d), "Compute Engine", 1000)
	}
	plantDriver(t, db, "gcp", "GKE", "Scheduled weekly training window", "recurring",
		"2026-04-01", "2026-04-10")

	got, _, lines, err := finops.ProjectWithDrivers(db, "gcp", "2026-04")
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 {
		t.Fatalf("driver lines = %d, want 1", len(lines))
	}
	// 100/day observed over 5 landed days, repeated across the window's own
	// 10 days: 500 * 10 / 5 = 1000, not the raw 500 a one-time treatment
	// would give.
	if lines[0].Effect != 1000 {
		t.Errorf("recurring driver effect = %s, want 10.00 (its own rate repeated across all 10 window days, not just the 5 landed)", lines[0].Effect)
	}
	if lines[0].Kind != "recurring" {
		t.Errorf("kind = %q, want recurring", lines[0].Kind)
	}
	// Compute Engine's own 900/day (inside the window) and 1000/day
	// (outside it) are BOTH still in the baseline: (900*5 + 1000*5) clean
	// over 10 landed days, extended across all 30 days of April (nothing
	// desk-WIDE is excluded, since the driver's own scope is GKE alone):
	// 9500*30/10 = 28500. Total = 28500 + 1000 = 29500.
	if got != 29500 {
		t.Errorf("projection = %s, want 295.00 (28500 baseline, with Compute Engine intact throughout, + 1000 driver)", got)
	}
}

// Boundary: no registered driver at all, and the projection must equal
// today's plain run rate exactly, cent for cent.
func TestProjectWithDriversWithNoDriversEqualsTheNaiveRunRate(t *testing.T) {
	db := schemaOnlyDB(t)
	amounts := []int64{1000, 1200, 900, 1100, 1300}
	for i, v := range amounts {
		plantCharge(t, db, "aws", "2026-05-0"+strconv.Itoa(i+1), "Amazon EC2", v)
	}

	naive, _, err := finops.Project(db, "2026-05")
	if err != nil {
		t.Fatal(err)
	}
	got, _, lines, err := finops.ProjectWithDrivers(db, "aws", "2026-05")
	if err != nil {
		t.Fatal(err)
	}
	if got != naive["aws"] {
		t.Errorf("driver-aware projection %s != naive run rate %s with no drivers registered", got, naive["aws"])
	}
	if len(lines) != 0 {
		t.Errorf("driver lines = %d, want 0", len(lines))
	}
}

// Boundary: a driver whose window ends before the projection's own month
// starts is not applied at all -- excluded from nothing, added to nothing.
func TestProjectWithDriversIgnoresADriverWhoseWindowEndsBeforeThePeriod(t *testing.T) {
	db := schemaOnlyDB(t)
	for d := 1; d <= 5; d++ {
		plantCharge(t, db, "aws", "2026-06-0"+strconv.Itoa(d), "Amazon EC2", 1000)
	}
	plantDriver(t, db, "aws", "*", "A May event", "one-time", "2026-05-20", "2026-05-25")

	got, _, lines, err := finops.ProjectWithDrivers(db, "aws", "2026-06")
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 0 {
		t.Fatalf("driver lines = %d, want 0 (the driver's window is entirely before June)", len(lines))
	}
	// 5000 over 5 days extended to 30 (June): 5000*30/5 = 30000.
	if got != 30000 {
		t.Errorf("projection = %s, want 300.00", got)
	}
}

// Hostile: a driver row whose End is before its own Start must never be
// applied and must never crash the projection.
func TestProjectWithDriversRefusesAMalformedDriverWindow(t *testing.T) {
	db := schemaOnlyDB(t)
	for d := 1; d <= 5; d++ {
		plantCharge(t, db, "aws", "2026-07-0"+strconv.Itoa(d), "Amazon EC2", 1000)
	}
	plantDriver(t, db, "aws", "*", "Malformed", "one-time", "2026-07-10", "2026-07-05")

	got, _, lines, err := finops.ProjectWithDrivers(db, "aws", "2026-07")
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 0 {
		t.Fatalf("driver lines = %d, want 0 for a malformed (End before Start) driver", len(lines))
	}
	if got != 31000 { // 5000 over 5 days extended to 31 (July)
		t.Errorf("projection = %s, want 310.00 (the malformed driver must not move it)", got)
	}
}

// Hostile: a driver effect in the billions of cents must come back exact,
// no overflow and no silent truncation. money.Cents is int64 (max about 92
// quadrillion units), and the one multiplication here (sofar * windowDays)
// stays many orders of magnitude below that even at this size.
func TestProjectWithDriversHandlesADriverEffectInTheBillionsOfCents(t *testing.T) {
	db := schemaOnlyDB(t)
	plantCharge(t, db, "aws", "2026-08-01", "BigJob", 5_000_000_000) // $50,000,000.00, one landed day
	plantCharge(t, db, "aws", "2026-08-20", "Amazon EC2", 1000)
	plantCharge(t, db, "aws", "2026-08-21", "Amazon EC2", 1000)
	plantDriver(t, db, "aws", "BigJob", "Huge one-off charge", "one-time", "2026-08-01", "2026-08-05")

	got, _, lines, err := finops.ProjectWithDrivers(db, "aws", "2026-08")
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 {
		t.Fatalf("driver lines = %d, want 1", len(lines))
	}
	// 5,000,000,000 observed on day 1 of a 5-day window, one landed day:
	// 5,000,000,000 * 5 / 1 = 25,000,000,000.
	if lines[0].Effect != 25_000_000_000 {
		t.Errorf("driver effect = %d cents, want 25000000000 exactly, no overflow", int64(lines[0].Effect))
	}
	if got < lines[0].Effect {
		t.Errorf("total projection %s is smaller than its own driver line %s", got, lines[0].Effect)
	}
}

// Mutant-catching: the effect must be (sofar * windowDays) / landed, ONE
// division after the multiply, not (sofar / landed) * windowDays, which
// truncates the per-day rate to a whole cent before it is ever multiplied.
// 100 cents over 3 landed days, repeated across a 7-day window: the correct
// order gives floor(700/3)=233; dividing first gives floor(100/3)*7=231.
func TestProjectWithDriversRoundsOnceMultiplyingBeforeDividing(t *testing.T) {
	db := schemaOnlyDB(t)
	plantCharge(t, db, "aws", "2026-09-01", "Y", 40)
	plantCharge(t, db, "aws", "2026-09-02", "Y", 30)
	plantCharge(t, db, "aws", "2026-09-03", "Y", 30)
	plantCharge(t, db, "aws", "2026-09-15", "Z", 500)
	plantDriver(t, db, "aws", "Y", "Odd-sum driver", "recurring", "2026-09-01", "2026-09-07")

	_, _, lines, err := finops.ProjectWithDrivers(db, "aws", "2026-09")
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 {
		t.Fatalf("driver lines = %d, want 1", len(lines))
	}
	if lines[0].Effect != 233 {
		t.Errorf("driver effect = %d cents, want 233 (100*7/3 truncated once, not (100/3)*7=231)", int64(lines[0].Effect))
	}
}

// A service-scoped driver excludes only ITS OWN service's days from the
// baseline, never the whole desk: found via the parity gate against the
// seeded estate's own onprem desk, where N04 ("Month-end batch on the
// storage array") is scoped to "Storage array" alone and spans the whole
// estate as a recurring driver. The first version of this function excluded
// the whole desk-day for ANY driver regardless of scope, which meant
// onprem's Batch cluster, Virtualisation and Network services -- every
// service on the desk except the one the driver actually names -- vanished
// from the projection entirely for as long as that driver's window covers
// the month, because the baseline saw nothing left to average and the
// driver's own line only ever measures its own scope.
func TestProjectWithDriversExcludesOnlyItsOwnScopeFromTheBaseline(t *testing.T) {
	db := schemaOnlyDB(t)
	for d := 1; d <= 10; d++ {
		day := fmt.Sprintf("2026-06-%02d", d)
		plantCharge(t, db, "onprem", day, "Storage array", 500)
		plantCharge(t, db, "onprem", day, "Batch cluster", 2000)
	}
	// A recurring driver scoped to ONE service, spanning far wider than the
	// month being projected -- the real shape N04 has in the seeded estate.
	plantDriver(t, db, "onprem", "Storage array", "Month-end batch on the storage array",
		"recurring", "2026-01-01", "2026-12-31")

	got, _, lines, err := finops.ProjectWithDrivers(db, "onprem", "2026-06")
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 {
		t.Fatalf("driver lines = %d, want 1", len(lines))
	}
	// Storage array: 500/day over 10 landed days, repeated across June's own
	// 30 days (the driver's window, clipped to June): 5000*30/10 = 15000.
	if lines[0].Effect != 15000 {
		t.Errorf("driver effect = %s, want 150.00", lines[0].Effect)
	}
	// Batch cluster never falls inside the driver's own scope, so it must
	// still be there: baseline is its 2000/day over 10 landed days,
	// extended to all 30 days of June (nothing else claims them): 20000*30/10
	// = 60000. Total = 60000 (Batch cluster, via the baseline) + 15000
	// (Storage array, via the driver's own line) = 75000.
	if got != 75000 {
		t.Errorf("projection = %s, want 750.00 (Batch cluster's own 600.00 baseline plus the driver's 150.00, not the driver's line alone)", got)
	}
}

// Missed: a driver that overlaps the month a basis describes, but whose
// label the basis text never mentions, is a driver the freeze did not know
// about -- C3-SPEC.md section 1's "next month explain the miss".
func TestMissedNamesADriverAddedAfterTheBasisWasWritten(t *testing.T) {
	db := schemaOnlyDB(t)
	plantDriver(t, db, "aws", "*", "Newly discovered migration", "one-time", "2026-01-10", "2026-01-10")

	missed, err := finops.Missed(db, "aws", "2026-01",
		"run rate over the 20 days of 2026-01 that have data, extended to 31")
	if err != nil {
		t.Fatal(err)
	}
	if len(missed) != 1 || missed[0].Label != "Newly discovered migration" {
		t.Errorf("missed = %+v, want exactly the migration named", missed)
	}
}

// The other direction: a driver whose label already appears in the basis
// was accounted for, and Missed says nothing about it.
func TestMissedIsEmptyWhenTheBasisAlreadyNamesTheDriver(t *testing.T) {
	db := schemaOnlyDB(t)
	plantDriver(t, db, "aws", "*", "Newly discovered migration", "one-time", "2026-01-10", "2026-01-10")

	basis := "run rate over the 20 days of 2026-01 that have data, extended to 31; " +
		"drivers applied: Newly discovered migration (one-time, 2026-01-10 to 2026-01-10, 100.00)"
	missed, err := finops.Missed(db, "aws", "2026-01", basis)
	if err != nil {
		t.Fatal(err)
	}
	if len(missed) != 0 {
		t.Errorf("missed = %+v, want none: the basis already named it", missed)
	}
}

// LargestMiss: two scored month-desks, one missed by 20%, the other by 5%.
// The 20% one is the largest miss, and its own undocumented driver is named.
func TestLargestMissPicksTheWorstErrorAndNamesItsDriver(t *testing.T) {
	db := schemaOnlyDB(t)
	if _, err := db.Exec(finops.ForecastSchema); err != nil {
		t.Fatal(err)
	}
	insertForecast(t, db, "2026-01", "aws", 10000, "baseline sentence, no drivers named")
	insertForecast(t, db, "2026-01", "gcp", 10000, "baseline sentence, no drivers named")
	plantCharge(t, db, "aws", "2026-01-15", "Amazon EC2", 12000) // 20% over
	plantCharge(t, db, "gcp", "2026-01-15", "GKE", 10500)        // 5% over
	plantDriver(t, db, "aws", "*", "AWS spike cause", "one-time", "2026-01-15", "2026-01-15")

	got, ok, err := finops.LargestMiss(db, "2026-02")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("LargestMiss found nothing to report")
	}
	if got.Source != "aws" {
		t.Errorf("largest miss desk = %q, want aws", got.Source)
	}
	if got.ErrorPct < 19.9 || got.ErrorPct > 20.1 {
		t.Errorf("largest miss error = %.2f%%, want 20%%", got.ErrorPct)
	}
	if len(got.MissedDrivers) != 1 || got.MissedDrivers[0].Label != "AWS spike cause" {
		t.Errorf("missed drivers = %+v, want exactly the AWS spike cause", got.MissedDrivers)
	}
}

// Mutant-catching, C3-SPEC.md section 4's third named mutant: LargestMiss
// must grade the FROZEN figure, never a freshly recomputed live one. A
// month frozen on day 10 (through only ten days of a rising series) and the
// same month re-projected live (through all twenty landed days) give
// measurably different numbers by construction; LargestMiss must report the
// frozen one.
func TestLargestMissGradesTheFrozenFigureNotALiveOne(t *testing.T) {
	db := schemaOnlyDB(t)
	for d := 1; d <= 20; d++ {
		day := "2026-02-"
		if d < 10 {
			day += "0" + strconv.Itoa(d)
		} else {
			day += strconv.Itoa(d)
		}
		plantCharge(t, db, "aws", day, "Amazon EC2", int64(d)*100)
	}
	if err := finops.FreezeAsAt(db, "2026-02", "tester", 10); err != nil {
		t.Fatal(err)
	}

	live, _, _, err := finops.ProjectWithDrivers(db, "aws", "2026-02")
	if err != nil {
		t.Fatal(err)
	}
	if live != 29400 {
		t.Fatalf("test fixture check: live re-projection = %s, want 294.00", live)
	}

	got, ok, err := finops.LargestMiss(db, "2026-03")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("LargestMiss found nothing to report")
	}
	if got.Forecast.Forecast != 15400 {
		t.Errorf("LargestMiss reported forecast %s, want the FROZEN 154.00, not something recomputed live", got.Forecast.Forecast)
	}
	if got.Forecast.Forecast == live {
		t.Fatal("test fixture check: the frozen and live figures coincide, so this test cannot tell them apart")
	}
}

func TestSetForecastBasisOverwritesEveryDesksRow(t *testing.T) {
	db := schemaOnlyDB(t)
	if _, err := db.Exec(finops.ForecastSchema); err != nil {
		t.Fatal(err)
	}
	insertForecast(t, db, "2026-01", "aws", 10000, "generated sentence")
	insertForecast(t, db, "2026-01", "gcp", 20000, "generated sentence")

	if err := finops.SetForecastBasis(db, "2026-01", "the analyst's own written words"); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(`SELECT source, basis FROM forecasts WHERE period='2026-01' ORDER BY source`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var src, basis string
		if err := rows.Scan(&src, &basis); err != nil {
			t.Fatal(err)
		}
		if basis != "the analyst's own written words" {
			t.Errorf("%s basis = %q, want the overriding text", src, basis)
		}
		n++
	}
	if n != 2 {
		t.Fatalf("saw %d forecast rows, want 2", n)
	}
}

// An empty override changes nothing: Freeze's own driver-aware basis stands
// when an option carried no summary of its own to replace it with.
func TestSetForecastBasisWithEmptyStringChangesNothing(t *testing.T) {
	db := schemaOnlyDB(t)
	if _, err := db.Exec(finops.ForecastSchema); err != nil {
		t.Fatal(err)
	}
	insertForecast(t, db, "2026-01", "aws", 10000, "generated sentence")
	if err := finops.SetForecastBasis(db, "2026-01", ""); err != nil {
		t.Fatal(err)
	}
	var basis string
	if err := db.QueryRow(`SELECT basis FROM forecasts WHERE period='2026-01' AND source='aws'`).
		Scan(&basis); err != nil {
		t.Fatal(err)
	}
	if basis != "generated sentence" {
		t.Errorf("basis = %q, want unchanged", basis)
	}
}

// Wiring: the forecast-accuracy KPI's own Note must say what LargestMiss
// says, not recompute the question a different way. Derives its own
// expectation from LargestMiss directly against the real seeded estate,
// rather than hand-verifying an absolute figure against generated noise:
// this is a test that the two paths AGREE, not a test of the arithmetic
// itself, which the tests above already hold.
func TestKPIsForecastAccuracyNamesTheLargestMissesDriver(t *testing.T) {
	db := applyTestDB(t)
	if err := crew.EnsureLiveSpendLedger(db); err != nil {
		t.Fatal(err) // KPIs() reads crew.LiveSpend, which applyTestDB's own setup does not migrate for
	}
	months, err := finops.Months(db)
	if err != nil || len(months) < 3 {
		t.Fatalf("months: %v %v", months, err)
	}
	closedMonth := months[2]
	if err := finops.Freeze(db, closedMonth, "tester"); err != nil {
		t.Fatal(err)
	}
	plantDriver(t, db, "aws", "*", "Retroactively found cause", "one-time",
		closedMonth+"-05", closedMonth+"-05")

	open, err := finops.OpenPeriod(db)
	if err != nil || open == "" {
		t.Fatalf("open period: %v %v", open, err)
	}
	want, ok, err := finops.LargestMiss(db, open)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("fixture check: LargestMiss found nothing to report after a freeze")
	}

	ks, err := finops.KPIs(db, open)
	if err != nil {
		t.Fatal(err)
	}
	var note string
	found := false
	for _, k := range ks {
		if k.ID == "forecast-accuracy" {
			note, found = k.Note, true
		}
	}
	if !found {
		t.Fatal("no forecast-accuracy KPI in the library")
	}
	if !strings.Contains(note, want.Source) || !strings.Contains(note, want.Period) {
		t.Errorf("KPI note %q does not name the largest miss (%s, %s)", note, want.Source, want.Period)
	}
	if len(want.MissedDrivers) > 0 && !strings.Contains(note, want.MissedDrivers[0].Label) {
		t.Errorf("KPI note %q does not name the missed driver %q", note, want.MissedDrivers[0].Label)
	}
}

// worseMiss's first tie-break: two scored month-desks with the SAME
// ErrorPct pick the one with the larger absolute cents gap.
func TestLargestMissBreaksATiedErrorOnTheAbsoluteGap(t *testing.T) {
	db := schemaOnlyDB(t)
	if _, err := db.Exec(finops.ForecastSchema); err != nil {
		t.Fatal(err)
	}
	insertForecast(t, db, "2026-01", "aws", 10000, "b")
	insertForecast(t, db, "2026-01", "gcp", 20000, "b")
	plantCharge(t, db, "aws", "2026-01-15", "Amazon EC2", 11000) // 10% error, 1000 gap
	plantCharge(t, db, "gcp", "2026-01-15", "GKE", 22000)        // 10% error, 2000 gap

	got, ok, err := finops.LargestMiss(db, "2026-02")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("LargestMiss found nothing")
	}
	if got.Source != "gcp" {
		t.Errorf("largest miss = %s, want gcp: same 10%% error as aws, but a 2000-cent gap against aws's 1000", got.Source)
	}
}

func insertForecast(t *testing.T, db *sql.DB, period, source string, cents int64, basis string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO forecasts
		(period, source, forecast_cents, basis, frozen_at, frozen_by)
		VALUES (?,?,?,?,'2026-01-10T00:00:00Z','tester')`,
		period, source, cents, basis); err != nil {
		t.Fatal(err)
	}
}
