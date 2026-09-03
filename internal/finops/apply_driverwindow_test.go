package finops_test

// DRIVER-WINDOW-SPEC.md sections 2 and 4: applyDriver's own apply-time
// rules, beside TestApplyDriverRecurringWritesADriversRow (apply_test.go),
// which holds the "target present" case for driver.recurring.
//
// Red against unchanged code: opt.Target does not exist on crew.Option, so
// none of this compiled before that field landed; once it does, every case
// here that expects "no drivers row and a descriptive error" instead wrote
// a one-day drivers row dated to time.Now().UTC(), because applyDriver never
// read the anomaly-vs-target distinction these tests are about.

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/finops"
)

func driversCount(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM drivers`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// plantAnomalyForApply writes one anomaly row of the shape applyDriver reads
// through anomaly.Get once a task names it: an.Service becomes the driver's
// scope, an.Source its desk, an.Day (for driver.one-time only) its window.
func plantAnomalyForApply(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	an := anomaly.Anomaly{
		ID: id, Source: "aws", Team: "ml-platform", Service: "Amazon EC2",
		Day: "2026-06-22", Direction: "up", Amount: 184000, Baseline: 80000, Excess: 104000,
		Z: 4.2, RuleVer: anomaly.RuleVersion, State: anomaly.Open, DetectedAt: "2026-06-23T00:00:00Z",
	}
	if _, err := db.Exec(`INSERT INTO anomalies
		(id, source, team, service, day, direction, amount_cents, baseline_cents,
		 excess_cents, z, rule_version, state, detected_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		an.ID, an.Source, an.Team, an.Service, an.Day, an.Direction,
		int64(an.Amount), int64(an.Baseline), int64(an.Excess), an.Z, an.RuleVer,
		string(an.State), an.DetectedAt); err != nil {
		t.Fatal(err)
	}
}

// Mutant "accept a recurring option without a target" (DRIVER-WINDOW-SPEC.md
// section 4), at the Apply layer: an option that reached Apply with no
// target at all -- one saved before this change, or a caller bypassing
// crew.ValidateAndSaveOptions the way plantOption always does -- writes no
// drivers row and Apply returns a descriptive error, never a silent
// today-dated one-day driver.
func TestApplyDriverRecurringWithNoTargetWritesNoDriversRow(t *testing.T) {
	db := applyTestDB(t)
	opt := plantOption(t, db, "aws", "", "driver.recurring", "a weekly batch job")

	before := driversCount(t, db)
	err := finops.Apply(db, opt, "supervisor", nil)
	if err == nil {
		t.Fatal("driver.recurring with no target was applied without error")
	}
	if !strings.Contains(err.Error(), "no target") {
		t.Errorf("error %q does not name the missing target", err.Error())
	}
	after := driversCount(t, db)
	if after != before {
		t.Errorf("drivers went from %d to %d rows, want unchanged: no target means no row", before, after)
	}
	got := mustGetOption(t, db, opt.Artifact, opt.Ordinal)
	if got.State != crew.OptionOpen {
		t.Errorf("option state %q, want still open: Apply returned an error, so "+
			"crew.MarkOptionApplied must never have run", got.State)
	}
}

// driver.one-time on a task WITH an anomaly: "that day IS the driver,
// nothing to ask" (section 2). No target at all here, and the anomaly's own
// day is still what gets written -- the anomaly, not a target, is this
// class's usual source of a day.
func TestApplyDriverOneTimeOnAnAnomalyKeepsTheAnomalysDay(t *testing.T) {
	db := applyTestDB(t)
	plantAnomalyForApply(t, db, "A-onetime-1")
	opt := plantOption(t, db, "aws", "A-onetime-1", "driver.one-time", "a scheduled batch job")

	if err := finops.Apply(db, opt, "supervisor", nil); err != nil {
		t.Fatal(err)
	}
	var start, end, scope, source string
	if err := db.QueryRow(`SELECT date_start, date_end, scope, source FROM drivers
		ORDER BY rowid DESC LIMIT 1`).Scan(&start, &end, &scope, &source); err != nil {
		t.Fatal(err)
	}
	if start != "2026-06-22" || end != "2026-06-22" {
		t.Errorf("drivers window is %s to %s, want 2026-06-22 to 2026-06-22 (the anomaly's own day)",
			start, end)
	}
	if scope != "Amazon EC2" || source != "aws" {
		t.Errorf("drivers row is (scope=%q, source=%q), want (Amazon EC2, aws): the anomaly's "+
			"own service and desk", scope, source)
	}
}

// driver.one-time on a task WITH an anomaly, and a target volunteered
// anyway: the anomaly's own day still wins. crew.ValidateAndSaveOptions
// already refuses this exact combination at save time (section 2's own
// hostile case, "a target on a class that takes none"); this is the same
// rule held again here for an option that reached Apply having bypassed it
// -- plantOption always does, since it writes artifact_options directly.
func TestApplyDriverOneTimeOnAnAnomalyIgnoresAnyTarget(t *testing.T) {
	db := applyTestDB(t)
	plantAnomalyForApply(t, db, "A-onetime-2")
	opt := plantOptionWithTarget(t, db, "aws", "A-onetime-2", "driver.one-time", "a scheduled batch job",
		`{"start": "2026-01-01", "end": "2026-01-31"}`)

	if err := finops.Apply(db, opt, "supervisor", nil); err != nil {
		t.Fatal(err)
	}
	var start, end string
	if err := db.QueryRow(`SELECT date_start, date_end FROM drivers ORDER BY rowid DESC LIMIT 1`).
		Scan(&start, &end); err != nil {
		t.Fatal(err)
	}
	if start != "2026-06-22" || end != "2026-06-22" {
		t.Errorf("drivers window is %s to %s, want 2026-06-22 to 2026-06-22: the anomaly's own day, "+
			"not the volunteered target", start, end)
	}
}

// driver.one-time on a task with NO anomaly, and no target either: neither
// source of a day exists, so this writes no drivers row and returns a
// descriptive error -- section 2's own words, "today's date is a guess the
// code makes today and stops making".
func TestApplyDriverOneTimeWithNoAnomalyAndNoTargetWritesNoDriversRow(t *testing.T) {
	db := applyTestDB(t)
	opt := plantOption(t, db, "aws", "", "driver.one-time", "a one-off migration step")

	before := driversCount(t, db)
	err := finops.Apply(db, opt, "supervisor", nil)
	if err == nil {
		t.Fatal("driver.one-time with no anomaly and no target was applied without error")
	}
	if !strings.Contains(err.Error(), "no target") || !strings.Contains(err.Error(), "anomaly") {
		t.Errorf("error %q does not name both the missing target and the missing anomaly", err.Error())
	}
	after := driversCount(t, db)
	if after != before {
		t.Errorf("drivers went from %d to %d rows, want unchanged", before, after)
	}
}

// Mutant "take today's date for a one-time driver on a plain task" (section
// 4): a driver.one-time option on a task with NO anomaly, but WITH a
// target, must write the target's own window. If applyDriver instead fell
// back to time.Now() whenever the task carried no anomaly -- the exact
// shape of the original bug, just for the other class -- this test's window
// assertion is what catches it, since the target's dates here are neither
// today (2026-09-03, this file's own read of the clock at write time) nor
// equal to each other by coincidence.
func TestApplyDriverOneTimeWithNoAnomalyAndATargetWritesItsWindow(t *testing.T) {
	db := applyTestDB(t)
	opt := plantOptionWithTarget(t, db, "aws", "", "driver.one-time", "a one-off migration step",
		`{"start": "2026-07-04", "end": "2026-07-04"}`)

	if err := finops.Apply(db, opt, "supervisor", nil); err != nil {
		t.Fatal(err)
	}
	var start, end, scope string
	if err := db.QueryRow(`SELECT date_start, date_end, scope FROM drivers ORDER BY rowid DESC LIMIT 1`).
		Scan(&start, &end, &scope); err != nil {
		t.Fatal(err)
	}
	if start != "2026-07-04" || end != "2026-07-04" {
		t.Errorf("drivers window is %s to %s, want 2026-07-04 to 2026-07-04 (the target's own day, "+
			"not today's date)", start, end)
	}
	if scope != "*" {
		t.Errorf("scope %q, want * (no anomaly to narrow it to one service)", scope)
	}
}

// A target that survived to Apply by bypassing crew.ValidateAndSaveOptions
// (plantOptionWithTarget writes artifact_options directly, the same way a
// caller skipping the save-time gate would) but is not a JSON object at
// all: decodeDriverTarget's own "does not decode" path, distinct from a
// merely absent target.
func TestApplyDriverRecurringWithAMalformedTargetReturnsADecodeError(t *testing.T) {
	db := applyTestDB(t)
	opt := plantOptionWithTarget(t, db, "aws", "", "driver.recurring", "a weekly batch job",
		`[1,2,3]`) // a JSON array, not an object with start/end

	before := driversCount(t, db)
	err := finops.Apply(db, opt, "supervisor", nil)
	if err == nil {
		t.Fatal("a malformed target was applied without error")
	}
	if !strings.Contains(err.Error(), "does not decode") {
		t.Errorf("error %q does not say the target failed to decode", err.Error())
	}
	if after := driversCount(t, db); after != before {
		t.Errorf("drivers went from %d to %d rows, want unchanged", before, after)
	}
}

// A target that decodes as an object but carries neither field: the same
// bypass shape, one step further in. decodeDriverTarget refuses this too,
// rather than writing a drivers row with an empty start and end.
func TestApplyDriverRecurringWithAnEmptyTargetObjectReturnsAnError(t *testing.T) {
	db := applyTestDB(t)
	opt := plantOptionWithTarget(t, db, "aws", "", "driver.recurring", "a weekly batch job", `{}`)

	before := driversCount(t, db)
	err := finops.Apply(db, opt, "supervisor", nil)
	if err == nil {
		t.Fatal("an empty target object was applied without error")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("error %q does not say the target's start or end is empty", err.Error())
	}
	if after := driversCount(t, db); after != before {
		t.Errorf("drivers went from %d to %d rows, want unchanged", before, after)
	}
}
