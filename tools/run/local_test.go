package main

// The local engine in the runner: a crew running on a model the organisation
// hosts itself, with nothing sent to any vendor.
//
// Two properties carry the weight and both are about a run that goes wrong
// silently.
//
//  1. Where it goes. A local call reaches the operator's own server, or the
//     OpenAI-shaped gateway in front of it, and with any gateway configured
//     and none that fronts that wire it is REFUSED, never sent direct
//     (invariant 54). No vendor host is reachable from this route.
//  2. What bounds it. Every guard here is in money, and a model on the
//     organisation's own hardware can be priced at 0, where a reservation of 0
//     refuses nothing. So a run that includes the local engine at a price of 0
//     is refused at start unless it carries a ceiling in tokens, and a server
//     that omits its usage block is counted at its worst case rather than at
//     zero.
//
// No test here reaches a real model or a real host: every server is an httptest
// server on loopback and every key is a stub.

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/deliver"
	"github.com/TAIPANBOX/costcrew/internal/engines"
	"github.com/TAIPANBOX/costcrew/internal/money"
)

// seenRequest is one request a fake server was handed, whole.
type seenRequest struct {
	Method, Path string
	Header       http.Header
	Body         []byte
}

// modelServer is an OpenAI-compatible server on loopback: it answers round i
// with rounds[i] (the last one for every request after that) and records every
// request, body included. It stands in for Ollama, vLLM, LM Studio and the
// llama.cpp server.
type modelServer struct {
	*httptest.Server
	mu   sync.Mutex
	reqs []seenRequest
}

func newModelServer(t *testing.T, rounds ...scriptedRound) *modelServer {
	t.Helper()
	m := &modelServer{}
	m.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		m.mu.Lock()
		i := len(m.reqs)
		m.reqs = append(m.reqs, seenRequest{r.Method, r.URL.Path, r.Header.Clone(), raw})
		m.mu.Unlock()
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
	t.Cleanup(m.Close)
	return m
}

func (m *modelServer) seen() []seenRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]seenRequest(nil), m.reqs...)
}

// chatPosts is the requests that were chat completions, leaving out the
// preflight probe's GET /models.
func (m *modelServer) chatPosts() []seenRequest {
	var out []seenRequest
	for _, r := range m.seen() {
		if r.Method == http.MethodPost {
			out = append(out, r)
		}
	}
	return out
}

const (
	localToolUse = `{"choices":[{"message":{"content":"","tool_calls":[{"id":"c1","type":"function",` +
		`"function":{"name":"no_such_tool","arguments":"{}"}}]},"finish_reason":"tool_calls"}],` +
		`"usage":{"prompt_tokens":100,"completion_tokens":10}}`
	localAnswer = `{"choices":[{"message":{"content":"the deliverable"},"finish_reason":"stop"}],` +
		`"usage":{"prompt_tokens":120,"completion_tokens":30}}`
	localAnswerNoUsage  = `{"choices":[{"message":{"content":"the deliverable"},"finish_reason":"stop"}]}`
	localToolUseNoUsage = `{"choices":[{"message":{"content":"","tool_calls":[{"id":"c1","type":"function",` +
		`"function":{"name":"no_such_tool","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`
)

// configureLocal publishes the operator's setting for the duration of a test.
func configureLocal(t *testing.T, model string, in, out float64) {
	t.Helper()
	if err := engines.ConfigureLocal(engines.LocalSetting{Model: model, InPerM: in, OutPerM: out}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(engines.ResetLocal)
}

// localEstimate is a priced local task, built the way price() builds one.
func localEstimate(task crew.Task, an crew.Analyst, in, out float64) estimate {
	return estimate{Task: task, Analyst: an, Engine: engines.LocalID, Model: "llama3.1:8b",
		Price: engines.Price{InPerM: in, OutPerM: out}, PromptTokens: 100,
		CatalogueTokens: deliver.ToolCatalogueTokens(engines.LocalID), Priced: true}
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	fn()
	os.Stderr = old
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func closedModelURL(t *testing.T) string {
	t.Helper()
	s := httptest.NewServer(http.NotFoundHandler())
	u := s.URL
	s.Close()
	return u + "/v1"
}

// ----------------------------------------------------------- one request shape

// The two engines that speak the OpenAI wire send the SAME request: the same
// body for the same conversation, the same content type, the same route under
// a gateway, the same x-fuse-* headers. Only whose key it carries differs.
//
// This is the test that stops the local engine from becoming a copy of the
// openrouter one that drifts: a mutant that makes the local round send a
// different body (it drops the tools, say) or a different route goes red here.
func TestBothOpenAIEnginesSendTheSameRequestShape(t *testing.T) {
	t.Setenv("COSTCREW_MODEL_KEY", "")
	msgs := []openAIMsg{
		{Role: "user", Content: "what moved"},
		{Role: "assistant", ToolCalls: []openAIToolCall{{ID: "c1", Type: "function"}}},
		{Role: "tool", ToolCallID: "c1", Content: "{\"rows\":3}"},
	}
	tools := openAITools()

	// Through a gateway: identical down to the headers except the key.
	gw := gatewayHeaders{OpenAIURL: "http://127.0.0.1:1", RunID: "crew-9",
		AgentID: "agent://x/y.mercer", BudgetUSD: "1.00"}
	or, err := openAIRoundRequest(context.Background(), "openrouter", "sk-or-k", "m", msgs, tools, 50, gw)
	if err != nil {
		t.Fatal(err)
	}
	lo, err := openAIRoundRequest(context.Background(), "local", "", "m", msgs, tools, 50, gw)
	if err != nil {
		t.Fatal(err)
	}
	if or.URL.String() != lo.URL.String() {
		t.Errorf("under a gateway the engines go to different routes: %s vs %s", or.URL, lo.URL)
	}
	if !bytes.Equal(readBody(t, or), readBody(t, lo)) {
		t.Errorf("the request bodies differ:\n openrouter: %s\n local:      %s", readBody(t, or), readBody(t, lo))
	}
	for _, h := range []string{"Content-Type", "x-fuse-run-id", "x-fuse-agent-id", "x-fuse-budget-usd"} {
		if or.Header.Get(h) != lo.Header.Get(h) || or.Header.Get(h) == "" {
			t.Errorf("header %s: openrouter %q, local %q", h, or.Header.Get(h), lo.Header.Get(h))
		}
	}
	if or.Header.Get("Authorization") != "Bearer sk-or-k" {
		t.Errorf("openrouter lost its key: %q", or.Header.Get("Authorization"))
	}
	if lo.Header.Get("Authorization") != "" {
		t.Errorf("local carries Authorization %q with no COSTCREW_MODEL_KEY", lo.Header.Get("Authorization"))
	}

	// Direct: the same body, to each engine's own address.
	orD, err := openAIRoundRequest(context.Background(), "openrouter", "sk-or-k", "m", msgs, tools, 50, gatewayHeaders{})
	if err != nil {
		t.Fatal(err)
	}
	loD, err := openAIRoundRequest(context.Background(), "local", "", "m", msgs, tools, 50,
		gatewayHeaders{ModelURL: "http://127.0.0.1:11434/v1"})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(readBody(t, orD), readBody(t, loD)) {
		t.Error("the direct request bodies differ between the engines")
	}
	if got := loD.URL.String(); got != "http://127.0.0.1:11434/v1/chat/completions" {
		t.Errorf("direct local URL %q", got)
	}
	if got := orD.URL.String(); got != openRouterEndpoint {
		t.Errorf("direct openrouter URL %q, want %q", got, openRouterEndpoint)
	}

	// And on the wire, through the real round function, to real servers: what
	// each server receives is the same JSON.
	t.Setenv("OPENROUTER_API_KEY", "sk-or-stub-not-real")
	orSrv := newModelServer(t, scriptedRound{body: localAnswer})
	loSrv := newModelServer(t, scriptedRound{body: localAnswer})
	old := openRouterEndpoint
	openRouterEndpoint = orSrv.URL + "/direct"
	t.Cleanup(func() { openRouterEndpoint = old })

	if _, _, err := openRouterRound(context.Background(), "m", msgs, tools, 50, gatewayHeaders{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := openAIRound(context.Background(), "local", "m", msgs, tools, 50,
		gatewayHeaders{ModelURL: loSrv.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	a, b := orSrv.seen(), loSrv.seen()
	if len(a) != 1 || len(b) != 1 {
		t.Fatalf("servers saw %d and %d requests", len(a), len(b))
	}
	if !bytes.Equal(a[0].Body, b[0].Body) {
		t.Errorf("on the wire the bodies differ:\n openrouter: %s\n local:      %s", a[0].Body, b[0].Body)
	}
	if b[0].Path != "/v1/chat/completions" {
		t.Errorf("local posted to %q", b[0].Path)
	}
}

func readBody(t *testing.T, r *http.Request) []byte {
	t.Helper()
	b, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// -------------------------------------------------------------- a local task

// A local task runs the tool loop against the operator's server, is charged at
// the operator's price, sends no key it was not given, reaches no host the
// operator did not type, and tells the bus that no vendor was involved.
func TestALocalTaskRunsTheToolLoopAndIsChargedAtTheOperatorsPrice(t *testing.T) {
	t.Setenv("COSTCREW_MODEL_KEY", "")
	srv := newModelServer(t, scriptedRound{body: localToolUse}, scriptedRound{body: localAnswer})

	db, task, analyst := runnerDB(t)
	b, path := testBus(t, "gcp.taipanbox.local", "crew-2026-w40")
	run := &runBudget{ceilingMicros: 5_000_000}
	gw := gatewayConfig{ModelURL: srv.URL + "/v1", Host: "gcp.taipanbox.local", CeilingUSD: money.Cents(500)}
	e := localEstimate(task, analyst, 1000, 2000)
	e.WorstMicros = 5_000

	var err error
	out := captureStdout(t, func() {
		err = execute(context.Background(), db, nil, e, 100, run, b, gw)
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	reqs := srv.chatPosts()
	if len(reqs) != 2 {
		t.Fatalf("the server saw %d chat request(s), want 2 (one tool round, one answer)", len(reqs))
	}
	for i, r := range reqs {
		if r.Path != "/v1/chat/completions" {
			t.Errorf("round %d path %q", i+1, r.Path)
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("round %d carries Authorization %q with no key set", i+1, got)
		}
		for _, h := range []string{"x-fuse-run-id", "x-fuse-agent-id", "x-fuse-budget-usd"} {
			if v := r.Header.Get(h); v != "" {
				t.Errorf("round %d carries %s on a direct call", i+1, h)
			}
		}
	}
	// Round one offers tools; the last round of the loop would not, but the
	// model answered before then.
	if !bytes.Contains(reqs[0].Body, []byte(`"tools"`)) {
		t.Error("the first round did not offer the tool catalogue")
	}
	if !bytes.Contains(reqs[1].Body, []byte(`"role":"tool"`)) {
		t.Error("the second round did not carry the tool's result back")
	}

	// 220 prompt tokens at 1000 $/M and 40 output tokens at 2000 $/M:
	// 220e-6*1000 + 40e-6*2000 = 0.22 + 0.08 = 0.30 USD = 300000 micros.
	want := deliver.ActualMicros(100, 10, e.Price) + deliver.ActualMicros(120, 30, e.Price)
	if want < 299_998 || want > 300_002 {
		t.Fatalf("the test's own arithmetic is off: %d", want)
	}
	if got := liveMicros(t, db, task.ID); got != want {
		t.Errorf("tasks.live_micros = %d, want %d: 220 tokens in and 40 out at the operator's price", got, want)
	}
	if got := run.total(); got != want {
		t.Errorf("run.total() = %d, want %d", got, want)
	}
	if !strings.Contains(out, "priced at your own local rate") || strings.Contains(out, "x-fuse-price") {
		t.Errorf("the per-task line should say the operator's rate priced it:\n%s", out)
	}

	// The loop's refused tool call is an event of its own; the call's record is
	// the one that carries a price basis.
	var data map[string]any
	for _, ev := range allEvents(t, path) {
		if d, _ := ev["data"].(map[string]any); d != nil {
			if _, ok := d["price_basis"]; ok {
				data = d
			}
		}
	}
	if data == nil {
		t.Fatal("no event on the bus carries a price_basis")
	}
	if data["price_basis"] != "local" {
		t.Errorf("bus price_basis = %v, want \"local\": the evidence must say no vendor was involved", data["price_basis"])
	}
	if data["engine"] != "local" {
		t.Errorf("bus engine = %v", data["engine"])
	}
	if data["settled"] != false {
		t.Errorf("bus settled = %v on a direct call", data["settled"])
	}
}

// price_basis is "local" on EVERY local call, including one the gateway
// settled and said "known" about: the engine is what says no vendor was
// involved, and a gateway's word about a price book is not.
func TestThePriceBasisOfALocalCallIsLocalWhateverTheGatewaySaid(t *testing.T) {
	for _, s := range []deliver.Settlement{
		{}, {Settled: true, SettledMicros: 5, PriceBasis: "known"}, {Settled: true, SettledMicros: 5, PriceBasis: "fallback"},
	} {
		if got := priceBasis("local", s); got != "local" {
			t.Errorf("priceBasis(local, %+v) = %q, want \"local\"", s, got)
		}
	}
	// And every other engine keeps what the gateway said.
	if got := priceBasis("openrouter", deliver.Settlement{PriceBasis: "known"}); got != "known" {
		t.Errorf("priceBasis(openrouter) = %q, want the gateway's word", got)
	}
	if got := priceBasis("anthropic", deliver.Settlement{}); got != "" {
		t.Errorf("priceBasis(anthropic, none) = %q, want empty", got)
	}
}

// A server that reports no usage must not make a task free. On this engine the
// price may be 0, and the count is what the ceiling is made of.
func TestALocalServerThatReportsNoUsageIsCountedAtTheWorstCaseNotZero(t *testing.T) {
	srv := newModelServer(t, scriptedRound{body: localAnswerNoUsage})
	db, task, analyst := runnerDB(t)
	b, _ := testBus(t, "gcp.taipanbox.local", "crew-1")
	run := &runBudget{ceilingMicros: 50_000_000, tokenCeiling: 1_000_000}
	gw := gatewayConfig{ModelURL: srv.URL + "/v1", Host: "gcp.taipanbox.local", CeilingUSD: money.Cents(5000),
		MaxRunTokens: 1_000_000}
	e := localEstimate(task, analyst, 1000, 2000)
	e.WorstMicros = 10_000

	var err error
	var errOut string
	out := captureStdout(t, func() {
		errOut = captureStderr(t, func() {
			err = execute(context.Background(), db, nil, e, 300, run, b, gw)
		})
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	reqs := srv.chatPosts()
	if len(reqs) != 1 {
		t.Fatalf("server saw %d requests", len(reqs))
	}
	wantIn, wantOut := len(reqs[0].Body), 300
	if run.tokensUsed() != int64(wantIn+wantOut) {
		t.Errorf("tokens used = %d, want %d (the request's %d bytes in, the whole 300 cap out): a server "+
			"that omits usage was counted as using nothing", run.tokensUsed(), wantIn+wantOut, wantIn)
	}
	want := deliver.ActualMicros(wantIn, wantOut, e.Price)
	if got := liveMicros(t, db, task.ID); got != want || got == 0 {
		t.Errorf("tasks.live_micros = %d, want %d: the unreported round must be charged at its worst case", got, want)
	}
	if !strings.Contains(errOut, "reported no token usage") {
		t.Errorf("the round did not say the server reported nothing:\n%s", errOut)
	}
	if !strings.Contains(out, fmt.Sprintf("in %5d out %5d", wantIn, wantOut)) {
		t.Errorf("the per-task line does not show the counted tokens:\n%s", out)
	}
}

// And the same for a tool-calling round: each unreported round is counted.
func TestEveryUnreportedRoundOfALoopIsCounted(t *testing.T) {
	srv := newModelServer(t, scriptedRound{body: localToolUseNoUsage}, scriptedRound{body: localAnswerNoUsage})
	db, task, analyst := runnerDB(t)
	b, _ := testBus(t, "gcp.taipanbox.local", "crew-1")
	run := &runBudget{ceilingMicros: 50_000_000, tokenCeiling: 5_000_000}
	gw := gatewayConfig{ModelURL: srv.URL + "/v1", Host: "gcp.taipanbox.local", CeilingUSD: money.Cents(5000),
		MaxRunTokens: 5_000_000}
	e := localEstimate(task, analyst, 0, 0)

	var err error
	captureStderr(t, func() {
		captureStdout(t, func() { err = execute(context.Background(), db, nil, e, 200, run, b, gw) })
	})
	if err != nil {
		t.Fatal(err)
	}
	var want int64
	for _, r := range srv.chatPosts() {
		want += int64(len(r.Body)) + 200
	}
	if got := run.tokensUsed(); got != want || len(srv.chatPosts()) != 2 {
		t.Errorf("tokens used = %d over %d rounds, want %d: both rounds at their worst case",
			got, len(srv.chatPosts()), want)
	}
}

// ---------------------------------------------------------------- the bound

// The central refusal. A run that includes the local engine at a price of 0 has
// no limit in money, and must be stopped at start unless it carries one in
// tokens. Nothing is called, not even the probe.
func TestALocalRunAtPriceZeroIsRefusedAtStartWithoutATokenCeiling(t *testing.T) {
	srv := newModelServer(t, scriptedRound{body: localAnswer})
	db, tasks, analyst := runnerTasks(t, 2)
	ests := []estimate{localEstimate(tasks[0], analyst, 0, 0), localEstimate(tasks[1], analyst, 0, 0)}
	gw := gatewayConfig{ModelURL: srv.URL + "/v1", Host: "gcp.taipanbox.local", CeilingUSD: money.Cents(500)}

	var err error
	captureStdout(t, func() {
		err = spend(db, nil, ests, 200, money.Cents(500), 0, bus{run: "crew-1"}, gw)
	})
	if err == nil {
		t.Fatal("a run on a zero-priced local engine started with no token ceiling: nothing bounds it")
	}
	if !strings.Contains(err.Error(), "-max-run-tokens") {
		t.Errorf("the refusal does not name the flag that would bound it: %v", err)
	}
	if n := len(srv.seen()); n != 0 {
		t.Errorf("the server was contacted %d time(s) by a run that was refused at start", n)
	}
	for _, task := range tasks {
		if st := taskState(t, db, task.ID); st != "queued" {
			t.Errorf("task %d is %q after a refused start: a run that never began changed the board", task.ID, st)
		}
	}
}

func taskState(t *testing.T, db *sql.DB, id int) string {
	t.Helper()
	var s string
	if err := db.QueryRow(`SELECT state FROM tasks WHERE id=?`, id).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

// With the ceiling, the same run goes ahead, and says what it used.
func TestALocalRunAtPriceZeroRunsOnceItHasATokenCeiling(t *testing.T) {
	srv := newModelServer(t, scriptedRound{body: localAnswer})
	db, tasks, analyst := runnerTasks(t, 2)
	ests := []estimate{localEstimate(tasks[0], analyst, 0, 0), localEstimate(tasks[1], analyst, 0, 0)}
	gw := gatewayConfig{ModelURL: srv.URL + "/v1", Host: "gcp.taipanbox.local", CeilingUSD: money.Cents(500),
		MaxRunTokens: 1_000_000}

	var err error
	out := captureStdout(t, func() {
		err = spend(db, nil, ests, 200, money.Cents(500), 0, bus{run: "crew-1"}, gw)
	})
	if err != nil {
		t.Fatalf("spend: %v", err)
	}
	if n := len(srv.chatPosts()); n != 2 {
		t.Errorf("server saw %d chat request(s), want 2 (one per task)", n)
	}
	if !strings.Contains(out, "Tokens used: 300 of a 1000000 ceiling.") {
		t.Errorf("the summary does not say how many tokens were used:\n%s", out)
	}
}
