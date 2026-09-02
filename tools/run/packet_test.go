package main

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/estate"
	"github.com/TAIPANBOX/costcrew/internal/money"
	"github.com/TAIPANBOX/costcrew/internal/store"
)

// The prompt for a task from an anomaly carries the anomaly's own figures.
//
// Red first, against the code before this step: live.go's prompt() sent one
// message built only from the task and the analyst -- persona, mission,
// title, goal, date -- and handed the model no number at all, so an analyst
// asked to explain a move could not see the move. B2-SPEC.md section 2,
// section 4's first named test.
func TestThePacketCarriesTheAnomalysFigures(t *testing.T) {
	db := packetTestDB(t)

	an := anomaly.Anomaly{
		ID: "A-test1", Source: "aws", Team: "ml-platform", Service: "Amazon EC2",
		Day: "2026-07-14", Direction: "up",
		Amount: money.Cents(1_840_00), Baseline: money.Cents(800_00),
		Excess: money.Cents(1_040_00), Z: 4.2, Rule: "z-score over 3.5",
		RuleVer: anomaly.RuleVersion, State: anomaly.Open, DetectedAt: "2026-07-15T00:00:00Z",
	}
	plantAnomaly(t, db, an)
	taskID := plantAnomalyTask(t, db, an.ID, "aws")

	task, err := crew.GetTask(db, taskID)
	if err != nil {
		t.Fatal(err)
	}
	a := crew.Analyst{Name: "investigator-aws", Role: "Investigator", Desk: "aws",
		State: "active", Skills: []string{"driver-classification"}}

	got := packet(db, task, a)
	if !strings.Contains(got, an.Excess.String()) {
		t.Fatalf("the packet does not carry the anomaly's excess (%s):\n%s",
			an.Excess, got)
	}
	if !strings.Contains(got, an.Service) {
		t.Errorf("the packet does not name the service:\n%s", got)
	}
	if !strings.Contains(got, an.Day) {
		t.Errorf("the packet does not name the day:\n%s", got)
	}
}

// A restricted or suspended analyst is told plainly rather than handed
// figures it has no right to, and rather than a silent empty block.
func TestAnAnalystWithNoFiguresRightIsToldSo(t *testing.T) {
	db := packetTestDB(t)
	task := crew.Task{ID: 1, Title: "something"}
	suspended := crew.Analyst{Name: "x", State: "suspended"}

	got := packet(db, task, suspended)
	if !strings.Contains(got, "You have not been given figures for this task") {
		t.Errorf("a suspended analyst's packet does not say so plainly: %q", got)
	}
}

// A task with no anomaly and an analyst with none of the reporting or
// forecasting skills gets no packet at all: additive, never misleading.
func TestAnOrdinaryTaskGetsNoPacket(t *testing.T) {
	db := packetTestDB(t)
	task := crew.Task{ID: 1, Title: "something"}
	a := crew.Analyst{Name: "y", State: "active", Skills: []string{"routing"}}

	if got := packet(db, task, a); got != "" {
		t.Errorf("an ordinary task's packet is not empty: %q", got)
	}
}

func packetTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	db := st.DB()
	for _, schema := range []string{crew.Schema, estate.SeedSchema, anomaly.Schema} {
		if _, err := db.Exec(schema); err != nil {
			t.Fatal(err)
		}
	}
	// The same two migrations runnerTasks (provenance_test.go) applies:
	// saveDraft writes artifacts.source and tasks.live_micros, neither of
	// which crew.Schema's CREATE TABLE carries on its own.
	if err := crew.EnsureArtifactProvenance(db); err != nil {
		t.Fatal(err)
	}
	if err := crew.EnsureLiveSpendLedger(db); err != nil {
		t.Fatal(err)
	}
	return db
}

func plantAnomaly(t *testing.T, db *sql.DB, an anomaly.Anomaly) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO anomalies
		(id, source, team, service, day, direction, amount_cents, baseline_cents,
		 excess_cents, z, rule, rule_version, driver, caused_by, caused_by_kind,
		 handled_by, state, reason, detected_at, closed_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		an.ID, an.Source, an.Team, an.Service, an.Day, an.Direction,
		int64(an.Amount), int64(an.Baseline), int64(an.Excess), an.Z,
		an.Rule, an.RuleVer, "", "", "", "", string(an.State), "", an.DetectedAt, nil); err != nil {
		t.Fatal(err)
	}
}

// plantedAnomalyFixture is one reusable anomaly value, shared by tests in
// this package that need a real anomaly row but do not care about its exact
// figures.
func plantedAnomalyFixture() anomaly.Anomaly {
	return anomaly.Anomaly{
		ID: "A-fixture1", Source: "aws", Team: "ml-platform", Service: "Amazon EC2",
		Day: "2026-07-14", Direction: "up",
		Amount: money.Cents(1_840_00), Baseline: money.Cents(800_00),
		Excess: money.Cents(1_040_00), Z: 4.2, Rule: "z-score over 3.5",
		RuleVer: anomaly.RuleVersion, State: anomaly.Open, DetectedAt: "2026-07-15T00:00:00Z",
	}
}

// seedLongSeries writes n days of charges, so a tool result (series, here)
// grows large enough to actually exercise a byte cap rather than only
// asserting one in isolation.
func seedLongSeries(t *testing.T, db *sql.DB, source, team, service string, n int) {
	t.Helper()
	if _, err := db.Exec(estate.SeedSchema); err != nil {
		t.Fatal(err)
	}
	base, err := time.Parse("2006-01-02", "2026-01-01")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		day := base.AddDate(0, 0, i).Format("2006-01-02")
		if _, err := db.Exec(`INSERT INTO charges
			(source, day, service, team, category, billed_cents)
			VALUES (?,?,?,?, 'Usage', ?)`, source, day, service, team, 100+i); err != nil {
			t.Fatal(err)
		}
	}
}

func plantAnomalyTask(t *testing.T, db *sql.DB, anomalyID, desk string) int {
	t.Helper()
	res, err := db.Exec(`INSERT INTO tasks
		(title, goal, assignee, desk, state, budget_cents, spent_cents, anomaly, created, updated)
		VALUES ('Explain the move', 'say what happened', '', ?, 'queued', 0, 0, ?,
		        datetime('now'), datetime('now'))`, desk, anomalyID)
	if err != nil {
		t.Fatal(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return int(id)
}
