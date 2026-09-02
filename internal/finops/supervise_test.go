package finops_test

// B3-SPEC.md section 6's third and fifth named tests, and the contradiction
// half of section 4 step 2 (dropUnfit's own mutant, named in this PR's
// report).

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/finops"
)

func superviseTestDB(t *testing.T) (*sql.DB, int) {
	t.Helper()
	db := applyTestDB(t) // apply_test.go: seeded() + crew.Schema + anomaly.Schema
	res, err := db.Exec(`INSERT INTO sprints (label, start, finish, state, goal)
		VALUES ('2026-W99', '2026-09-01', '2026-09-07', 'active', 'a goal')`)
	if err != nil {
		t.Fatal(err)
	}
	sid, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	// The table, empty, rather than crew.SeedRoster's 39 real analysts:
	// dropUnfit's over-guard check reads the roster for the writing
	// analyst's Monthly figure, and this test's options must survive that
	// check on their own arithmetic (an unknown analyst has Monthly 0, which
	// skips the check) rather than on a seeded fixture's guard numbers this
	// test does not control.
	if _, err := db.Exec(crew.RosterSchema); err != nil {
		t.Fatal(err)
	}
	return db, int(sid)
}

// plantPostedOption is plantOption (apply_test.go) plus a real owner on the
// task and the sprint, and the artifact already POSTED: OpenOptionsForSprint
// only collects options of a posted deliverable (B3-SPEC.md section 4 step
// 1), and the supervisor's pass reads tasks.owner, which crew.FromAnomaly
// and crew.Approve stamp at creation and plantOption does not.
func plantPostedOption(t *testing.T, db *sql.DB, sprintID int, desk, owner, class, summary string, savingCents int64, risk string) crew.Option {
	t.Helper()
	tres, err := db.Exec(`INSERT INTO tasks
		(sprint, title, goal, assignee, desk, state, budget_cents, spent_cents, created, updated, owner)
		VALUES (?, 'a task', 'a goal', ?, ?, 'active', 0, 0, datetime('now'), datetime('now'), ?)`,
		sprintID, "investigator-"+desk, desk, owner)
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
		VALUES (?, 1, ?, ?, 50000, ?, ?, 'nothing', '[]', 'open')`,
		artID, class, summary, savingCents, risk); err != nil {
		t.Fatal(err)
	}
	return crew.Option{Artifact: int(artID), Ordinal: 1, Class: class}
}

// Red first, against the code before this step: finops.Supervise does not
// exist, so nothing collects an open option, let alone decides between
// applying it and carrying it.
func TestTheSupervisorDecidesItsOwnClassesAndCarriesTheRest(t *testing.T) {
	db, sprintID := superviseTestDB(t)
	recurring := plantPostedOption(t, db, sprintID, "aws", "y.mercer",
		"driver.recurring", "a weekly batch job", 0, "low")
	closeIt := plantPostedOption(t, db, sprintID, "aws", "t.langley",
		"period.close", "close the books", 500000, "low")

	pass, err := finops.Supervise(db, sprintID, nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(pass.Applied) != 1 || pass.Applied[0].Class != "driver.recurring" {
		t.Fatalf("applied %v, want exactly one driver.recurring", pass.Applied)
	}
	if len(pass.Carried) != 1 || pass.Carried[0].Class != "period.close" {
		t.Fatalf("carried %v, want exactly one period.close", pass.Carried)
	}

	got := mustGetOption(t, db, recurring.Artifact, recurring.Ordinal)
	if got.State != crew.OptionApplied {
		t.Errorf("driver.recurring option state %q, want applied", got.State)
	}
	if got.DecidedBy != "supervisor" {
		t.Errorf("driver.recurring decided_by %q, want supervisor", got.DecidedBy)
	}

	got2 := mustGetOption(t, db, closeIt.Artifact, closeIt.Ordinal)
	if got2.State != crew.OptionCarried {
		t.Errorf("period.close option state %q, want carried", got2.State)
	}

	// And an analyst's Post never applies it: crew.Post only stamps the
	// deliverable and moves the TASK to posted; it has no path to
	// finops.Apply at all. Confirmed here by checking the closed period
	// stayed open, i.e. the side effect period.close would have produced
	// did not happen from anything other than Supervise's own routing.
	closed, err := finops.IsClosed(db, mustOpenPeriod(t, db))
	if err != nil {
		t.Fatal(err)
	}
	if closed {
		t.Errorf("the period closed even though period.close was only carried, never applied")
	}

	if len(pass.Requests) != 1 || pass.Requests[0].Owner != "t.langley" {
		t.Fatalf("requests %v, want exactly one for t.langley", pass.Requests)
	}
}

func mustOpenPeriod(t *testing.T, db *sql.DB) string {
	t.Helper()
	p, err := finops.OpenPeriod(db)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// Red first, against the code before this step: with no Supervise at all,
// nothing writes a decision request in the first place, so there is nothing
// to ask "how many" of.
func TestADecisionRequestAsksOncePerOwnerPerSprint(t *testing.T) {
	db, sprintID := superviseTestDB(t)
	plantPostedOption(t, db, sprintID, "aws", "t.langley",
		"period.close", "close August", 500000, "low")

	if _, err := finops.Supervise(db, sprintID, nil); err != nil {
		t.Fatal(err)
	}
	var n1 int
	if err := db.QueryRow(`SELECT COUNT(*) FROM decision_requests WHERE sprint=? AND owner='t.langley'`,
		sprintID).Scan(&n1); err != nil {
		t.Fatal(err)
	}
	if n1 != 1 {
		t.Fatalf("after one pass, %d decision requests for t.langley, want 1", n1)
	}

	// A second option, carried to the SAME owner in the SAME sprint, and a
	// second pass: still one request, not two.
	plantPostedOption(t, db, sprintID, "aws", "t.langley",
		"budget.set", "raise the ml-platform budget", 200000, "medium")
	if _, err := finops.Supervise(db, sprintID, nil); err != nil {
		t.Fatal(err)
	}
	var n2 int
	if err := db.QueryRow(`SELECT COUNT(*) FROM decision_requests WHERE sprint=? AND owner='t.langley'`,
		sprintID).Scan(&n2); err != nil {
		t.Fatal(err)
	}
	if n2 != 1 {
		t.Fatalf("after a second pass carrying a second option to the same owner, "+
			"%d decision requests exist, want still 1 (one per owner per sprint)", n2)
	}

	artID, found, err := crew.DecisionRequestFor(db, sprintID, "t.langley")
	if err != nil || !found {
		t.Fatalf("no decision request on file for t.langley: found=%v err=%v", found, err)
	}
	arts, err := crew.Artifacts(db, mustTaskOf(t, db, artID))
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, a := range arts {
		if a.Author == "supervisor" && strings.Contains(a.Title, "t.langley") {
			count++
		}
	}
	if count != 1 {
		t.Errorf("the supervisor's task carries %d deliverables addressed to t.langley, want 1", count)
	}
}

func mustTaskOf(t *testing.T, db *sql.DB, artifactID int) int {
	t.Helper()
	taskID, err := crew.TaskOfArtifact(db, artifactID)
	if err != nil {
		t.Fatal(err)
	}
	return taskID
}

// plantPostedOptionOnAnomaly is plantPostedOption plus tasks.anomaly, which
// dropUnfit's contradiction check groups by.
func plantPostedOptionOnAnomaly(t *testing.T, db *sql.DB, sprintID int, desk, owner, anomalyID, class, summary string) crew.Option {
	t.Helper()
	tres, err := db.Exec(`INSERT INTO tasks
		(sprint, title, goal, assignee, desk, state, budget_cents, spent_cents, anomaly, created, updated, owner)
		VALUES (?, 'a task', 'a goal', ?, ?, 'active', 0, 0, ?, datetime('now'), datetime('now'), ?)`,
		sprintID, "investigator-"+desk, desk, anomalyID, owner)
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
		VALUES (?, 1, ?, ?, 10000, 0, 'low', 'nothing', '[]', 'open')`,
		artID, class, summary); err != nil {
		t.Fatal(err)
	}
	return crew.Option{Artifact: int(artID), Ordinal: 1, Class: class}
}

// Red first, against the code before this step: dropUnfit does not exist, so
// nothing groups options by anomaly at all, let alone drops the ones that
// disagree.
func TestContradictingOptionsOnTheSameAnomalyAreDropped(t *testing.T) {
	db, sprintID := superviseTestDB(t)
	if _, err := db.Exec(anomaly.Schema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO anomalies
		(id, source, service, day, direction, amount_cents, baseline_cents,
		 excess_cents, z, rule_version, state, detected_at)
		VALUES ('A-contradict','aws','Amazon EC2','2026-07-14','up',184000,80000,
		        104000,4.2,?,?,?)`, anomaly.RuleVersion, string(anomaly.Open),
		"2026-07-15T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	one := plantPostedOptionOnAnomaly(t, db, sprintID, "aws", "y.mercer",
		"A-contradict", "anomaly.explain", "a scheduled batch job")
	two := plantPostedOptionOnAnomaly(t, db, sprintID, "aws", "t.langley",
		"A-contradict", "anomaly.explain", "a runaway process, unrelated to any batch job")

	pass, err := finops.Supervise(db, sprintID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(pass.Dropped) != 2 {
		t.Fatalf("dropped %d options, want 2 (both sides of the contradiction)", len(pass.Dropped))
	}
	if len(pass.Applied) != 0 || len(pass.Carried) != 0 {
		t.Fatalf("applied %d, carried %d: a contradiction must be dropped, not routed",
			len(pass.Applied), len(pass.Carried))
	}
	got1 := mustGetOption(t, db, one.Artifact, one.Ordinal)
	got2 := mustGetOption(t, db, two.Artifact, two.Ordinal)
	if got1.State != crew.OptionDropped || got2.State != crew.OptionDropped {
		t.Fatalf("states %q and %q, want both dropped", got1.State, got2.State)
	}
	if strings.TrimSpace(got1.Reason) == "" || strings.TrimSpace(got2.Reason) == "" {
		t.Errorf("a dropped option with no reason: %q / %q", got1.Reason, got2.Reason)
	}
}

// And options that agree, or that sit on different anomalies, are not a
// contradiction: dropUnfit must not over-fire.
func TestAgreeingOptionsOnTheSameAnomalyAreNotDropped(t *testing.T) {
	db, sprintID := superviseTestDB(t)
	if _, err := db.Exec(anomaly.Schema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO anomalies
		(id, source, service, day, direction, amount_cents, baseline_cents,
		 excess_cents, z, rule_version, state, detected_at)
		VALUES ('A-agree','aws','Amazon EC2','2026-07-14','up',184000,80000,
		        104000,4.2,?,?,?)`, anomaly.RuleVersion, string(anomaly.Open),
		"2026-07-15T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	plantPostedOptionOnAnomaly(t, db, sprintID, "aws", "y.mercer",
		"A-agree", "anomaly.explain", "a scheduled batch job")
	plantPostedOptionOnAnomaly(t, db, sprintID, "aws", "t.langley",
		"A-agree", "anomaly.explain", "a scheduled batch job")

	pass, err := finops.Supervise(db, sprintID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(pass.Dropped) != 0 {
		t.Fatalf("dropped %d options that agree with each other, want 0", len(pass.Dropped))
	}
	// anomaly.explain is not in the supervisor's own decides_alone list
	// (only anomaly.accept is), so two options that agree are carried, not
	// applied: this test's own point is that agreeing does not get them
	// dropped, not that agreeing makes them the supervisor's to decide.
	if len(pass.Carried) != 2 {
		t.Fatalf("carried %d, want 2", len(pass.Carried))
	}
}
