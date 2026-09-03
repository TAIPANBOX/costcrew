package finops_test

// Apply is B3-SPEC.md section 3's table: one row per class, reusing the
// existing function. TestTheSupervisorDecidesItsOwnClassesAndCarriesTheRest
// (supervise_test.go) proves the routing; these prove the table itself,
// class by class, against a real side effect.

import (
	"database/sql"
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

func TestApplyDriverRecurringWritesADriversRow(t *testing.T) {
	db := applyTestDB(t)
	opt := plantOption(t, db, "aws", "", "driver.recurring", "a weekly batch job")

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
	var kind, label string
	if err := db.QueryRow(`SELECT kind, label FROM drivers ORDER BY rowid DESC LIMIT 1`).
		Scan(&kind, &label); err != nil {
		t.Fatal(err)
	}
	if kind != "recurring" || label != "a weekly batch job" {
		t.Errorf("drivers row is (%q, %q), want (recurring, %q)", kind, label, "a weekly batch job")
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
