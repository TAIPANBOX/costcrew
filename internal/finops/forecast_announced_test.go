package finops_test

// ANNOUNCED-DRIVER-SPEC.md sections 5 and 7 (scenarios 5 and 6): a forecast
// frozen after an announcement carries the step on the days that have not
// landed yet; a window that has begun is measured as every driver is; and once
// a month closes, each announced driver has a measured effect beside its
// announced one, and a miss ranks the one furthest from its announcement first.
//
// Each test builds its own store from the estate's schema with no generated
// estate, so every charge in it is one this file wrote.
//
// Red against the code before this step: the frozen forecast ignores an
// unlanded window, so the step is absent; AnnouncedEffects is a stub that
// returns nothing, so the measured figures and the miss order are absent too.
// The measured-rate test is a guard: today's arithmetic already gives it.

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/estate"
	"github.com/TAIPANBOX/costcrew/internal/finops"
	"github.com/TAIPANBOX/costcrew/internal/money"
	"github.com/TAIPANBOX/costcrew/internal/world"
)

// annFixture is an empty store with the estate's schema, the drivers column
// included, and nothing generated in it.
func annFixture(t *testing.T) *sql.DB {
	t.Helper()
	db := rawDB(t)
	if _, err := db.Exec(estate.SeedSchema); err != nil {
		t.Fatal(err)
	}
	if err := estate.EnsureDriverExpectedColumn(db); err != nil {
		t.Fatal(err)
	}
	return db
}

// annSpend writes one usage charge a day for days days from first, each of
// cents, on one service of a desk.
func annSpend(t *testing.T, db *sql.DB, source, service, first string, days int, cents int64) {
	t.Helper()
	start, err := time.Parse("2006-01-02", first)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < days; i++ {
		day := start.AddDate(0, 0, i).Format("2006-01-02")
		if _, err := db.Exec(`INSERT INTO charges
			(source, day, service, team, category, billed_cents) VALUES (?,?,?,NULL,'Usage',?)`,
			source, day, service, cents); err != nil {
			t.Fatal(err)
		}
	}
}

// annDriver registers an announced driver on a desk's service, as an owner's
// answer would (estate.InsertDriver is the one writer).
func annDriver(t *testing.T, db *sql.DB, source, scope, start, end string, expected int64, label string) {
	t.Helper()
	v := expected
	if err := estate.InsertDriver(db, world.Driver{Start: start, End: end, Scope: scope, Label: label,
		Kind: "announced", Source: source, Expected: &v}); err != nil {
		t.Fatal(err)
	}
}

func lineFor(t *testing.T, lines []finops.DriverLine, label string) finops.DriverLine {
	t.Helper()
	for _, l := range lines {
		if l.Label == label {
			return l
		}
	}
	t.Fatalf("no driver line for %q in %+v", label, lines)
	return finops.DriverLine{}
}

// The window of 12th to 30th has 19 days. Nothing has landed in it when the
// forecast is frozen on the 5th, so the frozen figure is the clean run rate
// plus the announced step for each of those days, which is the figure a
// forecast made before the step would carry, minus 300.00 a day for 19 days.
func TestAForecastFrozenBeforeTheStepCarriesTheStep(t *testing.T) {
	db := annFixture(t)
	annSpend(t, db, "aws", "svc-a", "2026-11-01", 5, 1000_00)

	before, _, _, err := finops.ProjectWithDrivers(db, "aws", "2026-11")
	if err != nil {
		t.Fatal(err)
	}
	annDriver(t, db, "aws", "svc-a", "2026-11-12", "2026-11-30", -300_00, "swap to the smaller model")
	if err := finops.FreezeAsAt(db, "2026-11", "tester", 5); err != nil {
		t.Fatal(err)
	}

	var frozen int64
	var basis string
	if err := db.QueryRow(`SELECT forecast_cents, basis FROM forecasts
		WHERE period='2026-11' AND source='aws'`).Scan(&frozen, &basis); err != nil {
		t.Fatal(err)
	}
	want := int64(before) - 300_00*19
	if frozen != want {
		t.Errorf("frozen on the 5th = %d, want %d: the clean figure %d less 300.00 for each of 19 days", frozen, want, before)
	}
	if !strings.Contains(basis, "swap to the smaller model") {
		t.Errorf("the frozen basis %q does not name the announced driver", basis)
	}
	if !strings.Contains(basis, "-300.00/day") {
		t.Errorf("the frozen basis %q does not state the announced amount", basis)
	}
}

// Once a window day has landed the measured rate of the scope takes over, as
// it does for any driver: four landed days at 700.00 extended over the 19-day
// window, not the announced 300.00 a day.
func TestOnceAWindowDayLandsTheMeasuredRateWins(t *testing.T) {
	db := annFixture(t)
	annSpend(t, db, "aws", "svc-a", "2026-11-01", 11, 1000_00)
	annSpend(t, db, "aws", "svc-a", "2026-11-12", 4, 700_00)
	annDriver(t, db, "aws", "svc-a", "2026-11-12", "2026-11-30", -300_00, "swap to the smaller model")

	_, _, lines, err := finops.ProjectWithDrivers(db, "aws", "2026-11")
	if err != nil {
		t.Fatal(err)
	}
	got := lineFor(t, lines, "swap to the smaller model")
	if want := money.Cents(700_00 * 19); got.Effect != want {
		t.Errorf("the landed window's line = %s, want the measured 700.00 a day over 19 days = %s", got.Effect, want)
	}
	if got.Effect == money.Cents(-300_00*19) {
		t.Errorf("the line carries the announced step after its days landed")
	}
}

// A closed month: the service spent 1000.00 a day before the step and 880.00
// a day from the 5th, so the measured effect is -120.00 a day against the
// announced -300.00. A second decision on another service, announced -50.00
// and landing at -50.00, measures exactly what it said and sorts after it.
// The first decision was answered on the 1st, four days before its start.
func TestAnnouncedEffectsMeasuresTheStepAfterClose(t *testing.T) {
	db := annFixture(t)
	if _, err := db.Exec(crew.Schema); err != nil {
		t.Fatal(err)
	}
	annSpend(t, db, "aws", "svc-test-a", "2026-09-07", 24, 1000_00)
	annSpend(t, db, "aws", "svc-test-a", "2026-10-01", 4, 1000_00)
	annSpend(t, db, "aws", "svc-test-a", "2026-10-05", 27, 880_00)
	annSpend(t, db, "aws", "svc-test-b", "2026-09-07", 24, 1000_00)
	annSpend(t, db, "aws", "svc-test-b", "2026-10-01", 4, 1000_00)
	annSpend(t, db, "aws", "svc-test-b", "2026-10-05", 27, 950_00)
	annDriver(t, db, "aws", "svc-test-b", "2026-10-05", "2026-10-31", -50_00, "swap B")
	annDriver(t, db, "aws", "svc-test-a", "2026-10-05", "2026-10-31", -300_00, "swap A")
	if _, err := db.Exec(`INSERT INTO artifact_options
		(artifact, ordinal, class, summary, figure_cents, saving_cents, risk, needs, evidence, state, decided_by, decided_at)
		VALUES (1, 1, 'driver.announced', 'swap A', 0, 0, 'low', 'nothing', '[]', 'applied', 'owner', '2026-10-01T09:00:00Z')`); err != nil {
		t.Fatal(err)
	}

	effects, err := finops.AnnouncedEffects(db, "2026-10")
	if err != nil {
		t.Fatal(err)
	}
	if len(effects) != 2 {
		t.Fatalf("effects = %+v, want the two announced drivers of October", effects)
	}
	a := effects[0]
	if a.Label != "swap A" {
		t.Fatalf("the decision furthest from its announcement is %q, want swap A first", a.Label)
	}
	if a.Announced != -300_00 {
		t.Errorf("announced = %s, want -300.00", a.Announced)
	}
	if a.Measured != -120_00 {
		t.Errorf("measured = %s, want -120.00 a day against the 1000.00 baseline", a.Measured)
	}
	if a.Days != 27 {
		t.Errorf("covered days with spend = %d, want 27", a.Days)
	}
	if !a.Answered || a.AnsweredDaysBeforeStart != 4 {
		t.Errorf("answered = %v, %d days before start; want answered on the 1st, 4 days before the 5th",
			a.Answered, a.AnsweredDaysBeforeStart)
	}
	b := effects[1]
	if b.Label != "swap B" || b.Measured != -50_00 {
		t.Errorf("second effect = %s measured %s, want swap B measured -50.00", b.Label, b.Measured)
	}
	if b.Answered {
		t.Errorf("swap B reads as answered with no applied option for it")
	}
}

// A month that missed: a recurring driver on the desk starts before the two
// announced ones, so in the order the registry returns them it comes first.
// The announced decision furthest from its announcement must come before it,
// and the decision that landed exactly as announced after both.
func TestAMissedMonthNamesTheAnnouncedDriverFurthestFromItsAnnouncementFirst(t *testing.T) {
	db := annFixture(t)
	if _, err := db.Exec(crew.Schema); err != nil {
		t.Fatal(err)
	}
	annSpend(t, db, "aws", "svc-test-a", "2026-09-07", 24, 1000_00)
	annSpend(t, db, "aws", "svc-test-a", "2026-10-05", 27, 880_00)
	annSpend(t, db, "aws", "svc-test-b", "2026-09-07", 24, 1000_00)
	annSpend(t, db, "aws", "svc-test-b", "2026-10-05", 27, 950_00)
	if err := estate.InsertDriver(db, world.Driver{Start: "2026-10-01", End: "2026-10-31",
		Scope: "svc-test-c", Label: "month-end batch", Kind: "recurring", Source: "aws"}); err != nil {
		t.Fatal(err)
	}
	annDriver(t, db, "aws", "svc-test-b", "2026-10-05", "2026-10-31", -50_00, "swap B")
	annDriver(t, db, "aws", "svc-test-a", "2026-10-05", "2026-10-31", -300_00, "swap A")

	missed, err := finops.Missed(db, "aws", "2026-10", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(missed) != 3 {
		t.Fatalf("missed = %d drivers, want 3", len(missed))
	}
	if missed[0].Label != "swap A" || missed[1].Label != "swap B" || missed[2].Label != "month-end batch" {
		t.Errorf("order = %q, %q, %q; want swap A (furthest), swap B, then the recurring driver",
			missed[0].Label, missed[1].Label, missed[2].Label)
	}
}

// AnnouncedEffectOf is the one reader the decision page uses. It finds the
// decision by its label and its desk, measures it in the month its window
// starts in, and finds nothing for another desk or another label.
func TestAnnouncedEffectOfReadsTheDecisionOnItsDesk(t *testing.T) {
	db := annFixture(t)
	if _, err := db.Exec(crew.Schema); err != nil {
		t.Fatal(err)
	}
	annSpend(t, db, "aws", "svc-test-a", "2026-09-07", 24, 1000_00)
	annSpend(t, db, "aws", "svc-test-a", "2026-10-01", 4, 1000_00)
	annSpend(t, db, "aws", "svc-test-a", "2026-10-05", 27, 880_00)
	annDriver(t, db, "aws", "svc-test-a", "2026-10-05", "2026-10-31", -300_00, "swap A")
	if _, err := db.Exec(`INSERT INTO artifact_options
		(artifact, ordinal, class, summary, figure_cents, saving_cents, risk, needs, evidence, state, decided_by, decided_at)
		VALUES (1, 1, 'driver.announced', 'swap A', 0, 0, 'low', 'nothing', '[]', 'applied', 'owner', '2026-10-01T09:00:00Z')`); err != nil {
		t.Fatal(err)
	}

	e, found, err := finops.AnnouncedEffectOf(db, "swap A", "aws")
	if err != nil || !found {
		t.Fatalf("the decision on its own desk: found %v, err %v", found, err)
	}
	if e.Measured != -120_00 || e.Days != 27 || e.Announced != -300_00 {
		t.Errorf("measured %s over %d days, announced %s; want -120.00 over 27 days, announced -300.00",
			e.Measured, e.Days, e.Announced)
	}
	if !e.Answered || e.AnsweredDaysBeforeStart != 4 {
		t.Errorf("answered %v, %d days before start; want answered 4 days before", e.Answered, e.AnsweredDaysBeforeStart)
	}
	if _, found, _ := finops.AnnouncedEffectOf(db, "swap A", "onprem"); found {
		t.Errorf("a decision registered on aws was found on onprem")
	}
	if _, found, _ := finops.AnnouncedEffectOf(db, "no such decision", "aws"); found {
		t.Errorf("a label nobody registered was found")
	}
}

// A decision whose service has no spend before its start and none in its window
// has no measured figure: it says so, and it ranks after every measured one.
func TestAnUnmeasuredDecisionSaysSoAndRanksLast(t *testing.T) {
	db := annFixture(t)
	if _, err := db.Exec(crew.Schema); err != nil {
		t.Fatal(err)
	}
	annSpend(t, db, "aws", "svc-test-a", "2026-09-07", 24, 1000_00)
	annSpend(t, db, "aws", "svc-test-a", "2026-10-05", 27, 880_00)
	annDriver(t, db, "aws", "svc-test-quiet", "2026-10-05", "2026-10-31", -50_00, "quiet one")
	annDriver(t, db, "aws", "svc-test-a", "2026-10-05", "2026-10-31", -300_00, "measured one")

	effects, err := finops.AnnouncedEffects(db, "2026-10")
	if err != nil {
		t.Fatal(err)
	}
	if len(effects) != 2 || effects[0].Label != "measured one" || effects[1].Label != "quiet one" {
		t.Fatalf("order = %+v, want the measured decision first and the unmeasured one last", effects)
	}
	if q := effects[1]; q.Days != 0 || q.Measured != 0 {
		t.Errorf("the unmeasured decision reads %d days and %s, want no measured figure", q.Days, q.Measured)
	}
	e, found, err := finops.AnnouncedEffectOf(db, "quiet one", "aws")
	if err != nil || !found || e.Days != 0 {
		t.Errorf("AnnouncedEffectOf on the unmeasured decision: found %v, days %d, err %v", found, e.Days, err)
	}
}
