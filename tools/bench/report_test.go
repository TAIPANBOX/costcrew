package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/crew"
)

func twoResults(costEach int64) []caseResult {
	an1 := gkeAnomaly()
	an2 := gkeAnomaly()
	an2.ID, an2.Service, an2.Driver = "A-e04", "Batch cluster", "Batch cluster decommission, tranche 1"
	return []caseResult{
		{Case: knownCase{Anomaly: an1, Analyst: crew.Analyst{Name: "triage-gcp"}},
			Score: score{ServiceNamed: true, DayNamed: true, KindRight: false, CauseMatched: true,
				NamedCause: "the true cause", CostMicros: costEach}},
		{Case: knownCase{Anomaly: an2, Analyst: crew.Analyst{Name: "investigator-onprem"}},
			Score: score{ServiceNamed: true, DayNamed: false, KindRight: true, CauseMatched: false,
				NamedCause: "a wrong cause", CostMicros: costEach}},
	}
}

// B7-SPEC.md section 5: "the report shape prints the four counts and the
// cost".
func TestPrintDriverReportShowsTheFourCountsAndTheCost(t *testing.T) {
	var buf bytes.Buffer
	printDriverReport(&buf, 7, twoResults(4_100), "triage", "mock", "")
	out := buf.String()

	if !strings.HasPrefix(out, "BENCH  fixture, seed 7, 2 cases, skill triage, engine mock") {
		t.Fatalf("header is not the fixed shape:\n%s", out)
	}
	for _, want := range []string{"service", "day", "kind", "cause", "cost/task", "accuracy (cause)"} {
		if !strings.Contains(out, want) {
			t.Errorf("report is missing %q:\n%s", want, out)
		}
	}
	// Two cases: service 2/2, day 1/2, kind 1/2, cause 1/2.
	for _, want := range []string{"2/2", "1/2"} {
		if !strings.Contains(out, want) {
			t.Errorf("report is missing the count %q:\n%s", want, out)
		}
	}
	for _, id := range []string{"A-e02", "A-e04"} {
		if !strings.Contains(out, id) {
			t.Errorf("report does not print a line for case %s:\n%s", id, out)
		}
	}
	if !strings.Contains(out, "the true cause") || !strings.Contains(out, "a wrong cause") {
		t.Errorf("report does not print each case's own named cause:\n%s", out)
	}
}

func TestPrintDriverReportPrintsTheClampNote(t *testing.T) {
	var buf bytes.Buffer
	note := "       requested 20, 2 anomalies carry a driver and 1 eligible for skill triage; using 1"
	printDriverReport(&buf, 1, twoResults(0), "triage", "mock", note)
	if !strings.Contains(buf.String(), note) {
		t.Errorf("the clamp note was not printed:\n%s", buf.String())
	}
}

func TestPrintDriverReportOnZeroCasesDoesNotCrash(t *testing.T) {
	var buf bytes.Buffer
	printDriverReport(&buf, 1, nil, "triage", "mock", "")
	if !strings.Contains(buf.String(), "0 cases") {
		t.Errorf("a zero-case report does not say so:\n%s", buf.String())
	}
}

// The mutant B7-SPEC.md section 5 names by name: "count cost per call
// rounded to cents ... micro-dollars per case, round once at the total"
// (the memory finest-unit-per-row-round-once-at-the-aggregate). Two calls
// at 0.3 of a cent each: rounded to whole cents PER CASE first, both are
// nothing and the total would read as USD 0.0000; summed as micros first,
// the total is a genuine 0.6 of a cent.
func TestReportTotalSumsMicrosBeforeAnyRounding(t *testing.T) {
	var buf bytes.Buffer
	// 3,000 micros is 0.3 of a cent: money.Cents(micros/10_000) floors this
	// to zero on its own, which is exactly the fault this test exists for.
	printDriverReport(&buf, 1, twoResults(3_000), "triage", "mock", "")
	if strings.Contains(buf.String(), "USD 0.0000 total") {
		t.Errorf("two calls at 0.3 of a cent each summed to nothing:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "USD 0.0060 total") {
		t.Errorf("report does not show the true summed total (0.006 USD):\n%s", buf.String())
	}
}

func TestPrintStampReportShowsPostedAndReturned(t *testing.T) {
	var buf bytes.Buffer
	cases := []stampCase{
		{Task: crew.Task{ID: 1, Anomaly: "A-1"}, Outcome: outcomePosted},
		{Task: crew.Task{ID: 2, Anomaly: "A-2"}, Outcome: outcomeReturned},
		{Task: crew.Task{ID: 3, Anomaly: "A-3"}, Outcome: outcomePosted},
	}
	printStampReport(&buf, 3, cases, "triage", "openrouter", "")
	out := buf.String()
	if !strings.HasPrefix(out, "BENCH  stamps, seed 3, 3 cases, skill triage, engine openrouter") {
		t.Fatalf("header is not the fixed shape:\n%s", out)
	}
	if !strings.Contains(out, "posted (accepted first pass) 2/3") {
		t.Errorf("stamp report does not show 2/3 posted:\n%s", out)
	}
	if !strings.Contains(out, "accuracy (posted) 66%") {
		t.Errorf("stamp report does not show the accuracy percentage:\n%s", out)
	}
}

func TestPrintWorstCasePriceNamesTheEngineModelAndPrice(t *testing.T) {
	var buf bytes.Buffer
	printWorstCasePrice(&buf, 20, "anthropic", "claude-sonnet-5", 842_000)
	out := buf.String()
	if !strings.Contains(out, "20 case(s)") || !strings.Contains(out, "anthropic/claude-sonnet-5") {
		t.Errorf("worst-case message does not name the count and the engine/model:\n%s", out)
	}
	if !strings.Contains(out, "USD 0.8420") {
		t.Errorf("worst-case message does not show the price:\n%s", out)
	}
	if strings.Contains(strings.ToLower(out), "spent") && !strings.Contains(out, "never") {
		t.Errorf("worst-case message must not read as though it spent anything:\n%s", out)
	}
}
