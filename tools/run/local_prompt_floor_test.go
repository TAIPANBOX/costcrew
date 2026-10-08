package main

// A round on the local engine re-sends the whole conversation, so its prompt is
// never shorter than the previous round's. A server that reports fewer prompt
// tokens than that either cut the prompt to fit its context window or counted
// only what it did not have cached; either way the count is below what was
// sent, and it is raised to the previous round's, which is a floor on what was
// sent and never more than it.

import (
	"context"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/money"
)

const (
	localToolUseBig = `{"choices":[{"message":{"content":"","tool_calls":[{"id":"c1","type":"function",` +
		`"function":{"name":"no_such_tool","arguments":"{}"}}]},"finish_reason":"tool_calls"}],` +
		`"usage":{"prompt_tokens":3000,"completion_tokens":10}}`
	// What Ollama 0.40.0 answered, measured 2026-10-08, for a conversation of
	// 4309 tokens on a model loaded with a smaller context: 229.
	localAnswerCut = `{"choices":[{"message":{"content":"the deliverable"},"finish_reason":"stop"}],` +
		`"usage":{"prompt_tokens":229,"completion_tokens":30}}`
	localAnswerGrown = `{"choices":[{"message":{"content":"the deliverable"},"finish_reason":"stop"}],` +
		`"usage":{"prompt_tokens":3200,"completion_tokens":30}}`
)

func TestALocalRoundIsNeverCountedBelowThePreviousRoundsPrompt(t *testing.T) {
	configureLocal(t, "qwen2.5:7b", 1.0, 2.0)
	srv := newModelServer(t, scriptedRound{body: localToolUseBig}, scriptedRound{body: localAnswerCut})
	db, task, analyst := runnerDB(t)
	b, _ := testBus(t, "gcp.taipanbox.local", "crew-1")
	run := &runBudget{ceilingMicros: 50_000_000, tokenCeiling: 5_000_000}
	gw := gatewayConfig{ModelURL: srv.URL + "/v1", Host: "gcp.taipanbox.local", CeilingUSD: money.Cents(5000),
		MaxRunTokens: 5_000_000}
	e := localEstimate(task, analyst, 1.0, 2.0)

	var err error
	stderr := captureStderr(t, func() {
		captureStdout(t, func() { err = execute(context.Background(), db, nil, e, 200, run, b, gw) })
	})
	if err != nil {
		t.Fatal(err)
	}
	// Round one: 3000 in, 10 out. Round two re-sent all of round one and
	// reported 229: counted at 3000, its floor.
	if got := run.tokensUsed(); got != 3000+10+3000+30 {
		t.Errorf("the token ceiling counted %d, want %d: the second round re-sent the first round's "+
			"3000-token prompt and was counted at the 229 the server reported", got, 3000+10+3000+30)
	}
	// 6000 in at 1.00 and 40 out at 2.00 per million: 6080 micros.
	if got := liveMicros(t, db, task.ID); got != 6080 {
		t.Errorf("tasks.live_micros = %d, want 6080 at the operator's price", got)
	}
	for _, want := range []string{"229", "3000", "context window"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("the line that says the count was raised does not name %q:\n%s", want, stderr)
		}
	}
}

// A round that reports more than the previous one is taken as reported: the
// floor raises a count, it never replaces a larger one.
func TestALocalRoundThatReportsMoreThanThePreviousIsTakenAsReported(t *testing.T) {
	configureLocal(t, "qwen2.5:7b", 0, 0)
	srv := newModelServer(t, scriptedRound{body: localToolUseBig}, scriptedRound{body: localAnswerGrown})
	db, task, analyst := runnerDB(t)
	b, _ := testBus(t, "gcp.taipanbox.local", "crew-1")
	run := &runBudget{ceilingMicros: 50_000_000, tokenCeiling: 5_000_000}
	gw := gatewayConfig{ModelURL: srv.URL + "/v1", Host: "gcp.taipanbox.local", CeilingUSD: money.Cents(5000),
		MaxRunTokens: 5_000_000}
	e := localEstimate(task, analyst, 0, 0)

	var err error
	stderr := captureStderr(t, func() {
		captureStdout(t, func() { err = execute(context.Background(), db, nil, e, 200, run, b, gw) })
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := run.tokensUsed(); got != 3000+10+3200+30 {
		t.Errorf("the token ceiling counted %d, want %d", got, 3000+10+3200+30)
	}
	if strings.Contains(stderr, "context window") {
		t.Errorf("a round that reported a grown prompt was flagged as cut:\n%s", stderr)
	}
}

// A vendor engine on the same wire is billed by the vendor on what the vendor
// reports, so its counts are never raised: the floor is the local engine's.
func TestAnOpenRouterRoundIsNotFloored(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "sk-or-stub-not-real")
	srv := newModelServer(t, scriptedRound{body: localToolUseBig}, scriptedRound{body: localAnswerCut})
	old := openRouterEndpoint
	openRouterEndpoint = srv.URL + "/direct"
	t.Cleanup(func() { openRouterEndpoint = old })
	db, task, analyst := runnerDB(t)
	b, _ := testBus(t, "gcp.taipanbox.local", "crew-1")
	run := &runBudget{ceilingMicros: 50_000_000, tokenCeiling: 5_000_000}
	gw := gatewayConfig{Host: "gcp.taipanbox.local", CeilingUSD: money.Cents(5000), MaxRunTokens: 5_000_000}
	e := estimate{Task: task, Analyst: analyst, Engine: "openrouter", Model: "m", WorstMicros: 1_000, Priced: true}

	var err error
	captureStderr(t, func() {
		captureStdout(t, func() { err = execute(context.Background(), db, nil, e, 200, run, b, gw) })
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := run.tokensUsed(); got != 3000+10+229+30 {
		t.Errorf("an openrouter task counted %d tokens, want %d as the vendor reported them", got, 3000+10+229+30)
	}
}
