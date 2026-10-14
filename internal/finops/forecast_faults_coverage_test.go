package finops_test

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/finops"
)

// covBreakForecasts makes the forecasts relation unreadable after its
// CREATE TABLE IF NOT EXISTS has already passed: a view of that name over a
// table that is then dropped. Writes and reads that reach it fail with the
// store's own "no such table" error.
func covBreakForecasts(t *testing.T, db *sql.DB) {
	t.Helper()
	covExec(t, db, `CREATE TABLE fc_src(period TEXT)`)
	covExec(t, db, `CREATE VIEW forecasts AS SELECT * FROM fc_src`)
	covExec(t, db, `DROP TABLE fc_src`)
}

// The month used by the forecast tests: charges on its first days only, so a
// cutoff of day N sees exactly the days up to N.
const covMonth = "2026-03"

func covMonthWithCharges(t *testing.T, db *sql.DB) {
	t.Helper()
	covDayCharges(t, db, "aws", "compute", 1000, "2026-03-01", "2026-03-02", "2026-03-03")
}

// TestProjectAsAtReadsACutoffBelowTheFirstDayAsTheFirstDay: a cutoff of zero
// is not a request for an empty projection. It is the first day, the same
// projection a cutoff of one gives.
func TestProjectAsAtReadsACutoffBelowTheFirstDayAsTheFirstDay(t *testing.T) {
	db := covSchemaDB(t)
	covMonthWithCharges(t, db)
	zero, basisZero, err := finops.ProjectAsAt(db, covMonth, 0)
	if err != nil {
		t.Fatalf("cutoff 0: %v", err)
	}
	one, basisOne, err := finops.ProjectAsAt(db, covMonth, 1)
	if err != nil {
		t.Fatalf("cutoff 1: %v", err)
	}
	if zero["aws"] != one["aws"] || zero["aws"] == 0 {
		t.Errorf("cutoff 0 projects %v, cutoff 1 projects %v; want the same non-zero figure", zero["aws"], one["aws"])
	}
	if basisZero != basisOne {
		t.Errorf("cutoff 0 basis %q differs from cutoff 1 basis %q", basisZero, basisOne)
	}
	// One day of 10.00 extended to the 31 days of March is 310.00.
	if zero["aws"] != 31000 {
		t.Errorf("one day of 1000 cents projected to the month gave %d, want 31000", zero["aws"])
	}
}

func TestProjectAsAtRefusesAMonthWithNoChargeData(t *testing.T) {
	db := covSchemaDB(t)
	_, _, err := finops.ProjectAsAt(db, covMonth, 31)
	if err == nil || !strings.Contains(err.Error(), "has no charge data") {
		t.Fatalf("a month with no charges projected with err %v; want the no-data refusal", err)
	}
}

// A stored amount that is not a whole number of cents cannot be summed into
// the projection without losing part of it. The read must refuse, not round.
func TestProjectAsAtRefusesAStoredAmountThatIsNotWhole(t *testing.T) {
	db := covSchemaDB(t)
	covMonthWithCharges(t, db)
	covExec(t, db, `UPDATE charges SET billed_cents = 0.5 WHERE day = '2026-03-02'`)
	if _, _, err := finops.ProjectAsAt(db, covMonth, 31); err == nil {
		t.Fatal("a fractional stored amount was projected instead of refused")
	}
}

func TestProjectAsAtSurfacesAStoreFailureOnTheCharges(t *testing.T) {
	db := covSchemaDB(t)
	covMonthWithCharges(t, db)
	covExec(t, db, `DROP TABLE charges`)
	if _, _, err := finops.ProjectAsAt(db, covMonth, 31); err == nil {
		t.Fatal("a missing charges table projected with no error")
	}
}

func TestProjectWithDriversRefusesADeskWithNoChargeData(t *testing.T) {
	db := covSchemaDB(t)
	covMonthWithCharges(t, db)
	_, _, _, err := finops.ProjectWithDrivers(db, "nobody", covMonth)
	if err == nil || !strings.Contains(err.Error(), "no charge data on nobody") {
		t.Fatalf("a desk with no charges projected with err %v; want the refusal naming it", err)
	}
}

func TestProjectWithDriversRefusesAStoredAmountThatIsNotWhole(t *testing.T) {
	db := covSchemaDB(t)
	covMonthWithCharges(t, db)
	covExec(t, db, `UPDATE charges SET billed_cents = 0.5 WHERE day = '2026-03-02'`)
	if _, _, _, err := finops.ProjectWithDrivers(db, "aws", covMonth); err == nil {
		t.Fatal("a fractional stored amount was projected with drivers instead of refused")
	}
}

func TestProjectWithDriversSurfacesAFailingDriverRegister(t *testing.T) {
	db := covSchemaDB(t)
	covMonthWithCharges(t, db)
	covExec(t, db, `DROP TABLE drivers`)
	if _, _, _, err := finops.ProjectWithDrivers(db, "aws", covMonth); err == nil {
		t.Fatal("a missing driver register projected with no error")
	}
}

// A whole-month driver claims every day of the month, so no clean day is left
// to extend. The figure is the driver's own line alone, and the basis says so.
func TestAWholeMonthDriverLeavesNoCleanDayAndSaysSo(t *testing.T) {
	db := covSchemaDB(t)
	covMonthWithCharges(t, db)
	plantDriver(t, db, "aws", "*", "Whole-month outage", "one-time", "2026-03-01", "2026-03-31")
	total, basis, lines, err := finops.ProjectWithDrivers(db, "aws", covMonth)
	if err != nil {
		t.Fatalf("driver projection: %v", err)
	}
	if !strings.Contains(basis, "every day of 2026-03 that has landed falls inside a driver's own window") {
		t.Errorf("basis %q does not say every landed day sits in the driver's window", basis)
	}
	if len(lines) != 1 {
		t.Fatalf("got %d driver lines, want the one whole-month driver", len(lines))
	}
	// Three days of 1000 cents, the driver's window is 31 days:
	// 3000 * 31 / 3 = 31000, and the total is that line alone.
	if lines[0].Effect != 31000 || total != 31000 {
		t.Errorf("driver effect %d, total %d; want 31000 and 31000", lines[0].Effect, total)
	}
}

// Driver lines come back ordered by window start, then by label. The order is
// what the decisions page and the frozen basis both print, so a change in it
// changes what a reader sees.
func TestDriverLinesAreOrderedByStartThenLabel(t *testing.T) {
	db := covSchemaDB(t)
	covDayCharges(t, db, "aws", "compute", 1000, "2026-03-01", "2026-03-05", "2026-03-10", "2026-03-20", "2026-03-25")
	plantDriver(t, db, "aws", "compute", "Cooling", "one-time", "2026-03-05", "2026-03-05")
	plantDriver(t, db, "aws", "compute", "Batch", "one-time", "2026-03-20", "2026-03-20")
	plantDriver(t, db, "aws", "compute", "Archive", "one-time", "2026-03-20", "2026-03-20")
	_, basis, lines, err := finops.ProjectWithDrivers(db, "aws", covMonth)
	if err != nil {
		t.Fatalf("driver projection: %v", err)
	}
	var got []string
	for _, l := range lines {
		got = append(got, l.Label)
	}
	want := []string{"Cooling", "Archive", "Batch"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("driver lines ordered %v, want %v (start first, then label)", got, want)
	}
	if !strings.Contains(basis, "drivers applied: Cooling (") {
		t.Errorf("basis %q does not name the first driver line first", basis)
	}
}

func TestFreezeAsAtReadsACutoffBelowTheFirstDayAsTheFirstDay(t *testing.T) {
	zeroDB := covSchemaDB(t)
	covMonthWithCharges(t, zeroDB)
	oneDB := covSchemaDB(t)
	covMonthWithCharges(t, oneDB)
	if err := finops.FreezeAsAt(zeroDB, covMonth, "owner", 0); err != nil {
		t.Fatalf("freeze through 0: %v", err)
	}
	if err := finops.FreezeAsAt(oneDB, covMonth, "owner", 1); err != nil {
		t.Fatalf("freeze through 1: %v", err)
	}
	z, _ := finops.Forecasts(zeroDB, "9999-12")
	o, _ := finops.Forecasts(oneDB, "9999-12")
	if len(z) != 1 || len(o) != 1 {
		t.Fatalf("freezes wrote %d and %d rows, want one each", len(z), len(o))
	}
	if z[0].Forecast != o[0].Forecast || z[0].Forecast == 0 {
		t.Errorf("freeze through 0 wrote %d, through 1 wrote %d; want the same non-zero figure",
			z[0].Forecast, o[0].Forecast)
	}
}

// A month frozen once keeps the figure somebody was shown. A second freeze
// would move it, so it is refused, and the first figure stands.
func TestFreezeRefusesASecondFreezeAndKeepsTheFirstFigure(t *testing.T) {
	db := covSchemaDB(t)
	covMonthWithCharges(t, db)
	if err := finops.Freeze(db, covMonth, "owner"); err != nil {
		t.Fatalf("first freeze: %v", err)
	}
	before, _ := finops.Forecasts(db, "9999-12")
	err := finops.Freeze(db, covMonth, "owner")
	if err == nil || !strings.Contains(err.Error(), "already frozen") {
		t.Fatalf("second freeze returned %v; want the already-frozen refusal", err)
	}
	after, _ := finops.Forecasts(db, "9999-12")
	if len(before) != 1 || len(after) != 1 || before[0].Forecast != after[0].Forecast {
		t.Errorf("the refused freeze changed the frozen figure: before %v, after %v", before, after)
	}
}

func TestFreezeRefusesAMonthWithNoChargeData(t *testing.T) {
	db := covSchemaDB(t)
	err := finops.Freeze(db, covMonth, "owner")
	if err == nil || !strings.Contains(err.Error(), "has no charge data") {
		t.Fatalf("freezing an empty month returned %v; want the no-data refusal", err)
	}
}

func TestFreezeSurfacesAFailingDeskRead(t *testing.T) {
	db := covSchemaDB(t)
	covMonthWithCharges(t, db)
	covExec(t, db, `DROP TABLE charges`)
	if err := finops.Freeze(db, covMonth, "owner"); err == nil {
		t.Fatal("a freeze with the charges gone returned no error")
	}
}

func TestFreezeSurfacesAFailingDriverRegister(t *testing.T) {
	db := covSchemaDB(t)
	covMonthWithCharges(t, db)
	covExec(t, db, `DROP TABLE drivers`)
	if err := finops.Freeze(db, covMonth, "owner"); err == nil {
		t.Fatal("a freeze with the driver register gone returned no error")
	}
}

// A freeze that cannot write one desk's row writes none: the transaction
// rolls back, and the month is not left half frozen.
func TestFreezeWritesNothingWhenAnyRowIsRefused(t *testing.T) {
	db := covSchemaDB(t)
	covMonthWithCharges(t, db)
	if _, err := finops.IsFrozen(db, covMonth); err != nil {
		t.Fatal(err)
	}
	covExec(t, db, `CREATE TRIGGER refuse_forecast BEFORE INSERT ON forecasts
		BEGIN SELECT RAISE(ABORT, 'store refused the forecast'); END`)
	err := finops.Freeze(db, covMonth, "owner")
	if err == nil || !strings.Contains(err.Error(), "store refused the forecast") {
		t.Fatalf("a refused insert returned %v; want the store's own refusal", err)
	}
	frozen, err := finops.IsFrozen(db, covMonth)
	if err != nil {
		t.Fatal(err)
	}
	if frozen {
		t.Error("a refused freeze left the month frozen")
	}
}

func TestFreezeSurfacesAForecastsRelationThatCannotBeRead(t *testing.T) {
	db := covSchemaDB(t)
	covMonthWithCharges(t, db)
	covBreakForecasts(t, db)
	if err := finops.Freeze(db, covMonth, "owner"); err == nil {
		t.Fatal("a freeze over an unreadable forecasts relation returned no error")
	}
}

func TestIsFrozenSurfacesAForecastsRelationThatCannotBeRead(t *testing.T) {
	db := covSchemaDB(t)
	covBreakForecasts(t, db)
	if _, err := finops.IsFrozen(db, covMonth); err == nil {
		t.Fatal("IsFrozen over an unreadable forecasts relation returned no error")
	}
}

func TestForecastsSurfacesAForecastsRelationThatCannotBeRead(t *testing.T) {
	db := covSchemaDB(t)
	covBreakForecasts(t, db)
	if _, err := finops.Forecasts(db, "9999-12"); err == nil {
		t.Fatal("Forecasts over an unreadable forecasts relation returned no error")
	}
}

func TestFrozenPeriodsSurfacesAForecastsRelationThatCannotBeRead(t *testing.T) {
	db := covSchemaDB(t)
	covBreakForecasts(t, db)
	if _, err := finops.FrozenPeriods(db); err == nil {
		t.Fatal("FrozenPeriods over an unreadable forecasts relation returned no error")
	}
}

// A forecast row the store holds as a fraction of a cent cannot be read as a
// whole-cent figure. The read must say so.
func TestForecastsRefusesAStoredFigureThatIsNotWhole(t *testing.T) {
	db := covSchemaDB(t)
	covMonthWithCharges(t, db)
	if err := finops.Freeze(db, covMonth, "owner"); err != nil {
		t.Fatal(err)
	}
	covExec(t, db, `UPDATE forecasts SET forecast_cents = 0.5`)
	if _, err := finops.Forecasts(db, "9999-12"); err == nil {
		t.Fatal("a fractional frozen figure was read instead of refused")
	}
}

// A stored actual that is not whole cents cannot be scored against a frozen
// figure. The score must refuse, not round the actual away.
func TestForecastsRefusesAStoredActualThatIsNotWhole(t *testing.T) {
	db := covSchemaDB(t)
	covMonthWithCharges(t, db)
	if err := finops.Freeze(db, covMonth, "owner"); err != nil {
		t.Fatal(err)
	}
	covExec(t, db, `UPDATE charges SET billed_cents = 0.5 WHERE day = '2026-03-02'`)
	if _, err := finops.Forecasts(db, "9999-12"); err == nil {
		t.Fatal("a fractional stored actual was scored instead of refused")
	}
}

// A month still running is not scored: the frozen figure is listed, but it has
// no actual and no error, because a partial month would flatter the forecast.
func TestForecastsLeavesAMonthStillRunningUnscored(t *testing.T) {
	db := covSchemaDB(t)
	covMonthWithCharges(t, db)
	if err := finops.Freeze(db, covMonth, "owner"); err != nil {
		t.Fatal(err)
	}
	fs, err := finops.Forecasts(db, covMonth)
	if err != nil {
		t.Fatalf("forecasts: %v", err)
	}
	if len(fs) != 1 {
		t.Fatalf("got %d frozen rows, want 1", len(fs))
	}
	if fs[0].HasAct || fs[0].ErrorPct != 0 || fs[0].Grade != "" {
		t.Errorf("a running month was scored: %+v", fs[0])
	}
}

// Accuracy refuses to report while nothing is scored, and a refusal is not a
// zero: a zero would read as a perfect forecast.
func TestAccuracyRefusesWhileNothingIsScored(t *testing.T) {
	db := covSchemaDB(t)
	covMonthWithCharges(t, db)
	if err := finops.Freeze(db, covMonth, "owner"); err != nil {
		t.Fatal(err)
	}
	acc, n, ok, err := finops.Accuracy(db, covMonth)
	if err != nil {
		t.Fatalf("accuracy: %v", err)
	}
	if ok || n != 0 || acc != 0 {
		t.Errorf("accuracy with nothing scored = %v, %d, %v; want a refusal", acc, n, ok)
	}
}

// LargestMiss reports nothing when no month has been scored, rather than
// naming an arbitrary one.
func TestLargestMissIsAbsentWhileNothingIsScored(t *testing.T) {
	db := covSchemaDB(t)
	covMonthWithCharges(t, db)
	if err := finops.Freeze(db, covMonth, "owner"); err != nil {
		t.Fatal(err)
	}
	_, ok, err := finops.LargestMiss(db, covMonth)
	if err != nil {
		t.Fatalf("largest miss: %v", err)
	}
	if ok {
		t.Error("a largest miss was named with nothing scored")
	}
}

// A scored miss that cannot be checked against the driver register is an
// error, not a miss reported without its drivers.
func TestLargestMissSurfacesAFailingDriverRegister(t *testing.T) {
	db := covSchemaDB(t)
	covMonthWithCharges(t, db)
	covDayCharges(t, db, "aws", "compute", 1000, "2026-04-01", "2026-04-02")
	if err := finops.Freeze(db, covMonth, "owner"); err != nil {
		t.Fatal(err)
	}
	covExec(t, db, `DROP TABLE drivers`)
	if _, _, err := finops.LargestMiss(db, "2026-04"); err == nil {
		t.Fatal("a scored miss read with no driver register returned no error")
	}
}

// Missed names the registered drivers that overlap the month and that the
// frozen basis never named; a driver outside the month is not one of them.
func TestMissedNamesOnlyTheDriversInsideTheMonthTheBasisLeftOut(t *testing.T) {
	db := covSchemaDB(t)
	covMonthWithCharges(t, db)
	plantDriver(t, db, "aws", "*", "Inside the month", "one-time", "2026-03-05", "2026-03-05")
	plantDriver(t, db, "aws", "*", "Last year", "one-time", "2025-01-05", "2025-01-05")
	plantDriver(t, db, "aws", "*", "Already named", "one-time", "2026-03-06", "2026-03-06")
	missed, err := finops.Missed(db, "aws", covMonth, "basis mentions Already named")
	if err != nil {
		t.Fatalf("missed: %v", err)
	}
	var labels []string
	for _, d := range missed {
		labels = append(labels, d.Label)
	}
	if strings.Join(labels, ",") != "Inside the month" {
		t.Errorf("missed drivers %v; want only the one inside the month the basis did not name", labels)
	}
}

func TestMissedSurfacesAFailingDriverRegister(t *testing.T) {
	db := covSchemaDB(t)
	covMonthWithCharges(t, db)
	covExec(t, db, `DROP TABLE drivers`)
	if _, err := finops.Missed(db, "aws", covMonth, ""); err == nil {
		t.Fatal("missed with no driver register returned no error")
	}
}

// An announced driver is measured against spend, so its figure needs the
// charges. With the charges gone the miss cannot be ranked, and the error says
// so rather than ranking it last.
func TestMissedSurfacesAFailingMeasurementOfAnAnnouncedDriver(t *testing.T) {
	db := covSchemaDB(t)
	covMonthWithCharges(t, db)
	annDriver(t, db, "aws", "compute", "2026-03-05", "2026-03-10", 500, "Cutover")
	covExec(t, db, `DROP TABLE charges`)
	if _, err := finops.Missed(db, "aws", covMonth, ""); err == nil {
		t.Fatal("missed ranked an announced driver it could not measure without an error")
	}
}

// An announced driver with no measured day yet is still a driver the miss
// names. It sorts behind the ones that have a figure, and is not dropped.
func TestMissedKeepsAnAnnouncedDriverWithNoMeasuredDay(t *testing.T) {
	db := covSchemaDB(t)
	covMonthWithCharges(t, db)
	annDriver(t, db, "aws", "compute", "2026-03-20", "2026-03-25", 500, "Not measured yet")
	missed, err := finops.Missed(db, "aws", covMonth, "")
	if err != nil {
		t.Fatalf("missed: %v", err)
	}
	found := false
	for _, d := range missed {
		if d.Label == "Not measured yet" {
			found = true
		}
	}
	if !found {
		t.Errorf("an announced driver with no measured day was dropped from the miss: %v", missed)
	}
}

// FrozenPeriods lists every frozen month, newest first, and nothing else.
func TestFrozenPeriodsListsEveryFreezeNewestFirst(t *testing.T) {
	db := covSchemaDB(t)
	covMonthWithCharges(t, db)
	covDayCharges(t, db, "aws", "compute", 1000, "2026-04-01", "2026-04-02")
	if err := finops.Freeze(db, covMonth, "owner"); err != nil {
		t.Fatal(err)
	}
	if err := finops.Freeze(db, "2026-04", "owner"); err != nil {
		t.Fatal(err)
	}
	got, err := finops.FrozenPeriods(db)
	if err != nil {
		t.Fatalf("frozen periods: %v", err)
	}
	if strings.Join(got, ",") != "2026-04,2026-03" {
		t.Errorf("frozen periods %v; want the newest first", got)
	}
}

func TestFrozenPeriodsIsEmptyBeforeAnyFreeze(t *testing.T) {
	db := covSchemaDB(t)
	got, err := finops.FrozenPeriods(db)
	if err != nil {
		t.Fatalf("frozen periods: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("a store with nothing frozen lists %v", got)
	}
}
