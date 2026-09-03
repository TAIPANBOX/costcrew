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
	// analysts, so queueShowbackTasks (period.close's own statement half,
	// C2-SPEC.md section 2) has a roster to check "reporter-<desk>" against,
	// matching what main.go always guarantees before Apply can be reached in
	// production: crew.EnsureOwnershipHistory already creates this table on
	// every start, ahead of anything that could apply an option.
	if _, err := db.Exec(crew.RosterSchema); err != nil {
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

// C3-SPEC.md section 2: "the option's summary becomes the freeze's recorded
// basis." The forecaster's own written explanation, not the generated
// run-rate sentence, ends up in forecasts.basis once a supervisor applies
// the forecast.freeze option.
func TestApplyForecastFreezeUsesTheOptionsSummaryAsTheBasis(t *testing.T) {
	db := applyTestDB(t)
	period, err := finops.OpenPeriod(db)
	if err != nil || period == "" {
		t.Fatalf("no open period: %v %v", period, err)
	}
	opt := plantOption(t, db, "aws", "", "forecast.freeze",
		"the analyst's own written explanation, driver and all")

	if err := finops.Apply(db, opt, "supervisor", nil); err != nil {
		t.Fatal(err)
	}
	frozen, err := finops.IsFrozen(db, period)
	if err != nil {
		t.Fatal(err)
	}
	if !frozen {
		t.Fatalf("%s is not frozen after applying forecast.freeze", period)
	}
	var basis string
	if err := db.QueryRow(`SELECT basis FROM forecasts WHERE period=? AND source='aws'`,
		period).Scan(&basis); err != nil {
		t.Fatal(err)
	}
	if basis != opt.Summary {
		t.Errorf("basis = %q, want the option's own summary %q", basis, opt.Summary)
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

// plantExplainerPublishOption is plantOption's shape, for the one class
// whose side effect reads the ARTIFACT itself (its author and its whole
// body), not just the option's own summary: explainer.publish, C8-SPEC.md
// section 2, "the artifact's body as the explainer".
func plantExplainerPublishOption(t *testing.T, db *sql.DB, author, summary, body string) crew.Option {
	t.Helper()
	tres, err := db.Exec(`INSERT INTO tasks
		(title, goal, assignee, desk, state, budget_cents, spent_cents, created, updated)
		VALUES ('the fortnightly pack', 'write it', ?, 'management', 'active', 0, 0,
		        datetime('now'), datetime('now'))`, author)
	if err != nil {
		t.Fatal(err)
	}
	taskID, err := tres.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	ares, err := db.Exec(`INSERT INTO artifacts
		(task, author, title, body, state, created)
		VALUES (?, ?, 'The executive pack', ?, 'posted', datetime('now'))`,
		taskID, author, body)
	if err != nil {
		t.Fatal(err)
	}
	artID, err := ares.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO artifact_options
		(artifact, ordinal, class, summary, figure_cents, saving_cents, risk, needs, evidence, state)
		VALUES (?, 1, 'explainer.publish', ?, 0, 0, '', '', '[]', 'open')`,
		artID, summary); err != nil {
		t.Fatal(err)
	}
	return crew.Option{Artifact: int(artID), Ordinal: 1, Class: "explainer.publish",
		Summary: summary, State: crew.OptionOpen}
}

// Red first, against main: applySideEffect has no case for explainer.publish
// at all, so this class is recorded only -- the option is marked applied and
// crew.Explainers stays empty. C8-SPEC.md section 4: "applying an
// explainer.publish option publishes the artifact's body as an explainer
// (today recorded only)".
func TestApplyExplainerPublishPublishesTheArtifactsBodyAsAnExplainer(t *testing.T) {
	db := applyTestDB(t)
	body := "## The fortnight in four numbers\n\n" +
		"allocation-coverage: 92.3% (was 91.0%, +1.3)\n" +
		"cost-per-outcome: refused, the business metric this would divide by is not connected.\n"
	opt := plantExplainerPublishOption(t, db, "exec-reporter", "The fortnight in four numbers", body)

	before, err := crew.Explainers(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 0 {
		t.Fatalf("an explainer exists before Apply ever ran: %d", len(before))
	}

	if err := finops.Apply(db, opt, "supervisor", nil); err != nil {
		t.Fatal(err)
	}

	list, err := crew.Explainers(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("applying one explainer.publish option produced %d explainers, want 1", len(list))
	}
	e := list[0]
	if e.State != "published" {
		t.Errorf("state = %q, want published: applying the option IS the stamp", e.State)
	}
	if e.Publisher != "supervisor" {
		t.Errorf("publisher = %q, want the actor Apply was called with", e.Publisher)
	}
	if e.Body != body {
		t.Errorf("the explainer's body is not the artifact's own body verbatim:\nGOT:  %q\nWANT: %q", e.Body, body)
	}
	if e.Topic != "The fortnight in four numbers" {
		t.Errorf("topic = %q, want the option's own summary (the pack's title)", e.Topic)
	}
	if e.Author != "exec-reporter" {
		t.Errorf("author = %q, want the artifact's own author", e.Author)
	}
	if e.Audience != "leadership" {
		t.Errorf("audience = %q, want %q, so the explainers page can filter to it", e.Audience, "leadership")
	}

	got := mustGetOption(t, db, opt.Artifact, opt.Ordinal)
	if got.State != crew.OptionApplied {
		t.Errorf("option state %q, want applied", got.State)
	}
}

// TestApplyingPurchaseHasNoSideEffect is C4-SPEC.md section 4's own mutant
// (h), "put purchase into the apply table (must be refused by the class
// check)": purchase's owner is "nobody" in roles.yaml -- crew.MayDecide
// refuses it before it ever reaches an Owner field, for EVERY role,
// TestRolesAreBound's own coverage of classes/roles.yaml already holds that
// direction -- so applySideEffect's table has no case for it, on purpose,
// the same "text only" shape TestApplyAnUnwiredClassIsRecordedOnly already
// proves for allocation.rule. This test is sensitive to the one thing that
// property does not cover: applySideEffect ITSELF quietly growing a case for
// "purchase" that DOES do something, which is exactly what
// gates-have-teeth.sh's own "commitments: purchase in the apply table" case
// plants (a driver.recurring-shaped case, the cheapest real side effect this
// table already has an example of) and this test must catch.
func TestApplyingPurchaseHasNoSideEffect(t *testing.T) {
	db := applyTestDB(t)
	opt := plantOption(t, db, "management", "", "purchase",
		"buy a one-year Committed Use Discount on the ai desk")

	var before int
	if err := db.QueryRow(`SELECT COUNT(*) FROM drivers`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := finops.Apply(db, opt, "y.mercer", nil); err != nil {
		t.Fatal(err)
	}
	var after int
	if err := db.QueryRow(`SELECT COUNT(*) FROM drivers`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Errorf("drivers went from %d to %d rows: applying a purchase option must never "+
			"write a real side effect, because the purchase itself never happens in this console",
			before, after)
	}
	got := mustGetOption(t, db, opt.Artifact, opt.Ordinal)
	if got.State != crew.OptionApplied {
		t.Errorf("option state %q, want applied (the STAMP is recorded; only the money is not)", got.State)
	}

	// The class check itself, independent of the apply table: no role, not
	// even the owner link, may ever decide purchase alone.
	for _, role := range []string{"owner", "supervisor", "commitments"} {
		if may, why := crew.MayDecide(role, "purchase"); may {
			t.Errorf("crew.MayDecide(%q, \"purchase\") = true, want false: %s", role, why)
		}
	}
}
