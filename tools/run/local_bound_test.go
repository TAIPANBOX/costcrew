package main

// The local engine's bound, its route, and its failures: the token ceiling and
// how it is reserved and settled, the gateway rule (invariant 54) for this
// engine, an unreachable server, the operator's flags, and the hostile things
// a server can say back.

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/deliver"
	"github.com/TAIPANBOX/costcrew/internal/engines"
	"github.com/TAIPANBOX/costcrew/internal/money"
)

// --------------------------------------------------------------- token ceiling

// Reserved before the call, as money is: two reservations that each fit alone
// but not together cannot both be in flight.
func TestTokenReservationsAreHeldWhileInFlight(t *testing.T) {
	r := &runBudget{ceilingMicros: 1_000_000, tokenCeiling: 1_000}
	if err := r.reserveTokens(600); err != nil {
		t.Fatal(err)
	}
	if err := r.reserveTokens(600); err == nil {
		t.Fatal("two 600-token reservations were both accepted under a 1000-token ceiling")
	}
	if err := r.reserveTokens(400); err != nil {
		t.Errorf("a reservation that exactly fills the ceiling was refused: %v", err)
	}
	if err := r.reserveTokens(1); err == nil {
		t.Error("a reservation past a full ceiling was accepted")
	}
	// Settling puts back what was not used and books what was.
	r.settleTokens(600, 100)
	if got := r.tokensUsed(); got != 100 {
		t.Errorf("tokens used = %d after settling 100 of a 600 reservation", got)
	}
	if err := r.reserveTokens(500); err != nil {
		t.Errorf("the unused part of a settled reservation did not come back: %v", err)
	}
	// With no ceiling, nothing is tracked and nothing is refused.
	none := &runBudget{}
	if err := none.reserveTokens(math.MaxInt32); err != nil {
		t.Errorf("a run with no token ceiling refused a reservation: %v", err)
	}
}

// What a task actually used is booked even above what was reserved, so the NEXT
// task is checked against the truth: the same property the money ceiling has.
func TestTheTokenCeilingRefusesTheNextTaskOnceTheLastOneUsedIt(t *testing.T) {
	big := `{"choices":[{"message":{"content":"the deliverable"}}],` +
		`"usage":{"prompt_tokens":60000,"completion_tokens":1000}}`
	srv := newModelServer(t, scriptedRound{body: big})
	db, tasks, analyst := runnerTasks(t, 2)
	b, _ := testBus(t, "gcp.taipanbox.local", "crew-1")
	e := localEstimate(tasks[0], analyst, 0, 0)
	// A nonzero money reservation, so that a refusal which forgets to give the
	// money back is visible: at 0 the leak reserves nothing and shows nothing.
	e.WorstMicros = 1_000
	worst := reservedWorstTokens(e, 100)
	run := &runBudget{ceilingMicros: 50_000_000, tokenCeiling: worst + 50_000}
	gw := gatewayConfig{ModelURL: srv.URL + "/v1", Host: "gcp.taipanbox.local", CeilingUSD: money.Cents(5000),
		MaxRunTokens: int(worst + 50_000)}

	captureStdout(t, func() {
		if err := execute(context.Background(), db, nil, e, 100, run, b, gw); err != nil {
			t.Fatalf("the first task: %v", err)
		}
	})
	if run.tokensUsed() != 61_000 {
		t.Fatalf("tokens used = %d, want 61000", run.tokensUsed())
	}

	e2 := localEstimate(tasks[1], analyst, 0, 0)
	e2.WorstMicros = 1_000
	err := execute(context.Background(), db, nil, e2, 100, run, b, gw)
	if !isRefusal(err) {
		t.Fatalf("the second task: err = %v, want a refusal: the first used what the second's "+
			"reservation needed", err)
	}
	if !strings.Contains(err.Error(), "token ceiling") {
		t.Errorf("the refusal does not say it was the token ceiling: %v", err)
	}
	if n := len(srv.chatPosts()); n != 1 {
		t.Errorf("the server saw %d chat request(s): a refused task must make none", n)
	}
	if run.reserved != 0 || run.tokensReserved != 0 {
		t.Errorf("reserved %d micros and %d tokens after a refused task: a refusal reserves nothing",
			run.reserved, run.tokensReserved)
	}
}

// The whole run is checked before the first call, as the money ceiling is.
func TestTheWholeRunsWorstCaseOverTheTokenCeilingIsRefusedBeforeAnyCall(t *testing.T) {
	srv := newModelServer(t, scriptedRound{body: localAnswer})
	db, tasks, analyst := runnerTasks(t, 2)
	ests := []estimate{localEstimate(tasks[0], analyst, 0, 0), localEstimate(tasks[1], analyst, 0, 0)}
	one := reservedWorstTokens(ests[0], 200)
	gw := gatewayConfig{ModelURL: srv.URL + "/v1", Host: "gcp.taipanbox.local", CeilingUSD: money.Cents(500),
		MaxRunTokens: int(one)} // fits one task, not two

	var err error
	captureStdout(t, func() {
		err = spend(db, nil, ests, 200, money.Cents(500), 0, bus{run: "crew-1"}, gw)
	})
	if err == nil || !strings.Contains(err.Error(), "-max-run-tokens") {
		t.Fatalf("err = %v, want the whole-run token refusal", err)
	}
	if n := len(srv.seen()); n != 0 {
		t.Errorf("the server was contacted %d time(s) before the refusal", n)
	}
	// One task fits exactly: the boundary is inclusive, as the money one is.
	gw.MaxRunTokens = int(2 * one)
	captureStdout(t, func() {
		err = spend(db, nil, ests, 200, money.Cents(500), 0, bus{run: "crew-1"}, gw)
	})
	if err != nil {
		t.Errorf("a run whose worst case exactly equals the ceiling was refused: %v", err)
	}
}

// reservedWorstTokens is the money bound's twin and counts the same things.
func TestReservedWorstTokensCountsTheLoopThePromptTheCatalogueAndTheCap(t *testing.T) {
	e := estimate{Engine: "local", PromptTokens: 1000, CatalogueTokens: 4538}
	if got, want := reservedWorstTokens(e, 2000), int64(6*(1000+4538+2000)); got != want {
		t.Errorf("reservedWorstTokens = %d, want %d", got, want)
	}
	e.Engine = "bedrock"
	e.CatalogueTokens = 0
	if got, want := reservedWorstTokens(e, 2000), int64(1000+2000); got != want {
		t.Errorf("a single-call engine reserves %d, want %d", got, want)
	}
}

// Priced, the local engine is bounded by money like every other, and needs no
// token ceiling.
func TestAPricedLocalRunNeedsNoTokenCeilingAndMoneyBoundsIt(t *testing.T) {
	srv := newModelServer(t, scriptedRound{body: localAnswer})
	db, tasks, analyst := runnerTasks(t, 1)
	e := localEstimate(tasks[0], analyst, 1000, 2000)
	e.WorstMicros = deliver.WorstCaseMicros(e.PromptTokens+e.CatalogueTokens, 200, e.Price)
	gw := gatewayConfig{ModelURL: srv.URL + "/v1", Host: "gcp.taipanbox.local", CeilingUSD: money.Cents(1)}

	if err := localPreflight(gw, []estimate{e}, 200); err != nil {
		t.Fatalf("a priced local task was refused for want of a token ceiling: %v", err)
	}
	// And the money ceiling does the refusing: one cent against a worst case
	// of several dollars.
	var err error
	captureStdout(t, func() {
		err = spend(db, nil, []estimate{e}, 200, money.Cents(1), 0, bus{run: "crew-1"}, gw)
	})
	if err == nil || !strings.Contains(err.Error(), "refused before the first call") {
		t.Errorf("err = %v, want the money preflight to refuse", err)
	}
	if n := len(srv.seen()); n != 0 {
		t.Errorf("the server was contacted %d time(s)", n)
	}
	// A price on EITHER side is enough: every call reserves something.
	for _, p := range []engines.Price{{InPerM: 0.5}, {OutPerM: 0.5}} {
		e.Price = p
		if err := localPreflight(gw, []estimate{e}, 200); err != nil {
			t.Errorf("price %+v refused: %v", p, err)
		}
	}
}

// ------------------------------------------------------------------- the route

// Invariant 54, for this engine: with a gateway configured and none that
// fronts the OpenAI wire, a local task is refused before the first call, and
// the operator's server is not used instead.
func TestALocalTaskWithOnlyAnAnthropicGatewayIsRefusedAndNothingIsCalled(t *testing.T) {
	own := newModelServer(t, scriptedRound{body: localAnswer})
	anthropicGW := newModelServer(t, scriptedRound{body: localAnswer})
	db, tasks, analyst := runnerTasks(t, 1)
	e := localEstimate(tasks[0], analyst, 1000, 2000)
	e.WorstMicros = 1_000
	gw := gatewayConfig{URL: anthropicGW.URL, ModelURL: own.URL + "/v1", Host: "gcp.taipanbox.local",
		CeilingUSD: money.Cents(500), MaxRunTokens: 1_000_000}

	// The run-level preflight.
	var err error
	captureStdout(t, func() {
		err = spend(db, nil, []estimate{e}, 200, money.Cents(500), 0, bus{run: "crew-1"}, gw)
	})
	if !errors.Is(err, deliver.ErrNoGatewayRoute) {
		t.Fatalf("spend: err = %v, want one wrapping ErrNoGatewayRoute", err)
	}
	if !strings.Contains(err.Error(), "1 on local") {
		t.Errorf("the refusal does not name how many tasks are on which engine: %v", err)
	}

	// And execute() on its own, which is what a caller that skips the
	// preflight would reach.
	run := &runBudget{ceilingMicros: 5_000_000}
	err = execute(context.Background(), db, nil, e, 200, run, bus{run: "crew-1"}, gw)
	if !errors.Is(err, deliver.ErrNoGatewayRoute) {
		t.Fatalf("execute: err = %v, want one wrapping ErrNoGatewayRoute", err)
	}
	if n := len(own.seen()) + len(anthropicGW.seen()); n != 0 {
		t.Errorf("%d request(s) were made: the local engine went around the gateway rule", n)
	}
	if run.reserved != 0 {
		t.Errorf("reserved = %d after a refused task", run.reserved)
	}
}

// Through the OpenAI-shaped gateway the call is metered, the charge is the
// gateway's settlement (invariant 51), and the operator's server is never named
// by the call.
func TestALocalTaskThroughTheOpenAIGatewayIsChargedItsSettlement(t *testing.T) {
	t.Setenv("COSTCREW_MODEL_KEY", "")
	gwSrv := newModelServer(t,
		scriptedRound{body: localToolUse, cost: "0.100000", spent: "0.100000", price: "known"},
		scriptedRound{body: localAnswer, cost: "0.050000", spent: "0.150000", price: "known"})
	own := newModelServer(t, scriptedRound{body: localAnswer})

	db, task, analyst := runnerDB(t)
	task.Budget = money.Cents(500)
	b, path := testBus(t, "gcp.taipanbox.local", "crew-2026-w40")
	run := &runBudget{ceilingMicros: 5_000_000}
	gw := gatewayConfig{OpenAIURL: gwSrv.URL, ModelURL: own.URL + "/v1", Host: "gcp.taipanbox.local",
		CeilingUSD: money.Cents(1000)}
	e := localEstimate(task, analyst, 1000, 2000)
	e.WorstMicros = 1_000

	var err error
	out := captureStdout(t, func() { err = execute(context.Background(), db, nil, e, 100, run, b, gw) })
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if n := len(own.seen()); n != 0 {
		t.Errorf("the operator's server was called %d time(s) with a gateway configured", n)
	}
	reqs := gwSrv.chatPosts()
	if len(reqs) != 2 {
		t.Fatalf("the gateway saw %d chat request(s), want 2", len(reqs))
	}
	for i, r := range reqs {
		if r.Path != "/v1/chat/completions" {
			t.Errorf("round %d path %q", i+1, r.Path)
		}
		if got := r.Header.Get("x-fuse-run-id"); got != "crew-2026-w40" {
			t.Errorf("round %d x-fuse-run-id %q", i+1, got)
		}
		if got, want := r.Header.Get("x-fuse-agent-id"), "agent://gcp.taipanbox.local/y.mercer"; got != want {
			t.Errorf("round %d x-fuse-agent-id %q, want %q", i+1, got, want)
		}
		if got := r.Header.Get("x-fuse-budget-usd"); got != "5.00" {
			t.Errorf("round %d x-fuse-budget-usd %q, want 5.00", i+1, got)
		}
	}
	if got := liveMicros(t, db, task.ID); got != 150_000 {
		t.Errorf("tasks.live_micros = %d, want 150000: the sum of the gateway's two settlements, "+
			"not the operator's own price", got)
	}
	if !strings.Contains(out, "settled by the gateway") {
		t.Errorf("the per-task line does not say the gateway settled it:\n%s", out)
	}
	var basis any
	for _, ev := range allEvents(t, path) {
		if d, _ := ev["data"].(map[string]any); d != nil {
			if v, ok := d["price_basis"]; ok {
				basis = v
			}
		}
	}
	if basis != "local" {
		t.Errorf("bus price_basis = %v, want \"local\" even though the gateway said \"known\"", basis)
	}
}

// A 402 from the gateway in front of the local model stops the run.
func TestA402FromTheGatewayInFrontOfTheLocalModelStopsTheRun(t *testing.T) {
	gwSrv := newModelServer(t, scriptedRound{status: http.StatusPaymentRequired, body: budgetRefusal})
	db, task, analyst := runnerDB(t)
	b, _ := testBus(t, "gcp.taipanbox.local", "crew-1")
	run := &runBudget{ceilingMicros: 1_000_000}
	gw := gatewayConfig{OpenAIURL: gwSrv.URL, Host: "gcp.taipanbox.local", CeilingUSD: money.Cents(500)}
	e := localEstimate(task, analyst, 1000, 2000)
	e.WorstMicros = 20_000

	err := execute(context.Background(), db, nil, e, 100, run, b, gw)
	if !isRefusal(err) || !strings.Contains(err.Error(), "per-run budget exceeded") {
		t.Fatalf("err = %v, want a refusal carrying the gateway's reason", err)
	}
	if run.reserved != 0 {
		t.Errorf("reserved = %d, want the reservation back", run.reserved)
	}
}

// ------------------------------------------------------------------ unreachable

// A server that is down stops the run at start with one line naming it, and
// blocks no task: nothing was asked of a model, so no task failed.
func TestAnUnreachableLocalServerStopsTheRunAtStartWithOneLine(t *testing.T) {
	base := closedModelURL(t)
	db, tasks, analyst := runnerTasks(t, 2)
	ests := []estimate{localEstimate(tasks[0], analyst, 1, 1), localEstimate(tasks[1], analyst, 1, 1)}
	gw := gatewayConfig{ModelURL: base, Host: "gcp.taipanbox.local", CeilingUSD: money.Cents(5000)}

	var err error
	captureStdout(t, func() {
		err = spend(db, nil, ests, 200, money.Cents(5000), 0, bus{run: "crew-1"}, gw)
	})
	if err == nil {
		t.Fatal("a run against a server that is not there started")
	}
	if !strings.Contains(err.Error(), base) {
		t.Errorf("the refusal does not name the URL %q: %v", base, err)
	}
	if strings.ContainsAny(err.Error(), "\n\r") || strings.Contains(err.Error(), "Post \"") {
		t.Errorf("the refusal is not one clean line: %q", err.Error())
	}
	for _, task := range tasks {
		if st := taskState(t, db, task.ID); st != "queued" {
			t.Errorf("task %d is %q: a server that was never reached must not block a task", task.ID, st)
		}
	}
}

// A server that goes away MID-run is still one line, from the round itself.
func TestAServerThatGoesAwayMidRunIsOneLineFromTheRound(t *testing.T) {
	base := closedModelURL(t)
	db, task, analyst := runnerDB(t)
	b, _ := testBus(t, "gcp.taipanbox.local", "crew-1")
	run := &runBudget{ceilingMicros: 5_000_000}
	gw := gatewayConfig{ModelURL: base, Host: "gcp.taipanbox.local", CeilingUSD: money.Cents(500)}
	e := localEstimate(task, analyst, 1000, 2000)
	e.WorstMicros = 1_000

	err := execute(context.Background(), db, nil, e, 100, run, b, gw)
	if err == nil {
		t.Fatal("no error from a server that is not there")
	}
	if isRefusal(err) {
		t.Error("a server that did not answer was read as a budget refusal")
	}
	if !strings.Contains(err.Error(), base) || strings.ContainsAny(err.Error(), "\n\r") ||
		strings.Contains(err.Error(), "Post \"") {
		t.Errorf("not one line naming the URL: %q", err.Error())
	}
	if run.reserved != 0 || run.total() != 0 {
		t.Errorf("reserved %d, spent %d after a call that never reached a model", run.reserved, run.total())
	}
}

// With a local task and nowhere to send it, the run is refused and says how to
// give it somewhere.
func TestALocalTaskWithNoServerAndNoGatewayIsRefusedAtStart(t *testing.T) {
	db, tasks, analyst := runnerTasks(t, 1)
	e := localEstimate(tasks[0], analyst, 1, 1)
	var err error
	captureStdout(t, func() {
		err = spend(db, nil, []estimate{e}, 200, money.Cents(500), 0, bus{run: "crew-1"},
			gatewayConfig{Host: "x.test", CeilingUSD: money.Cents(500)})
	})
	if err == nil || !strings.Contains(err.Error(), "-model-url") || !strings.Contains(err.Error(), "-gateway-openai") {
		t.Errorf("err = %v, want a refusal naming both ways to give it a server", err)
	}
}

// -------------------------------------------------------------------- the key

// The optional key is a bearer token and goes nowhere else: not to stdout, not
// to stderr, not into any error, on a success or on every failure.
func TestTheModelKeyIsNeverPrintedOrReturned(t *testing.T) {
	const key = "sk-local-secret-4d2e"
	t.Setenv("COSTCREW_MODEL_KEY", key)
	for name, body := range map[string]scriptedRound{
		"success":      {body: localAnswer},
		"no usage":     {body: localAnswerNoUsage},
		"server error": {status: 500, body: `{"error":"model not loaded"}`},
		"garbage":      {body: "<html>"},
	} {
		srv := newModelServer(t, body)
		db, task, analyst := runnerDB(t)
		b, _ := testBus(t, "gcp.taipanbox.local", "crew-1")
		run := &runBudget{ceilingMicros: 5_000_000, tokenCeiling: 1_000_000}
		gw := gatewayConfig{ModelURL: srv.URL + "/v1", Host: "gcp.taipanbox.local",
			CeilingUSD: money.Cents(500), MaxRunTokens: 1_000_000}
		e := localEstimate(task, analyst, 1000, 2000)
		e.WorstMicros = 1_000

		var err error
		var errOut string
		out := captureStdout(t, func() {
			errOut = captureStderr(t, func() {
				err = execute(context.Background(), db, nil, e, 100, run, b, gw)
			})
		})
		if got := srv.chatPosts()[0].Header.Get("Authorization"); got != "Bearer "+key {
			t.Errorf("%s: the server was sent Authorization %q", name, got)
		}
		for where, text := range map[string]string{"stdout": out, "stderr": errOut} {
			if strings.Contains(text, key) {
				t.Errorf("%s: the key reached %s", name, where)
			}
		}
		if err != nil && strings.Contains(err.Error(), key) {
			t.Errorf("%s: the key is in the error: %v", name, err)
		}
	}
	// And the dry run, which prints the estimate and the price table.
	configureLocal(t, "m", 0, 0)
	text := engines.PriceTable()
	if strings.Contains(text, key) {
		t.Error("the key is in the price table")
	}
}

// ----------------------------------------------------------------- hostile

// Whatever a server says back blocks at most the one task, with a bounded
// message, and returns every reservation.
func TestAHostileLocalResponseFailsOneTaskWithABoundedMessage(t *testing.T) {
	huge := strings.Repeat("z", 3<<20)
	for name, rd := range map[string]scriptedRound{
		"empty":         {body: ""},
		"html":          {body: "<html>gateway timeout</html>"},
		"truncated":     {body: `{"choices":[{"message":{"content":"ab`},
		"no choices":    {body: `{"choices":[]}`},
		"empty content": {body: `{"choices":[{"message":{"content":"   "},"finish_reason":"length"}]}`},
		"giant 500":     {status: 500, body: huge},
		"giant 200":     {body: `{"choices":[{"message":{"content":"` + huge + `"}}]}`},
		"404":           {status: 404, body: "404 page not found"},
	} {
		srv := newModelServer(t, rd)
		db, task, analyst := runnerDB(t)
		b, _ := testBus(t, "gcp.taipanbox.local", "crew-1")
		run := &runBudget{ceilingMicros: 5_000_000, tokenCeiling: 50_000_000}
		gw := gatewayConfig{ModelURL: srv.URL + "/v1", Host: "gcp.taipanbox.local",
			CeilingUSD: money.Cents(500), MaxRunTokens: 50_000_000}
		e := localEstimate(task, analyst, 1000, 2000)
		e.WorstMicros = 1_000

		var err error
		captureStderr(t, func() {
			captureStdout(t, func() { err = execute(context.Background(), db, nil, e, 100, run, b, gw) })
		})
		if name == "giant 200" {
			// A large answer is an answer.
			if err != nil {
				t.Errorf("%s: %v", name, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("%s: no error", name)
			continue
		}
		if len(err.Error()) > 400 {
			t.Errorf("%s: the message is %d bytes", name, len(err.Error()))
		}
		if isRefusal(err) {
			t.Errorf("%s: a bad answer was read as a budget refusal and would stop the run", name)
		}
		if run.reserved != 0 || run.tokensReserved != 0 {
			t.Errorf("%s: %d micros and %d tokens still reserved after the failure",
				name, run.reserved, run.tokensReserved)
		}
	}
}

// ------------------------------------------------------------------- pricing

// An unconfigured engine is refused for the thing that is missing, and a
// configured one is priced by the operator.
func TestALocalTaskIsPricedByTheOperatorsModelAndPrice(t *testing.T) {
	task := crew.Task{Title: "Explain the move", Goal: "say why", Budget: money.Cents(1500)}
	an := crew.Analyst{Name: "investigator-aws", Engine: "local", State: "active"}

	engines.ResetLocal()
	e := price(nil, task, an, 2000)
	if !e.Refused || !strings.Contains(e.Verdict, "-model-name") {
		t.Errorf("an unconfigured local task: refused=%v verdict=%q, want a refusal naming -model-name",
			e.Refused, e.Verdict)
	}
	if strings.Contains(e.Verdict, "no price is known") {
		t.Errorf("the verdict blames a missing price list for an engine whose price is the operator's: %q", e.Verdict)
	}

	configureLocal(t, "llama3.1:8b", 0.5, 1.5)
	e = price(nil, task, an, 2000)
	if e.Refused || !e.Priced {
		t.Fatalf("a configured local task: refused=%v priced=%v verdict=%q", e.Refused, e.Priced, e.Verdict)
	}
	if e.Model != "llama3.1:8b" || e.Price.InPerM != 0.5 || e.Price.OutPerM != 1.5 {
		t.Errorf("model %q price %+v, want the operator's", e.Model, e.Price)
	}
	if e.CatalogueTokens != deliver.ToolCatalogueTokens("local") || e.CatalogueTokens == 0 {
		t.Errorf("CatalogueTokens = %d: a local task is sent the OpenAI catalogue on every round", e.CatalogueTokens)
	}
	want := deliver.WorstCaseMicros(e.PromptTokens+e.CatalogueTokens, 2000, e.Price)
	if e.WorstMicros != want || e.WorstMicros == 0 {
		t.Errorf("WorstMicros = %d, want %d", e.WorstMicros, want)
	}
	if got := reservedWorstCase(e); got != want*6 {
		t.Errorf("reservedWorstCase = %d, want the six rounds of a tool loop: %d", got, want*6)
	}

	// Priced at zero it is still priced (and so still refused-or-run by the
	// bound that applies to it), never "free, nothing here runs it".
	configureLocal(t, "llama3.1:8b", 0, 0)
	e = price(nil, task, an, 2000)
	if e.Refused || !e.Priced || e.WorstMicros != 0 {
		t.Errorf("a zero-priced local task: refused=%v priced=%v worst=%d verdict=%q",
			e.Refused, e.Priced, e.WorstMicros, e.Verdict)
	}
}

func TestTheDryRunSaysWhichLocalTasksMoneyCannotBound(t *testing.T) {
	task := crew.Task{Title: "Explain the move", Goal: "say why", Budget: money.Cents(1500)}
	an := crew.Analyst{Name: "investigator-aws", Engine: "local", State: "active"}
	configureLocal(t, "llama3.1:8b", 0, 0)
	e := price(nil, task, an, 2000)
	out := captureStdout(t, func() { report(nil, []estimate{e}, 2000, 0, false) })
	if !strings.Contains(out, "on the local engine at a price of 0") || !strings.Contains(out, "-max-run-tokens") {
		t.Errorf("the dry run does not warn that a zero-priced local task has no money bound:\n%s", out)
	}
	if !strings.Contains(out, "local/llama3.1:8b") || !strings.Contains(out, "operator-set") {
		t.Errorf("the dry run's price table does not name the operator's price:\n%s", out)
	}

	configureLocal(t, "llama3.1:8b", 0.1, 0.1)
	e = price(nil, task, an, 2000)
	out = captureStdout(t, func() { report(nil, []estimate{e}, 2000, 0, false) })
	if strings.Contains(out, "money cannot bound") {
		t.Errorf("a priced local task is warned about as if it were unbounded:\n%s", out)
	}
}

// ----------------------------------------------------------- the flags

func TestTheLocalOptionsAreValidatedBeforeAnythingElse(t *testing.T) {
	t.Cleanup(engines.ResetLocal)
	bad := map[string]localOptions{
		"credentials in the URL": {ModelURL: "http://svc:pw@models.internal/v1", ModelName: "m"},
		"not http":               {ModelURL: "ftp://models.internal/v1", ModelName: "m"},
		"query in the URL":       {ModelURL: "http://models.internal/v1?x=1", ModelName: "m"},
		"negative input price":   {ModelName: "m", PriceIn: -1},
		"negative output price":  {ModelName: "m", PriceOut: -0.01},
		"NaN price":              {ModelName: "m", PriceIn: math.NaN()},
		"infinite price":         {ModelName: "m", PriceOut: math.Inf(1)},
		"price without a model":  {PriceIn: -5},
		"negative token ceiling": {ModelName: "m", MaxRunTokens: -1},
	}
	for name, o := range bad {
		if _, err := o.apply(); err == nil {
			t.Errorf("%s: accepted %+v", name, o)
		} else if strings.Contains(err.Error(), "pw") {
			t.Errorf("%s: the refusal repeats the password: %v", name, err)
		}
	}

	url, err := localOptions{ModelURL: "http://127.0.0.1:11434/v1/", ModelName: "llama3.1:8b", PriceIn: 0.2, PriceOut: 0.4}.apply()
	if err != nil || url != "http://127.0.0.1:11434/v1" {
		t.Fatalf("a valid set: %q, %v", url, err)
	}
	if s, ok := engines.Local(); !ok || s.Model != "llama3.1:8b" || s.InPerM != 0.2 || s.OutPerM != 0.4 {
		t.Errorf("the setting was not published: %+v ok=%v", s, ok)
	}
	// A second apply with no model name forgets the first run's setting: a price
	// must never outlive the invocation that typed it.
	if _, err := (localOptions{}).apply(); err != nil {
		t.Fatal(err)
	}
	if _, ok := engines.Local(); ok {
		t.Error("the previous invocation's setting survived the next apply")
	}
}

// run() validates all of it before it opens a store, so a bad flag reports
// before anything has happened.
func TestARunWithABadModelURLFailsBeforeTheStoreIsOpened(t *testing.T) {
	t.Cleanup(engines.ResetLocal)
	dir := t.TempDir()
	err := run(dir, "", 2000, 0, false, false, false, 0, "", "", "", "", "",
		localOptions{ModelURL: "http://svc:pw@models.internal/v1"}, "")
	if err == nil || strings.Contains(err.Error(), "pw") {
		t.Fatalf("err = %v, want a refusal that does not repeat the password", err)
	}
	if entries, _ := readDirNames(dir); len(entries) != 0 {
		t.Errorf("the data directory holds %v: the store was opened before the flag was checked", entries)
	}
}

// ----------------------------------------------------------- structure

// The catalogue the loop sends is pinned for local too, in both directions.
func TestTheToolCatalogueBoundCoversTheLocalEngine(t *testing.T) {
	raw, err := json.Marshal(openAITools())
	if err != nil {
		t.Fatal(err)
	}
	if got := deliver.ToolCatalogueTokens("local"); got != len(raw) {
		t.Errorf("ToolCatalogueTokens(local) = %d, the OpenAI catalogue the loop sends is %d bytes", got, len(raw))
	}
	if loopsFor("local") != maxToolRounds {
		t.Errorf("loopsFor(local) = %d, want %d", loopsFor("local"), maxToolRounds)
	}
}

func readDirNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names, err
}

func TestMainGoStillCannotSpendOrReadTheEnvironment(t *testing.T) {
	// TestThisBinaryCannotSpend holds this; naming it here because the new flags
	// read COSTCREW_MODEL_* and must do it through internal/deliver, not main.go.
	if modelURLEnvDefault() != deliver.ModelURLEnvDefault() || modelNameEnvDefault() != deliver.ModelNameEnvDefault() {
		t.Error("the env defaults are not the deliver ones")
	}
}
