package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/crew"
)

// An analyst without sql-readonly calling charges_query gets the refusal
// sentence and a tool_refused journal line.
//
// Red first, against the code before this step: there was no dispatcher at
// all, so nothing stood between a tool name the model typed and the right
// its card carries. B2-SPEC.md section 3.2, section 4's second named test.
func TestAToolTheAnalystHasNoRightForIsRefused(t *testing.T) {
	db := packetTestDB(t)
	a := crew.Analyst{Name: "investigator-aws", State: "active", Skills: []string{"routing"}}
	// "routing" grants only figures-read (internal/crew/mandate.go's
	// rightsForSkill), never sql-readonly, so this analyst genuinely does
	// not hold the right charges_query needs.

	b, path := testBus(t, "costcrew.test", "run-1")
	got := dispatch(context.Background(), db, nil, a, "charges_query",
		json.RawMessage(`{"sql":"SELECT 1 FROM charges"}`), b)

	if got.Outcome != outcomeRefused {
		t.Fatalf("outcome %q, want %q", got.Outcome, outcomeRefused)
	}
	if !strings.Contains(got.Text, "sql-readonly") {
		t.Errorf("the refusal does not name the right it needed: %q", got.Text)
	}
	if got.Right != "sql-readonly" {
		t.Errorf("Right = %q, want sql-readonly", got.Right)
	}

	ev := oneEvent(t, path)
	if ev["type"] != "tool_call" {
		t.Errorf("bus event type %q, want tool_call", ev["type"])
	}
	data, _ := ev["data"].(map[string]any)
	if data["tool"] != "charges_query" {
		t.Errorf("bus event does not name the tool: %v", data)
	}
	if data["right"] != "sql-readonly" {
		t.Errorf("bus event does not name the right: %v", data)
	}
	if data["outcome"] != "tool_refused" {
		t.Errorf("bus event outcome %v, want tool_refused", data["outcome"])
	}
}

// A tool name the model made up is refused, journaled as tool_unknown, and
// never reaches a right check that would need one to exist.
func TestAnUnknownToolIsRefused(t *testing.T) {
	db := packetTestDB(t)
	a := crew.Analyst{Name: "x", State: "active", Skills: []string{"routing"}}
	b, path := testBus(t, "costcrew.test", "run-1")

	got := dispatch(context.Background(), db, nil, a, "delete_everything", nil, b)
	if got.Outcome != outcomeUnknownTool {
		t.Fatalf("outcome %q, want %q", got.Outcome, outcomeUnknownTool)
	}
	if !strings.Contains(got.Text, "delete_everything") {
		t.Errorf("the refusal does not name the tool that was asked for: %q", got.Text)
	}
	ev := oneEvent(t, path)
	if ev["data"].(map[string]any)["outcome"] != "tool_unknown" {
		t.Errorf("bus event does not carry tool_unknown: %v", ev)
	}
}

// An analyst who DOES hold the right, but sends arguments missing a
// required field, is refused before the tool's own function runs at all.
func TestMissingRequiredArgumentIsRefused(t *testing.T) {
	db := packetTestDB(t)
	a := crew.Analyst{Name: "x", State: "active", Skills: []string{"driver-classification"}}
	// driver-classification grants figures-read and sql-readonly.

	got := dispatch(context.Background(), db, nil, a, "anomaly", json.RawMessage(`{}`), noBus())
	if got.Outcome != outcomeInvalidArgs {
		t.Fatalf("outcome %q, want %q", got.Outcome, outcomeInvalidArgs)
	}
	if !strings.Contains(got.Text, "id") {
		t.Errorf("the refusal does not name the missing argument: %q", got.Text)
	}
}

// An allowed call with a well-formed argument actually runs and returns the
// tool's own text.
func TestAnAllowedToolActuallyRuns(t *testing.T) {
	db := packetTestDB(t)
	an := plantedAnomalyFixture()
	plantAnomaly(t, db, an)

	a := crew.Analyst{Name: "investigator-aws", State: "active",
		Skills: []string{"driver-classification"}}
	got := dispatch(context.Background(), db, nil, a, "anomaly",
		json.RawMessage(`{"id":"`+an.ID+`"}`), noBus())
	if got.Outcome != outcomeOK {
		t.Fatalf("outcome %q, want ok: %s", got.Outcome, got.Text)
	}
	if !strings.Contains(got.Text, an.Service) {
		t.Errorf("the tool's own result does not name the service: %q", got.Text)
	}
}

// A result over toolResultMaxBytes is cut, and says so, rather than handed
// to the model whole. dispatch() itself is exercised end to end with a real
// registered tool (series, whose result grows with how many days it is
// asked for) so the cap is proven on the actual path the loop uses, not
// only on boundBytes in isolation.
func TestALongToolResultIsCutAtTheCap(t *testing.T) {
	db := packetTestDB(t)
	seedLongSeries(t, db, "aws", "ml-platform", "Amazon EC2", 400)

	a := crew.Analyst{Name: "x", State: "active", Skills: []string{"driver-classification"}}
	got := dispatch(context.Background(), db, nil, a, "series",
		json.RawMessage(`{"source":"aws","team":"ml-platform","service":"Amazon EC2","days":120}`), noBus())
	if got.Outcome != outcomeOK {
		t.Fatalf("outcome %q, want ok: %s", got.Outcome, got.Text)
	}
	if len(got.Text) > toolResultMaxBytes {
		t.Fatalf("dispatch returned %d bytes, want at most %d", len(got.Text), toolResultMaxBytes)
	}

	// And the pure function underneath it, directly: a result actually over
	// the cap says it was cut.
	huge := strings.Repeat("x", toolResultMaxBytes+500)
	cutResult := boundBytes(huge, toolResultMaxBytes)
	if len(cutResult) > toolResultMaxBytes {
		t.Fatalf("boundBytes returned %d bytes, want at most %d", len(cutResult), toolResultMaxBytes)
	}
	if !strings.Contains(cutResult, "cut") {
		t.Errorf("a cut result does not say so: tail is %q", cutResult[len(cutResult)-60:])
	}
}

func noBus() bus { return bus{} }
