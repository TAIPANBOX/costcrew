package main

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/deliver"
	"github.com/TAIPANBOX/costcrew/internal/money"
)

// The answer a thinking model gave through Vertex AI's OpenAI-compatible
// endpoint on 2026-10-07 (google/gemini-2.5-flash), its usage block verbatim:
// completion_tokens leaves the 560 reasoning tokens out, and only the total
// (14 + 59 + 560 = 633) carries them.
func thinkingAnswer(usage string) string {
	return `{"choices":[{"message":{"content":"the deliverable"},"finish_reason":"stop"}],"usage":` + usage + `}`
}

const (
	vertexThinkingUsage = `{"completion_tokens": 59, "completion_tokens_details": {"reasoning_tokens": 560}, ` +
		`"prompt_tokens": 14, "total_tokens": 633}`
	openAIThinkingUsage = `{"completion_tokens": 619, "completion_tokens_details": {"reasoning_tokens": 560}, ` +
		`"prompt_tokens": 14, "total_tokens": 633}`
	hostileTotalUsage = `{"completion_tokens": 59, "prompt_tokens": 14, "total_tokens": 20}`
)

// One round of the tool loop, on both OpenAI-shaped engines: the round parser
// is where every looping task's token count and runner price come from.
func TestTheToolLoopsRoundCountsAThinkingModelsReasoningAsOutput(t *testing.T) {
	t.Setenv("COSTCREW_MODEL_KEY", "")
	t.Setenv("OPENROUTER_API_KEY", "sk-or-stub-not-real")
	for _, c := range []struct {
		name    string
		usage   string
		wantOut int
	}{
		{"vertex", vertexThinkingUsage, 619},
		{"openai shape, reasoning already inside", openAIThinkingUsage, 619},
		{"a total smaller than its parts", hostileTotalUsage, 59},
	} {
		for _, engine := range []string{"local", "openrouter"} {
			t.Run(c.name+" on "+engine, func(t *testing.T) {
				srv := newModelServer(t, scriptedRound{body: thinkingAnswer(c.usage)})
				gw := gatewayHeaders{ModelURL: srv.URL + "/v1"}
				if engine == "openrouter" {
					gw = gatewayHeaders{OpenAIURL: srv.URL, RunID: "crew-1", AgentID: "agent://x/y.mercer",
						OnBehalfOf: []string{"user://x/alice", "agent://x/y.mercer"}, BudgetUSD: "1.00"}
				}
				rr, _, err := openAIRound(context.Background(), engine, "google/gemini-2.5-flash",
					[]openAIMsg{{Role: "user", Content: "p"}}, nil, 4096, gw)
				if err != nil {
					t.Fatal(err)
				}
				if rr.InTokens != 14 || rr.OutTokens != c.wantOut {
					t.Errorf("the round counted %d in and %d out, want 14 and %d (usage %s)",
						rr.InTokens, rr.OutTokens, c.wantOut, c.usage)
				}
			})
		}
	}
}

// A whole local task against the token ceiling, the bound that stands in for
// money on a model the operator hosts: the 560 reasoning tokens are tokens the
// run used, so they reach the ceiling, the runner's own price and the
// tool_call event, and they decide whether the NEXT task still fits.
func TestTheTokenCeilingCountsAThinkingModelsReasoning(t *testing.T) {
	t.Setenv("COSTCREW_MODEL_KEY", "")
	srv := newModelServer(t, scriptedRound{body: thinkingAnswer(vertexThinkingUsage)})
	db, tasks, analyst := runnerTasks(t, 2)
	b, path := testBus(t, "gcp.taipanbox.local", "crew-1")
	e := localEstimate(tasks[0], analyst, 1000, 2000)
	e.WorstMicros = 1_000
	const maxTok = 100
	worst := reservedWorstTokens(e, maxTok)
	// Room for the first task's reservation plus 300 tokens: enough left for
	// the second task's reservation if the first used 73, not if it used 633.
	run := &runBudget{ceilingMicros: 50_000_000, tokenCeiling: worst + 300}
	gw := gatewayConfig{ModelURL: srv.URL + "/v1", Host: "gcp.taipanbox.local", CeilingUSD: money.Cents(5000),
		MaxRunTokens: int(worst + 300)}

	captureStdout(t, func() {
		if err := execute(context.Background(), db, nil, e, maxTok, run, b, gw); err != nil {
			t.Fatalf("the first task: %v", err)
		}
	})
	if got := run.tokensUsed(); got != 633 {
		t.Errorf("the token ceiling counted %d tokens, want 633: 14 in and 619 out, the 560 reasoning "+
			"tokens included", got)
	}
	if want := deliver.ActualMicros(14, 619, e.Price); liveMicros(t, db, tasks[0].ID) != want {
		t.Errorf("tasks.live_micros = %d, want %d: the runner's own price of 14 in and 619 out",
			liveMicros(t, db, tasks[0].ID), want)
	}
	var outTokens any
	for _, ev := range allEvents(t, path) {
		if d, _ := ev["data"].(map[string]any); d != nil {
			if _, ok := d["price_basis"]; ok {
				outTokens = d["output_tokens"]
			}
		}
	}
	if fmt.Sprint(outTokens) != "619" {
		t.Errorf("the tool_call event says output_tokens = %v, want 619", outTokens)
	}

	e2 := localEstimate(tasks[1], analyst, 1000, 2000)
	e2.WorstMicros = 1_000
	err := execute(context.Background(), db, nil, e2, maxTok, run, b, gw)
	if !isRefusal(err) || !strings.Contains(err.Error(), "token ceiling") {
		t.Errorf("the second task: err = %v, want the token ceiling's refusal: the first task used "+
			"633 tokens of the 300 left over its reservation", err)
	}
	if n := len(srv.chatPosts()); n != 1 {
		t.Errorf("the server saw %d chat request(s), want 1: the second task must not be called", n)
	}
}
