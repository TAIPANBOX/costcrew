package deliver

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The usage block measured on 2026-10-07 from Vertex AI's OpenAI-compatible
// endpoint (google/gemini-2.5-flash), verbatim apart from the field order: the
// 560 reasoning tokens are NOT inside completion_tokens, because the total is
// 14 + 59 + 560 = 633. OpenAI's own shape puts them inside, so total =
// prompt + completion there.
const (
	vertexUsage = `{"completion_tokens": 59, "completion_tokens_details": {"reasoning_tokens": 560}, ` +
		`"prompt_tokens": 14, "total_tokens": 633}`
	openAIShapedReasoningUsage = `{"completion_tokens": 619, "completion_tokens_details": {"reasoning_tokens": 560}, ` +
		`"prompt_tokens": 14, "total_tokens": 633}`
)

// reasoningCases is every usage shape the rule has to read, with the output it
// must count. Only the first is wrong on a reader of completion_tokens alone;
// the rest are what a wrong fix would break (adding reasoning_tokens on top of
// a completion that already holds them, or believing a total that is smaller
// than its own parts).
var reasoningCases = []struct {
	name    string
	usage   string
	wantIn  int
	wantOut int
}{
	{"vertex leaves reasoning out of completion_tokens", vertexUsage, 14, 619},
	{"openai keeps reasoning inside completion_tokens", openAIShapedReasoningUsage, 14, 619},
	{"no total_tokens at all", `{"prompt_tokens": 14, "completion_tokens": 59}`, 14, 59},
	{"a total equal to its parts", `{"prompt_tokens": 14, "completion_tokens": 59, "total_tokens": 73}`, 14, 59},
	{"a hostile total smaller than its parts", `{"prompt_tokens": 14, "completion_tokens": 59, "total_tokens": 20}`, 14, 59},
	{"a hostile total smaller than the prompt", `{"prompt_tokens": 14, "completion_tokens": 59, "total_tokens": 5}`, 14, 59},
	{"a hostile negative total", `{"prompt_tokens": 14, "completion_tokens": 59, "total_tokens": -1000}`, 14, 59},
}

// chatServer answers every chat completion with one answer carrying usage.
func chatServer(t *testing.T, usage string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"choices":[{"message":{"content":"the deliverable"},"finish_reason":"stop"}],"usage":%s}`, usage)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// The single-shot local call (the route tools/bench and the console's planning
// ask share with the runner) counts what a thinking model generated.
func TestALocalCallCountsAThinkingModelsReasoningAsOutput(t *testing.T) {
	t.Setenv("COSTCREW_MODEL_KEY", "")
	for _, c := range reasoningCases {
		t.Run(c.name, func(t *testing.T) {
			srv := chatServer(t, c.usage)
			res, err := Call(context.Background(), "local", "google/gemini-2.5-flash", "p", 4096,
				Gateway{ModelURL: srv.URL + "/v1"})
			if err != nil {
				t.Fatal(err)
			}
			if res.InTokens != c.wantIn || res.OutTokens != c.wantOut {
				t.Errorf("counted %d in and %d out, want %d and %d (usage %s)",
					res.InTokens, res.OutTokens, c.wantIn, c.wantOut, c.usage)
			}
		})
	}
}

// The same rule on the openrouter route, through the OpenAI-shaped gateway so
// no request leaves loopback.
func TestAnOpenRouterCallCountsAThinkingModelsReasoningAsOutput(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "sk-or-stub-not-real")
	for _, c := range reasoningCases {
		t.Run(c.name, func(t *testing.T) {
			srv := chatServer(t, c.usage)
			gw := Gateway{OpenAIURL: srv.URL, RunID: "crew-1", AgentID: "agent://x/y.mercer",
				OnBehalfOf: testChain, BudgetUSD: "1.00"}
			res, err := Call(context.Background(), "openrouter", "google/gemini-2.5-flash", "p", 4096, gw)
			if err != nil {
				t.Fatal(err)
			}
			if res.InTokens != c.wantIn || res.OutTokens != c.wantOut {
				t.Errorf("counted %d in and %d out, want %d and %d (usage %s)",
					res.InTokens, res.OutTokens, c.wantIn, c.wantOut, c.usage)
			}
		})
	}
}
