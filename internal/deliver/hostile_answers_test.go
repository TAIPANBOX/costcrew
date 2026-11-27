package deliver

// What a model endpoint can answer besides a good answer, through the
// gateway: an empty 200, a 200 that is not JSON, an answer with no text, a
// refusal. Each becomes an error that says which of those it was, never a
// zero-cost empty deliverable.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func answering(t *testing.T, status int, body string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestAnAnthropicAnswerWithNoTextSaysWhy(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-stub-not-real")
	cases := []struct{ name, body, want string }{
		{"empty 200", "  \n", "anthropic answered 200 with an empty body"},
		{"not JSON", "<html>bad gateway</html>", "anthropic's answer did not parse"},
		{"no blocks", `{"content":[],"stop_reason":"max_tokens","usage":{"output_tokens":0}}`,
			`anthropic returned no text (stop_reason "max_tokens", no content blocks at all, 0 output tokens)`},
		{"only a tool block", `{"content":[{"type":"tool_use"},{"type":"thinking"}],"stop_reason":"tool_use","usage":{"output_tokens":12}}`,
			`(stop_reason "tool_use", blocks: tool_use, thinking, 12 output tokens)`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gw := Gateway{URL: answering(t, 200, c.body), RunID: "crew-1",
				AgentID: "agent://x/y.mercer", OnBehalfOf: testChain, BudgetUSD: "1.00"}
			res, err := callAnthropic(context.Background(), "claude-x", "hello", 100, gw)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("%+v, %v; want an error containing %q", res, err, c.want)
			}
		})
	}
}

func TestAnOpenAIWireAnswerThatIsNotAnAnswerIsRefused(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "sk-or-stub-not-real")
	cases := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"a 500", 500, "upstream exploded " + strings.Repeat("x", 400), "the router answered 500: upstream exploded"},
		{"empty 200", 200, "", "the router answered 200 with an empty body"},
		{"not JSON", 200, "{choices", "the router's answer did not parse"},
		{"no choices", 200, `{"choices":[],"usage":{"prompt_tokens":3}}`, "the router returned no answer"},
		{"a 402 not in the documented shape", 402, "payment required", "was not the documented shape: payment required"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gw := Gateway{OpenAIURL: answering(t, c.status, c.body), RunID: "crew-1",
				AgentID: "agent://x/y.mercer", OnBehalfOf: testChain, BudgetUSD: "1.00"}
			res, err := callOpenRouter(context.Background(), "openai/gpt-x", "hello", 100, gw)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("%+v, %v; want an error containing %q", res, err, c.want)
			}
			if len(err.Error()) > 400 {
				t.Errorf("the error carries %d bytes of the answer; it is trimmed to a prefix", len(err.Error()))
			}
		})
	}
}
