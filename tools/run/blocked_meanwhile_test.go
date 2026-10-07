package main

// A task a person blocked while its call was in flight must not get its
// deliverable (invariant 57 named this as its open limit: workable() drops a
// blocked task BEFORE anything is priced, and nothing looked again once the
// model had answered). The call was made and billed, so the money is
// recorded; the answer is what is discarded, and a line says so.
//
// Every test blocks the task from inside the fake gateway's handler, between
// the request arriving and the response going back, which is the one moment
// that matters: it is exactly where a person's click lands in a real run.

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/money"
)

const blockReason = "stopped by alice: the figures are wrong"

// blockTheTask is what the console's block button does to the row.
func blockTheTask(db *sql.DB, taskID int) error {
	_, err := db.Exec(`UPDATE tasks SET state='blocked', reason=?, updated=datetime('now') WHERE id=?`,
		blockReason, taskID)
	return err
}

// blockingGateway answers like a settled gateway, and blocks taskID in the
// store during request number blockOn (1-based) before replying to it.
func blockingGateway(t *testing.T, db *sql.DB, taskID, blockOn int, bodies ...string) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		n++
		i := n
		mu.Unlock()
		if i == blockOn {
			if err := blockTheTask(db, taskID); err != nil {
				t.Errorf("blocking the task from the gateway: %v", err)
			}
		}
		body := bodies[len(bodies)-1]
		if i <= len(bodies) {
			body = bodies[i-1]
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("x-fuse-cost-usd", "0.050000")
		w.Header().Set("x-fuse-spent-usd", fmt.Sprintf("%.6f", 0.05*float64(i)))
		w.Header().Set("x-fuse-price", "known")
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func artifactsOf(t *testing.T, db *sql.DB, taskID int) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM artifacts WHERE task=?`, taskID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func stateOf(t *testing.T, db *sql.DB, taskID int) (state, reason string) {
	t.Helper()
	if err := db.QueryRow(`SELECT state, COALESCE(reason,'') FROM tasks WHERE id=?`, taskID).
		Scan(&state, &reason); err != nil {
		t.Fatal(err)
	}
	return
}

func oneTaskExecute(t *testing.T, srvURL string, db *sql.DB, task crew.Task, analyst crew.Analyst) (run *runBudget, out string, err error) {
	t.Helper()
	b, _ := testBus(t, "gcp.taipanbox.local", "crew-2026-w41")
	run = &runBudget{ceilingMicros: 5_000_000}
	gw := gatewayConfig{URL: srvURL, Host: "gcp.taipanbox.local", CeilingUSD: money.Cents(1000)}
	e := estimate{Task: task, Analyst: analyst, Engine: "anthropic",
		Model: "claude-x", WorstMicros: 1_000, Priced: true}
	out = captureStdout(t, func() { err = execute(context.Background(), db, nil, e, 100, run, b, gw) })
	return run, out, err
}

// The incident: the model answered after a person blocked the task, and the
// deliverable was written anyway.
func TestATaskBlockedWhileItsCallWasInFlightGetsNoDeliverable(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-stub-not-real")
	db, task, analyst := runnerDB(t)
	srv := blockingGateway(t, db, task.ID, 1, anthropicAnswer)

	run, out, _ := oneTaskExecute(t, srv.URL, db, task, analyst)

	if n := artifactsOf(t, db, task.ID); n != 0 {
		t.Errorf("%d artifact(s) were written for a task a person blocked before the answer arrived", n)
	}
	var options int
	if err := db.QueryRow(`SELECT COUNT(*) FROM artifact_options`).Scan(&options); err == nil && options != 0 {
		t.Errorf("%d option(s) were saved for a discarded answer", options)
	}
	state, reason := stateOf(t, db, task.ID)
	if state != "blocked" || reason != blockReason {
		t.Errorf("the person's block was changed: state %q reason %q, want blocked and %q", state, reason, blockReason)
	}
	// The call was made and the gateway billed it: that money is real.
	if got := liveMicros(t, db, task.ID); got != 50_000 {
		t.Errorf("tasks.live_micros = %d, want 50000: a discarded answer was still bought", got)
	}
	if got := run.total(); got != 50_000 {
		t.Errorf("run.total() = %d, want 50000", got)
	}
	if !strings.Contains(out, "DISCARDED") || !strings.Contains(out, "after a person blocked the task") {
		t.Errorf("no line says the answer was discarded because the task was blocked:\n%s", out)
	}
}

// Blocked during the FIRST round of a tool loop: the second round is already
// on its way, both are billed, and nothing is saved.
func TestATaskBlockedDuringAToolRoundBooksBothRoundsAndSavesNothing(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-stub-not-real")
	db, task, analyst := runnerDB(t)
	srv := blockingGateway(t, db, task.ID, 1, anthropicToolUse, anthropicAnswer)

	run, out, _ := oneTaskExecute(t, srv.URL, db, task, analyst)

	if n := artifactsOf(t, db, task.ID); n != 0 {
		t.Errorf("%d artifact(s) were written for a blocked task", n)
	}
	if got := liveMicros(t, db, task.ID); got != 100_000 {
		t.Errorf("tasks.live_micros = %d, want 100000 (two settled rounds)", got)
	}
	if got := run.total(); got != 100_000 {
		t.Errorf("run.total() = %d, want 100000", got)
	}
	if !strings.Contains(out, "DISCARDED") {
		t.Errorf("no line says the answer was discarded:\n%s", out)
	}
}

// The negative control: a task nobody blocked still gets its draft, so the
// fix has not simply stopped saving.
func TestATaskNobodyBlockedStillGetsItsDeliverable(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-stub-not-real")
	db, task, analyst := runnerDB(t)
	srv := blockingGateway(t, db, task.ID, 0, anthropicAnswer) // 0: never blocks

	_, out, err := oneTaskExecute(t, srv.URL, db, task, analyst)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if n := artifactsOf(t, db, task.ID); n != 1 {
		t.Errorf("%d artifact(s), want 1 draft for a task that stayed open", n)
	}
	if got := liveMicros(t, db, task.ID); got != 50_000 {
		t.Errorf("tasks.live_micros = %d, want 50000", got)
	}
	if strings.Contains(out, "DISCARDED") {
		t.Errorf("a task nobody blocked printed a discard line:\n%s", out)
	}
}

// saveDraft is the last door: even a block that lands after execute last
// looked leaves no draft, because the insert itself refuses a blocked task
// (one statement, so there is no gap between looking and writing).
func TestSaveDraftWritesNothingForABlockedTask(t *testing.T) {
	db, task, analyst := runnerDB(t)
	if err := blockTheTask(db, task.ID); err != nil {
		t.Fatal(err)
	}
	e := estimate{Task: task, Analyst: analyst, Engine: "anthropic", Model: "claude-x"}

	err := saveDraft(db, e, callResult{Text: "a deliverable"}, bus{})
	if err == nil {
		t.Error("saveDraft accepted a deliverable for a blocked task")
	}
	if n := artifactsOf(t, db, task.ID); n != 0 {
		t.Errorf("%d artifact(s) were written for a blocked task", n)
	}
	if got := liveMicros(t, db, task.ID); got != 0 {
		t.Errorf("saveDraft booked %d micros for a deliverable it did not save", got)
	}
}

// Through spend(): the person's reason is not overwritten with "the engine did
// not answer", the task is not counted done, and the summary says one answer
// was discarded.
func TestARunLeavesAPersonsBlockAloneAndCountsTheDiscardedAnswer(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-stub-not-real")
	db, task, analyst := runnerDB(t)
	srv := blockingGateway(t, db, task.ID, 1, anthropicAnswer)
	b, _ := testBus(t, "gcp.taipanbox.local", "crew-2026-w41")
	gw := gatewayConfig{URL: srv.URL, Host: "gcp.taipanbox.local", CeilingUSD: money.Cents(1000)}
	e := estimate{Task: task, Analyst: analyst, Engine: "anthropic",
		Model: "claude-x", WorstMicros: 1_000, Priced: true}

	var err error
	out := captureStdout(t, func() { err = spend(db, nil, []estimate{e}, 100, money.Cents(1000), 0, b, gw) })
	if err != nil {
		t.Fatalf("spend: %v", err)
	}
	state, reason := stateOf(t, db, task.ID)
	if state != "blocked" || reason != blockReason {
		t.Errorf("the person's block was rewritten: state %q reason %q", state, reason)
	}
	if !strings.Contains(out, "0 of 1 done") || !strings.Contains(out, "1 discarded") {
		t.Errorf("the summary does not say 0 of 1 done and 1 discarded:\n%s", out)
	}
	if n := artifactsOf(t, db, task.ID); n != 0 {
		t.Errorf("%d artifact(s) written", n)
	}
	if got := liveMicros(t, db, task.ID); got != 50_000 {
		t.Errorf("tasks.live_micros = %d, want 50000", got)
	}
}
