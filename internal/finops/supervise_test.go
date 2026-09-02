package finops_test

// B3-SPEC.md section 6's third and fifth named tests, and the review fixes:
// options in one deliverable are alternatives, never independent actions;
// a contradiction between two deliverables is carried as one question, not
// dropped; and the guard the supervisor's pass checks a figure against is
// roles.yaml's own T.anomaly, not an analyst's unrelated LLM-spend guard.

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
	// The table, empty rather than crew.SeedRoster's 39 real analysts, for
	// every test except the one that deliberately loads it
	// (TestARealAnalystsGuardNeverBlocksACloudFigure): Supervise no longer
	// reads the roster at all, so this is only here because other tables in
	// this schema join against it, not because any test's own arithmetic
	// depends on it being empty.
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
// the contradiction check groups by.
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

func plantAnomalyRow(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	if _, err := db.Exec(anomaly.Schema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO anomalies
		(id, source, service, day, direction, amount_cents, baseline_cents,
		 excess_cents, z, rule_version, state, detected_at)
		VALUES (?,'aws','Amazon EC2','2026-07-14','up',184000,80000,
		        104000,4.2,?,?,?)`, id, anomaly.RuleVersion, string(anomaly.Open),
		"2026-07-15T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
}

// Red first (test e from the review): two DIFFERENT deliverables naming a
// different cause for the same anomaly must be carried, not dropped --
// roles.yaml's own hands_to_owner_conditions, "any question two analysts
// answer differently on the same evidence", makes this one question rather
// than two, and one question is one decision request. Neither side is ever
// applied: anomaly.explain is not in the supervisor's own decides_alone
// list, so the ordinary per-deliverable rule already carries both, with or
// without a contradiction. Named for the mutant in this PR's report that
// ignores artifact identity in the contradiction check -- see the next test
// for the property that mutant actually breaks.
func TestContradictingOptionsAreCarriedAsOneQuestion(t *testing.T) {
	db, sprintID := superviseTestDB(t)
	plantAnomalyRow(t, db, "A-contradict")
	one := plantPostedOptionOnAnomaly(t, db, sprintID, "aws", "y.mercer",
		"A-contradict", "anomaly.explain", "a scheduled batch job")
	two := plantPostedOptionOnAnomaly(t, db, sprintID, "aws", "t.langley",
		"A-contradict", "anomaly.explain", "a runaway process, unrelated to any batch job")

	pass, err := finops.Supervise(db, sprintID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(pass.Applied) != 0 {
		t.Fatalf("applied %d, want 0: anomaly.explain is never the supervisor's own class", len(pass.Applied))
	}
	if len(pass.Carried) != 2 {
		t.Fatalf("carried %d, want 2 (both sides of the disagreement)", len(pass.Carried))
	}
	got1 := mustGetOption(t, db, one.Artifact, one.Ordinal)
	got2 := mustGetOption(t, db, two.Artifact, two.Ordinal)
	if got1.State != crew.OptionCarried || got2.State != crew.OptionCarried {
		t.Fatalf("states %q and %q, want both carried", got1.State, got2.State)
	}
	// One question, one owner, one decision request -- not two, even though
	// the two deliverables have different owners.
	if len(pass.Requests) != 1 {
		t.Fatalf("wrote %d decision requests, want 1: two analysts disagreeing is ONE question",
			len(pass.Requests))
	}
}

// And options that agree, or that sit on different anomalies, are not a
// contradiction: they are carried (or applied) independently, each on its
// own deliverable's terms, and land in as many decision requests as they
// have distinct owners.
func TestAgreeingOptionsOnTheSameAnomalyAreNotLinked(t *testing.T) {
	db, sprintID := superviseTestDB(t)
	plantAnomalyRow(t, db, "A-agree")
	plantPostedOptionOnAnomaly(t, db, sprintID, "aws", "y.mercer",
		"A-agree", "anomaly.explain", "a scheduled batch job")
	plantPostedOptionOnAnomaly(t, db, sprintID, "aws", "t.langley",
		"A-agree", "anomaly.explain", "a scheduled batch job")

	pass, err := finops.Supervise(db, sprintID, nil)
	if err != nil {
		t.Fatal(err)
	}
	// anomaly.explain is not in the supervisor's own decides_alone list
	// (only anomaly.accept is), so two options that agree are carried, not
	// applied: this test's own point is that agreeing does not link them,
	// not that agreeing makes them the supervisor's to decide.
	if len(pass.Carried) != 2 {
		t.Fatalf("carried %d, want 2", len(pass.Carried))
	}
	if len(pass.Requests) != 2 {
		t.Fatalf("wrote %d decision requests, want 2: two independent deliverables with "+
			"two different owners, not one linked question", len(pass.Requests))
	}
}

// optSpec is one option of a plantDeliverable call.
type optSpec struct {
	class, summary, risk     string
	figureCents, savingCents int64
}

// plantDeliverable is a task, a POSTED artifact, and one or more options on
// it -- one deliverable's own alternatives, B3-SPEC.md section 2's shape,
// used by the tests below that need more than one option per artifact or a
// specific figure_cents.
func plantDeliverable(t *testing.T, db *sql.DB, sprintID int, desk, owner, anomalyID string, specs ...optSpec) (artifact int, ordinals []int) {
	t.Helper()
	tres, err := db.Exec(`INSERT INTO tasks
		(sprint, title, goal, assignee, desk, state, budget_cents, spent_cents, anomaly, created, updated, owner)
		VALUES (?, 'a task', 'a goal', ?, ?, 'active', 0, 0, ?, datetime('now'), datetime('now'), ?)`,
		sprintID, "investigator-"+desk, desk, nullableString(anomalyID), owner)
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
	for i, s := range specs {
		ordinal := i + 1
		if _, err := db.Exec(`INSERT INTO artifact_options
			(artifact, ordinal, class, summary, figure_cents, saving_cents, risk, needs, evidence, state)
			VALUES (?, ?, ?, ?, ?, ?, ?, 'nothing', '[]', 'open')`,
			artID, ordinal, s.class, s.summary, s.figureCents, s.savingCents, s.risk); err != nil {
			t.Fatal(err)
		}
		ordinals = append(ordinals, ordinal)
	}
	return int(artID), ordinals
}

// Red first (test a from the review): two options of the SAME deliverable
// naming different causes for the same anomaly are alternatives, never a
// contradiction with each other -- `@yurii 2026-09-02`, "давати на вибір
// якісь певні рішення" is offering a CHOICE, one deliverable's own. Both
// survive the pass as one carried choice on one task.
func TestOptionsWithinOneDeliverableNeverContradict(t *testing.T) {
	db, sprintID := superviseTestDB(t)
	plantAnomalyRow(t, db, "A-one-deliverable")
	artID, ords := plantDeliverable(t, db, sprintID, "aws", "y.mercer", "A-one-deliverable",
		optSpec{"anomaly.explain", "a scheduled batch job", "low", 10000, 0},
		optSpec{"anomaly.explain", "a runaway process, unrelated", "low", 10000, 0},
	)

	pass, err := finops.Supervise(db, sprintID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(pass.Applied) != 0 {
		t.Fatalf("applied %d, want 0", len(pass.Applied))
	}
	if len(pass.Carried) != 2 {
		t.Fatalf("carried %d, want 2: one deliverable's own two alternatives, "+
			"never dropped as a contradiction with each other", len(pass.Carried))
	}
	got1 := mustGetOption(t, db, artID, ords[0])
	got2 := mustGetOption(t, db, artID, ords[1])
	if got1.State != crew.OptionCarried || got2.State != crew.OptionCarried {
		t.Fatalf("states %q and %q, want both carried", got1.State, got2.State)
	}
	if len(pass.Requests) != 1 {
		t.Fatalf("wrote %d decision requests, want 1: one deliverable, one owner, one question",
			len(pass.Requests))
	}
	// The tell-tale of the contradiction check firing on this deliverable's
	// OWN two alternatives: a per-option note naming a rival that does not
	// exist here, because there is only one analyst and one deliverable.
	// (The body's own generic intro paragraph always mentions "two analysts
	// answered differently" as one of the reasons a request can exist at
	// all, so the check below matches the specific per-option note's own
	// wording -- "on anomaly" -- rather than that shared phrase.) Sensitive
	// to a mutant that ignores artifact identity even when the
	// applied/carried counts above are not.
	body := decisionRequestBodyFor(t, db, sprintID, "y.mercer")
	if strings.Contains(body, "answered differently on anomaly") {
		t.Errorf("the decision request reads this deliverable's own two alternatives "+
			"as a disagreement between two analysts:\n%s", body)
	}
}

func decisionRequestBodyFor(t *testing.T, db *sql.DB, sprintID int, owner string) string {
	t.Helper()
	artID, found, err := crew.DecisionRequestFor(db, sprintID, owner)
	if err != nil || !found {
		t.Fatalf("no decision request on file for %s: found=%v err=%v", owner, found, err)
	}
	taskID := mustTaskOf(t, db, artID)
	arts, err := crew.Artifacts(db, taskID)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range arts {
		if a.ID == artID {
			return a.Body
		}
	}
	t.Fatalf("decision request artifact %d not found on task %d", artID, taskID)
	return ""
}

// Red first (test b): a deliverable offering two SUPERVISOR-decidable
// alternatives gets exactly one applied -- the top-ranked by saving, then
// risk -- and the other marked not_chosen, never both.
func TestOnlyOneAlternativeOfOneDeliverableIsApplied(t *testing.T) {
	db, sprintID := superviseTestDB(t)
	var before int
	if err := db.QueryRow(`SELECT COUNT(*) FROM drivers`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	artID, ords := plantDeliverable(t, db, sprintID, "aws", "y.mercer", "",
		optSpec{"driver.one-time", "a one-off migration step", "low", 10000, 0},
		optSpec{"driver.recurring", "a weekly batch job", "low", 10000, 30000}, // higher saving: ranks first
	)

	pass, err := finops.Supervise(db, sprintID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(pass.Applied) != 1 {
		t.Fatalf("applied %d, want exactly 1", len(pass.Applied))
	}
	if pass.Applied[0].Class != "driver.recurring" {
		t.Errorf("applied %q, want driver.recurring (ranked first by saving)", pass.Applied[0].Class)
	}

	var after int
	if err := db.QueryRow(`SELECT COUNT(*) FROM drivers`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before+1 {
		t.Fatalf("drivers rows went from %d to %d, want exactly +1: applying BOTH "+
			"alternatives of one deliverable is exactly the fault this test exists to catch",
			before, after)
	}

	got1 := mustGetOption(t, db, artID, ords[0]) // driver.one-time
	got2 := mustGetOption(t, db, artID, ords[1]) // driver.recurring
	if got2.State != crew.OptionApplied {
		t.Errorf("driver.recurring (ords[1]) state %q, want applied", got2.State)
	}
	if got1.State != crew.OptionNotChosen {
		t.Errorf("driver.one-time (ords[0]) state %q, want not_chosen", got1.State)
	}
	if !strings.Contains(got1.Reason, "option 2") || !strings.Contains(got1.Reason, "driver.recurring") {
		t.Errorf("not_chosen reason %q does not name the option that was applied", got1.Reason)
	}
}

// Red first (test c): when the top-ranked option of a deliverable is one
// the supervisor hands up, the WHOLE choice is carried -- including a
// lower-ranked alternative the supervisor's own job description would
// otherwise decide alone -- because the question is the deliverable's
// choice, not one row of it.
func TestWhenTheTopRankedOptionIsHandsUpTheWholeChoiceIsCarried(t *testing.T) {
	db, sprintID := superviseTestDB(t)
	artID, ords := plantDeliverable(t, db, sprintID, "aws", "y.mercer", "",
		optSpec{"period.close", "close August", "low", 10000, 50000},       // hands up, ranks first
		optSpec{"driver.recurring", "a weekly batch job", "low", 10000, 0}, // supervisor's own class, ranks second
	)

	pass, err := finops.Supervise(db, sprintID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(pass.Applied) != 0 {
		t.Fatalf("applied %d, want 0: the top-ranked option is not the supervisor's to decide", len(pass.Applied))
	}
	if len(pass.Carried) != 2 {
		t.Fatalf("carried %d, want 2 (the whole choice, including the lower-ranked "+
			"supervisor-decidable option)", len(pass.Carried))
	}
	got1 := mustGetOption(t, db, artID, ords[0])
	got2 := mustGetOption(t, db, artID, ords[1])
	if got1.State != crew.OptionCarried || got2.State != crew.OptionCarried {
		t.Fatalf("states %q and %q, want both carried", got1.State, got2.State)
	}
}

// Red first (test e -- renumbered from the amended review's own list, which
// used "e" for this one): a supervisor-decidable option whose figure_cents
// exceeds T.anomaly is a key decision, carried even though the supervisor's
// own job description would otherwise decide its class alone.
func TestAnOptionAboveTAnomalyIsCarriedNotApplied(t *testing.T) {
	db, sprintID := superviseTestDB(t)
	thresh, ok := crew.ThresholdFor("T.anomaly")
	if !ok {
		t.Fatal("T.anomaly is missing from roles.yaml")
	}
	over := thresh.ValueCents + 1
	artID, ords := plantDeliverable(t, db, sprintID, "aws", "y.mercer", "",
		optSpec{"driver.recurring", "a very large recurring cost", "low", over, 0},
	)

	pass, err := finops.Supervise(db, sprintID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(pass.Applied) != 0 {
		t.Fatalf("applied %d, want 0: %d exceeds T.anomaly (%d)", len(pass.Applied), over, thresh.ValueCents)
	}
	if len(pass.Carried) != 1 {
		t.Fatalf("carried %d, want 1", len(pass.Carried))
	}
	got := mustGetOption(t, db, artID, ords[0])
	if got.State != crew.OptionCarried {
		t.Errorf("state %q, want carried", got.State)
	}
}

// Red first (test f): a real, seeded roster must never gate a cloud figure.
// The first version compared figure_cents against the WRITING ANALYST's own
// LLM-spend guard (a.Monthly, a few thousand cents against 184000): with a
// real roster loaded, that dropped nearly every option this practice would
// ever see. There is no guard check against the roster any more, at all.
func TestARealAnalystsGuardNeverBlocksACloudFigure(t *testing.T) {
	db, sprintID := superviseTestDB(t)
	if _, err := crew.SeedRoster(db, "installer"); err != nil {
		t.Fatal(err)
	}
	artID, ords := plantDeliverable(t, db, sprintID, "aws", "y.mercer", "",
		optSpec{"driver.recurring", "a scheduled batch job", "low", 184000, 0},
	)

	pass, err := finops.Supervise(db, sprintID, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := mustGetOption(t, db, artID, ords[0])
	if got.State != crew.OptionApplied {
		t.Fatalf("state %q, want applied: 184000 is under T.anomaly (500000) and "+
			"driver.recurring is the supervisor's own class -- a real analyst's "+
			"unrelated LLM-spend guard must never gate a cloud figure", got.State)
	}
	if len(pass.Applied) != 1 {
		t.Fatalf("applied %d, want 1", len(pass.Applied))
	}
}

// Red first: a decision request's lapse date names the supervisor's own
// deadline, and nothing enforces it (heraldyx's and agent-passport's own
// words for this event). A second pass that rewrites the same request --
// because a new option was carried to the same owner -- must not push that
// date out again, which is the false promise heraldyx once made
// ("eventually times out") and had to retract; and once today is past the
// original date, the rewritten body must say the request is stale rather
// than silently repeating a deadline that has already gone by.
func TestASecondPassKeepsTheFirstLapseDateAndMarksItStale(t *testing.T) {
	db, sprintID := superviseTestDB(t)
	plantPostedOption(t, db, sprintID, "aws", "t.langley",
		"period.close", "close August", 500000, "low")
	if _, err := finops.Supervise(db, sprintID, nil); err != nil {
		t.Fatal(err)
	}
	artID, found, err := crew.DecisionRequestFor(db, sprintID, "t.langley")
	if err != nil || !found {
		t.Fatalf("no decision request on file for t.langley: found=%v err=%v", found, err)
	}

	// As if real time had passed since the first pass: push the stored date
	// into the past. The property under test is that a SECOND pass does not
	// push it forward again to a new future date.
	const past = "2020-01-01"
	if _, err := db.Exec(`UPDATE decision_requests SET lapses=? WHERE artifact=?`, past, artID); err != nil {
		t.Fatal(err)
	}

	// A second, later pass: a new option carried to the same owner forces
	// the same request to be rewritten.
	plantPostedOption(t, db, sprintID, "aws", "t.langley",
		"budget.set", "raise the ml-platform budget", 200000, "medium")
	if _, err := finops.Supervise(db, sprintID, nil); err != nil {
		t.Fatal(err)
	}

	var gotLapses string
	if err := db.QueryRow(`SELECT lapses FROM decision_requests WHERE artifact=?`, artID).
		Scan(&gotLapses); err != nil {
		t.Fatal(err)
	}
	if gotLapses != past {
		t.Errorf("lapses moved from %q to %q on a second pass: the deadline must "+
			"never move once a request is first written", past, gotLapses)
	}

	body := decisionRequestBodyFor(t, db, sprintID, "t.langley")
	if !strings.Contains(body, "Unanswered since "+past) {
		t.Errorf("the rewritten body does not say this request is stale past its "+
			"own (unmoved) deadline:\n%s", body)
	}
	if strings.Contains(body, "Answer by") {
		t.Errorf("the rewritten body still reads as an open invitation past its own deadline:\n%s", body)
	}
}
