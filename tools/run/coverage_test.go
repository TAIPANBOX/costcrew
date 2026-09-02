package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/estate"
	"github.com/TAIPANBOX/costcrew/internal/finops"
)

// Every section of the packet actually renders its real content, not only
// its own-nothing-to-say short circuit: the anomaly section is already
// proven by TestThePacketCarriesTheAnomalysFigures, so this fixture is
// built to reach the four sections that need MORE than a bare anomaly row
// -- a real series (so the weekday marker and the arrow both print), a
// driver on the right service and desk, a budget row, and a posted
// explanation on a sibling anomaly with the same service.
func TestThePacketCoversEverySection(t *testing.T) {
	db := packetTestDB(t)
	if _, err := db.Exec(finops.Schema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(estate.BudgetSchema); err != nil {
		t.Fatal(err)
	}

	an := plantedAnomalyFixture() // aws / ml-platform / Amazon EC2 / 2026-07-14
	plantAnomaly(t, db, an)

	// 40 days of real usage around the anomaly day, so SeriesDays returns a
	// dense run long enough for the 28-before/7-after window to print
	// several lines, including at least one sharing the anomaly's weekday.
	base, err := time.Parse("2006-01-02", an.Day)
	if err != nil {
		t.Fatal(err)
	}
	for i := -35; i <= 10; i++ {
		day := base.AddDate(0, 0, i).Format("2006-01-02")
		if _, err := db.Exec(`INSERT INTO charges
			(source, day, service, team, category, billed_cents)
			VALUES (?,?,?,?, 'Usage', ?)`, an.Source, day, an.Service, an.Team, 1000+i); err != nil {
			t.Fatal(err)
		}
	}

	// A driver on this exact service and desk, covering the anomaly day.
	if _, err := db.Exec(`INSERT INTO drivers VALUES (?,?,?,?,?,?)`,
		"2026-07-10", "2026-07-16", an.Service, "a planned rollout", "one-time", an.Source); err != nil {
		t.Fatal(err)
	}

	// A budget for this team, this desk, this month.
	if _, err := db.Exec(`INSERT INTO budgets VALUES (?,?,?,?)`,
		an.Source, an.Team, an.Day[:7], 500_00); err != nil {
		t.Fatal(err)
	}

	// A posted explanation on a SIBLING anomaly, same service, different id
	// and day -- lastExplanationSection must find it by service, not by
	// this exact anomaly.
	sibling := an
	sibling.ID = "A-sibling1"
	sibling.Day = "2026-06-01"
	plantAnomaly(t, db, sibling)
	siblingTask := plantAnomalyTask(t, db, sibling.ID, sibling.Source)
	// Past 600 bytes, so lastExplanationSection's own cut (trimBytes) is
	// actually exercised, not only its pass-through path.
	longBody := "A scheduled migration explains this move. " + strings.Repeat("Details. ", 100)
	if _, err := db.Exec(`INSERT INTO artifacts
		(task, author, title, body, state, created, stamped, stamper)
		VALUES (?, 'an-analyst', 'explanation', ?,
		        'posted', datetime('now'), datetime('now'), 'an-operator')`, siblingTask, longBody); err != nil {
		t.Fatal(err)
	}

	task, err := crew.GetTask(db, plantAnomalyTask(t, db, an.ID, an.Source))
	if err != nil {
		t.Fatal(err)
	}
	a := crew.Analyst{Name: "investigator-aws", State: "active", Skills: []string{"driver-classification"}}

	got := packet(db, task, a)

	for _, want := range []string{
		"The series", "*", "->", // the weekday marker and the anomaly-day arrow
		"Drivers on this service and desk", "a planned rollout",
		"The team's month", "500.00",
		"The last posted explanation", "A scheduled migration explains this move.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the packet is missing %q:\n%s", want, got)
		}
	}
}

// The reporting and forecasting sections, each keyed off the analyst's own
// skill rather than off the task's anomaly.
func TestThePacketReportingAndForecastingSections(t *testing.T) {
	db := packetTestDB(t)
	if _, err := db.Exec(finops.Schema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(finops.ForecastSchema); err != nil {
		t.Fatal(err)
	}
	if err := finops.SeedRules(db); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		day, team string
		cents     int64
	}{
		{"2026-06-10", "ml-platform", 200_00}, // gives FreezeAsAt("2026-06") a month to project from
		{"2026-07-10", "ml-platform", 300_00},
		{"2026-07-10", "data-eng", 100_00},
	} {
		if _, err := db.Exec(`INSERT INTO charges
			(source, day, service, team, category, billed_cents)
			VALUES ('aws', ?, 'Amazon EC2', ?, 'Usage', ?)`,
			row.day, row.team, row.cents); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO anomalies
		(id, source, team, service, day, direction, amount_cents, baseline_cents,
		 excess_cents, z, rule, rule_version, state, detected_at)
		VALUES ('A-open1','aws','ml-platform','Amazon EC2','2026-07-10','up',
		        10000,5000,5000,4.0,'z-score','v1','open',datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	if err := finops.FreezeAsAt(db, "2026-06", "an-operator", 31); err != nil {
		t.Fatal(err)
	}

	task := crew.Task{ID: 1, Title: "the desk's month", Desk: "aws"}

	reporter := crew.Analyst{Name: "reporter", State: "active", Skills: []string{"exec-reporting"}}
	got := packet(db, task, reporter)
	for _, want := range []string{"The desk's month", "total:", "allocation coverage", "top movers", "ml-platform"} {
		if !strings.Contains(got, want) {
			t.Errorf("the reporting section is missing %q:\n%s", want, got)
		}
	}

	forecaster := crew.Analyst{Name: "forecaster", State: "active", Skills: []string{"forecasting-commentary"}}
	got2 := packet(db, task, forecaster)
	for _, want := range []string{"Forecasting", "run-rate projection", "last frozen forecast"} {
		if !strings.Contains(got2, want) {
			t.Errorf("the forecasting section is missing %q:\n%s", want, got2)
		}
	}
}

// The eight catalogue tools TestAnAllowedToolActuallyRuns (dispatch_test.go)
// and the loop tests do not already cover, each dispatched with a right
// its analyst genuinely holds, each asserted on something specific the
// tool itself computed -- a covered line with no assertion that can go red
// is not coverage, it is execution.
func TestEveryRemainingCatalogueToolRuns(t *testing.T) {
	db := packetTestDB(t)
	if _, err := db.Exec(finops.Schema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(estate.BudgetSchema); err != nil {
		t.Fatal(err)
	}
	if err := finops.SeedRules(db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO drivers VALUES ('2026-07-01','2026-07-31','Amazon EC2','a driver row','recurring','aws')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO charges
		(source, day, service, team, category, billed_cents)
		VALUES ('aws','2026-07-10','Amazon EC2','ml-platform','Usage',30000)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO budgets VALUES ('aws','ml-platform','2026-07',50000)`); err != nil {
		t.Fatal(err)
	}

	a := crew.Analyst{Name: "x", State: "active",
		Skills: []string{"exec-reporting", "kpi-benchmarking", "commitment-modelling"}}
	// exec-reporting -> budgets-read, export-data; kpi-benchmarking -> kpi-registry;
	// commitment-modelling -> budgets-read (already covered, kept for clarity).

	cases := []struct {
		tool string
		args string
		want string
	}{
		{"drivers", `{"service":"Amazon EC2","since":"2026-01-01"}`, "a driver row"},
		{"team_month", `{"team":"ml-platform","period":"2026-07"}`, "300.00"},
		{"budgets", `{"source":"aws","period":"2026-07"}`, "ml-platform"},
		{"variance", `{"team":"ml-platform","period":"2026-07"}`, "300.00"},
		{"kpis", `{"period":"2026-07"}`, "Allocation coverage"},
		{"maturity", `{"period":"2026-07"}`, "Allocation"},
		{"allocation", `{"period":"2026-07"}`, "aws"},
		{"showback", `{"team":"ml-platform","period":"2026-07"}`, "ml-platform"},
	}
	for _, c := range cases {
		t.Run(c.tool, func(t *testing.T) {
			got := dispatch(context.Background(), db, nil, a, c.tool, json.RawMessage(c.args), noBus())
			if got.Outcome != outcomeOK {
				t.Fatalf("%s: outcome %q, want ok: %s", c.tool, got.Outcome, got.Text)
			}
			if !strings.Contains(got.Text, c.want) {
				t.Errorf("%s: result does not contain %q:\n%s", c.tool, c.want, got.Text)
			}
		})
	}

	// showback's other two shapes: a CLOSED period (the frozen figures,
	// not the live allocation) and a team with nothing in the period at
	// all.
	if err := finops.Close(db, "2026-07", "an-operator"); err != nil {
		t.Fatal(err)
	}
	closedResult := dispatch(context.Background(), db, nil, a, "showback",
		json.RawMessage(`{"team":"ml-platform","period":"2026-07"}`), noBus())
	if closedResult.Outcome != outcomeOK || !strings.Contains(closedResult.Text, "FROZEN") {
		t.Errorf("showback on a closed period is not FROZEN: %q (%s)", closedResult.Text, closedResult.Outcome)
	}
	emptyResult := dispatch(context.Background(), db, nil, a, "showback",
		json.RawMessage(`{"team":"nobody-spent-here","period":"2026-07"}`), noBus())
	if emptyResult.Outcome != outcomeOK || !strings.Contains(emptyResult.Text, "no frozen row") {
		t.Errorf("showback for a team absent from a closed period does not say so: %q", emptyResult.Text)
	}
}
