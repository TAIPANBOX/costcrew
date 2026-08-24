package main

import (
	"database/sql"
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
	if err := saveDraft(db, e, res); err != nil {
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
func TestASubCentCallStillLandsOnTheTask(t *testing.T) {
	db, task, analyst := runnerDB(t)
	before := spentOn(t, db, task.ID)

	// 1234 micro-dollars is 0.1234 of a cent.
	if err := saveDraft(db, estimate{Task: task, Analyst: analyst},
		callResult{Text: "x", ActualMicros: 1_234}); err != nil {
		t.Fatal(err)
	}

	after := spentOn(t, db, task.ID)
	if after <= before {
		t.Errorf("spend went %d -> %d: a call that cost a fraction of a cent "+
			"rounded to nothing, which is how a bill grows out of zeroes",
			before, after)
	}
}

func runnerDB(t *testing.T) (*sql.DB, crew.Task, crew.Analyst) {
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
	if _, err := db.Exec(`INSERT INTO tasks
		(title, goal, assignee, state, budget_cents, spent_cents, created, updated)
		VALUES ('a task','a goal','y.mercer','queued', 5000, 0,
		        datetime('now'), datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	var id int
	if err := db.QueryRow(`SELECT id FROM tasks LIMIT 1`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return db, crew.Task{ID: id, Title: "a task"}, crew.Analyst{Name: "y.mercer"}
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
