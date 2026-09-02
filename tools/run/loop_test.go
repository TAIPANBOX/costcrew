package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/engines"
	"github.com/TAIPANBOX/costcrew/internal/money"
)

// A fake model that always asks for a tool is answered without tools on
// round 6, and the reservation covers six rounds.
//
// Red first, against the code before this step: execute() made exactly one
// call, so a fake server that always asks for a tool would either hang the
// test (nothing ever dispatched a second round) or the call would simply
// fail to compile against the pre-loop signature at all -- this test
// itself could not even be written until runToolLoop and the widened
// execute() existed. B2-SPEC.md section 3.4, section 4's third named test.
func TestTheLoopStopsAtMaxRounds(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-stub-not-real")

	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			Tools []any `json:"tools"`
		}
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")
		if len(body.Tools) > 0 {
			// Offered tools: always asks for one, exactly like a model
			// that never runs out of things to check.
			fmt.Fprintf(w, `{"content":[{"type":"tool_use","id":"call-%d","name":"series",`+
				`"input":{"source":"aws","team":"ml-platform","service":"Amazon EC2"}}],`+
				`"stop_reason":"tool_use","usage":{"input_tokens":10,"output_tokens":5}}`, calls)
			return
		}
		// No tools offered (round 6): forced to answer.
		fmt.Fprint(w, `{"content":[{"type":"text","text":"the answer, forced on the last round"}],`+
			`"stop_reason":"end_turn","usage":{"input_tokens":12,"output_tokens":6}}`)
	}))
	defer srv.Close()

	db := packetTestDB(t)
	task := crew.Task{ID: 1, Title: "t"}
	a := crew.Analyst{Name: "x", State: "active"} // figures-read is the unconditional floor
	e := estimate{Task: task, Analyst: a, Engine: "anthropic", Model: "claude-x",
		Price: engines.Price{InPerM: 1, OutPerM: 1}, WorstMicros: 1_000, Priced: true}
	gw := gatewayConfig{URL: srv.URL, Host: "x.test", CeilingUSD: money.Cents(10_000_00)}

	// Too tight for six rounds: refused before the first call is made.
	tight := &runBudget{ceilingMicros: 1_000*maxToolRounds - 1}
	if err := execute(context.Background(), db, nil, e, 100, tight, noBus(), gw); err == nil {
		t.Fatal("a ceiling one micro short of six rounds' worth was not refused: " +
			"the reservation does not actually cover six rounds")
	}
	if calls != 0 {
		t.Errorf("the too-tight run still reached the server %d time(s): the reservation "+
			"must refuse BEFORE any call, not after", calls)
	}

	// Exactly six rounds' worth: succeeds, and uses every one of them.
	fits := &runBudget{ceilingMicros: 1_000 * maxToolRounds}
	if err := execute(context.Background(), db, nil, e, 100, fits, noBus(), gw); err != nil {
		t.Fatalf("a ceiling that is exactly six rounds' worth was refused: %v", err)
	}
	if calls != maxToolRounds {
		t.Errorf("the server was called %d time(s), want exactly %d: a model that always "+
			"asks for a tool must be sent no tools on the last round and forced to answer",
			calls, maxToolRounds)
	}
	if fits.total() <= 0 {
		t.Error("six real rounds were made and the run recorded no spend at all")
	}
}

// A fake Anthropic server: tool_use, then text. The loop dispatches the
// tool, feeds the result back, and the SECOND round's answer is what the
// task's deliverable becomes.
func TestAFakeAnthropicServerAsksForAToolThenAnswers(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-stub-not-real")

	db := packetTestDB(t)
	an := plantedAnomalyFixture()
	plantAnomaly(t, db, an)

	round := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		round++
		w.Header().Set("Content-Type", "application/json")
		if round == 1 {
			fmt.Fprintf(w, `{"content":[{"type":"tool_use","id":"call-1","name":"anomaly",`+
				`"input":{"id":%q}}],"stop_reason":"tool_use",`+
				`"usage":{"input_tokens":20,"output_tokens":8}}`, an.ID)
			return
		}
		fmt.Fprint(w, `{"content":[{"type":"text","text":"Explained, using the anomaly tool."}],`+
			`"stop_reason":"end_turn","usage":{"input_tokens":40,"output_tokens":15}}`)
	}))
	defer srv.Close()

	task := crew.Task{ID: 1, Title: "explain it", Anomaly: an.ID, Desk: an.Source}
	a := crew.Analyst{Name: "investigator-aws", State: "active",
		Skills: []string{"driver-classification"}} // figures-read, sql-readonly
	e := estimate{Task: task, Analyst: a, Engine: "anthropic", Model: "claude-x",
		Price: engines.Price{InPerM: 1, OutPerM: 1}, WorstMicros: 5_000, Priced: true}
	run := &runBudget{ceilingMicros: 1_000_000}
	gw := gatewayConfig{URL: srv.URL, Host: "x.test", CeilingUSD: money.Cents(10_000_00)}
	b, path := testBus(t, "x.test", "run-loop-1")

	if err := execute(context.Background(), db, nil, e, 200, run, b, gw); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if round != 2 {
		t.Fatalf("the server was called %d time(s), want 2 (one tool round, one answer)", round)
	}

	as, err := crew.Artifacts(db, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(as) != 1 {
		t.Fatalf("wrote %d drafts, want 1", len(as))
	}
	if !strings.Contains(as[0].Body, "Explained, using the anomaly tool.") {
		t.Errorf("the deliverable is not the second round's text: %q", as[0].Body)
	}

	// Usage is SUMMED across both rounds, which is what the ledger bills.
	events := allEvents(t, path)
	var toolCalls, modelCalls int
	for _, ev := range events {
		data, _ := ev["data"].(map[string]any)
		if data["tool"] == "anomaly" {
			toolCalls++
			if data["outcome"] != "ok" {
				t.Errorf("the dispatched anomaly call did not succeed: %v", data)
			}
		}
		if _, ok := data["cost_micros"]; ok {
			modelCalls++
		}
	}
	if toolCalls != 1 {
		t.Errorf("the bus carries %d tool_call dispatch event(s) for anomaly, want 1", toolCalls)
	}
	if modelCalls != 1 {
		t.Errorf("the bus carries %d model-call event(s), want 1 (one deliverable, one charge)", modelCalls)
	}
}

// The same shape against OpenRouter's OpenAI-style tool_calls, proving the
// loop is not Anthropic-specific: both providers dispatch through the same
// dispatch() and the same catalogue.
func TestAFakeOpenRouterServerAsksForAToolThenAnswers(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "sk-or-stub-not-real")

	db := packetTestDB(t)
	seedLongSeries(t, db, "aws", "ml-platform", "Amazon EC2", 3)

	round := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		round++
		w.Header().Set("Content-Type", "application/json")
		if round == 1 {
			fmt.Fprint(w, `{"choices":[{"message":{"content":null,"tool_calls":[`+
				`{"id":"call_1","type":"function","function":{"name":"series",`+
				`"arguments":"{\"source\":\"aws\",\"team\":\"ml-platform\",\"service\":\"Amazon EC2\"}"}}]},`+
				`"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":18,"completion_tokens":9}}`)
			return
		}
		fmt.Fprint(w, `{"choices":[{"message":{"content":"Explained, using the series tool."},`+
			`"finish_reason":"stop"}],"usage":{"prompt_tokens":35,"completion_tokens":12}}`)
	}))
	defer srv.Close()
	old := openRouterEndpoint
	openRouterEndpoint = srv.URL
	t.Cleanup(func() { openRouterEndpoint = old })

	task := crew.Task{ID: 1, Title: "explain the series"}
	a := crew.Analyst{Name: "an-analyst", State: "active", Skills: []string{"driver-classification"}}
	e := estimate{Task: task, Analyst: a, Engine: "openrouter", Model: "a-model",
		Price: engines.Price{InPerM: 1, OutPerM: 1}, WorstMicros: 5_000, Priced: true}
	run := &runBudget{ceilingMicros: 1_000_000}
	b, path := testBus(t, "x.test", "run-loop-2")

	if err := execute(context.Background(), db, nil, e, 200, run, b, gatewayConfig{}); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if round != 2 {
		t.Fatalf("the server was called %d time(s), want 2", round)
	}

	as, err := crew.Artifacts(db, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(as) != 1 || !strings.Contains(as[0].Body, "Explained, using the series tool.") {
		t.Fatalf("the deliverable is not the second round's text: %#v", as)
	}

	found := false
	for _, ev := range allEvents(t, path) {
		data, _ := ev["data"].(map[string]any)
		if data["tool"] == "series" && data["outcome"] == "ok" {
			found = true
		}
	}
	if !found {
		t.Error("no successful series tool_call event reached the bus")
	}
}

// Sanity on the round-cost helper itself, since every test above depends on
// it summing correctly rather than only asserting on the end-to-end shape.
func TestRoundCostMicrosSumsAcrossRounds(t *testing.T) {
	p := engines.Price{InPerM: 3, OutPerM: 15}
	c1 := roundCostMicros(1000, 200, p)
	c2 := roundCostMicros(500, 100, p)
	total := c1 + c2
	want := roundCostMicros(1000, 200, p) + roundCostMicros(500, 100, p)
	if total != want {
		t.Fatalf("sums do not agree: %d vs %d", total, want)
	}
	if c1 <= 0 || c2 <= 0 {
		t.Errorf("a round with real tokens costs %d and %d, want positive", c1, c2)
	}
}
