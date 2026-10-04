package main

// Two defects, both about money a run spent and did not say.
//
// 1. An openrouter task ignored -gateway. The tool loop's openRouterRound
// built its own request to openrouter.ai and was never handed the gateway at
// all, so a run pointed at a metering gateway kept spending outside it with a
// line saying so (directCallsNotice) and nothing stopping it. TokenFuse has
// served POST /v1/chat/completions since 2026-09-07 (tokenfuse docs/26), so
// the task now goes through the gateway that fronts the OpenAI wire
// (-gateway-openai), or, with a gateway configured and none fronting it, is
// refused.
//
// 2. costcrew#82. A task stopped mid-run carried 0.00 on the board while the
// gateway had already settled real money for its earlier rounds: the 402
// that stops a run arrives as an empty round, an empty round turned the whole
// task "unsettled", and the error path booked nothing at all.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/deliver"
	"github.com/TAIPANBOX/costcrew/internal/engines"
	"github.com/TAIPANBOX/costcrew/internal/money"
)

// scriptedRound is one response of a fake gateway.
type scriptedRound struct {
	status int    // 0 means 200
	body   string // the response body
	cost   string // x-fuse-cost-usd, when non-empty
	spent  string // x-fuse-spent-usd, when non-empty
	price  string // x-fuse-price, when non-empty
}

type scriptedGateway struct {
	*httptest.Server
	mu       sync.Mutex
	requests []*http.Request
}

func (g *scriptedGateway) seen() []*http.Request {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]*http.Request(nil), g.requests...)
}

// newScriptedGateway answers round i with rounds[i], and the last one for
// every request after that.
func newScriptedGateway(t *testing.T, rounds ...scriptedRound) *scriptedGateway {
	t.Helper()
	g := &scriptedGateway{}
	g.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		i := len(g.requests)
		g.requests = append(g.requests, r.Clone(context.Background()))
		g.mu.Unlock()
		if i >= len(rounds) {
			i = len(rounds) - 1
		}
		rd := rounds[i]
		w.Header().Set("Content-Type", "application/json")
		if rd.cost != "" {
			w.Header().Set("x-fuse-cost-usd", rd.cost)
		}
		if rd.spent != "" {
			w.Header().Set("x-fuse-spent-usd", rd.spent)
		}
		if rd.price != "" {
			w.Header().Set("x-fuse-price", rd.price)
		}
		if rd.status != 0 {
			w.WriteHeader(rd.status)
		}
		fmt.Fprint(w, rd.body)
	}))
	t.Cleanup(g.Close)
	return g
}

const (
	anthropicToolUse = `{"content":[{"type":"tool_use","id":"t1","name":"no_such_tool","input":{}}],` +
		`"stop_reason":"tool_use","usage":{"input_tokens":100,"output_tokens":10}}`
	anthropicAnswer = `{"content":[{"type":"text","text":"the deliverable"}],` +
		`"stop_reason":"end_turn","usage":{"input_tokens":100,"output_tokens":10}}`
	openAIToolUse = `{"choices":[{"message":{"content":"","tool_calls":[{"id":"c1","type":"function",` +
		`"function":{"name":"no_such_tool","arguments":"{}"}}]},"finish_reason":"tool_calls"}],` +
		`"usage":{"prompt_tokens":100,"completion_tokens":10}}`
	openAIAnswer = `{"choices":[{"message":{"content":"the deliverable"},"finish_reason":"stop"}],` +
		`"usage":{"prompt_tokens":100,"completion_tokens":10}}`
	budgetRefusal = `{"error":{"type":"run_budget_exceeded","budget_usd":0.35,"spent_usd":0.2,` +
		`"reason":"per-run budget exceeded","run_id":"crew-1"}}`
)

// trapTheDirectOpenRouterEndpoint points the loop's DIRECT openrouter
// endpoint at a server that records every request it gets, so a task that
// leaves the gateway is SEEN leaving it and never sent anywhere real.
func trapTheDirectOpenRouterEndpoint(t *testing.T) *scriptedGateway {
	t.Helper()
	trap := newScriptedGateway(t, scriptedRound{body: openAIAnswer})
	old := openRouterEndpoint
	openRouterEndpoint = trap.URL + "/direct"
	t.Cleanup(func() { openRouterEndpoint = old })
	return trap
}

// The defect as it ran, with identifiers the unchanged tree already has: a
// gateway configured (the Anthropic-shaped one, the only one there was), an
// openrouter task, and the task went to the direct endpoint anyway.
func TestAnOpenRouterTaskIsNeverSentDirectWhileAGatewayIsConfigured(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "sk-or-stub-not-real")
	trap := trapTheDirectOpenRouterEndpoint(t)
	anthropicGW := newScriptedGateway(t, scriptedRound{body: anthropicAnswer})

	db, task, analyst := runnerDB(t)
	b, _ := testBus(t, "gcp.taipanbox.local", "crew-1")
	run := &runBudget{ceilingMicros: 5_000_000}
	gw := gatewayConfig{URL: anthropicGW.URL, Host: "gcp.taipanbox.local", CeilingUSD: money.Cents(500)}
	e := estimate{Task: task, Analyst: analyst, Engine: "openrouter",
		Model: "some/model", WorstMicros: 1_000, Priced: true}

	err := execute(context.Background(), db, nil, e, 100, run, b, gw)

	if n := len(trap.seen()); n != 0 {
		t.Fatalf("the openrouter task made %d request(s) to the direct endpoint with a gateway "+
			"configured: the spend left the gateway", n)
	}
	if n := len(anthropicGW.seen()); n != 0 {
		t.Errorf("the openrouter task was sent to the Anthropic-shaped gateway (%d request(s)): "+
			"that gateway forwards the wrong wire", n)
	}
	if err == nil {
		t.Fatal("an openrouter task with only an Anthropic-shaped gateway was accepted")
	}
	if !errors.Is(err, deliver.ErrNoGatewayRoute) {
		t.Errorf("err = %v, want one wrapping deliver.ErrNoGatewayRoute", err)
	}
	if run.reserved != 0 || run.total() != 0 {
		t.Errorf("reserved %d, spent %d after a refused task, want 0 and 0", run.reserved, run.total())
	}
	if got := liveMicros(t, db, task.ID); got != 0 {
		t.Errorf("live_micros = %d for a task that never ran", got)
	}
}

// Through the OpenAI-shaped gateway the task is metered: the request lands on
// the gateway's own /v1/chat/completions with the same x-fuse-* headers an
// Anthropic round carries, the provider key travels as a bearer token, and
// the charge recorded is the gateway's settlement of each round, summed.
func TestAnOpenRouterTaskThroughTheOpenAIGatewayIsChargedItsSettlementPerRound(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "sk-or-stub-not-real")
	trap := trapTheDirectOpenRouterEndpoint(t)
	gwSrv := newScriptedGateway(t,
		scriptedRound{body: openAIToolUse, cost: "0.100000", spent: "0.100000", price: "known"},
		scriptedRound{body: openAIAnswer, cost: "0.050000", spent: "0.150000", price: "known"},
	)

	db, task, analyst := runnerDB(t)
	task.Budget = money.Cents(500)
	b, _ := testBus(t, "gcp.taipanbox.local", "crew-2026-w40")
	run := &runBudget{ceilingMicros: 5_000_000}
	gw := gatewayConfig{OpenAIURL: gwSrv.URL, Host: "gcp.taipanbox.local", CeilingUSD: money.Cents(1000)}
	e := estimate{Task: task, Analyst: analyst, Engine: "openrouter",
		Model: "some/model", WorstMicros: 1_000, Priced: true}

	var err error
	out := captureStdout(t, func() {
		err = execute(context.Background(), db, nil, e, 100, run, b, gw)
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	if n := len(trap.seen()); n != 0 {
		t.Errorf("%d request(s) reached the direct endpoint", n)
	}
	reqs := gwSrv.seen()
	if len(reqs) != 2 {
		t.Fatalf("the gateway saw %d request(s), want 2 (one tool round, one answer)", len(reqs))
	}
	for i, r := range reqs {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("round %d path %q, want /v1/chat/completions", i+1, r.URL.Path)
		}
		if got := r.Header.Get("x-fuse-run-id"); got != "crew-2026-w40" {
			t.Errorf("round %d x-fuse-run-id %q", i+1, got)
		}
		if got, want := r.Header.Get("x-fuse-agent-id"), "agent://gcp.taipanbox.local/y.mercer"; got != want {
			t.Errorf("round %d x-fuse-agent-id %q, want %q", i+1, got, want)
		}
		if got := r.Header.Get("x-fuse-budget-usd"); got != "5.00" {
			t.Errorf("round %d x-fuse-budget-usd %q, want 5.00 (the task guard is tighter than the ceiling)", i+1, got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-or-stub-not-real" {
			t.Errorf("round %d Authorization %q: the provider key must travel for the gateway to pass through", i+1, got)
		}
	}

	if got := liveMicros(t, db, task.ID); got != 150_000 {
		t.Errorf("tasks.live_micros = %d, want 150000: the sum of the gateway's two settlements", got)
	}
	if got := run.total(); got != 150_000 {
		t.Errorf("run.total() = %d, want 150000", got)
	}
	if !strings.Contains(out, "settled by the gateway") {
		t.Errorf("the per-task line does not say the gateway settled it:\n%s", out)
	}
}

// A 402 from the OpenAI-shaped gateway is a budget refusal that stops the
// run, exactly as one from the Anthropic-shaped gateway does.
func TestA402FromTheOpenAIGatewayStopsTheRunAsARefusal(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "sk-or-stub-not-real")
	gwSrv := newScriptedGateway(t, scriptedRound{status: http.StatusPaymentRequired, body: budgetRefusal})

	db, task, analyst := runnerDB(t)
	b, _ := testBus(t, "gcp.taipanbox.local", "crew-1")
	run := &runBudget{ceilingMicros: 1_000_000}
	gw := gatewayConfig{OpenAIURL: gwSrv.URL, Host: "gcp.taipanbox.local", CeilingUSD: money.Cents(500)}
	e := estimate{Task: task, Analyst: analyst, Engine: "openrouter",
		Model: "some/model", WorstMicros: 20_000, Priced: true}

	err := execute(context.Background(), db, nil, e, 100, run, b, gw)
	var r refusal
	if !errors.As(err, &r) {
		t.Fatalf("err = %v, want a refusal: a budget 402 stops the run, it does not block one task", err)
	}
	if !strings.Contains(err.Error(), "per-run budget exceeded") {
		t.Errorf("the refusal %q does not carry the gateway's own reason", err)
	}
	if run.reserved != 0 {
		t.Errorf("reserved = %d, want 0: the reservation must come back", run.reserved)
	}
}

// The OpenAI round's request, built without a network, carries what the
// Anthropic round's does.
func TestAnOpenRouterRoundRequestThroughTheGatewayCarriesTheFuseHeaders(t *testing.T) {
	gw := gatewayHeaders{OpenAIURL: "http://127.0.0.1:1", RunID: "crew-9",
		AgentID: "agent://x/y.mercer", BudgetUSD: "1.00"}
	req, err := openRouterRoundRequest(context.Background(), "sk-or-k", "m",
		[]openAIMsg{{Role: "user", Content: "hi"}}, nil, 50, gw)
	if err != nil {
		t.Fatal(err)
	}
	if got := req.URL.String(); got != "http://127.0.0.1:1/v1/chat/completions" {
		t.Errorf("URL %q", got)
	}
	for h, want := range map[string]string{
		"x-fuse-run-id": "crew-9", "x-fuse-agent-id": "agent://x/y.mercer", "x-fuse-budget-usd": "1.00",
		"Authorization": "Bearer sk-or-k",
	} {
		if got := req.Header.Get(h); got != want {
			t.Errorf("%s = %q, want %q", h, got, want)
		}
	}
	// With no gateway, the direct endpoint and none of the headers.
	direct, err := openRouterRoundRequest(context.Background(), "sk-or-k", "m",
		[]openAIMsg{{Role: "user", Content: "hi"}}, nil, 50, gatewayHeaders{})
	if err != nil {
		t.Fatal(err)
	}
	if got := direct.URL.String(); got != openRouterEndpoint {
		t.Errorf("direct URL %q, want %q", got, openRouterEndpoint)
	}
	for _, h := range []string{"x-fuse-run-id", "x-fuse-agent-id", "x-fuse-budget-usd"} {
		if v := direct.Header.Get(h); v != "" {
			t.Errorf("a direct round carries %s = %q", h, v)
		}
	}
}

// costcrew#82, as the issue measured it: rounds the gateway settled, then the
// 402 that stops the run. The task did not finish and it did not cost
// nothing: the board, the ceiling and the summary must carry what the gateway
// billed for the rounds that were billed.
func TestAStoppedTaskRecordsWhatTheGatewaySettledForItsRounds(t *testing.T) {
	for _, c := range []struct {
		name, engine string
		toolUse      string
	}{
		{"anthropic", "anthropic", anthropicToolUse},
		{"openrouter", "openrouter", openAIToolUse},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("ANTHROPIC_API_KEY", "sk-ant-stub-not-real")
			t.Setenv("OPENROUTER_API_KEY", "sk-or-stub-not-real")
			srv := newScriptedGateway(t,
				scriptedRound{body: c.toolUse, cost: "0.100000", spent: "0.100000", price: "fallback"},
				scriptedRound{body: c.toolUse, cost: "0.100000", spent: "0.200000", price: "fallback"},
				scriptedRound{status: http.StatusPaymentRequired, body: budgetRefusal},
			)

			db, task, analyst := runnerDB(t)
			b, _ := testBus(t, "gcp.taipanbox.local", "crew-1")
			run := &runBudget{ceilingMicros: 350_000}
			gw := gatewayConfig{Host: "gcp.taipanbox.local", CeilingUSD: money.Cents(35)}
			if c.engine == "openrouter" {
				gw.OpenAIURL = srv.URL
			} else {
				gw.URL = srv.URL
			}
			e := estimate{Task: task, Analyst: analyst, Engine: c.engine,
				Model: "m", WorstMicros: 10_000, Priced: true}

			err := execute(context.Background(), db, nil, e, 100, run, b, gw)
			var r refusal
			if !errors.As(err, &r) {
				t.Fatalf("err = %v, want the 402's refusal", err)
			}
			if got := len(srv.seen()); got != 3 {
				t.Fatalf("the gateway saw %d request(s), want 3 (two settled rounds, then the 402)", got)
			}
			if got := liveMicros(t, db, task.ID); got != 200_000 {
				t.Errorf("tasks.live_micros = %d, want 200000: the gateway settled two rounds at 0.1 "+
					"and the stopped task recorded nothing of it", got)
			}
			if got := run.total(); got != 200_000 {
				t.Errorf("run.total() = %d, want 200000: the ceiling must count what the gateway billed", got)
			}
			if run.reserved != 0 {
				t.Errorf("reserved = %d, want 0", run.reserved)
			}
		})
	}
}

// The whole run: the board and the summary line, the two things the issue
// shows reading 0.00 and the runner's own estimate.
func TestARunStoppedMidTaskBooksTheSettledMoneyOnTheBoardAndLeadsWithIt(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-stub-not-real")
	srv := newScriptedGateway(t,
		scriptedRound{body: anthropicToolUse, cost: "0.100000", spent: "0.100000", price: "fallback"},
		scriptedRound{body: anthropicToolUse, cost: "0.100000", spent: "0.200000", price: "fallback"},
		scriptedRound{status: http.StatusPaymentRequired, body: budgetRefusal},
	)

	db, task, analyst := runnerDB(t)
	b, _ := testBus(t, "gcp.taipanbox.local", "crew-1")
	gw := gatewayConfig{URL: srv.URL, Host: "gcp.taipanbox.local", CeilingUSD: money.Cents(35)}
	ests := []estimate{{Task: task, Analyst: analyst, Engine: "anthropic",
		Model: "m", WorstMicros: 10_000, Priced: true}}

	var err error
	out := captureStdout(t, func() {
		err = spend(db, nil, ests, 100, money.Cents(35), 0, b, gw)
	})
	if err != nil {
		t.Fatalf("spend: %v", err)
	}

	if !strings.Contains(out, "stopped at") {
		t.Fatalf("the run did not stop on the gateway's 402:\n%s", out)
	}
	if !strings.Contains(out, "Spent 0.2000 of a 0.35 ceiling: 1 of 1 task(s) settled by the gateway, "+
		"whose own run total is 0.2000.") {
		t.Errorf("the headline does not lead with the gateway's figure:\n%s", out)
	}
	if !strings.Contains(out, "The board now carries 0.20 against these tasks") {
		t.Errorf("the board line does not carry the settled 0.20:\n%s", out)
	}
	var cents int64
	if err := db.QueryRow(`SELECT spent_cents FROM tasks WHERE id=?`, task.ID).Scan(&cents); err != nil {
		t.Fatal(err)
	}
	if cents != 20 {
		t.Errorf("tasks.spent_cents = %d, want 20: the stopped task cost 0.20 and the board carried %d", cents, cents)
	}
}

// A stopped task whose gateway total is higher than anything that reached
// this runner says so beside the figure.
func TestTheHeadlineSaysWhenTheGatewaysTotalIsHigherThanWhatWasBooked(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-stub-not-real")
	srv := newScriptedGateway(t,
		// The gateway's own ledger says 0.30 for the run; this task's one
		// settled round says 0.10.
		scriptedRound{body: anthropicToolUse, cost: "0.100000", spent: "0.300000", price: "known"},
		scriptedRound{status: http.StatusPaymentRequired, body: budgetRefusal},
	)
	db, task, analyst := runnerDB(t)
	b, _ := testBus(t, "gcp.taipanbox.local", "crew-1")
	gw := gatewayConfig{URL: srv.URL, Host: "gcp.taipanbox.local", CeilingUSD: money.Cents(35)}
	ests := []estimate{{Task: task, Analyst: analyst, Engine: "anthropic",
		Model: "m", WorstMicros: 10_000, Priced: true}}

	out := captureStdout(t, func() {
		if err := spend(db, nil, ests, 100, money.Cents(35), 0, b, gw); err != nil {
			t.Errorf("spend: %v", err)
		}
	})
	if !strings.Contains(out, "whose own run total is 0.3000. The gateway's total is 0.2000 more than this run booked") {
		t.Errorf("the headline does not name the gap between the gateway's total and the booked one:\n%s", out)
	}
}

// A task that failed before any round answered cost the runner nothing it can
// see, and books nothing: the fix for #82 must not invent a charge.
func TestAStoppedTaskWithNoSettledRoundBooksNothing(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-stub-not-real")
	srv := newScriptedGateway(t, scriptedRound{status: http.StatusPaymentRequired, body: budgetRefusal})

	db, task, analyst := runnerDB(t)
	b, _ := testBus(t, "gcp.taipanbox.local", "crew-1")
	run := &runBudget{ceilingMicros: 350_000}
	gw := gatewayConfig{URL: srv.URL, Host: "gcp.taipanbox.local", CeilingUSD: money.Cents(35)}
	e := estimate{Task: task, Analyst: analyst, Engine: "anthropic", Model: "m", WorstMicros: 10_000, Priced: true}

	if err := execute(context.Background(), db, nil, e, 100, run, b, gw); err == nil {
		t.Fatal("a 402 on the first round was not an error")
	}
	if got := liveMicros(t, db, task.ID); got != 0 {
		t.Errorf("live_micros = %d, want 0: nothing was settled", got)
	}
	if got := run.total(); got != 0 {
		t.Errorf("run.total() = %d, want 0", got)
	}
}

// What #82 must NOT change: a task is settled only when every round that
// answered was. A round that answered with no settlement header makes the
// task's figure the runner's own price over all of it, never the gateway's
// figure for some rounds mixed with the runner's for others.
func TestARoundThatAnsweredWithoutAHeaderStillMakesTheTaskPricedByTheRunner(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-stub-not-real")
	srv := newScriptedGateway(t,
		scriptedRound{body: anthropicToolUse, cost: "0.100000", spent: "0.100000", price: "known"},
		scriptedRound{body: anthropicAnswer}, // answered, tokens counted, no headers at all
	)
	db, task, analyst := runnerDB(t)
	b, _ := testBus(t, "gcp.taipanbox.local", "crew-1")
	run := &runBudget{ceilingMicros: 350_000}
	price := engines.Price{InPerM: 3.00, OutPerM: 15.00}
	gw := gatewayConfig{URL: srv.URL, Host: "gcp.taipanbox.local", CeilingUSD: money.Cents(35)}
	e := estimate{Task: task, Analyst: analyst, Engine: "anthropic", Model: "m", Price: price,
		WorstMicros: 10_000, Priced: true}

	if err := execute(context.Background(), db, nil, e, 100, run, b, gw); err != nil {
		t.Fatalf("execute: %v", err)
	}
	want := 2 * deliver.ActualMicros(100, 10, price) // the runner's own price for both rounds
	if got := liveMicros(t, db, task.ID); got != want {
		t.Errorf("live_micros = %d, want %d: one round had no header, so the task is priced by "+
			"the runner over both rounds (the gateway's 100000 for one round would be a third "+
			"kind of number)", got, want)
	}
}

// recordingTransport stands in for the network in a test whose point is that
// nothing may reach it: every request is recorded and answered from memory,
// so a task that DID leave the gateway is seen leaving and never sent.
type recordingTransport struct {
	mu    sync.Mutex
	hosts []string
}

func (rt *recordingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	rt.mu.Lock()
	rt.hosts = append(rt.hosts, r.URL.Host)
	rt.mu.Unlock()
	return &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(anthropicAnswer)),
		Request:    r,
	}, nil
}

func (rt *recordingTransport) seen() []string {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return append([]string(nil), rt.hosts...)
}

// The other side of the same rule: an anthropic task with only the
// OpenAI-shaped gateway configured has no route, and is refused before the
// loop's first request instead of going to api.anthropic.com.
func TestAnAnthropicTaskWithOnlyAnOpenAIGatewayIsRefusedAndNeverGoesDirect(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-stub-not-real")
	rt := &recordingTransport{}
	old := http.DefaultTransport
	http.DefaultTransport = rt
	t.Cleanup(func() { http.DefaultTransport = old })

	db, task, analyst := runnerDB(t)
	b, _ := testBus(t, "gcp.taipanbox.local", "crew-1")
	run := &runBudget{ceilingMicros: 5_000_000}
	gw := gatewayConfig{OpenAIURL: "http://openai-gateway.invalid", Host: "gcp.taipanbox.local", CeilingUSD: money.Cents(500)}
	e := estimate{Task: task, Analyst: analyst, Engine: "anthropic",
		Model: "claude-x", WorstMicros: 1_000, Priced: true}

	err := execute(context.Background(), db, nil, e, 100, run, b, gw)
	if hosts := rt.seen(); len(hosts) != 0 {
		t.Fatalf("the anthropic task reached %v with only an OpenAI-shaped gateway configured", hosts)
	}
	if !errors.Is(err, deliver.ErrNoGatewayRoute) {
		t.Errorf("err = %v, want one wrapping deliver.ErrNoGatewayRoute", err)
	}
}
