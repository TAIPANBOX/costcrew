package main

// -supervise needs -sprint, the same shape -live needs -ceiling for: a pass
// over every sprint on the board because nobody named one is a pass nobody
// chose. finops.Supervise itself is tested at internal/finops's own level
// (apply_test.go, supervise_test.go), against a database this file's run()
// would take much more scaffolding to reach the same way.

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/estate"
)

func TestSuperviseNeedsASprint(t *testing.T) {
	dir := t.TempDir()
	err := run(dir, "", 2000, 0, false, true, false, 0, "", "", "", "")
	if err == nil {
		t.Fatal("-supervise with -sprint 0 was accepted")
	}
	if got := err.Error(); !strings.Contains(got, "-supervise needs -sprint") {
		t.Errorf("refused for the wrong reason: %v", got)
	}
}

// superviseRun itself: the CLI wrapper around finops.Supervise, which is
// where the actual routing is proven (internal/finops/supervise_test.go).
// This is the thin layer specific to this binary -- it must not error on an
// ordinary sprint, and it must not panic when there is nothing to review.
func TestSuperviseRunReportsAnOrdinaryPass(t *testing.T) {
	db, sprintID := superviseRunTestDB(t)
	if err := superviseRun(db, sprintID, bus{}); err != nil {
		t.Fatal(err)
	}
}

func TestSuperviseRunOnAnEmptySprintDoesNotError(t *testing.T) {
	db, _ := superviseRunTestDB(t)
	// A second, empty sprint: nothing posted, nothing to collect.
	res, err := db.Exec(`INSERT INTO sprints (label, start, finish, state, goal)
		VALUES ('2026-W96', '2026-08-18', '2026-08-24', 'active', 'a goal')`)
	if err != nil {
		t.Fatal(err)
	}
	emptyID, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if err := superviseRun(db, int(emptyID), bus{}); err != nil {
		t.Fatal(err)
	}
}

func superviseRunTestDB(t *testing.T) (*sql.DB, int) {
	t.Helper()
	db, _, _ := runnerTasks(t, 0)
	// driver.recurring's own side effect (internal/finops.Apply) writes a
	// drivers row, which needs estate.SeedSchema's table; runnerTasks
	// (provenance_test.go) has no reason to carry that on its own.
	if _, err := db.Exec(estate.SeedSchema); err != nil {
		t.Fatal(err)
	}
	res, err := db.Exec(`INSERT INTO sprints (label, start, finish, state, goal)
		VALUES ('2026-W97', '2026-08-25', '2026-08-31', 'active', 'a goal')`)
	if err != nil {
		t.Fatal(err)
	}
	sid, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	tres, err := db.Exec(`INSERT INTO tasks
		(sprint, title, goal, assignee, desk, state, budget_cents, spent_cents, created, updated, owner)
		VALUES (?, 'a task', 'a goal', 'investigator-aws', 'aws', 'active', 0, 0,
		        datetime('now'), datetime('now'), 'y.mercer')`, sid)
	if err != nil {
		t.Fatal(err)
	}
	taskID, err := tres.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	ares, err := db.Exec(`INSERT INTO artifacts
		(task, author, title, body, state, created)
		VALUES (?, 'investigator-aws', 'a deliverable', 'body', 'posted', datetime('now'))`, taskID)
	if err != nil {
		t.Fatal(err)
	}
	artID, err := ares.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO artifact_options
		(artifact, ordinal, class, summary, figure_cents, saving_cents, risk, needs, evidence, state)
		VALUES (?, 1, 'driver.recurring', 'a weekly batch job', 10000, 0, 'low', 'nothing', '[]', 'open')`,
		artID); err != nil {
		t.Fatal(err)
	}
	return db, int(sid)
}
