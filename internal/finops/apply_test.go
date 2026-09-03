package finops_test

// Apply is B3-SPEC.md section 3's table: one row per class, reusing the
// existing function. TestTheSupervisorDecidesItsOwnClassesAndCarriesTheRest
// (supervise_test.go) proves the routing; these prove the table itself,
// class by class, against a real side effect.

import (
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/finops"
)

func applyTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db := seeded(t) // finops_test.go: estate.Seed + finops.SeedRules
	if _, err := db.Exec(crew.Schema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(anomaly.Schema); err != nil {
		t.Fatal(err)
	}
	if err := crew.EnsureArtifactProvenance(db); err != nil {
		t.Fatal(err)
	}
	return db
}

// plantOption writes a task, a POSTED artifact and one open option, and
// returns the option ready for Apply.
func plantOption(t *testing.T, db *sql.DB, desk, anomalyID, class, summary string) crew.Option {
	t.Helper()
	tres, err := db.Exec(`INSERT INTO tasks
		(title, goal, assignee, desk, state, budget_cents, spent_cents, anomaly, created, updated)
		VALUES ('a task', 'a goal', 'investigator-`+desk+`', ?, 'active', 0, 0, ?,
		        datetime('now'), datetime('now'))`, desk, nullableString(anomalyID))
	if err != nil {
		t.Fatal(err)
	}
	taskID, err := tres.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	ares, err := db.Exec(`INSERT INTO artifacts
		(task, author, title, body, state, created)
		VALUES (?, 'investigator-`+desk+`', 'a deliverable', 'body', 'posted', datetime('now'))`,
		taskID)
	if err != nil {
		t.Fatal(err)
	}
	artID, err := ares.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO artifact_options
		(artifact, ordinal, class, summary, figure_cents, saving_cents, risk, needs, evidence, state)
		VALUES (?, 1, ?, ?, 50000, 0, 'low', 'nothing', '[]', 'open')`,
		artID, class, summary); err != nil {
		t.Fatal(err)
	}
	return crew.Option{Artifact: int(artID), Ordinal: 1, Class: class, Summary: summary,
		FigureCents: 50000, State: crew.OptionOpen}
}

// plantOptionWithTarget is plantOption plus a target (DRIVER-WINDOW-SPEC.md
// section 2's {"start", "end"} window, raw JSON or "" for none): a second
// function rather than a plantOption parameter every one of this package's
// other call sites would have to grow to match.
func plantOptionWithTarget(t *testing.T, db *sql.DB, desk, anomalyID, class, summary, target string) crew.Option {
	t.Helper()
	tres, err := db.Exec(`INSERT INTO tasks
		(title, goal, assignee, desk, state, budget_cents, spent_cents, anomaly, created, updated)
		VALUES ('a task', 'a goal', 'investigator-`+desk+`', ?, 'active', 0, 0, ?,
		        datetime('now'), datetime('now'))`, desk, nullableString(anomalyID))
	if err != nil {
		t.Fatal(err)
	}
	taskID, err := tres.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	ares, err := db.Exec(`INSERT INTO artifacts
		(task, author, title, body, state, created)
		VALUES (?, 'investigator-`+desk+`', 'a deliverable', 'body', 'posted', datetime('now'))`,
		taskID)
	if err != nil {
		t.Fatal(err)
	}
	artID, err := ares.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO artifact_options
		(artifact, ordinal, class, summary, figure_cents, saving_cents, risk, needs, evidence, target, state)
		VALUES (?, 1, ?, ?, 50000, 0, 'low', 'nothing', '[]', ?, 'open')`,
		artID, class, summary, nullableString(target)); err != nil {
		t.Fatal(err)
	}
	// finops.Apply reads opt.Target from the struct it is CALLED with, never
	// re-reading the row it was just written to (Apply's own signature takes
	// an already-loaded crew.Option) -- so the target just inserted above
	// has to be set here too, or every caller of this helper would see the
	// same "no target" refusal apply_test.go's own red-first run once caught
	// from a first draft of this helper that forgot exactly this line.
	var rawTarget json.RawMessage
	if target != "" {
		rawTarget = json.RawMessage(target)
	}
	return crew.Option{Artifact: int(artID), Ordinal: 1, Class: class, Summary: summary,
		FigureCents: 50000, Target: rawTarget, State: crew.OptionOpen}
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func mustGetOption(t *testing.T, db *sql.DB, artifact, ordinal int) crew.Option {
	t.Helper()
	o, err := crew.GetOption(db, artifact, ordinal)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

// DRIVER-WINDOW-SPEC.md section 2: a driver written from an option carries
// the window the option's own target named, never Start == End == the day
// Apply happened to run on. Red against unchanged code: applyDriver ignored
// opt.Target entirely and always wrote time.Now().UTC() for both ends, so
// this test's own window assertion failed with
// "drivers window is 2026-09-03 to 2026-09-03, want 2026-08-01 to
// 2026-08-30" -- today's real date, not the target's -- before the fix.
func TestApplyDriverRecurringWritesADriversRow(t *testing.T) {
	db := applyTestDB(t)
	opt := plantOptionWithTarget(t, db, "aws", "", "driver.recurring", "a weekly batch job",
		`{"start": "2026-08-01", "end": "2026-08-30"}`)

	var before int
	if err := db.QueryRow(`SELECT COUNT(*) FROM drivers`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := finops.Apply(db, opt, "supervisor", nil); err != nil {
		t.Fatal(err)
	}
	var after int
	if err := db.QueryRow(`SELECT COUNT(*) FROM drivers`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before+1 {
		t.Fatalf("drivers went from %d to %d rows, want +1", before, after)
	}
	var kind, label, start, end string
	if err := db.QueryRow(`SELECT kind, label, date_start, date_end FROM drivers ORDER BY rowid DESC LIMIT 1`).
		Scan(&kind, &label, &start, &end); err != nil {
		t.Fatal(err)
	}
	if kind != "recurring" || label != "a weekly batch job" {
		t.Errorf("drivers row is (%q, %q), want (recurring, %q)", kind, label, "a weekly batch job")
	}
	// The whole point of DRIVER-WINDOW-SPEC.md: the window is the target's
	// own thirty days, and every day of it is what a driver-aware forecast
	// (internal/finops.ProjectWithDrivers, unmerged as of this file on
	// feat/c3-the-forecast-that-is-scored) and the detector
	// (internal/detect.Driver.Covers, already in this repo) both read.
	if start != "2026-08-01" || end != "2026-08-30" {
		t.Errorf("drivers window is %s to %s, want 2026-08-01 to 2026-08-30 "+
			"(the target's own window, not today's date)", start, end)
	}

	got := mustGetOption(t, db, opt.Artifact, opt.Ordinal)
	if got.State != crew.OptionApplied {
		t.Errorf("option state %q, want applied", got.State)
	}
	if got.DecidedBy != "supervisor" {
		t.Errorf("decided_by %q, want supervisor", got.DecidedBy)
	}
}

func TestApplyPeriodCloseClosesTheOpenPeriod(t *testing.T) {
	db := applyTestDB(t)
	period, err := finops.OpenPeriod(db)
	if err != nil || period == "" {
		t.Fatalf("no open period to close: %v", err)
	}
	opt := plantOption(t, db, "aws", "", "period.close", "close the books")

	if err := finops.Apply(db, opt, "y.mercer", nil); err != nil {
		t.Fatal(err)
	}
	closed, err := finops.IsClosed(db, period)
	if err != nil {
		t.Fatal(err)
	}
	if !closed {
		t.Fatalf("%s is not closed after applying a period.close option", period)
	}
	frozen, err := finops.FrozenPeriod(db, period)
	if err != nil {
		t.Fatal(err)
	}
	if frozen.ClosedBy != "y.mercer" {
		t.Errorf("closed by %q, want the applying owner y.mercer", frozen.ClosedBy)
	}
}

func TestApplyAnomalyExplainSetsItsState(t *testing.T) {
	db := applyTestDB(t)
	an := anomaly.Anomaly{
		ID: "A-applytest", Source: "aws", Team: "ml-platform", Service: "Amazon EC2",
		Day: "2026-07-14", Direction: "up", Amount: 184000, Baseline: 80000, Excess: 104000,
		Z: 4.2, RuleVer: anomaly.RuleVersion, State: anomaly.Open, DetectedAt: "2026-07-15T00:00:00Z",
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
	opt := plantOption(t, db, "aws", an.ID, "anomaly.explain", "a scheduled batch job")

	if err := finops.Apply(db, opt, "supervisor", nil); err != nil {
		t.Fatal(err)
	}
	got, err := anomaly.Get(db, an.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != anomaly.Explained {
		t.Errorf("anomaly state %q, want explained", got.State)
	}
	if got.Reason != "a scheduled batch job" {
		t.Errorf("reason %q, want the option's summary", got.Reason)
	}
}

// A class with no row in the table -- allocation.rule needs a specific rule
// id and method the generic option shape does not carry -- is recorded only:
// no error, the option is marked applied, and nothing it has no data for is
// invented.
func TestApplyAnUnwiredClassIsRecordedOnly(t *testing.T) {
	db := applyTestDB(t)
	opt := plantOption(t, db, "management", "", "allocation.rule", "split Purchase evenly")

	if err := finops.Apply(db, opt, "y.mercer", nil); err != nil {
		t.Fatal(err)
	}
	got := mustGetOption(t, db, opt.Artifact, opt.Ordinal)
	if got.State != crew.OptionApplied {
		t.Errorf("option state %q, want applied", got.State)
	}
}
