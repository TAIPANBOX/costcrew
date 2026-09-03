package finops_test

// C2-SPEC.md section 2's two Apply-side wirings: allocation.rule now calls
// finops.SetRule when its option carries a target, and period.close queues
// one showback-narration task per team for the desk's reporter. Both are red
// against unchanged code: allocation.rule is still "recorded only" (no
// target field even parses) and period.close only calls Close.

import (
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/finops"
)

// plantAllocationRuleOption is plantOption (apply_test.go) plus a target column,
// for allocation.rule's own structured target.
func plantAllocationRuleOption(t *testing.T, db *sql.DB, desk, class, summary string, target any) crew.Option {
	t.Helper()
	tres, err := db.Exec(`INSERT INTO tasks
		(title, goal, assignee, desk, state, budget_cents, spent_cents, created, updated)
		VALUES ('the close pack', 'write it', 'chargeback', ?, 'active', 0, 0,
		        datetime('now'), datetime('now'))`, desk)
	if err != nil {
		t.Fatal(err)
	}
	taskID, err := tres.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	ares, err := db.Exec(`INSERT INTO artifacts
		(task, author, title, body, state, created)
		VALUES (?, 'chargeback', 'the close pack', 'body', 'posted', datetime('now'))`, taskID)
	if err != nil {
		t.Fatal(err)
	}
	artID, err := ares.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	targetJSON, err := json.Marshal(target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO artifact_options
		(artifact, ordinal, class, summary, figure_cents, saving_cents, risk, needs, evidence, target, state)
		VALUES (?, 1, ?, ?, 50000, 0, 'low', 'nothing', '[]', ?, 'open')`,
		artID, class, summary, string(targetJSON)); err != nil {
		t.Fatal(err)
	}
	return crew.Option{Artifact: int(artID), Ordinal: 1, Class: class, Summary: summary,
		FigureCents: 50000, Target: json.RawMessage(targetJSON), State: crew.OptionOpen}
}

func TestApplyAllocationRuleWithATargetCallsSetRule(t *testing.T) {
	db := applyTestDB(t)
	rules, err := finops.Rules(db)
	if err != nil || len(rules) == 0 {
		t.Fatal(err)
	}
	var purchase finops.Rule
	for _, r := range rules {
		if r.Category == "Purchase" {
			purchase = r
		}
	}
	if purchase.ID == 0 {
		t.Fatal("no Purchase rule in the seeded defaults")
	}
	if purchase.Method == finops.Even {
		t.Fatal("the fixture already uses even-split; this test needs a different starting method")
	}

	opt := plantAllocationRuleOption(t, db, "management", "allocation.rule",
		"split Purchase evenly instead of by usage",
		map[string]any{"rule_id": purchase.ID, "method": "even-split", "share": 1.0})

	if err := finops.Apply(db, opt, "y.mercer", nil); err != nil {
		t.Fatal(err)
	}
	after, err := finops.Rules(db)
	if err != nil {
		t.Fatal(err)
	}
	var got finops.Rule
	for _, r := range after {
		if r.ID == purchase.ID {
			got = r
		}
	}
	if got.Method != finops.Even {
		t.Errorf("rule %d's method is %q after applying the target, want %q",
			purchase.ID, got.Method, finops.Even)
	}
	opts, err := crew.Options(db, opt.Artifact)
	if err != nil {
		t.Fatal(err)
	}
	if opts[0].State != crew.OptionApplied {
		t.Errorf("option state %q, want applied", opts[0].State)
	}
}

// A target-less allocation.rule option reaching Apply directly (bypassing
// crew.ValidateAndSaveOptions, the same way TestApplyAnUnwiredClassIsRecordedOnly
// already does for other unwired classes) still degrades to a no-op rather
// than a crash: the save-time gate is what actually stops this in the real
// save path, and this is the belt matching that braces.
func TestApplyAllocationRuleWithNoTargetIsANoOp(t *testing.T) {
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

// finops.SetRule already refuses a rule id it does not have; this is that
// refusal reached through Apply, for a target that was structurally fine
// enough to save (internal/crew's own
// TestAllocationRuleTargetIsAcceptedEvenWithAnUnknownRuleId) but names
// nothing real.
func TestApplyAllocationRuleWithAnUnknownRuleIDFails(t *testing.T) {
	db := applyTestDB(t)
	opt := plantAllocationRuleOption(t, db, "management", "allocation.rule",
		"split a rule that does not exist",
		map[string]any{"rule_id": 999999, "method": "even-split", "share": 1.0})

	if err := finops.Apply(db, opt, "y.mercer", nil); err == nil {
		t.Fatal("applying a target naming an unknown rule id succeeded")
	}
	got := mustGetOption(t, db, opt.Artifact, opt.Ordinal)
	if got.State == crew.OptionApplied {
		t.Error("the option was marked applied even though SetRule failed")
	}
}

// ------------------------------------------------------- period.close tasks

func TestApplyPeriodCloseQueuesOneReporterTaskPerTeam(t *testing.T) {
	db := applyTestDB(t)
	period, err := finops.OpenPeriod(db)
	if err != nil || period == "" {
		t.Fatalf("no open period to close: %v", err)
	}
	live, err := finops.Allocate(db, period)
	if err != nil {
		t.Fatal(err)
	}
	// A reporter analyst per desk that has one: reporter-aws, reporter-gcp,
	// reporter-azure, reporter-onprem (world.go). Desks like "ai" and "saas"
	// have none.
	mustExecArgs(t, db, crew.RosterSchema)
	for _, name := range []string{"reporter-aws", "reporter-gcp", "reporter-azure", "reporter-onprem"} {
		mustExecArgs(t, db, `INSERT INTO analysts (name, role, desk, state)
			VALUES (?, 'Reporter', substr(?, 10), 'active')`, name, name)
	}

	opt := plantOption(t, db, "management", "", "period.close", "close the books")
	if err := finops.Apply(db, opt, "y.mercer", nil); err != nil {
		t.Fatal(err)
	}

	wantTeams := map[string]bool{} // "source|team" for every desk with a reporter
	haveReporter := map[string]bool{"aws": true, "gcp": true, "azure": true, "onprem": true}
	for _, tc := range live.Teams {
		if haveReporter[tc.Source] {
			wantTeams[tc.Source+"|"+tc.Team] = true
		}
	}
	if len(wantTeams) == 0 {
		t.Fatal("the seeded estate has no team on a desk with a reporter; this test cannot see the property")
	}

	rows, err := db.Query(`SELECT COALESCE(assignee,''), COALESCE(desk,''), title
		FROM tasks WHERE assignee LIKE 'reporter-%'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	gotTeams := map[string]int{}
	for rows.Next() {
		var assignee, desk, title string
		if err := rows.Scan(&assignee, &desk, &title); err != nil {
			t.Fatal(err)
		}
		gotTeams[desk+"|"+assignee+"|"+title]++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(gotTeams) == 0 {
		t.Fatal("period.close was applied and no reporter task was queued at all")
	}
	for key, n := range gotTeams {
		if n != 1 {
			t.Errorf("task %q was queued %d times, want exactly 1", key, n)
		}
	}

	var total int
	if err := db.QueryRow(`SELECT COUNT(*) FROM tasks WHERE assignee LIKE 'reporter-%'`).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if total != len(wantTeams) {
		t.Errorf("%d reporter tasks queued, want exactly %d (one per team on a desk with a reporter)",
			total, len(wantTeams))
	}
}

// A team whose allocated share is exactly zero (it spent directly but no
// shared pot landed on it) still gets its showback task: a $0 team is still
// a team, and skipping it silently is exactly the kind of thing this
// console's own culture calls out ("left where it is and counted, rather
// than quietly spread onto teams that never touched it" -- Allocate's own
// comment, about the money; this is the same argument about the task).
func TestApplyPeriodCloseQueuesATaskForATeamWithZeroAllocatedShare(t *testing.T) {
	db := applyTestDB(t)
	period, err := finops.OpenPeriod(db)
	if err != nil || period == "" {
		t.Fatalf("no open period to close: %v", err)
	}
	mustExecArgs(t, db, crew.RosterSchema)
	mustExecArgs(t, db, `INSERT INTO analysts (name, role, desk, state) VALUES ('reporter-aws', 'Reporter', 'aws', 'active')`)
	// Every allocation rule set to Unallocated: no team gets any Allocated
	// share, so every team on aws with direct spend has Loaded() == Direct
	// and Allocated == 0.
	rules, err := finops.Rules(db)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rules {
		if err := finops.SetRule(db, r.ID, finops.Unallocated); err != nil {
			t.Fatal(err)
		}
	}
	live, err := finops.Allocate(db, period)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tc := range live.Teams {
		if tc.Source == "aws" && tc.Allocated == 0 && tc.Direct > 0 {
			found = true
		}
	}
	if !found {
		t.Fatal("setting every rule to unallocated did not produce a zero-allocated aws team; " +
			"this test cannot see the property")
	}

	opt := plantOption(t, db, "management", "", "period.close", "close the books")
	if err := finops.Apply(db, opt, "y.mercer", nil); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM tasks WHERE assignee='reporter-aws'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("no reporter-aws task was queued even though aws teams with zero allocated share exist")
	}
}

// A desk with no reporter analyst at all (ai, saas) is skipped rather than
// crashing or leaving an orphan, unassigned task nobody owns.
func TestApplyPeriodCloseSkipsADeskWithNoReporter(t *testing.T) {
	db := applyTestDB(t)
	period, err := finops.OpenPeriod(db)
	if err != nil || period == "" {
		t.Fatalf("no open period to close: %v", err)
	}
	day := period + "-09"
	mustExecArgs(t, db, `INSERT INTO charges (source, day, service, team, category, billed_cents)
		VALUES ('saas', ?, 'Vendor X', 'growth', 'Usage', 5000)`, day)
	mustExecArgs(t, db, crew.RosterSchema)
	mustExecArgs(t, db, `INSERT INTO analysts (name, role, desk, state) VALUES ('reporter-aws', 'Reporter', 'aws', 'active')`)

	before := taskCount(t, db)
	opt := plantOption(t, db, "management", "", "period.close", "close the books")
	if err := finops.Apply(db, opt, "y.mercer", nil); err != nil {
		t.Fatal(err)
	}
	var saasTasks int
	if err := db.QueryRow(`SELECT COUNT(*) FROM tasks WHERE desk='saas'`).Scan(&saasTasks); err != nil {
		t.Fatal(err)
	}
	if saasTasks != 0 {
		t.Errorf("%d task(s) queued on the saas desk, which has no reporter analyst", saasTasks)
	}
	after := taskCount(t, db)
	if after <= before {
		t.Error("no task at all was created by this close; the desks that DO have a reporter " +
			"should still have queued one")
	}
}

func taskCount(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM tasks`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
