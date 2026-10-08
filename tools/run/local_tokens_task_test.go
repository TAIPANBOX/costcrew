package main

import (
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/money"
)

// TestEachTaskRecordsTheTokensItsCallsUsed (invariant 93): a local run at a
// price of 0 books no money, so the tokens each task used, and the run's
// token ceiling, are kept on the task itself for the task page and the card.
func TestEachTaskRecordsTheTokensItsCallsUsed(t *testing.T) {
	srv := newModelServer(t, scriptedRound{body: localAnswer})
	db, tasks, analyst := runnerTasks(t, 2)
	ests := []estimate{localEstimate(tasks[0], analyst, 0, 0), localEstimate(tasks[1], analyst, 0, 0)}
	gw := gatewayConfig{ModelURL: srv.URL + "/v1", Host: "gcp.taipanbox.local", CeilingUSD: money.Cents(500),
		MaxRunTokens: 1_000_000}
	var err error
	captureStdout(t, func() {
		err = spend(db, nil, ests, 200, money.Cents(500), 0, bus{run: "crew-1"}, gw)
	})
	if err != nil {
		t.Fatalf("spend: %v", err)
	}
	var total int64
	for _, task := range tasks {
		n, ceiling, err := crew.TaskTokens(db, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if n <= 0 || ceiling != 1_000_000 {
			t.Errorf("task %d: %d tokens against a ceiling of %d, want its own tokens against 1000000", task.ID, n, ceiling)
		}
		total += n
	}
	if total != 300 {
		t.Errorf("the tasks carry %d tokens between them, want the 300 the run summary counts", total)
	}
}
