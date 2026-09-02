package main

// Red first, all of it, against the unchanged tree: none of the names below
// exist yet (gatewayConfig, gatewayHeaders, normalizeGateway, anthropicRequest,
// directCallsNotice, gatewayBudgetUSD, gatewayEnvDefault, and the widened
// signatures of call/execute/spend). The failing output is a build error
// naming exactly that, which is the only kind of "red" a feature with no
// existing code at all can produce.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/money"
)

// A call through the gateway carries the run id, the analyst's identity, and
// what it may spend, and the identity it carries is the SAME one the bus
// event for that call carries. Two producers of the same fact that could
// disagree is exactly the shape of bug this console exists to catch in
// somebody else's data.
func TestAGatewayCallCarriesTheAnalystsIdentity(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-stub-not-real")

	var gotPath string
	var gotHeaders http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"content":[{"type":"text","text":"ok"}],`+
			`"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":5}}`)
	}))
	defer srv.Close()

	db, task, analyst := runnerDB(t)
	task.Budget = money.Cents(500) // 5.00: tighter than the run's ceiling below
	b, path := testBus(t, "gcp.taipanbox.local", "crew-2026-w35")

	run := &runBudget{ceilingMicros: 5_000_000}
	gw := gatewayConfig{URL: srv.URL, Host: "gcp.taipanbox.local", CeilingUSD: money.Cents(1000)}
	e := estimate{Task: task, Analyst: analyst, Engine: "anthropic",
		Model: "claude-x", WorstMicros: 1_000, Priced: true}

	// roDB is nil: the canned response below carries no tool_use block, so
	// the loop stops at round one and the dispatcher (the only reader of
	// roDB) is never reached.
	if err := execute(context.Background(), db, nil, e, 100, run, b, gw); err != nil {
		t.Fatalf("execute: %v", err)
	}

	if gotPath != "/v1/messages" {
		t.Errorf("the gateway server saw path %q, want /v1/messages", gotPath)
	}
	if got := gotHeaders.Get("x-fuse-run-id"); got != "crew-2026-w35" {
		t.Errorf("x-fuse-run-id %q, want the invocation's run id crew-2026-w35", got)
	}
	wantAgent := "agent://gcp.taipanbox.local/y.mercer"
	if got := gotHeaders.Get("x-fuse-agent-id"); got != wantAgent {
		t.Errorf("x-fuse-agent-id %q, want %q", got, wantAgent)
	}
	if got := gotHeaders.Get("x-fuse-budget-usd"); got != "5.00" {
		t.Errorf("x-fuse-budget-usd %q, want 5.00: the task's guard (5.00) is "+
			"tighter than the run's ceiling (10.00) and the tighter one is what "+
			"must be named", got)
	}

	ev := oneEvent(t, path)
	if ev["agent_id"] != wantAgent {
		t.Errorf("the bus wrote agent_id %q and the gateway was told %q: the "+
			"trace and the bus must name the same agent for the same call",
			ev["agent_id"], wantAgent)
	}
}

// A 402 from the gateway is a budget refusal, not a call that merely failed.
// It stops the run the same way the runner's own ceiling check does, and it
// gives back the reservation it took, because a refusal that keeps the
// reservation shrinks the ceiling every time the gateway says no.
func TestA402FromTheGatewayReturnsTheReservationAndNamesTheBudget(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-stub-not-real")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
		fmt.Fprint(w, `{"error":{"type":"unit_budget_exceeded","budget_usd":0.05,`+
			`"spent_usd":0.0525,"reason":"run budget exceeded","run_id":"crew-1"}}`)
	}))
	defer srv.Close()

	db, task, analyst := runnerDB(t)
	b, _ := testBus(t, "gcp.taipanbox.local", "crew-1")
	run := &runBudget{ceilingMicros: 1_000_000}
	gw := gatewayConfig{URL: srv.URL, Host: "gcp.taipanbox.local", CeilingUSD: money.Cents(500)}
	e := estimate{Task: task, Analyst: analyst, Engine: "anthropic",
		Model: "claude-x", WorstMicros: 20_000, Priced: true}

	err := execute(context.Background(), db, nil, e, 100, run, b, gw)
	if err == nil {
		t.Fatal("a 402 from the gateway was not reported as an error at all")
	}
	var r refusal
	if !errors.As(err, &r) {
		t.Errorf("error %v is not a refusal: spend()'s loop stops the run on a "+
			"refusal and marks a task blocked on anything else, and a 402 must "+
			"take the first path or a budget event reads as a broken analyst", err)
	}
	for _, want := range []string{"0.05", "0.0525"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the message %q does not name %q from the gateway's body", err.Error(), want)
		}
	}
	if got := run.total(); got != 0 {
		t.Errorf("run.total() = %d, want 0: a call the gateway refused must not book any spend", got)
	}
	if run.reserved != 0 {
		t.Errorf("reserved = %d, want 0: the whole reservation must come back on a gateway refusal", run.reserved)
	}
}

// The gateway's 402 body is untrusted input from a process this runner does
// not control. A body that is not the documented JSON shape must still
// produce a readable sentence, never a panic and never a swallowed error.
func TestA402WithANonJSONBodyStillProducesAReadableRefusal(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-stub-not-real")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
		fmt.Fprint(w, `this is not json`)
	}))
	defer srv.Close()

	db, task, analyst := runnerDB(t)
	b, _ := testBus(t, "gcp.taipanbox.local", "crew-1")
	run := &runBudget{ceilingMicros: 1_000_000}
	gw := gatewayConfig{URL: srv.URL, Host: "gcp.taipanbox.local", CeilingUSD: money.Cents(500)}
	e := estimate{Task: task, Analyst: analyst, Engine: "anthropic",
		Model: "claude-x", WorstMicros: 20_000, Priced: true}

	err := execute(context.Background(), db, nil, e, 100, run, b, gw)
	if err == nil {
		t.Fatal("a non-JSON 402 body was not reported as an error")
	}
	var r refusal
	if !errors.As(err, &r) {
		t.Errorf("a malformed 402 body must still be treated as a gateway "+
			"refusal, not a plain failed call: %v", err)
	}
	if strings.TrimSpace(err.Error()) == "" {
		t.Error("the refusal carries no readable message at all")
	}
}

// With no -gateway, nothing changes: the request is built for the real
// upstream host. Asserted on the request the client would send, never by
// actually sending it, so this test spends nothing and reaches nothing.
func TestWithNoGatewayTheRequestGoesToAnthropicDirectly(t *testing.T) {
	req, err := anthropicRequest(context.Background(), "a-key", "claude-x", "hello", 100, gatewayHeaders{})
	if err != nil {
		t.Fatal(err)
	}
	if got := req.URL.String(); got != "https://api.anthropic.com/v1/messages" {
		t.Errorf("URL %q, want the direct Anthropic endpoint unchanged", got)
	}
	for _, h := range []string{"x-fuse-run-id", "x-fuse-agent-id", "x-fuse-budget-usd", "x-fuse-outcome"} {
		if v := req.Header.Get(h); v != "" {
			t.Errorf("with no gateway configured, header %s carries %q; it must not be set at all", h, v)
		}
	}
}

// Every gateway request carries all three required headers, never
// x-fuse-outcome (not built yet, and said so in a comment rather than sent as
// an empty lie), and x-fuse-parent-run-id only when the caller actually gave
// one: a runner with no notion of a parent run must not invent one.
func TestAGatewayRequestCarriesTheThreeHeadersAndNeverInventsAParent(t *testing.T) {
	gw := gatewayHeaders{
		URL: "http://127.0.0.1:1", RunID: "crew-9", AgentID: "agent://x/y.mercer", BudgetUSD: "1.00",
	}
	req, err := anthropicRequest(context.Background(), "a-key", "claude-x", "hello", 100, gw)
	if err != nil {
		t.Fatal(err)
	}
	if got := req.URL.String(); got != "http://127.0.0.1:1/v1/messages" {
		t.Errorf("URL %q, want the gateway's own /v1/messages", got)
	}
	for h, want := range map[string]string{
		"x-fuse-run-id":     "crew-9",
		"x-fuse-agent-id":   "agent://x/y.mercer",
		"x-fuse-budget-usd": "1.00",
	} {
		if got := req.Header.Get(h); got != want {
			t.Errorf("%s = %q, want %q", h, got, want)
		}
	}
	if v := req.Header.Get("x-fuse-outcome"); v != "" {
		t.Errorf("x-fuse-outcome carries %q; this step never sets it", v)
	}
	if v := req.Header.Get("x-fuse-parent-run-id"); v != "" {
		t.Errorf("x-fuse-parent-run-id carries %q with no parent given; a "+
			"parent must never be invented", v)
	}

	gw.ParentRunID = "crew-8"
	req2, err := anthropicRequest(context.Background(), "a-key", "claude-x", "hello", 100, gw)
	if err != nil {
		t.Fatal(err)
	}
	if got := req2.Header.Get("x-fuse-parent-run-id"); got != "crew-8" {
		t.Errorf("x-fuse-parent-run-id %q, want crew-8 once the caller actually gave one", got)
	}
}

// -gateway accepts only http(s), and refuses before any call rather than
// surfacing as a confusing dial error on the first one.
func TestNormalizeGatewayRefusesANonHTTPURL(t *testing.T) {
	for _, bad := range []string{"ftp://localhost:4177", "localhost:4177", "not a url at all", "  "} {
		if _, err := normalizeGateway(bad); err == nil {
			t.Errorf("normalizeGateway(%q) was accepted; only http(s) may be a gateway", bad)
		}
	}
}

// A trailing slash on -gateway must not double up against /v1/messages.
func TestNormalizeGatewayStripsATrailingSlash(t *testing.T) {
	got, err := normalizeGateway("http://localhost:4177/")
	if err != nil {
		t.Fatal(err)
	}
	if got != "http://localhost:4177" {
		t.Errorf("normalizeGateway with a trailing slash = %q, want it stripped", got)
	}
}

// Empty means off, and off must not be an error: -gateway is optional.
func TestNormalizeGatewayEmptyMeansOff(t *testing.T) {
	got, err := normalizeGateway("")
	if err != nil || got != "" {
		t.Errorf("normalizeGateway(\"\") = (%q, %v), want (\"\", nil)", got, err)
	}
}

// OpenRouter and Bedrock have no route through the gateway yet, and that must
// be said once per run, with the count, rather than happening in silence.
func TestEnginesTheGatewayCannotFrontAreCalledDirectAndSaidSo(t *testing.T) {
	todo := []estimate{
		{Engine: "anthropic"}, {Engine: "openrouter"}, {Engine: "bedrock"}, {Engine: "anthropic"},
	}
	msg := directCallsNotice(true, todo)
	if !strings.Contains(msg, "2") {
		t.Errorf("message %q does not name the count of 2 direct calls", msg)
	}
	if !strings.Contains(msg, "direct") {
		t.Errorf("message %q does not say these calls go direct", msg)
	}
	if got := directCallsNotice(false, todo); got != "" {
		t.Errorf("with no -gateway at all, got %q: nothing should be said about direct calls", got)
	}
	if got := directCallsNotice(true, []estimate{{Engine: "anthropic"}}); got != "" {
		t.Errorf("with nothing to bypass, got %q: a line saying 0 calls go direct is noise", got)
	}
}

// x-fuse-budget-usd is the tighter of the run's ceiling and the task's own
// guard: sending the wider of the two would let the gateway wave through a
// call this runner's own reservation would have refused first.
func TestGatewayBudgetUSDIsTheTighterOfCeilingAndTaskGuard(t *testing.T) {
	if got := gatewayBudgetUSD(money.Cents(1000), money.Cents(500)); got != "5.00" {
		t.Errorf("ceiling 10.00, guard 5.00: got %q, want 5.00", got)
	}
	if got := gatewayBudgetUSD(money.Cents(500), money.Cents(1000)); got != "5.00" {
		t.Errorf("ceiling 5.00, guard 10.00: got %q, want 5.00", got)
	}
	if got := gatewayBudgetUSD(money.Cents(500), 0); got != "5.00" {
		t.Errorf("no per-task guard: got %q, want the run ceiling 5.00", got)
	}
}

// -gateway falls back to COSTCREW_GATEWAY, so a deployment can set it once
// rather than on every invocation's command line.
func TestGatewayEnvDefaultReadsCOSTCREW_GATEWAY(t *testing.T) {
	t.Setenv("COSTCREW_GATEWAY", " http://127.0.0.1:4177 ")
	if got := gatewayEnvDefault(); got != "http://127.0.0.1:4177" {
		t.Errorf("gatewayEnvDefault() = %q, want the trimmed env value", got)
	}
	t.Setenv("COSTCREW_GATEWAY", "")
	if got := gatewayEnvDefault(); got != "" {
		t.Errorf("gatewayEnvDefault() with nothing set = %q, want empty", got)
	}
}
