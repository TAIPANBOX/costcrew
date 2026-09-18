package main

// costcrew#67: the charge a run records is the gateway's own settlement of
// each call, read off three response headers on every metered 2xx
// (tokenfuse crates/gateway/src/proxy.rs; internal/deliver/settlement.go
// parses them), never the runner's own estimate of what a call cost. Until
// this file existed, tasks.live_micros, run.total(), the tool_call event
// and the plan-ask ledger all carried deliver.ActualMicros -- the
// provider's token counts at THIS repository's own price table -- and a
// run whose one call the gateway settled at 0.05811 printed "Spent 0.0116
// of a 0.15 ceiling".
//
// R3 to R8 compile at the base (cb90412): they reference no API this
// change adds, only estimate, execute, spend, runBudget and the existing
// test helpers. Each goes red with a FIGURE at the base, quoted in the
// implementer's own report, because the base books ActualMicros rather
// than the gateway's settlement.
//
// The issue's own call is the shared fixture across several of these:
// usage {"input_tokens":2874,"output_tokens":200}, headers
// x-fuse-cost-usd: 0.058110, x-fuse-spent-usd: 0.058110, x-fuse-price:
// fallback, and the runner's own price engines.Price{InPerM: 3.00,
// OutPerM: 15.00} (the anthropic/claude-sonnet-5 row). None of these tests
// assert the runner's own figure as a literal (it is float arithmetic,
// 11622 or 11621): they compute it through deliver.ActualMicros and
// require the recorded figure to DIFFER from it and to equal the
// settlement.

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/deliver"
	"github.com/TAIPANBOX/costcrew/internal/engines"
	"github.com/TAIPANBOX/costcrew/internal/money"
)

func liveMicros(t *testing.T, db *sql.DB, task int) int64 {
	t.Helper()
	var n int64
	if err := db.QueryRow(`SELECT live_micros FROM tasks WHERE id=?`, task).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// settledGatewayFixture answers the Anthropic shape gateway_test.go's own
// harness already uses, with the three x-fuse-* response headers set when
// non-empty, so a caller can build the issue's own fixture (or a variant
// of it) without repeating the httptest wiring five times over.
func settledGatewayFixture(t *testing.T, cost, spent, priceBasis string, inTok, outTok int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if cost != "" {
			w.Header().Set("x-fuse-cost-usd", cost)
		}
		if spent != "" {
			w.Header().Set("x-fuse-spent-usd", spent)
		}
		if priceBasis != "" {
			w.Header().Set("x-fuse-price", priceBasis)
		}
		fmt.Fprintf(w, `{"content":[{"type":"text","text":"the deliverable"}],`+
			`"stop_reason":"end_turn","usage":{"input_tokens":%d,"output_tokens":%d}}`, inTok, outTok)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestTheChargeRecordedIsTheGatewaysSettlement is the incident replayed:
// the issue's own 2874/200 call, settled by the gateway at 0.05811 against
// the runner's own 3/15 price table, must land on tasks.live_micros,
// run.total(), the per-task console line and the bus event, not the
// runner's own estimate.
func TestTheChargeRecordedIsTheGatewaysSettlement(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-stub-not-real")
	srv := settledGatewayFixture(t, "0.058110", "0.058110", "fallback", 2874, 200)

	db, task, analyst := runnerDB(t)
	b, path := testBus(t, "gcp.taipanbox.local", "crew-294")
	run := &runBudget{ceilingMicros: 150_000}
	price := engines.Price{InPerM: 3.00, OutPerM: 15.00}
	e := estimate{Task: task, Analyst: analyst, Engine: "anthropic",
		Model: "claude-sonnet-5", Price: price, WorstMicros: 11_622, Priced: true}
	gw := gatewayConfig{URL: srv.URL, Host: "gcp.taipanbox.local", CeilingUSD: money.Cents(15)}

	own := deliver.ActualMicros(2874, 200, price)
	if own == 58110 {
		t.Fatal("the fixture's own runner price happens to equal the gateway's settlement; it proves nothing")
	}

	var err error
	out := captureStdout(t, func() {
		err = execute(context.Background(), db, nil, e, 200, run, b, gw)
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	if got := liveMicros(t, db, task.ID); got != 58110 {
		t.Errorf("tasks.live_micros = %d, want 58110: the gateway settled the call at "+
			"0.05811 and the runner reported its own estimate as the charge", got)
	}
	if got := run.total(); got != 58110 {
		t.Errorf("run.total() = %d, want 58110", got)
	}
	if !strings.Contains(out, "cost 0.0581 settled by the gateway at its fallback price (x-fuse-price: fallback)") {
		t.Errorf("the per-task line does not name the settlement and its fallback price:\n%s", out)
	}

	ev := oneEvent(t, path)
	data, _ := ev["data"].(map[string]any)
	if data["cost_micros"] != float64(58110) {
		t.Errorf("bus cost_micros = %v, want 58110", data["cost_micros"])
	}
	if data["settled"] != true {
		t.Errorf("bus settled = %v, want true", data["settled"])
	}
	if data["price_basis"] != "fallback" {
		t.Errorf("bus price_basis = %v, want \"fallback\"", data["price_basis"])
	}
	if data["priced_micros"] != float64(own) {
		t.Errorf("bus priced_micros = %v, want %d (the runner's own estimate, kept beside "+
			"the charge for reconciliation)", data["priced_micros"], own)
	}
}

// TestTheSummaryLineReadsTheSettledTotalAndTheGatewaysOwnRunTotal is the
// run-level half of the same incident: the summary line must read the
// settled total and, when the gateway said one, its own run total -- never
// the runner's own estimate of what was spent.
func TestTheSummaryLineReadsTheSettledTotalAndTheGatewaysOwnRunTotal(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-stub-not-real")
	srv := settledGatewayFixture(t, "0.058110", "0.058110", "fallback", 2874, 200)

	db, tasks, analyst := runnerTasks(t, 1)
	price := engines.Price{InPerM: 3.00, OutPerM: 15.00}
	e := estimate{Task: tasks[0], Analyst: analyst, Engine: "anthropic",
		Model: "claude-sonnet-5", Price: price, WorstMicros: 11_622, Priced: true}
	gw := gatewayConfig{URL: srv.URL, Host: "gcp.taipanbox.local", CeilingUSD: money.Cents(15)}

	var err error
	out := captureStdout(t, func() {
		err = spend(db, nil, []estimate{e}, 200, money.Cents(15), 0, bus{}, gw)
	})
	if err != nil {
		t.Fatalf("spend: %v", err)
	}
	wantLine := "Spent 0.0581 of a 0.15 ceiling: 1 of 1 task(s) settled by the gateway, " +
		"whose own run total is 0.0581."
	if !strings.Contains(out, wantLine) {
		t.Errorf("the summary line reads:\n%s\nwant the settled total and the gateway's "+
			"own run total; the run printed its own estimate as spent", out)
	}
	if !strings.Contains(out, "The board now carries 0.06 against these tasks") {
		t.Errorf("the board's own line is missing or does not match the settled total:\n%s", out)
	}
}

// TestFourTasksUnderOneRunIdAreNotOverCountedByTheCumulativeHeader is
// decision 1: the per-call charge is x-fuse-cost-usd, never the RUN's
// cumulative x-fuse-spent-usd, because tools/run shares one run id across
// every task of an invocation and runs atOnce=4 of them at once, so
// recording the cumulative header per task would book task N with every
// earlier task's own calls.
func TestFourTasksUnderOneRunIdAreNotOverCountedByTheCumulativeHeader(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-stub-not-real")

	var mu sync.Mutex
	k := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		k++
		n := k
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("x-fuse-cost-usd", "0.005310")
		w.Header().Set("x-fuse-spent-usd", fmt.Sprintf("0.%06d", n*5310))
		w.Header().Set("x-fuse-price", "known")
		fmt.Fprint(w, `{"content":[{"type":"text","text":"the deliverable"}],`+
			`"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":5}}`)
	}))
	t.Cleanup(srv.Close)

	db, tasks, analyst := runnerTasks(t, 4)
	price := engines.Price{InPerM: 1, OutPerM: 1}
	ests := make([]estimate, 0, 4)
	for _, task := range tasks {
		ests = append(ests, estimate{Task: task, Analyst: analyst, Engine: "anthropic",
			Model: "claude-x", Price: price, WorstMicros: 1_000, Priced: true})
	}
	gw := gatewayConfig{URL: srv.URL, Host: "gcp.taipanbox.local", CeilingUSD: money.Cents(100)}

	var err error
	out := captureStdout(t, func() {
		err = spend(db, nil, ests, 100, money.Cents(100), 0, bus{}, gw)
	})
	if err != nil {
		t.Fatalf("spend: %v", err)
	}

	var sum int64
	for _, task := range tasks {
		got := liveMicros(t, db, task.ID)
		sum += got
		if got != 5310 {
			t.Errorf("task %d recorded %d micros; the gateway settled its own call at "+
				"5310, and the cumulative x-fuse-spent-usd values (5310, 10620, 15930, "+
				"21240) must never be booked per task", task.ID, got)
		}
	}
	if sum != 21_240 {
		t.Errorf("the sum of live_micros is %d, want 21240", sum)
	}

	var cents int64
	for _, task := range tasks {
		cents += spentOn(t, db, task.ID)
	}
	if cents != 3 {
		t.Errorf("the board carries %d cents for a run that cost 0.021240, want 3: "+
			"rounded once over the run, not per call (4) and not the cumulative header "+
			"summed (6)", cents)
	}

	if !strings.Contains(out, "4 of 4 task(s) settled by the gateway, whose own run total is 0.0212.") {
		t.Errorf("the summary line does not name the gateway's own run total:\n%s", out)
	}
}

// TestAHostileSettlementFallsBackToTheRunnersOwnPriceAndSaysSo is decision
// 5's per-task half: a header the runner cannot read must never become a
// charge, and the console line must say so.
func TestAHostileSettlementFallsBackToTheRunnersOwnPriceAndSaysSo(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-stub-not-real")
	price := engines.Price{InPerM: 1, OutPerM: 1}

	cases := []struct {
		name string
		set  func(h http.Header)
	}{
		{"1e300", func(h http.Header) { h.Set("x-fuse-cost-usd", "1e300") }},
		{"negative", func(h http.Header) { h.Set("x-fuse-cost-usd", "-1") }},
		{"not-a-number", func(h http.Header) { h.Set("x-fuse-cost-usd", "abc") }},
		{"over-the-cap", func(h http.Header) { h.Set("x-fuse-cost-usd", "1000000.000001") }},
		{"absent", func(h http.Header) {}},
		{"duplicate", func(h http.Header) {
			h.Add("x-fuse-cost-usd", "0.058110")
			h.Add("x-fuse-cost-usd", "0.058110")
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				c.set(w.Header())
				fmt.Fprint(w, `{"content":[{"type":"text","text":"the deliverable"}],`+
					`"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":5}}`)
			}))
			t.Cleanup(srv.Close)

			db, task, analyst := runnerDB(t)
			run := &runBudget{ceilingMicros: 1_000_000}
			e := estimate{Task: task, Analyst: analyst, Engine: "anthropic",
				Model: "claude-x", Price: price, WorstMicros: 1_000, Priced: true}
			gw := gatewayConfig{URL: srv.URL, Host: "gcp.taipanbox.local", CeilingUSD: money.Cents(100)}

			var err error
			out := captureStdout(t, func() {
				err = execute(context.Background(), db, nil, e, 100, run, bus{}, gw)
			})
			if err != nil {
				t.Fatalf("execute: %v", err)
			}
			if got, want := liveMicros(t, db, task.ID), deliver.ActualMicros(10, 5, price); got != want {
				t.Errorf("live_micros = %d, want %d (the runner's own price)", got, want)
			}
			if !strings.Contains(out, "priced by the runner: no settlement header") {
				t.Errorf("the per-task line for x-fuse-cost-usd case %q reads:\n%s\nwant "+
					"\"priced by the runner: no settlement header\"", c.name, out)
			}
		})
	}
}

// TestWithNoGatewayTheChargeIsTheRunnersOwnPriceAndTheLineSaysSo is the
// negative control invariant 51 names: a direct call, with no gateway
// configured at all, is unchanged.
func TestWithNoGatewayTheChargeIsTheRunnersOwnPriceAndTheLineSaysSo(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "sk-or-stub-not-real")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"the deliverable"},`+
			`"finish_reason":"stop"}],"usage":{"prompt_tokens":18,"completion_tokens":9}}`)
	}))
	t.Cleanup(srv.Close)
	old := openRouterEndpoint
	openRouterEndpoint = srv.URL
	t.Cleanup(func() { openRouterEndpoint = old })

	db, task, analyst := runnerDB(t)
	price := engines.Price{InPerM: 1, OutPerM: 1}
	run := &runBudget{ceilingMicros: 1_000_000}
	e := estimate{Task: task, Analyst: analyst, Engine: "openrouter",
		Model: "a-model", Price: price, WorstMicros: 1_000, Priced: true}

	var err error
	out := captureStdout(t, func() {
		err = execute(context.Background(), db, nil, e, 100, run, bus{}, gatewayConfig{})
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got, want := liveMicros(t, db, task.ID), deliver.ActualMicros(18, 9, price); got != want {
		t.Errorf("live_micros = %d, want %d (the runner's own price, unchanged with no gateway)", got, want)
	}
	if !strings.Contains(out, "priced by the runner: no settlement header") {
		t.Errorf("the per-task line does not say there was no settlement to read:\n%s", out)
	}
}

// TestASettlementAboveTheReservationStillCountsAgainstTheCeiling is
// decision 10: settle books the settlement even above the reservation, so
// the next reserve is checked against what was actually spent and the
// ceiling holds forward.
func TestASettlementAboveTheReservationStillCountsAgainstTheCeiling(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-stub-not-real")

	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("x-fuse-cost-usd", "0.015000")
		fmt.Fprint(w, `{"content":[{"type":"text","text":"the deliverable"}],`+
			`"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":5}}`)
	}))
	t.Cleanup(srv.Close)

	db, tasks, analyst := runnerTasks(t, 2)
	run := &runBudget{ceilingMicros: 20_000} // fits two reservations of 6,000 at the runner's own price
	price := engines.Price{InPerM: 1, OutPerM: 1}
	gw := gatewayConfig{URL: srv.URL, Host: "gcp.taipanbox.local", CeilingUSD: money.Cents(100)}
	e1 := estimate{Task: tasks[0], Analyst: analyst, Engine: "anthropic",
		Model: "claude-x", Price: price, WorstMicros: 1_000, Priced: true}
	e2 := estimate{Task: tasks[1], Analyst: analyst, Engine: "anthropic",
		Model: "claude-x", Price: price, WorstMicros: 1_000, Priced: true}

	if err := execute(context.Background(), db, nil, e1, 100, run, bus{}, gw); err != nil {
		t.Fatalf("the first task was refused: %v", err)
	}
	if got := run.total(); got != 15_000 {
		t.Errorf("run.total() = %d, want 15000 (the gateway's own settlement of the first call)", got)
	}

	err := execute(context.Background(), db, nil, e2, 100, run, bus{}, gw)
	if !isRefusal(err) {
		t.Errorf("the second task was let through (%d calls, err %v): the ceiling was "+
			"checked against the runner's own price of the first call, not the gateway's "+
			"settlement of 0.015000", calls, err)
	}
	if calls != 1 {
		t.Errorf("the server was called %d time(s), want exactly 1: the second task must "+
			"be refused before any call is made", calls)
	}
}
