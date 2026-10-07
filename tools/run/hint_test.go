package main

// Invariant 76 at the runner: -live asks typryx for a typed hint about each
// anomaly task before its packet is built, so the analyst reads it; a dry run
// asks nothing.

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/estate"
	"github.com/TAIPANBOX/costcrew/internal/store"
	"github.com/TAIPANBOX/costcrew/internal/typryx"
)

func fakeTypryx(t *testing.T, asks *int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/templates":
			_, _ = io.WriteString(w, `{"templates":[{"id":"triage.anomaly_class","version":"v","type":"choice",
				"fields":["anomaly","recent_changes"],
				"options":["expected_growth","misconfiguration","price_change","runaway_agent","unknown"]}]}`)
		case "/v1/ask":
			atomic.AddInt32(asks, 1)
			b, _ := json.Marshal(map[string]any{"answer_id": "ans-run", "template": "triage.anomaly_class",
				"type": "choice", "answer": "price_change", "backend": "openai-logprobs", "model": "qwen2.5:7b",
				"probabilities": map[string]float64{"expected_growth": 0.1, "misconfiguration": 0.1,
					"price_change": 0.6, "runaway_agent": 0.1, "unknown": 0.1}})
			_, _ = w.Write(b)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// runnerHintDir is a data directory with one anomaly and one open task on it,
// assigned to nobody: price() refuses it, so a -live run makes no model call.
func runnerHintDir(t *testing.T) (string, int) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	db := st.DB()
	for _, s := range []string{crew.Schema, crew.RosterSchema, estate.SeedSchema, anomaly.Schema} {
		if _, err := db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO anomalies (id, source, service, day, direction, amount_cents,
		baseline_cents, excess_cents, z, rule_version, state, detected_at)
		VALUES ('A-run', 'aws', 'Amazon EC2', '2026-07-14', 'up', 900, 100, 800, 5, 'v1', 'open', '2026-07-15T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	return dir, plantAnomalyTask(t, db, "A-run", "aws")
}

func reopen(t *testing.T, dir string) *sql.DB {
	t.Helper()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st.DB()
}

func TestADryRunAsksTypryxNothing(t *testing.T) {
	var asks int32
	srv := fakeTypryx(t, &asks)
	dir, _ := runnerHintDir(t)
	if err := run(dir, "", 2000, 0, false, false, false, 0, "", "", "", "", "", srv.URL); err != nil {
		t.Fatal(err)
	}
	if asks != 0 {
		t.Errorf("a dry run asked typryx %d times", asks)
	}
	if _, ok, _ := anomaly.HintOf(reopen(t, dir), "A-run"); ok {
		t.Error("a dry run recorded a hint")
	}
}

func TestALiveRunAsksTypryxBeforeTheTaskIsPriced(t *testing.T) {
	var asks int32
	srv := fakeTypryx(t, &asks)
	dir, _ := runnerHintDir(t)
	err := run(dir, "0.10", 2000, 0, true, false, false, 0, "", "", "", "", "", srv.URL)
	if err == nil || !strings.Contains(err.Error(), "nothing to run") {
		t.Fatalf("want the run to stop at 'nothing to run' (the task has no assignee), got %v", err)
	}
	if asks != 1 {
		t.Errorf("typryx was asked %d times, want 1", asks)
	}
	h, ok, _ := anomaly.HintOf(reopen(t, dir), "A-run")
	if !ok || h.Class != "price_change" || h.Backend != anomaly.BackendOwnModel {
		t.Errorf("stored hint %+v, %v", h, ok)
	}
	a, _ := anomaly.Get(reopen(t, dir), "A-run")
	if a.State != anomaly.Open || a.HandledBy != "" || a.Reason != "" {
		t.Errorf("a hint moved the anomaly: %+v", a)
	}
}

// A -live run with no ceiling is refused before anything, typryx included.
func TestALiveRunWithNoCeilingAsksTypryxNothing(t *testing.T) {
	var asks int32
	srv := fakeTypryx(t, &asks)
	dir, _ := runnerHintDir(t)
	if err := run(dir, "", 2000, 0, true, false, false, 0, "", "", "", "", "", srv.URL); err == nil {
		t.Fatal("-live with no ceiling was accepted")
	}
	if asks != 0 {
		t.Errorf("typryx was asked %d times before the run was refused", asks)
	}
}

func TestABadTypryxURLIsRefusedBeforeTheStoreOpens(t *testing.T) {
	err := run(t.TempDir()+"/nope/deeper", "", 2000, 0, false, false, false, 0, "", "", "", "", "", "http://u:p@h")
	if err == nil || !strings.Contains(err.Error(), "-typryx-url") {
		t.Errorf("got %v", err)
	}
}

// The packet the runner prices, and later sends, carries the hint hintTasks
// stored; an anomaly already answered is not asked again.
func TestTheRunnersPacketCarriesTheHintAndAnAnswerIsNotAskedAgain(t *testing.T) {
	var asks int32
	srv := fakeTypryx(t, &asks)
	dir, taskID := runnerHintDir(t)
	db := reopen(t, dir)
	task, err := crew.GetTask(db, taskID)
	if err != nil {
		t.Fatal(err)
	}
	tx := typryx.New(srv.URL, "", typryx.DefaultTimeout)
	for i := 0; i < 2; i++ {
		if _, err := hintTasks(db, tx, []crew.Task{task, task}, bus{}); err != nil {
			t.Fatal(err)
		}
	}
	if asks != 1 {
		t.Errorf("typryx was asked %d times for one anomaly over two passes, want 1", asks)
	}
	a := crew.Analyst{Name: "triage-aws", Desk: "aws", State: "active", Engine: "anthropic",
		Skills: []string{"anomaly-triage"}}
	if e := price(db, task, a, 2000); !strings.Contains(e.Packet, "typryx suggests: price_change, probability 0.60") {
		t.Errorf("the priced packet does not carry the hint:\n%s", e.Packet)
	}
}
