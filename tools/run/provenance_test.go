package main

import (
	"database/sql"
	"fmt"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/store"
)

// What the runner writes must say a model wrote it.
//
// Red first: with the INSERT naming no source, this read back "fixture", which
// is what 63 real deliverables looked like after the first full run. The column
// defaults to fixture deliberately, so nothing but the writer naming itself can
// turn this green.
func TestARunnerDeliverableIsMarkedLive(t *testing.T) {
	db, task, analyst := runnerDB(t)

	e := estimate{Task: task, Analyst: analyst}
	res := callResult{Text: "the deliverable", InTokens: 100, OutTokens: 200,
		ActualMicros: 12_345}
	if err := saveDraft(db, e, res, bus{}); err != nil {
		t.Fatal(err)
	}

	as, err := crew.Artifacts(db, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(as) != 1 {
		t.Fatalf("wrote %d drafts, want 1", len(as))
	}
	if as[0].Source != "live" {
		t.Errorf("a deliverable a model wrote reads %q: it is indistinguishable "+
			"from the 279 generated ones in the same table", as[0].Source)
	}
	if as[0].State != crew.Draft {
		t.Errorf("state %q: a live deliverable must be a draft, never a post",
			as[0].State)
	}
}

// A fraction of a cent still cost something.
//
// One call, one task, and the run's total is a fifth of a cent. Rounded to
// nothing it would read as free, which is how a bill grows out of a column of
// zeroes. The run's total rounds UP, so it reads as a cent.
func TestASubCentCallStillLandsOnTheTask(t *testing.T) {
	db, tasks, analyst := runnerTasks(t, 1)
	before := spentOn(t, db, tasks[0].ID)

	if err := saveDraft(db, estimate{Task: tasks[0], Analyst: analyst},
		callResult{Text: "x", ActualMicros: 1_234}, bus{}); err != nil {
		t.Fatal(err)
	}
	if _, err := crew.SettleLiveSpend(db); err != nil {
		t.Fatal(err)
	}

	if after := spentOn(t, db, tasks[0].ID); after <= before {
		t.Errorf("spend went %d -> %d: a call that cost a fraction of a cent "+
			"rounded to nothing, which is how a bill grows out of zeroes",
			before, after)
	}
}

// Many small calls must add up to what they cost, ACROSS TASKS.
//
// This is the shape the runner actually has: one call per task, 44 of them.
//
// Red first, twice. Rounding each call up on its own recorded 0.44 for 0.2336.
// Then rounding each TASK up recorded 0.44 as well, and the first version of
// this test missed it entirely because it put all 44 calls on ONE task, where
// per-task rounding happens to be right. @measured on a live run afterwards:
// the router billed 0.2319 and the console still said 0.56.
//
// A test can only prove what it describes.
func TestTheLedgerDoesNotOverstateManySmallCalls(t *testing.T) {
	const n, each = 44, 5_310 // 0.531 of a cent per call
	db, tasks, analyst := runnerTasks(t, n)

	for _, task := range tasks {
		if err := saveDraft(db, estimate{Task: task, Analyst: analyst},
			callResult{Text: "x", ActualMicros: each}, bus{}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := crew.SettleLiveSpend(db); err != nil {
		t.Fatal(err)
	}

	trueMicros := int64(n * each)
	want := (trueMicros + 9_999) / 10_000 // 24 cents
	var got int64
	for _, task := range tasks {
		got += spentOn(t, db, task.ID)
	}
	if got != want {
		t.Errorf("%d calls on %d tasks costing %.4f USD were recorded as %.2f USD, "+
			"want %.2f: a fifth of a cent rounded up on each one overstates the "+
			"run, on the page whose heading is what the crew cost",
			n, n, float64(trueMicros)/1e6, float64(got)/100, float64(want)/100)
	}
}

// Settling twice must not book the money twice.
func TestSettlingTheSameRunTwiceChangesNothing(t *testing.T) {
	db, tasks, analyst := runnerTasks(t, 7)
	for _, task := range tasks {
		if err := saveDraft(db, estimate{Task: task, Analyst: analyst},
			callResult{Text: "x", ActualMicros: 7_777}, bus{}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := crew.SettleLiveSpend(db)
	if err != nil {
		t.Fatal(err)
	}
	var afterFirst int64
	for _, task := range tasks {
		afterFirst += spentOn(t, db, task.ID)
	}
	second, err := crew.SettleLiveSpend(db)
	if err != nil {
		t.Fatal(err)
	}
	var afterSecond int64
	for _, task := range tasks {
		afterSecond += spentOn(t, db, task.ID)
	}
	if afterFirst != afterSecond || first != second {
		t.Errorf("settling twice moved the board from %d to %d cents "+
			"(%v then %v): every startup step in this console runs on every "+
			"start, so this one has to be safe to repeat", afterFirst,
			afterSecond, first, second)
	}
}

// A task somebody stopped stays stopped.
//
// Red first: with the runner taking every open task, 19 blocked ones were
// worked anyway, and the page then showed
//
//	blocked: Tagging feed from the azure desk has been stale since the 9th;
//	         the numbers would be wrong.
//
// directly above a finished deliverable written from those numbers.
func TestABlockedTaskIsNotWorkedAround(t *testing.T) {
	in := []crew.Task{
		{ID: 1, State: "queued"},
		{ID: 2, State: "blocked", Reason: "the feed is stale; the numbers would be wrong"},
		{ID: 3, State: "active"},
		{ID: 4, State: "returned"},
		{ID: 5, State: "blocked", Reason: "the engine did not answer"},
	}
	got := workable(in)

	for _, task := range got {
		if task.State == "blocked" {
			t.Errorf("task %d is blocked (%q) and was picked up anyway: a "+
				"deliverable written past a reason a person recorded is worse "+
				"than no deliverable", task.ID, task.Reason)
		}
	}
	if len(got) != 3 {
		t.Errorf("took %d of 5, want 3: queued, active and returned are work "+
			"waiting to be done and must not be dropped with the blocked ones",
			len(got))
	}
}

func runnerDB(t *testing.T) (*sql.DB, crew.Task, crew.Analyst) {
	db, tasks, a := runnerTasks(t, 1)
	return db, tasks[0], a
}

func runnerTasks(t *testing.T, n int) (*sql.DB, []crew.Task, crew.Analyst) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	db := st.DB()
	if _, err := db.Exec(crew.Schema); err != nil {
		t.Fatal(err)
	}
	if _, err := crew.SeedRoster(db, "installer"); err != nil {
		t.Fatal(err)
	}
	if err := crew.EnsureArtifactProvenance(db); err != nil {
		t.Fatal(err)
	}
	if err := crew.EnsureLiveSpendLedger(db); err != nil {
		t.Fatal(err)
	}
	tasks := make([]crew.Task, 0, n)
	for i := 0; i < n; i++ {
		title := fmt.Sprintf("task %d", i+1)
		res, err := db.Exec(`INSERT INTO tasks
			(title, goal, assignee, state, budget_cents, spent_cents, created, updated)
			VALUES (?, 'a goal', 'y.mercer', 'queued', 5000, 0,
			        datetime('now'), datetime('now'))`, title)
		if err != nil {
			t.Fatal(err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
		tasks = append(tasks, crew.Task{ID: int(id), Title: title})
	}
	return db, tasks, crew.Analyst{Name: "y.mercer"}
}

func spentOn(t *testing.T, db *sql.DB, task int) int64 {
	t.Helper()
	var n int64
	if err := db.QueryRow(`SELECT spent_cents FROM tasks WHERE id=?`, task).
		Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
