package crew_test

// DRIVER-WINDOW-SPEC.md section 2: driver.recurring and driver.one-time
// alone, of every class an analyst's or the supervisor's own deliverable may
// name, carry a structured target naming the window a rhythm is expected or
// a one-time event covers -- absent target refused at save time, present
// and malformed refused at save time, the same shape C2's own
// allocation.rule target (costcrew#31, unmerged as of this file) holds for
// its own class.
//
// Red against unchanged code: crew.Option carries no Target field and
// ValidateAndSaveOptions checks no driver class's target at all, so every
// case here that expects a refusal instead saved the option unrefused.

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/crew"
)

// driverOptionBody builds a one-option deliverable body naming class, with
// target as a raw JSON fragment (or "" to omit the field entirely) so each
// case below controls exactly one thing.
func driverOptionBody(class, targetJSON string) string {
	target := ""
	if targetJSON != "" {
		target = `, "target": ` + targetJSON
	}
	return "## A driver option\nContext for the option below.\n\n```options\n" +
		`{"options": [{"class": "` + class + `", "summary": "a weekly batch job", ` +
		`"figure_cents": 0, "saving_cents": 0, "risk": "low", "needs": "nothing"` + target + `}]}` +
		"\n```\n"
}

// plantAnomalyTask is plantPlainTask (options_test.go) plus an anomaly link,
// for the driver.one-time cases where the task's own anomaly is what
// supplies the day (section 2, "that day IS the driver, nothing to ask").
// The crew package's own hasAnomaly check (artifactHasAnomaly, options.go)
// only reads this column -- never whether the row it names still exists in
// the anomalies table -- so no anomalies row has to exist for these tests.
func plantAnomalyTask(t *testing.T, db *sql.DB, anomalyID string) int {
	t.Helper()
	res, err := db.Exec(`INSERT INTO tasks
		(title, goal, assignee, desk, state, budget_cents, spent_cents, anomaly, created, updated)
		VALUES ('a task', 'a goal', 'investigator-aws', 'aws', 'active', 0, 0, ?,
		        datetime('now'), datetime('now'))`, anomalyID)
	if err != nil {
		t.Fatal(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return int(id)
}

func TestDriverRecurringWithNoTargetIsRefused(t *testing.T) {
	db := optionsTestDB(t)
	taskID := plantPlainTask(t, db)
	body := driverOptionBody("driver.recurring", "")
	artID := plantDraftArtifact(t, db, taskID, body)

	refused, reason, err := crew.ValidateAndSaveOptions(db, artID, "supervisor", body, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !refused {
		t.Fatal("driver.recurring with no target was accepted")
	}
	if !strings.Contains(reason, "target") {
		t.Errorf("reason %q does not name the target", reason)
	}
	opts, err := crew.Options(db, artID)
	if err != nil {
		t.Fatal(err)
	}
	if len(opts) != 0 {
		t.Errorf("%d options stored despite the refusal", len(opts))
	}
}

func TestDriverRecurringWithAValidTargetIsSaved(t *testing.T) {
	db := optionsTestDB(t)
	taskID := plantPlainTask(t, db)
	body := driverOptionBody("driver.recurring", `{"start": "2026-08-01", "end": "2026-08-30"}`)
	artID := plantDraftArtifact(t, db, taskID, body)

	refused, reason, err := crew.ValidateAndSaveOptions(db, artID, "supervisor", body, nil)
	if err != nil {
		t.Fatal(err)
	}
	if refused {
		t.Fatalf("a well-formed driver.recurring target was refused: %s", reason)
	}
	opts, err := crew.Options(db, artID)
	if err != nil {
		t.Fatal(err)
	}
	if len(opts) != 1 {
		t.Fatalf("got %d options, want 1", len(opts))
	}
	if !strings.Contains(string(opts[0].Target), `"start"`) {
		t.Errorf("Target %q does not carry start", string(opts[0].Target))
	}
}

func TestDriverOneTimeOnAnAnomalyTaskNeedsNoTarget(t *testing.T) {
	db := optionsTestDB(t)
	taskID := plantAnomalyTask(t, db, "A-crew-1")
	body := driverOptionBody("driver.one-time", "")
	artID := plantDraftArtifact(t, db, taskID, body)

	refused, reason, err := crew.ValidateAndSaveOptions(db, artID, "investigator-aws", body, nil)
	if err != nil {
		t.Fatal(err)
	}
	if refused {
		t.Fatalf("driver.one-time on an anomaly task with no target was refused: %s", reason)
	}
}

func TestDriverOneTimeWithNoAnomalyAndNoTargetIsRefused(t *testing.T) {
	db := optionsTestDB(t)
	taskID := plantPlainTask(t, db)
	body := driverOptionBody("driver.one-time", "")
	artID := plantDraftArtifact(t, db, taskID, body)

	refused, reason, err := crew.ValidateAndSaveOptions(db, artID, "investigator-aws", body, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !refused {
		t.Fatal("driver.one-time with no anomaly and no target was accepted")
	}
	if !strings.Contains(reason, "target") {
		t.Errorf("reason %q does not name the target", reason)
	}
	opts, err := crew.Options(db, artID)
	if err != nil {
		t.Fatal(err)
	}
	if len(opts) != 0 {
		t.Errorf("%d options stored despite the refusal", len(opts))
	}
}

func TestDriverOneTimeWithNoAnomalyAndAValidTargetIsSaved(t *testing.T) {
	db := optionsTestDB(t)
	taskID := plantPlainTask(t, db)
	body := driverOptionBody("driver.one-time", `{"start": "2026-07-04", "end": "2026-07-04"}`)
	artID := plantDraftArtifact(t, db, taskID, body)

	refused, reason, err := crew.ValidateAndSaveOptions(db, artID, "investigator-aws", body, nil)
	if err != nil {
		t.Fatal(err)
	}
	if refused {
		t.Fatalf("driver.one-time with no anomaly and a valid target was refused: %s", reason)
	}
}

// Boundaries (DRIVER-WINDOW-SPEC.md section 4): a window right at the edge
// of what is allowed is accepted, not refused.
func TestDriverTargetBoundariesAreAccepted(t *testing.T) {
	cases := []struct {
		name       string
		start, end string
	}{
		{"start equals end", "2026-08-01", "2026-08-01"},
		{"exactly 366 days", "2025-01-01", "2026-01-02"}, // 2025 is not a leap year: 365 days to 2026-01-01, +1 more
		{"window entirely in the past", "2020-01-01", "2020-01-31"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db := optionsTestDB(t)
			taskID := plantPlainTask(t, db)
			targetJSON := fmt.Sprintf(`{"start": %q, "end": %q}`, c.start, c.end)
			body := driverOptionBody("driver.recurring", targetJSON)
			artID := plantDraftArtifact(t, db, taskID, body)

			refused, reason, err := crew.ValidateAndSaveOptions(db, artID, "supervisor", body, nil)
			if err != nil {
				t.Fatal(err)
			}
			if refused {
				t.Fatalf("a boundary-valid window (%s to %s) was refused: %s", c.start, c.end, reason)
			}
		})
	}
}

// Hostile inputs (DRIVER-WINDOW-SPEC.md section 4).
func TestDriverTargetHostileInputs(t *testing.T) {
	cases := []struct {
		name    string
		class   string
		target  string // raw JSON fragment
		anomaly string // "" for a plain task, else the task carries this anomaly
	}{
		{"end before start", "driver.recurring",
			`{"start": "2026-08-30", "end": "2026-08-01"}`, ""},
		{"a five-year window", "driver.recurring",
			`{"start": "2020-01-01", "end": "2025-01-01"}`, ""},
		{"start does not parse", "driver.recurring",
			`{"start": "not-a-date", "end": "2026-08-30"}`, ""},
		{"end does not parse", "driver.recurring",
			`{"start": "2026-08-01", "end": "not-a-date"}`, ""},
		{"a target on a class that takes none: driver.one-time on an anomaly task",
			"driver.one-time", `{"start": "2026-08-01", "end": "2026-08-30"}`, "A-crew-2"},
		// A syntactically valid JSON value that is not an object driverTarget
		// can decode into: dates written as bare numbers rather than quoted
		// strings, a realistic slip rather than a contrived one. This is the
		// one case in this table that exercises validateDriverTarget's own
		// json.Unmarshal error path rather than a date that parses as a
		// string but not as a date -- every OTHER case here already survives
		// ParseOptions's own outer unmarshal (a json.RawMessage field is
		// always syntactically valid JSON by the time this function sees it,
		// since the outer decoder has to tokenize its boundaries to capture
		// it at all), so only a type mismatch, not a syntax error, can still
		// fail here.
		{"start and end written as numbers, not strings", "driver.recurring",
			`{"start": 20260801, "end": 20260830}`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db := optionsTestDB(t)
			var taskID int
			if c.anomaly != "" {
				taskID = plantAnomalyTask(t, db, c.anomaly)
			} else {
				taskID = plantPlainTask(t, db)
			}
			body := driverOptionBody(c.class, c.target)
			artID := plantDraftArtifact(t, db, taskID, body)

			roleName := "supervisor"
			if c.class == "driver.one-time" {
				roleName = "investigator-aws"
			}
			refused, reason, err := crew.ValidateAndSaveOptions(db, artID, roleName, body, nil)
			if err != nil {
				t.Fatalf("ValidateAndSaveOptions returned an error rather than a refusal: %v", err)
			}
			if !refused {
				t.Fatalf("want refused, got accepted")
			}
			if strings.TrimSpace(reason) == "" {
				t.Errorf("refused with no reason")
			}
			opts, err := crew.Options(db, artID)
			if err != nil {
				t.Fatal(err)
			}
			if len(opts) != 0 {
				t.Errorf("%d options stored despite the refusal", len(opts))
			}
		})
	}
}

// The last hostile case section 4 names, "a 1 MB target", is caught by the
// pre-existing 64 KiB whole-block cap (ParseOptions's own
// optionsBlockMaxBytes) before a class-specific target is ever looked at --
// "C2's cap", reused rather than a second, driver-specific one.
func TestDriverTargetOversizeIsCaughtByTheWholeBlockCap(t *testing.T) {
	db := optionsTestDB(t)
	taskID := plantPlainTask(t, db)
	huge := `{"start": "2026-08-01", "end": "2026-08-30", "padding": "` +
		strings.Repeat("x", 1_100_000) + `"}`
	body := driverOptionBody("driver.recurring", huge)
	artID := plantDraftArtifact(t, db, taskID, body)

	refused, reason, err := crew.ValidateAndSaveOptions(db, artID, "supervisor", body, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !refused {
		t.Fatal("a 1 MB target was accepted")
	}
	if !strings.Contains(reason, "byte") {
		t.Errorf("reason %q does not name the byte limit", reason)
	}
}

// And validateDriverTarget's OWN, smaller cap (targetMaxBytes, 4 KiB),
// sized so a target between 4 KiB and the 64 KiB whole-block cap is caught
// by this function's own check rather than the outer one -- the case above
// proves the outer cap still catches an extreme target; this proves the
// inner one is not simply dead code sitting under it.
func TestDriverTargetOverTargetMaxBytesIsRefusedByItsOwnCap(t *testing.T) {
	db := optionsTestDB(t)
	taskID := plantPlainTask(t, db)
	mid := `{"start": "2026-08-01", "end": "2026-08-30", "padding": "` +
		strings.Repeat("x", 10_000) + `"}` // ~10 KB: over targetMaxBytes (4 KiB), under optionsBlockMaxBytes (64 KiB)
	body := driverOptionBody("driver.recurring", mid)
	artID := plantDraftArtifact(t, db, taskID, body)

	refused, reason, err := crew.ValidateAndSaveOptions(db, artID, "supervisor", body, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !refused {
		t.Fatal("a target over targetMaxBytes but under the whole-block cap was accepted")
	}
	if !strings.Contains(reason, "byte") {
		t.Errorf("reason %q does not name the byte limit", reason)
	}
}

// EnsureOptionTarget migrates an installation from before artifact_options
// carried a target column at all -- the same "create the OLD schema by
// hand, run the ensure function twice, confirm the column now accepts a
// value" shape connectors.TestEnsureFocusSchemaAddsInvoiceIdColumns already
// holds for its own added column.
func TestEnsureOptionTargetAddsTheColumn(t *testing.T) {
	db := optionsTestDB(t)
	// Simulate an installation from before this column existed: replace the
	// table with the OLD shape (no target anywhere), then run the ensure
	// function and confirm it gained the column, safely, twice.
	if _, err := db.Exec(`DROP TABLE artifact_options`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE artifact_options(
		artifact INTEGER NOT NULL, ordinal INTEGER NOT NULL, class TEXT NOT NULL,
		summary TEXT, figure_cents INTEGER NOT NULL DEFAULT 0,
		saving_cents INTEGER NOT NULL DEFAULT 0, risk TEXT, needs TEXT,
		evidence TEXT, state TEXT NOT NULL, decided_by TEXT, decided_at TEXT,
		reason TEXT, PRIMARY KEY (artifact, ordinal))`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := crew.EnsureOptionTarget(db); err != nil {
			t.Fatalf("run %d: %v", i+1, err)
		}
	}
	if _, err := db.Exec(`INSERT INTO artifact_options
		(artifact, ordinal, class, state, target) VALUES (1, 1, 'driver.recurring', 'open', 'x')`); err != nil {
		t.Fatalf("artifact_options.target does not exist after EnsureOptionTarget: %v", err)
	}
}
