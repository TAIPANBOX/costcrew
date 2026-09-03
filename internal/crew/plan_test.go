package crew_test

// B4-SPEC.md: crew.Propose takes the sprint form's goal and produces items
// from five sources rather than two, each routed by skill first, then desk,
// then headroom, priced from the analyst's own PerTask. Section 6's tests,
// in the order the testing rule names them: red first, boundaries, hostile
// input, and the four named mutants (gates-have-teeth.sh).
//
// Every test below filters crew.Propose's returned Items by a marker
// specific to the behaviour under test (a Why prefix, a Title prefix)
// rather than asserting the list's total length: a freshly seeded roster
// has thirty-nine analysts and none of them has ever posted a deliverable,
// so cadence-due work (source 3) alone fills the plan with items on every
// test that seeds the real roster. Asserting "exactly N items" against that
// would be asserting an accident of the fixture, not the behaviour named.

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/money"
	"github.com/TAIPANBOX/costcrew/internal/store"
)

// planDB is a fresh store carrying the schema every planning source reads:
// crew's own (tasks, artifacts, sprints, decision_requests) plus anomalies.
func planDB(t *testing.T) *sql.DB {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	db := st.DB()
	if _, err := db.Exec(crew.Schema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(anomaly.Schema); err != nil {
		t.Fatal(err)
	}
	// Propose always reads the roster, even when a test hires nobody or
	// hires by hand rather than seeding the full fixture: the table has to
	// exist for that read to return "empty" instead of "no such table".
	if _, err := db.Exec(crew.RosterSchema); err != nil {
		t.Fatal(err)
	}
	return db
}

// fullRoster is planDB seeded with the real 39-analyst fixture: what source
// 1's "more than one analyst on the desk" branch and the suspended/onboard
// carve-outs need, since only the real fixture has that shape.
func fullRoster(t *testing.T) *sql.DB {
	t.Helper()
	db := planDB(t)
	if _, err := crew.SeedRoster(db, "yurii"); err != nil {
		t.Fatal(err)
	}
	return db
}

// hire is a minimal, isolated analyst for a routing test: cadence
// "on-request" so it is never cadence-due and never crowds out the item a
// test is actually looking for.
func hire(t *testing.T, db *sql.DB, name, desk, engine string, skills []string, perTask, monthly money.Cents) {
	t.Helper()
	if err := crew.Hire(db, crew.Analyst{
		Name: name, Role: "test analyst", Desk: desk, Engine: engine,
		Skills: skills, Rights: []string{"figures-read"},
		PerTask: perTask, Monthly: monthly, Cadence: "on-request",
		Audience: "the desk", Owner: "yurii", Parent: "supervisor",
		Attestation: "none",
	}); err != nil {
		t.Fatal(err)
	}
}

func plantAnomaly(t *testing.T, db *sql.DB, id, source, service, day string, excessCents int64) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO anomalies
		(id, source, service, day, direction, amount_cents, baseline_cents,
		 excess_cents, z, rule_version, state, detected_at)
		VALUES (?,?,?,?, 'above', ?, 0, ?, 3.0, 'v1', 'open', ?)`,
		id, source, service, day, excessCents, excessCents, day); err != nil {
		t.Fatal(err)
	}
}

// spend gives name spent_cents inside a sprint whose start falls in month,
// which is what SpendInMonth (and so this file's headroom checks) reads.
func spend(t *testing.T, db *sql.DB, name, month string, cents int64) {
	t.Helper()
	res, err := db.Exec(`INSERT INTO sprints (label, start, finish, state, goal)
		VALUES (?, ?, ?, 'active', '')`, "spend-"+name+"-"+month, month+"-01", month+"-07")
	if err != nil {
		t.Fatal(err)
	}
	sid, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO tasks
		(sprint, title, goal, assignee, desk, state, budget_cents, spent_cents, created, updated)
		VALUES (?, 'spend', '', ?, 'aws', 'posted', 0, ?, datetime('now'), datetime('now'))`,
		sid, name, cents); err != nil {
		t.Fatal(err)
	}
}

func itemsWhy(items []crew.PlanItem, prefix string) []crew.PlanItem {
	var out []crew.PlanItem
	for _, it := range items {
		if strings.HasPrefix(it.Why, prefix) {
			out = append(out, it)
		}
	}
	return out
}

// ------------------------------------------------------------ red first (1)
// The red-first test named in the plan: a goal naming a skill produces an
// item for the matching analyst, and today produces none. "commitments" is
// the commitments analyst's own NAME, not a taxonomy skill string (its
// skills are commitment-modelling and waterline-tracking); section 3's own
// rule matches skill NAMES, so this test names the skill directly, which
// reaches the same analyst PLAN-2026-09.md's own red-first line asked for.
func TestGoalNamingASkillAddsAnItemForTheAnalystThatHoldsIt(t *testing.T) {
	db := fullRoster(t)
	p, err := crew.Propose(db, "2026-W99", "2026-09-08", "2026-09-14",
		"This sprint's goal names commitment-modelling for the crew.")
	if err != nil {
		t.Fatal(err)
	}
	got := itemsWhy(p.Items, "the sprint goal names commitment-modelling")
	if len(got) != 1 {
		t.Fatalf("goal items for commitment-modelling = %d, want 1 (%v)", len(got), got)
	}
	if got[0].Assignee != "commitments" {
		t.Errorf("assignee = %q, want commitments (the only active analyst with commitment-modelling)", got[0].Assignee)
	}
	if got[0].Goal != "This sprint's goal names commitment-modelling for the crew." {
		t.Errorf("PlanItem.Goal = %q, want the goal text verbatim", got[0].Goal)
	}
}

// Review of this PR: the plan's own red-first sentence (PLAN-2026-09.md,
// B4) is a goal naming "commitments" reaching the commitments analyst by
// that word alone, not the taxonomy's exact skill string. "commitments" is
// the analyst's own roster NAME (rule b), which this test exercises
// directly; the two tests after it exercise rule (b) with a desk word
// alongside it, and rule (c)'s singular/plural segment match.
func TestGoalNamingTheAnalystsOwnRosterNameRoutesToIt(t *testing.T) {
	db := fullRoster(t)
	p, err := crew.Propose(db, "2026-W99", "2026-09-08", "2026-09-14", "commitments")
	if err != nil {
		t.Fatal(err)
	}
	got := itemsWhy(p.Items, "the sprint goal names commitments")
	if len(got) != 1 {
		t.Fatalf("goal items for commitments = %d, want 1 (%v)", len(got), p.Items)
	}
	if got[0].Assignee != "commitments" {
		t.Errorf("assignee = %q, want commitments", got[0].Assignee)
	}
}

// A roster-name match routes straight to that analyst, on its own desk --
// a desk word elsewhere in the goal does not need to agree, and here it
// happens to (renewals is itself on the saas desk).
func TestGoalNamingARosterNameWithADeskWordStillRoutesByName(t *testing.T) {
	db := fullRoster(t)
	p, err := crew.Propose(db, "2026-W99", "2026-09-08", "2026-09-14", "renewals for the saas desk")
	if err != nil {
		t.Fatal(err)
	}
	got := itemsWhy(p.Items, "the sprint goal names renewals")
	if len(got) != 1 {
		t.Fatalf("goal items for renewals = %d, want 1 (%v)", len(got), p.Items)
	}
	if got[0].Assignee != "renewals" {
		t.Errorf("assignee = %q, want renewals", got[0].Assignee)
	}
	if got[0].Desk != "saas" {
		t.Errorf("desk = %q, want saas (renewals's own desk)", got[0].Desk)
	}
}

// Rule (c): the singular "commitment" is not a roster name and not the
// exact skill token, but it is commitment-modelling's own first segment.
func TestGoalNamingTheSingularSegmentReachesTheSkill(t *testing.T) {
	db := fullRoster(t)
	p, err := crew.Propose(db, "2026-W99", "2026-09-08", "2026-09-14", "commitment review this sprint")
	if err != nil {
		t.Fatal(err)
	}
	got := itemsWhy(p.Items, "the sprint goal names commitment-modelling")
	if len(got) != 1 {
		t.Fatalf("goal items for commitment-modelling = %d, want 1 (%v)", len(got), p.Items)
	}
	if got[0].Assignee != "commitments" {
		t.Errorf("assignee = %q, want commitments", got[0].Assignee)
	}
}

// A segment more than one skill shares (renewal-calendar,
// renewal-negotiation-prep) is left unresolved by rule (c) rather than
// guessed at: the singular "renewal" alone (no roster name matches it;
// "renewals" the analyst is plural) must add nothing.
func TestAnAmbiguousSegmentIsNotGuessedAt(t *testing.T) {
	db := fullRoster(t)
	p, err := crew.Propose(db, "2026-W99", "2026-09-08", "2026-09-14", "renewal")
	if err != nil {
		t.Fatal(err)
	}
	got := itemsWhy(p.Items, "the sprint goal names")
	if len(got) != 0 {
		t.Errorf("goal items for the ambiguous \"renewal\" = %d, want 0 (%v)", len(got), got)
	}
	if !p.GoalUnmatched {
		t.Error("an ambiguous, otherwise-unmatched goal did not set GoalUnmatched")
	}
}

// ------------------------------------------------------------ red first (2)
func TestAWeeklyAnalystDueByCadenceGetsOneItem(t *testing.T) {
	db := planDB(t)
	hire(t, db, "weekly-writer", "aws", "openrouter", []string{"variance-commentary"}, money.Cents(1000), money.Cents(10000))
	if _, err := db.Exec(`UPDATE analysts SET cadence='weekly' WHERE name='weekly-writer'`); err != nil {
		t.Fatal(err)
	}
	// Posted eight days before the sprint's start: over the seven-day
	// cadence, so due.
	tres, err := db.Exec(`INSERT INTO tasks (title, goal, assignee, desk, state, budget_cents, spent_cents, created, updated)
		VALUES ('t', 'g', 'weekly-writer', 'aws', 'posted', 0, 0, datetime('now'), datetime('now'))`)
	if err != nil {
		t.Fatal(err)
	}
	tid, _ := tres.LastInsertId()
	if _, err := db.Exec(`INSERT INTO artifacts (task, author, title, body, state, created, stamped, stamper)
		VALUES (?, 'weekly-writer', 'a', 'b', 'posted', '2026-08-31T00:00:00Z', '2026-08-31T00:00:00Z', 'owner')`, tid); err != nil {
		t.Fatal(err)
	}

	p, err := crew.Propose(db, "2026-W99", "2026-09-08", "2026-09-14", "")
	if err != nil {
		t.Fatal(err)
	}
	got := itemsWhy(p.Items, "weekly cadence")
	if len(got) != 1 {
		t.Fatalf("weekly-cadence items = %d, want 1 (%v)", len(got), p.Items)
	}
	if got[0].Assignee != "weekly-writer" {
		t.Errorf("assignee = %q, want weekly-writer", got[0].Assignee)
	}
	if !strings.Contains(got[0].Why, "2026-08-31") {
		t.Errorf("Why = %q, does not name the date of the last post", got[0].Why)
	}
}

// ------------------------------------------------------------ red first (3)
func TestAReturnedArtifactProducesAReworkItemWithTheReasonVerbatim(t *testing.T) {
	db := planDB(t)
	hire(t, db, "reworked", "aws", "openrouter", []string{"variance-commentary"}, money.Cents(1000), money.Cents(10000))
	tres, err := db.Exec(`INSERT INTO tasks (title, goal, assignee, desk, state, budget_cents, spent_cents, created, updated, reason)
		VALUES ('a deliverable task', 'g', 'reworked', 'aws', 'returned', 0, 0, datetime('now'), datetime('now'), 'the figure did not tie to the invoice')`)
	if err != nil {
		t.Fatal(err)
	}
	tid, _ := tres.LastInsertId()
	if _, err := db.Exec(`INSERT INTO artifacts (task, author, title, body, state, created, reason)
		VALUES (?, 'reworked', 'the deliverable', 'body', 'returned', datetime('now'), 'the figure did not tie to the invoice')`, tid); err != nil {
		t.Fatal(err)
	}

	p, err := crew.Propose(db, "2026-W99", "2026-09-08", "2026-09-14", "")
	if err != nil {
		t.Fatal(err)
	}
	got := itemsWhy(p.Items, "artifact ")
	if len(got) != 1 {
		t.Fatalf("rework items = %d, want 1 (%v)", len(got), p.Items)
	}
	it := got[0]
	if it.Title != "Rework: a deliverable task" {
		t.Errorf("title = %q, want %q", it.Title, "Rework: a deliverable task")
	}
	if it.Assignee != "reworked" {
		t.Errorf("assignee = %q, want reworked (the same assignee)", it.Assignee)
	}
	if it.Goal != "the figure did not tie to the invoice" {
		t.Errorf("goal = %q, want the return reason verbatim", it.Goal)
	}
	if !strings.Contains(it.Why, "the deliverable") {
		t.Errorf("Why = %q, does not name the artifact", it.Why)
	}
}

// ------------------------------------------------------------ red first (4)
func TestADecisionRequestWithCarriedOptionsProducesOneSupervisorItem(t *testing.T) {
	db := planDB(t)
	sres, err := db.Exec(`INSERT INTO sprints (label, start, finish, state, goal) VALUES ('2026-W90', '2026-08-01', '2026-08-07', 'closed', '')`)
	if err != nil {
		t.Fatal(err)
	}
	sid, _ := sres.LastInsertId()
	tres, err := db.Exec(`INSERT INTO tasks (sprint, title, goal, assignee, desk, state, budget_cents, spent_cents, created, updated, owner)
		VALUES (?, 't', 'g', 'investigator-aws', 'aws', 'posted', 0, 0, datetime('now'), datetime('now'), 'y.mercer')`, sid)
	if err != nil {
		t.Fatal(err)
	}
	tid, _ := tres.LastInsertId()
	ares, err := db.Exec(`INSERT INTO artifacts (task, author, title, body, state, created)
		VALUES (?, 'investigator-aws', 'a', 'b', 'posted', datetime('now'))`, tid)
	if err != nil {
		t.Fatal(err)
	}
	aid, _ := ares.LastInsertId()
	for i := 1; i <= 2; i++ {
		if _, err := db.Exec(`INSERT INTO artifact_options
			(artifact, ordinal, class, summary, figure_cents, saving_cents, risk, needs, evidence, state)
			VALUES (?, ?, 'period.close', 's', 100, 0, 'low', 'n', '[]', 'carried')`, aid, i); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := crew.WriteDecisionRequest(db, int(sid), "y.mercer", "body", "2026-08-14"); err != nil {
		t.Fatal(err)
	}

	p, err := crew.Propose(db, "2026-W99", "2026-09-08", "2026-09-14", "")
	if err != nil {
		t.Fatal(err)
	}
	got := itemsWhy(p.Items, "from 2026-W90")
	if len(got) != 1 {
		t.Fatalf("decision-request items = %d, want 1 (%v)", len(got), p.Items)
	}
	if got[0].Assignee != "supervisor" || got[0].Desk != "management" || got[0].Budget != 0 {
		t.Errorf("item = %+v, want supervisor/management/0", got[0])
	}
	if got[0].Title != "Decision request for y.mercer: 2 option(s) waiting" {
		t.Errorf("title = %q", got[0].Title)
	}
}

// ------------------------------------------------------------ red first (5)
// Also the catcher for gates-have-teeth.sh's "skip the headroom check"
// mutant: dropping chooseAnalyst's headroom filter makes triage-aws (the
// analyst this test spends through its guard) get chosen anyway.
func TestAnAnalystWithNoHeadroomIsSkippedAndTheItemSaysSo(t *testing.T) {
	db := fullRoster(t)
	plantAnomaly(t, db, "E900", "aws", "Amazon EC2", "2026-09-05", 50_000)
	// triage-aws's own monthly guard is 120.00; spend all of it in the
	// sprint's month before asking for a plan.
	spend(t, db, "triage-aws", "2026-09", 120_00)

	p, err := crew.Propose(db, "2026-W99", "2026-09-08", "2026-09-14", "")
	if err != nil {
		t.Fatal(err)
	}
	got := itemsWhy(p.Items, "anomaly E900")
	if len(got) != 1 {
		t.Fatalf("items for E900 = %d, want 1 (%v)", len(got), p.Items)
	}
	it := got[0]
	if it.Assignee != "investigator-aws" {
		t.Errorf("assignee = %q, want investigator-aws (triage-aws has no headroom left)", it.Assignee)
	}
	if !strings.Contains(it.Why, "triage-aws skipped: no headroom this month") {
		t.Errorf("Why = %q, does not say triage-aws was skipped for headroom", it.Why)
	}
	// investigator-aws's own PerTask happens to equal the old 15.00 literal
	// too; TestRoutedItemsArePricedByThePerTaskGuardNotALiteral is the test
	// that picks a desk where the two numbers differ.
	if it.Budget != money.Cents(15_00) {
		t.Errorf("budget = %s, want investigator-aws's own PerTask (15.00)", it.Budget)
	}
}

// -------------------------------------------------------------- boundary
func TestAnOnRequestRoleIsNeverDueOnItsOwn(t *testing.T) {
	db := planDB(t)
	hire(t, db, "on-request-analyst", "aws", "openrouter", []string{"variance-commentary"}, money.Cents(1000), money.Cents(10000))
	// hire() already sets cadence on-request and this analyst has never
	// posted, which would make every OTHER cadence due immediately.

	p, err := crew.Propose(db, "2026-W99", "2026-09-08", "2026-09-14", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range p.Items {
		if it.Assignee == "on-request-analyst" {
			t.Errorf("on-request-analyst got a cadence item: %+v", it)
		}
	}
}

// Also the catcher for gates-have-teeth.sh's "count a daily cadence per
// day" mutant: a version that adds one item per day since the last post
// would produce more than one item for an analyst several days overdue.
func TestADailyCadenceProducesOneItemNotOnePerDay(t *testing.T) {
	db := fullRoster(t)
	// triage-aws is daily and, on a bare fullRoster fixture, has never
	// posted -- ten sprints' worth of "overdue" by any per-day counting.
	p, err := crew.Propose(db, "2026-W99", "2026-09-08", "2026-09-14", "")
	if err != nil {
		t.Fatal(err)
	}
	got := itemsWhy(p.Items, "daily cadence")
	n := 0
	for _, it := range got {
		if it.Assignee == "triage-aws" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("triage-aws got %d daily-cadence items, want exactly 1", n)
	}
}

func TestAnEmptyGoalAddsNothingFromTheGoalSource(t *testing.T) {
	db := fullRoster(t)
	p, err := crew.Propose(db, "2026-W99", "2026-09-08", "2026-09-14", "")
	if err != nil {
		t.Fatal(err)
	}
	got := itemsWhy(p.Items, "the sprint goal names")
	if len(got) != 0 {
		t.Errorf("an empty goal produced %d goal items, want 0 (%v)", len(got), got)
	}
	if p.GoalUnmatched {
		t.Error("an empty goal set GoalUnmatched; it should say nothing, not that nothing matched")
	}
}

func TestAGoalMatchingTwoSkillsAddsTwoItems(t *testing.T) {
	db := fullRoster(t)
	// Not "...this sprint": rule (c) (added on review) reaches
	// sprint-planning through the word "sprint" itself, which would add a
	// third, unintended item and defeat the point of this test.
	p, err := crew.Propose(db, "2026-W99", "2026-09-08", "2026-09-14",
		"commitment-modelling and variance-commentary this week")
	if err != nil {
		t.Fatal(err)
	}
	got := itemsWhy(p.Items, "the sprint goal names")
	if len(got) != 2 {
		t.Fatalf("goal items = %d, want 2 (%v)", len(got), got)
	}
}

// Also the catcher for gates-have-teeth.sh's "route by desk before skill"
// mutant, together with the routing test below: a version that drops the
// skill filter and keeps only desk would sometimes choose optimizer-aws or
// reporter-aws (both on the aws desk, neither holding anomaly-triage) over
// the two real candidates.
func TestASuspendedAnalystIsNeverChosenByRouting(t *testing.T) {
	db := fullRoster(t)
	// migration-tracking exists only on migration-watch, which is Suspended.
	p, err := crew.Propose(db, "2026-W99", "2026-09-08", "2026-09-14",
		"a migration-tracking review this week")
	if err != nil {
		t.Fatal(err)
	}
	got := itemsWhy(p.Items, "the sprint goal names migration-tracking")
	if len(got) != 1 {
		t.Fatalf("goal items for migration-tracking = %d, want 1 (%v)", len(got), got)
	}
	if got[0].Assignee == "migration-watch" {
		t.Error("the suspended migration-watch was chosen; it must never be routed to")
	}
	if got[0].Assignee != "supervisor" {
		t.Errorf("assignee = %q, want supervisor (nobody ACTIVE holds migration-tracking)", got[0].Assignee)
	}
}

// -------------------------------------------------------------- hostile input
func TestAOneMegabyteGoalDoesNotBreakMatching(t *testing.T) {
	db := fullRoster(t)
	huge := strings.Repeat("filler word ", 90000) + "commitment-modelling"
	if len(huge) < 1_000_000 {
		t.Fatalf("test fixture is only %d bytes, want at least 1MB", len(huge))
	}
	p, err := crew.Propose(db, "2026-W99", "2026-09-08", "2026-09-14", huge)
	if err != nil {
		t.Fatal(err)
	}
	got := itemsWhy(p.Items, "the sprint goal names commitment-modelling")
	if len(got) != 1 {
		t.Fatalf("goal items for commitment-modelling in a 1MB goal = %d, want 1", len(got))
	}
	if len([]rune(got[0].Title)) > 120 {
		t.Errorf("title is %d runes, want at most 120", len([]rune(got[0].Title)))
	}
}

func TestAGoalOfOnlyPunctuationMatchesNothing(t *testing.T) {
	db := fullRoster(t)
	p, err := crew.Propose(db, "2026-W99", "2026-09-08", "2026-09-14", "!!! ... ??? ,,, ---")
	if err != nil {
		t.Fatal(err)
	}
	got := itemsWhy(p.Items, "the sprint goal names")
	if len(got) != 0 {
		t.Errorf("a punctuation-only goal produced %d items, want 0", len(got))
	}
	if !p.GoalUnmatched {
		t.Error("a punctuation-only goal did not set GoalUnmatched")
	}
}

func TestAGoalNamingASkillOnNoActiveAnalystGoesToTheSupervisor(t *testing.T) {
	db := fullRoster(t)
	p, err := crew.Propose(db, "2026-W99", "2026-09-08", "2026-09-14", "intake-reading this sprint")
	if err != nil {
		t.Fatal(err)
	}
	// intake-reading exists only on intake-triage, which is on Probation
	// (not Active), so this is "nobody active holds it", not "matches no
	// skill at all": GoalUnmatched must stay false.
	if p.GoalUnmatched {
		t.Error("intake-reading is a real skill in the taxonomy; GoalUnmatched must not be set")
	}
	got := itemsWhy(p.Items, "the sprint goal names intake-reading")
	if len(got) != 1 {
		t.Fatalf("goal items for intake-reading = %d, want 1 (%v)", len(got), got)
	}
	if got[0].Assignee != "supervisor" {
		t.Errorf("assignee = %q, want supervisor", got[0].Assignee)
	}
}

// -------------------------------------------------------------- mutant: skill before desk
//
// Hand-hired rather than the seeded fixture: on the real aws desk, the
// highest-Monthly analyst (triage-aws, 120.00) already happens to hold
// anomaly-triage, so a "desk only, skill ignored" mutant picks a name this
// test would have accepted anyway and the fixture cannot tell the two
// apart. Here the outsider's headroom is deliberately the largest on the
// desk, so dropping the skill filter has a definite, wrong answer to fall
// into.
func TestRoutingPicksTheSkillHolderNotJustAnyoneOnTheDesk(t *testing.T) {
	db := planDB(t)
	hire(t, db, "skilled-a", "testdesk", "openrouter", []string{"anomaly-triage"}, money.Cents(1000), money.Cents(5000))
	hire(t, db, "skilled-b", "testdesk", "openrouter", []string{"anomaly-triage"}, money.Cents(1000), money.Cents(6000))
	hire(t, db, "unskilled-outsider", "testdesk", "openrouter", []string{"variance-commentary"}, money.Cents(1000), money.Cents(99900))
	plantAnomaly(t, db, "E901", "testdesk", "a service", "2026-09-05", 40_000)

	p, err := crew.Propose(db, "2026-W99", "2026-09-08", "2026-09-14", "")
	if err != nil {
		t.Fatal(err)
	}
	got := itemsWhy(p.Items, "anomaly E901")
	if len(got) != 1 {
		t.Fatalf("items for E901 = %d, want 1", len(got))
	}
	switch got[0].Assignee {
	case "skilled-a", "skilled-b":
		// both hold anomaly-triage on testdesk
	default:
		t.Errorf("assignee = %q, want skilled-a or skilled-b (unskilled-outsider has no anomaly-triage skill but the largest headroom on the desk)", got[0].Assignee)
	}
}

// -------------------------------------------------------------- mutant: price by PerTask
func TestRoutedItemsArePricedByThePerTaskGuardNotALiteral(t *testing.T) {
	db := fullRoster(t)
	plantAnomaly(t, db, "E902", "gcp", "GKE", "2026-09-05", 40_000)

	p, err := crew.Propose(db, "2026-W99", "2026-09-08", "2026-09-14", "")
	if err != nil {
		t.Fatal(err)
	}
	got := itemsWhy(p.Items, "anomaly E902")
	if len(got) != 1 {
		t.Fatalf("items for E902 = %d, want 1", len(got))
	}
	if got[0].Budget == money.Cents(15_00) {
		t.Error("budget is the old 15.00 literal; the >1-candidate branch must price by PerTask")
	}
	want := map[string]money.Cents{"investigator-gcp": 1500, "triage-gcp": 1200}[got[0].Assignee]
	if got[0].Budget != want {
		t.Errorf("budget = %s, want %s (%s's own PerTask)", got[0].Budget, want, got[0].Assignee)
	}
}

// -------------------------------------------------------------- unchanged (source 1, <=1 candidate)
// The ROUTING for a single-analyst desk is unchanged: still the desk's
// named triage analyst, regardless of headroom or skill. The PRICE is not
// (see TestASingleCandidateDeskPricesFromThatAnalystsOwnPerTaskWhenItExists
// below, added on review): saas-manager is on the roster, so this prices
// from its own 14.00 PerTask rather than the old 15.00 literal.
func TestUnownedAnomaliesOnASingleAnalystDeskKeepsTheOldRouting(t *testing.T) {
	db := fullRoster(t)
	plantAnomaly(t, db, "E903", "saas", "Zendesk", "2026-09-05", 10_000)

	p, err := crew.Propose(db, "2026-W99", "2026-09-08", "2026-09-14", "")
	if err != nil {
		t.Fatal(err)
	}
	got := itemsWhy(p.Items, "anomaly E903")
	if len(got) != 1 {
		t.Fatalf("items for E903 = %d, want 1", len(got))
	}
	if got[0].Assignee != "saas-manager" {
		t.Errorf("assignee = %q, want saas-manager (the desk's own triage pick, unchanged)", got[0].Assignee)
	}
	if got[0].Budget != money.Cents(14_00) {
		t.Errorf("budget = %s, want 14.00 (saas-manager's own PerTask, not the old 15.00 literal)", got[0].Budget)
	}
}

// Review of this PR: a single-candidate desk priced every item at the old
// 15.00 literal even when the desk's named analyst is on the roster with
// its own guard, so two desks priced the same shape of work differently
// for no reason. Red first: this failed with budget 15.00 before the fix.
func TestASingleCandidateDeskPricesFromThatAnalystsOwnPerTaskWhenItExists(t *testing.T) {
	db := planDB(t)
	hire(t, db, "investigator-onprem", "onprem", "openrouter", []string{"anomaly-triage"}, money.Cents(1800), money.Cents(5000))
	plantAnomaly(t, db, "E904", "onprem", "Storage array", "2026-09-05", 20_000)

	p, err := crew.Propose(db, "2026-W99", "2026-09-08", "2026-09-14", "")
	if err != nil {
		t.Fatal(err)
	}
	got := itemsWhy(p.Items, "anomaly E904")
	if len(got) != 1 {
		t.Fatalf("items for E904 = %d, want 1", len(got))
	}
	if got[0].Assignee != "investigator-onprem" {
		t.Fatalf("assignee = %q, want investigator-onprem (triageDesk's unchanged pick)", got[0].Assignee)
	}
	if got[0].Budget != money.Cents(1800) {
		t.Errorf("budget = %s, want 18.00 (investigator-onprem's own PerTask), not the old 15.00 literal", got[0].Budget)
	}
}

// And the literal survives when nobody of that name exists on the roster
// at all: an empty roster has no "triage-aws" to price from.
func TestASingleCandidateDeskKeepsTheLiteralWhenNobodyOfThatNameExists(t *testing.T) {
	db := planDB(t) // nobody hired
	plantAnomaly(t, db, "E905", "aws", "Amazon EC2", "2026-09-05", 20_000)

	p, err := crew.Propose(db, "2026-W99", "2026-09-08", "2026-09-14", "")
	if err != nil {
		t.Fatal(err)
	}
	got := itemsWhy(p.Items, "anomaly E905")
	if len(got) != 1 {
		t.Fatalf("items for E905 = %d, want 1", len(got))
	}
	if got[0].Assignee != "triage-aws" || got[0].Budget != money.Cents(15_00) {
		t.Errorf("item = %+v, want triage-aws at 15.00 (nobody of that name is on this empty roster)", got[0])
	}
}

// -------------------------------------------------------------- unchanged (source 2)
func TestBlockedTasksStillProduceUnblockItems(t *testing.T) {
	db := planDB(t)
	if _, err := db.Exec(`INSERT INTO tasks (title, goal, assignee, desk, state, reason, budget_cents, spent_cents, created, updated)
		VALUES ('stuck', 'g', 'investigator-aws', 'aws', 'blocked', 'waiting on a driver', 0, 0, datetime('now'), datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	p, err := crew.Propose(db, "2026-W99", "2026-09-08", "2026-09-14", "")
	if err != nil {
		t.Fatal(err)
	}
	got := itemsWhy(p.Items, "task ")
	if len(got) != 1 || got[0].Title != "Unblock: stuck" || got[0].Budget != money.Cents(10_00) {
		t.Errorf("blocked-task items = %v, want one Unblock item at 10.00", got)
	}
}

// -------------------------------------------------------------- engine by class
// Names and exercises engineByClass directly: two otherwise-tied analysts
// (same headroom), one on the engine section 4 prefers for this class, one
// not; the preferred one must win.
func TestEngineByClassPrefersTheMatchingRouteOnATie(t *testing.T) {
	db := planDB(t)
	hire(t, db, "cheap-writer", "aws", "openrouter", []string{"variance-commentary"}, money.Cents(2000), money.Cents(10000))
	hire(t, db, "strong-writer", "aws", "anthropic", []string{"variance-commentary"}, money.Cents(2000), money.Cents(10000))

	p, err := crew.Propose(db, "2026-W99", "2026-09-08", "2026-09-14", "variance-commentary this week")
	if err != nil {
		t.Fatal(err)
	}
	got := itemsWhy(p.Items, "the sprint goal names variance-commentary")
	if len(got) != 1 {
		t.Fatalf("goal items = %d, want 1", len(got))
	}
	// variance-commentary prefers the cheap engine (engineByClass).
	if got[0].Assignee != "cheap-writer" {
		t.Errorf("assignee = %q, want cheap-writer (engineByClass prefers the cheap route for variance-commentary)", got[0].Assignee)
	}
}

// Review of this PR: chooseAnalyst sorted by headroom FIRST and applied
// the engine preference only on an exact headroom tie, which real cents
// almost never produce, making engineByClass nearly dead code. Section 4's
// own order is headroom-as-a-FILTER (drop anyone with none), then the
// preferred engine, then the most headroom, then the name: here the
// strong-engine candidate has LESS headroom than the cheap one and must
// still win, because decision-framing prefers the strong route. Red first:
// this chose more-headroom-cheap-writer before the fix.
func TestEngineByClassWinsOverMoreHeadroomWhenBothHaveSome(t *testing.T) {
	db := planDB(t)
	hire(t, db, "more-headroom-cheap", "aws", "openrouter", []string{"decision-framing"}, money.Cents(2000), money.Cents(10000))
	hire(t, db, "less-headroom-strong", "aws", "anthropic", []string{"decision-framing"}, money.Cents(2000), money.Cents(6000))
	// Spend both down, unevenly, so neither has its full monthly guard and
	// the strong-engine one ends up with LESS headroom than the cheap one.
	spend(t, db, "more-headroom-cheap", "2026-09", 1000)  // headroom 90.00
	spend(t, db, "less-headroom-strong", "2026-09", 4000) // headroom 20.00

	p, err := crew.Propose(db, "2026-W99", "2026-09-08", "2026-09-14", "decision-framing this sprint")
	if err != nil {
		t.Fatal(err)
	}
	got := itemsWhy(p.Items, "the sprint goal names decision-framing")
	if len(got) != 1 {
		t.Fatalf("goal items = %d, want 1", len(got))
	}
	if got[0].Assignee != "less-headroom-strong" {
		t.Errorf("assignee = %q, want less-headroom-strong (decision-framing prefers the strong engine, checked before headroom)", got[0].Assignee)
	}
}
